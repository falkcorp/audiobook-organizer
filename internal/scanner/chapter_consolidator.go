// file: internal/scanner/chapter_consolidator.go
// version: 1.1.0
// guid: b2c3d4e5-f6a7-8901-bcde-f01234567890
// last-edited: 2026-07-07

package scanner

import (
	"path/filepath"
	"regexp"
	"strings"
	"unicode"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// chapterNumPrefixDetectRe matches a leading numeric chapter/track prefix:
// "01 - ", "02. ", "003:", "1 " etc. — must be at the start of the stem.
var chapterNumPrefixDetectRe = regexp.MustCompile(`^\d{1,3}[\s\-_\.\:]+`)

// ChapterGroup is a set of book IDs that belong to the same multi-part audiobook.
type ChapterGroup struct {
	BookIDs       []string `json:"book_ids"`
	CommonTitle   string   `json:"common_title"`   // title with numeric prefix stripped
	TotalDuration float64  `json:"total_duration"` // sum of all file durations in seconds
	FileCount     int      `json:"file_count"`
}

// stripNumPrefix removes a leading numeric chapter/track number from a filename
// stem, e.g. "01 - My Book" → "My Book", "002. Foo" → "Foo".
func stripNumPrefix(title string) string {
	return strings.TrimSpace(chapterNumPrefixDetectRe.ReplaceAllString(title, ""))
}

// normForCompare lowercases, replaces non-alphanumeric chars with spaces, and
// collapses whitespace for a stable comparison key.
func normForCompare(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		} else {
			b.WriteRune(' ')
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// chapterTitlesAreSimilar returns true when two stripped titles are considered
// the same audiobook: identical after normalisation, one is a prefix of the
// other, or they share ≥ 80% of words.
func chapterTitlesAreSimilar(a, b string) bool {
	na, nb := normForCompare(a), normForCompare(b)
	if na == nb {
		return true
	}
	if strings.HasPrefix(na, nb) || strings.HasPrefix(nb, na) {
		return true
	}
	wa, wb := strings.Fields(na), strings.Fields(nb)
	if len(wa) == 0 || len(wb) == 0 {
		return false
	}
	setB := make(map[string]struct{}, len(wb))
	for _, w := range wb {
		setB[w] = struct{}{}
	}
	common := 0
	for _, w := range wa {
		if _, ok := setB[w]; ok {
			common++
		}
	}
	longer := len(wa)
	if len(wb) > longer {
		longer = len(wb)
	}
	return float64(common)/float64(longer) >= 0.80
}

// DetectChapterGroups inspects already-scanned database.Book records and
// returns groups that look like sequential chapters of the same audiobook.
//
// All four heuristics must be satisfied for a group to be returned:
//  1. Files share the same parent directory.
//  2. Filenames match a sequential chapter pattern (leading 1-3 digit prefix).
//  3. After stripping the numeric prefix, base titles are ≥ 80% similar.
//  4. Each individual file's duration is < maxPerFileDuration seconds  OR
//     the group has ≥ minFiles files and the average per-file duration is
//     < 1800 s (30 min).
//
// minFiles defaults to 3 when ≤ 0. maxPerFileDuration defaults to 600 s
// (10 min) when ≤ 0.
func DetectChapterGroups(books []database.BookCore, minFiles, maxPerFileDuration int) []ChapterGroup {
	if len(books) == 0 {
		return nil
	}
	if minFiles <= 0 {
		minFiles = 3
	}
	if maxPerFileDuration <= 0 {
		maxPerFileDuration = 600
	}

	type candidate struct {
		book          database.BookCore
		strippedTitle string
	}

	// Group candidates by parent directory.
	byDir := make(map[string][]candidate)
	dirOrder := make([]string, 0)
	seen := make(map[string]bool)
	for _, b := range books {
		dir := filepath.Dir(b.FilePath)
		stem := strings.TrimSuffix(filepath.Base(b.FilePath), filepath.Ext(b.FilePath))
		if !chapterNumPrefixDetectRe.MatchString(stem) {
			continue // not a chapter-numbered file
		}
		stripped := stripNumPrefix(stem)
		if !seen[dir] {
			seen[dir] = true
			dirOrder = append(dirOrder, dir)
		}
		byDir[dir] = append(byDir[dir], candidate{book: b, strippedTitle: stripped})
	}

	var groups []ChapterGroup

	for _, dir := range dirOrder {
		cands := byDir[dir]
		if len(cands) < 2 {
			continue
		}

		// Sub-group by title similarity: each new candidate either joins an
		// existing sub-group or starts a new one.
		type subGroup struct{ cands []candidate }
		var sgs []subGroup
		for _, c := range cands {
			placed := false
			for i := range sgs {
				if chapterTitlesAreSimilar(c.strippedTitle, sgs[i].cands[0].strippedTitle) {
					sgs[i].cands = append(sgs[i].cands, c)
					placed = true
					break
				}
			}
			if !placed {
				sgs = append(sgs, subGroup{cands: []candidate{c}})
			}
		}

		for _, sg := range sgs {
			fc := len(sg.cands)
			if fc < 2 {
				continue
			}

			totalSec := 0
			for _, c := range sg.cands {
				if c.book.Duration != nil {
					totalSec += *c.book.Duration
				}
			}
			avgSec := 0
			if fc > 0 {
				avgSec = totalSec / fc
			}

			// Heuristic 4: all short OR (enough files AND avg short enough).
			allShort := true
			for _, c := range sg.cands {
				dur := 0
				if c.book.Duration != nil {
					dur = *c.book.Duration
				}
				if dur >= maxPerFileDuration {
					allShort = false
					break
				}
			}
			if !allShort && !(fc >= minFiles && avgSec < 1800) {
				continue
			}

			ids := make([]string, fc)
			for i, c := range sg.cands {
				ids[i] = c.book.ID
			}
			groups = append(groups, ChapterGroup{
				BookIDs:       ids,
				CommonTitle:   sg.cands[0].strippedTitle,
				TotalDuration: float64(totalSec),
				FileCount:     fc,
			})
		}
	}

	return groups
}
