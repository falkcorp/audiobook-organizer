// file: internal/scanner/chapter_consolidator.go
// version: 1.3.0
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
	// Exclude, when set, removes a book from detection entirely (counted in
	// SkippedExcluded). The merge job uses it for the owner's manual-only
	// libraries (Doctor Who / Big Finish / Torchwood) and the active iTunes
	// library.
	Exclude func(b *database.BookCore) bool
}

// ChapterDetection is the full detector output.
type ChapterDetection struct {
	Groups []ChapterGroup
	// SkippedUnknownDuration counts chapter-shaped groups that were NOT
	// returned because at least one member's duration is unknown (nil or <= 0).
	SkippedUnknownDuration int
	// SkippedDuplicateCopies counts groups that look like several full copies
	// of one book (identical titles, each about a full book long and nearly
	// equal, or mixed containers such as .mp3 and .m4b). Those are dedup's job.
	SkippedDuplicateCopies int
	// SkippedExcluded counts books removed by ChapterDetectOptions.Exclude.
	SkippedExcluded int
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

// chapterTokenRe matches a within-book position token and its number:
// "Chapter 3", "Part 2", "Track 07", "Disc 1", "CD2", "pt.3". These are the
// ONLY words allowed to differ between two files of one chapter group. Series
// and episode numbers ("Book 2", "Volume 3", "Episode 4") are deliberately
// absent: they name different books.
var chapterTokenRe = regexp.MustCompile(`(?i)[\s\-_.,:]*\b(chapter|chap|ch|track|trk|part|pt|disc|disk|cd|section)[\s\-_.]*\d+\b`)

// chapterTitleBase strips a stripped title's within-book position tokens and
// trailing separators, keeping the original casing (used as CommonTitle).
func chapterTitleBase(stripped string) string {
	return strings.Trim(chapterTokenRe.ReplaceAllString(stripped, ""), " -_.,:")
}

// minChapterKeyLen is the shortest normalised base title that may group. An
// empty or one/two-character title ("It", "01.mp3" with nothing after the
// number) carries no evidence that two files belong together.
const minChapterKeyLen = 3

// chapterTitleKey is the grouping key: the normalised base title. Two files
// group only when their keys are EQUAL -- they differ by nothing but the
// chapter-number prefix and position tokens. There is no prefix match and no
// word-overlap score: "Dune" vs "Dune Messiah", "Book 01" vs "Book 02" and
// "Short Trips Alpha" vs "Short Trips Beta" are all different books. Returns
// "" for a title too short to group.
func chapterTitleKey(stripped string) string {
	k := normForCompare(chapterTitleBase(stripped))
	if len(strings.ReplaceAll(k, " ", "")) < minChapterKeyLen {
		return ""
	}
	return k
}

// fullBookSeconds is the per-file length above which a member reads as a
// whole book rather than a chapter, for the duplicate-copies check.
const fullBookSeconds = 3600

// looksLikeDuplicateCopies reports whether a would-be group is several copies
// of one book: mixed audio containers (an .mp3 and an .m4b of the same title),
// or identical titles whose members are each a full book long and within 10%
// of each other. Those are dedup's job; merging them would fold two copies of
// a book into one "book" with twice the running time.
func looksLikeDuplicateCopies(members []database.BookCore, strippedNorms []string) bool {
	exts := map[string]bool{}
	for _, m := range members {
		exts[strings.ToLower(filepath.Ext(m.FilePath))] = true
	}
	if len(exts) > 1 {
		return true
	}
	for _, n := range strippedNorms[1:] {
		if n != strippedNorms[0] {
			return false
		}
	}
	lo, hi := -1, 0
	for _, m := range members {
		d := *m.Duration
		if d < fullBookSeconds {
			return false
		}
		if lo < 0 || d < lo {
			lo = d
		}
		if d > hi {
			hi = d
		}
	}
	return float64(hi) <= float64(lo)*1.10
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
//  3. After stripping the numeric prefix and within-book position tokens
//     (chapter/part/track/disc N), the titles are EQUAL and at least
//     minChapterKeyLen characters long (see chapterTitleKey).
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
		key           string
	}

	// Group candidates by parent directory.
	byDir := make(map[string][]candidate)
	for i := range books {
		b := books[i]
		if !chapterCandidateEligible(&b) {
			continue
		}
		if opts.Exclude != nil && opts.Exclude(&b) {
			out.SkippedExcluded++
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
		stripped := stripNumPrefix(stem)
		key := chapterTitleKey(stripped)
		if key == "" {
			continue // empty or too-short title: never groups
		}
		byDir[dir] = append(byDir[dir], candidate{
			book: b, base: base, chapter: chapterNumber(stem), strippedTitle: stripped, key: key,
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

		// Sub-group by exact title key, in chapter order, so each sub-group's
		// first candidate is its lowest-numbered chapter.
		type subGroup struct{ cands []candidate }
		var sgs []subGroup
		byKey := map[string]int{}
		for _, c := range cands {
			if i, ok := byKey[c.key]; ok {
				sgs[i].cands = append(sgs[i].cands, c)
				continue
			}
			byKey[c.key] = len(sgs)
			sgs = append(sgs, subGroup{cands: []candidate{c}})
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
			members := make([]database.BookCore, fc)
			norms := make([]string, fc)
			for i, c := range sg.cands {
				members[i] = c.book
				norms[i] = normForCompare(c.strippedTitle)
			}
			if looksLikeDuplicateCopies(members, norms) {
				out.SkippedDuplicateCopies++
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
				CommonTitle:   chapterTitleBase(sg.cands[0].strippedTitle),
				TotalDuration: float64(totalSec),
				FileCount:     fc,
				Directory:     dir,
			})
		}
	}

	return out
}

// ChapterTitleIsFilenameDerived reports whether title is still the
// scanner's filename-derived title for filePath: empty, or equal (after
// normalisation) to the file's stem with or without its numeric chapter
// prefix. A chapter merge may replace such a title with the group's common
// title; any other title was curated (by a metadata apply or by hand) and a
// merge must leave it alone.
func ChapterTitleIsFilenameDerived(title, filePath string) bool {
	nt := normForCompare(title)
	if nt == "" {
		return true
	}
	stem := strings.TrimSuffix(filepath.Base(filePath), filepath.Ext(filePath))
	return nt == normForCompare(stem) || nt == normForCompare(stripNumPrefix(stem))
}
