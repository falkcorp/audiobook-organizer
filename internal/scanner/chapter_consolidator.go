// file: internal/scanner/chapter_consolidator.go
// version: 1.2.0
// guid: b2c3d4e5-f6a7-8901-bcde-f01234567890
// last-edited: 2026-09-19

package scanner

import (
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// chapterNumPrefixDetectRe matches a leading numeric chapter/track prefix:
// "01 - ", "02. ", "003:", "1 " etc. — must be at the start of the stem.
var chapterNumPrefixDetectRe = regexp.MustCompile(`^\d{1,3}[\s\-_\.\:]+`)

// ChapterGroup is a set of book IDs that belong to the same multi-part audiobook.
//
// BookIDs is ordered by chapter number (the numeric filename prefix, then the
// file name, then the ID as a final tie-break), so the order is deterministic
// whatever order the store listed the rows in. PrimaryBookID is BookIDs[0] --
// the lowest-numbered chapter -- and is the book a merge folds the others into.
type ChapterGroup struct {
	PrimaryBookID string   `json:"primary_book_id"`
	BookIDs       []string `json:"book_ids"`
	CommonTitle   string   `json:"common_title"`   // primary's title with numeric prefix stripped
	TotalDuration float64  `json:"total_duration"` // sum of all file durations in seconds
	FileCount     int      `json:"file_count"`
	Directory     string   `json:"directory"`
}

// ChapterDetectOptions tunes DetectChapterGroupsWithOptions.
type ChapterDetectOptions struct {
	// MinFiles is the file count at which the average-duration branch of
	// heuristic 4 applies. Defaults to 3 when <= 0.
	MinFiles int
	// MaxPerFileDuration is the per-file "short chapter" ceiling in seconds.
	// Defaults to 600 when <= 0.
	MaxPerFileDuration int
	// PathPrefix, when set, limits detection to books whose file path is the
	// prefix itself or lies beneath it (directory-boundary match, so "/a/b"
	// does not match "/a/bc").
	PathPrefix string
}

// ChapterDetection is the full detector output.
type ChapterDetection struct {
	Groups []ChapterGroup
	// SkippedUnknownDuration counts chapter-shaped groups that were NOT
	// returned because at least one member's duration is unknown (nil or <= 0).
	SkippedUnknownDuration int
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
	longer := max(len(wb), len(wa))
	return float64(common)/float64(longer) >= 0.80
}

// DetectChapterGroups is DetectChapterGroupsWithOptions without a path
// filter, returning only the groups.
func DetectChapterGroups(books []database.BookCore, minFiles, maxPerFileDuration int) []ChapterGroup {
	return DetectChapterGroupsWithOptions(books, ChapterDetectOptions{
		MinFiles:           minFiles,
		MaxPerFileDuration: maxPerFileDuration,
	}).Groups
}

// chapterNumber returns the numeric chapter prefix of a filename stem, or -1.
func chapterNumber(stem string) int {
	n, digits := 0, 0
	for _, r := range stem {
		if r < '0' || r > '9' {
			break
		}
		n = n*10 + int(r-'0')
		digits++
	}
	if digits == 0 {
		return -1
	}
	return n
}

// pathUnderPrefix reports whether p is prefix or lies beneath it.
func pathUnderPrefix(p, prefix string) bool {
	prefix = filepath.Clean(prefix)
	p = filepath.Clean(p)
	if p == prefix {
		return true
	}
	if strings.HasSuffix(prefix, string(filepath.Separator)) {
		return strings.HasPrefix(p, prefix) // prefix is the root
	}
	return strings.HasPrefix(p, prefix+string(filepath.Separator))
}

// chapterCandidateEligible reports whether a book may take part in chapter
// detection at all. Books already absorbed by an earlier merge
// (MergedIntoBookID), soft-deleted books (a chapter merge soft-deletes its
// sources), and non-primary versions of another book are all excluded, so a
// re-run can never regroup or re-merge what a previous run already merged.
func chapterCandidateEligible(b *database.BookCore) bool {
	if b.MergedIntoBookID != nil && *b.MergedIntoBookID != "" {
		return false
	}
	if b.IsSoftDeleted() {
		return false
	}
	return database.EffectiveIsPrimaryVersion(b.IsPrimaryVersion)
}

// DetectChapterGroupsWithOptions inspects already-scanned database.BookCore
// records and returns groups that look like sequential chapters of the same
// audiobook.
//
// All four heuristics must be satisfied for a group to be returned:
//  1. Files share the same parent directory.
//  2. Filenames match a sequential chapter pattern (leading 1-3 digit prefix).
//  3. After stripping the numeric prefix, base titles are >= 80% similar.
//  4. Each individual file's duration is < MaxPerFileDuration seconds OR
//     the group has >= MinFiles files and the average per-file duration is
//     < 1800 s (30 min).
//
// Heuristic 4 needs every member's duration. A member whose duration is
// unknown (nil, or <= 0 -- what a failed probe leaves) makes the whole group
// ineligible: it is neither "short" nor part of a meaningful average, and
// treating it as 0 s is what used to make unprobed full-length books look like
// short chapters. Such groups are counted in SkippedUnknownDuration so an
// operator can backfill durations and re-run, instead of silently vanishing.
// Excluding only the unknown member instead would merge the rest and strand
// that chapter as its own book, which is worse than not merging at all.
func DetectChapterGroupsWithOptions(books []database.BookCore, opts ChapterDetectOptions) ChapterDetection {
	var out ChapterDetection
	if len(books) == 0 {
		return out
	}
	minFiles := opts.MinFiles
	if minFiles <= 0 {
		minFiles = 3
	}
	maxPerFileDuration := opts.MaxPerFileDuration
	if maxPerFileDuration <= 0 {
		maxPerFileDuration = 600
	}

	type candidate struct {
		book          database.BookCore
		base          string
		chapter       int
		strippedTitle string
	}

	// Group candidates by parent directory.
	byDir := make(map[string][]candidate)
	for i := range books {
		b := books[i]
		if !chapterCandidateEligible(&b) {
			continue
		}
		if opts.PathPrefix != "" && !pathUnderPrefix(b.FilePath, opts.PathPrefix) {
			continue
		}
		dir := filepath.Dir(b.FilePath)
		base := filepath.Base(b.FilePath)
		stem := strings.TrimSuffix(base, filepath.Ext(b.FilePath))
		if !chapterNumPrefixDetectRe.MatchString(stem) {
			continue // not a chapter-numbered file
		}
		byDir[dir] = append(byDir[dir], candidate{
			book: b, base: base, chapter: chapterNumber(stem), strippedTitle: stripNumPrefix(stem),
		})
	}

	// Directories in sorted order so the output does not depend on the order
	// the store listed rows in.
	dirs := make([]string, 0, len(byDir))
	for d := range byDir {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)

	for _, dir := range dirs {
		cands := byDir[dir]
		if len(cands) < 2 {
			continue
		}
		// Chapter order first, so sub-grouping below seeds each sub-group
		// with its lowest-numbered chapter and the primary is chapter 01.
		sort.SliceStable(cands, func(i, j int) bool {
			a, b := cands[i], cands[j]
			if a.chapter != b.chapter {
				return a.chapter < b.chapter
			}
			if a.base != b.base {
				return a.base < b.base
			}
			return a.book.ID < b.book.ID
		})

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
			allKnown := true
			allShort := true
			for _, c := range sg.cands {
				if c.book.Duration == nil || *c.book.Duration <= 0 {
					allKnown = false
					break
				}
				d := *c.book.Duration
				totalSec += d
				if d >= maxPerFileDuration {
					allShort = false
				}
			}
			if !allKnown {
				out.SkippedUnknownDuration++
				continue
			}
			avgSec := totalSec / fc

			// Heuristic 4: all short OR (enough files AND avg short enough).
			if !allShort && !(fc >= minFiles && avgSec < 1800) {
				continue
			}

			ids := make([]string, fc)
			for i, c := range sg.cands {
				ids[i] = c.book.ID
			}
			out.Groups = append(out.Groups, ChapterGroup{
				PrimaryBookID: ids[0],
				BookIDs:       ids,
				CommonTitle:   sg.cands[0].strippedTitle,
				TotalDuration: float64(totalSec),
				FileCount:     fc,
				Directory:     dir,
			})
		}
	}

	return out
}
