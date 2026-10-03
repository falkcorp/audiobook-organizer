// file: internal/metadata/chapter_group_key.go
// version: 1.3.0
// guid: 7b1e4c2a-9d53-4f8e-a6b0-3c5d8e2f1a94
// last-edited: 2026-10-03

package metadata

import (
	"regexp"
	"strconv"
	"strings"
)

// Moved here from internal/scanner (chapter_consolidation.go) on 2026-09-28 so
// the Repairs-lane fragment-consolidation fixer groups chapter files with the
// exact rule the scanner uses; the scanner keeps thin aliases.

// ChapterKeyKind says which numbering shape ChapterGroupKey recognised.
type ChapterKeyKind int

const (
	ChapterKeyNone           ChapterKeyKind = iota // no chapter numbering: a standalone file
	ChapterKeyLeading                              // "01 - Title", "01. Title", bare "98"
	ChapterKeyMarker                               // "Title - Chapter 12", "Title Part 3", "Title Disc 2", "Chapter 01 - Title"
	ChapterKeyOfTotal                              // "Title 3 of 12", "Title (03 of 12)"
	ChapterKeyTrailingNumber                       // "Book Title 01"
)

var (
	// chapterLeadingNumRe: a leading track/chapter number followed by a
	// separator, or a stem that is nothing but a number ("98").
	chapterLeadingNumRe = regexp.MustCompile(`^\d+(?:[\s\-–._]+|$)`)
	// chapterLeadingPairRe: a leading DISC-TRACK (or book-chapter) pair,
	// "8-02 Rubicon", "01_07-Star Wars", "02_001", "1-09 Starshine". The first
	// number is short (1-2 digits) so a year ("2016 - Title") never reads as
	// one; both numbers are stripped from the key and both kept in the
	// position. Until 2026-10-03 only the first was read, so every file of
	// "8-02 Rubicon … — 02" carried chapter number 8.
	chapterLeadingPairRe = regexp.MustCompile(`^(\d{1,2})[-_](\d{1,3})(?:[\s\-–._]+|$)`)
	// chapterLeadingMarkerRe: "Chapter 01 - Title", "Disc 2 - Title". The
	// marker word must be followed by a number, so "Discworld - Mort" is not
	// a disc marker.
	chapterLeadingMarkerRe = regexp.MustCompile(`(?i)^(?:chapter|chap|ch|part|pt|disc|disk|cd|track)\.?[\s_]*\d+(?:[\s_]*of[\s_]*\d+)?(?:[\s\-–._:]+|$)`)
	// chapterTrailingMarkerRe: "Title - Chapter 12", "Title Part 3",
	// "Title_Disc2", "Title (CD 1)", "Title Part 3 of 12". The marker must
	// start a word (start of stem or after a separator) and be followed by a
	// number, so "Discworld 5" is NOT a disc marker.
	chapterTrailingMarkerRe = regexp.MustCompile(`(?i)(?:^|[\s\-–_.,(\[]+)(?:chapter|chap|ch|part|pt|disc|disk|cd|track)\.?[\s_\-]*\d+(?:[\s_]*of[\s_]*\d+)?[)\]]?\s*$`)
	// chapterTrailingOfRe: "Title 3 of 12", "Title (03 of 12)". Captures the
	// total, which is part of the grouping key.
	chapterTrailingOfRe = regexp.MustCompile(`(?i)(?:^|[\s\-–_.,(\[]+)\d+[\s_]*of[\s_]*(\d+)[)\]]?\s*$`)
	// chapterTrailingNumRe: "Book Title 01", "Book Title - 01", "Title_01".
	chapterTrailingNumRe = regexp.MustCompile(`(?:^|[\s\-–_.,(\[]+)\d+[)\]]?\s*$`)
	// chapterSeriesWordRe: a trailing number after one of these is a series
	// entry ("Mistborn Book 2", "Vol. 3", "#4"), a separate book, not a
	// chapter.
	chapterSeriesWordRe = regexp.MustCompile(`(?i)(?:^|[\s\-–_.,(\[])(?:book|bk|volume|vol|no|#)\.?$`)
)

// ChapterGroupKey reduces a filename stem to the key its chapter siblings
// share, by stripping ONE leading and ONE trailing chapter number:
//
//	"01 - My Book"          -> "my book"   (leading)
//	"98"                    -> ""          (leading; bare chapter number)
//	"My Book - Chapter 12"  -> "my book"   (marker)
//	"My Book Disc 2"        -> "my book"   (marker)
//	"My Book 3 of 12"       -> "my book|of 12" (of-total; the total is kept so
//	                           two differently-sized sets do not merge)
//	"My Book 01"            -> "my book"   (trailing number)
//	"01 Genesis 001"        -> "genesis"   (both ends)
//	"8-02 Rubicon"          -> "rubicon"   (leading disc-track pair)
//	"02_Eldest_002_of_349"  -> "eldest|of 349" ("_" around "of" too)
//
// kind is ChapterKeyNone when the stem carries no chapter numbering at all; the
// file then stands alone. A trailing number after "Book", "Vol" or "#" is a
// series position, not a chapter, and is not stripped. "Discworld" is not a
// disc marker: every marker must be followed by a number.
//
// Recognising a key is not a decision to merge: the scanner's
// consolidateChapterGroups and the fragment-consolidation repair both still
// require ≥3 files and short durations before they group anything.
func ChapterGroupKey(stem string) (key string, kind ChapterKeyKind) {
	s := strings.TrimSpace(stem)

	if loc := chapterLeadingMarkerRe.FindStringIndex(s); loc != nil {
		s, kind = s[loc[1]:], ChapterKeyMarker
	} else if loc := chapterLeadingPairRe.FindStringIndex(s); loc != nil {
		s, kind = s[loc[1]:], ChapterKeyLeading
	} else if loc := chapterLeadingNumRe.FindStringIndex(s); loc != nil {
		s, kind = s[loc[1]:], ChapterKeyLeading
	}

	suffix := ""
	switch {
	case chapterTrailingMarkerRe.MatchString(s):
		s = chapterTrailingMarkerRe.ReplaceAllString(s, "")
		kind = ChapterKeyMarker
	case chapterTrailingOfRe.MatchString(s):
		suffix = "|of " + chapterTrailingOfRe.FindStringSubmatch(s)[1]
		s = chapterTrailingOfRe.ReplaceAllString(s, "")
		if kind == ChapterKeyNone {
			kind = ChapterKeyOfTotal
		}
	case chapterTrailingNumRe.MatchString(s):
		rest := chapterTrailingNumRe.ReplaceAllString(s, "")
		if rest != "" && !chapterSeriesWordRe.MatchString(strings.TrimSpace(rest)) {
			s = rest
			if kind == ChapterKeyNone {
				kind = ChapterKeyTrailingNumber
			}
		}
	}
	if kind == ChapterKeyNone {
		return "", ChapterKeyNone
	}

	s = strings.Trim(strings.ToLower(s), " -–_.,:")
	s = strings.Join(strings.Fields(s), " ")
	return s + suffix, kind
}

// HasLeadingChapterNumber reports whether the stem opens with a bare number
// ("002 - Arc Part 1", "070 - Skating", "98"), whatever else it carries.
// ChapterGroupKey reports the LAST piece it strips as the kind, so a stem
// with a leading number and a trailing marker is ChapterKeyMarker; a caller
// that asks "is this file numbered at the front?" needs this instead.
func HasLeadingChapterNumber(stem string) bool {
	return chapterLeadingNumRe.MatchString(strings.TrimSpace(stem))
}

// ChapterPos is where a chapter file sits in its set, read from exactly the
// pieces ChapterGroupKey strips. Disc is the number a disc/disk/cd marker
// carries (0 when there is none) and is the MAJOR sort key; Parts are the
// other stripped numbers (the leading one first, then the trailing one).
type ChapterPos struct {
	Disc  int
	Parts []int
}

// Compare orders two positions: disc first, then Parts element by element, a
// shorter Parts first when one is a prefix of the other.
func (p ChapterPos) Compare(q ChapterPos) int {
	if p.Disc != q.Disc {
		if p.Disc < q.Disc {
			return -1
		}
		return 1
	}
	for i := 0; i < len(p.Parts) && i < len(q.Parts); i++ {
		if p.Parts[i] != q.Parts[i] {
			if p.Parts[i] < q.Parts[i] {
				return -1
			}
			return 1
		}
	}
	switch {
	case len(p.Parts) < len(q.Parts):
		return -1
	case len(p.Parts) > len(q.Parts):
		return 1
	}
	return 0
}

var (
	chapterMarkerNumRe = regexp.MustCompile(`(?i)(chapter|chap|ch|part|pt|disc|disk|cd|track)\.?[\s_\-]*(\d+)`)
	chapterOfNumRe     = regexp.MustCompile(`(?i)(\d+)[\s_]*of[\s_]*\d+`)
	chapterAnyNumRe    = regexp.MustCompile(`\d+`)
)

// markerPiece reads a stripped marker piece ("Chapter 12", "Disc 2 of 3"):
// whether its word is a disc word, and its number.
func markerPiece(piece string) (disc bool, n int, ok bool) {
	m := chapterMarkerNumRe.FindStringSubmatch(piece)
	if m == nil {
		return false, 0, false
	}
	n, err := strconv.Atoi(m[2])
	if err != nil {
		return false, 0, false
	}
	switch strings.ToLower(m[1]) {
	case "disc", "disk", "cd":
		return true, n, true
	}
	return false, n, true
}

func firstNum(re *regexp.Regexp, piece string, group int) (int, bool) {
	m := re.FindStringSubmatch(piece)
	if m == nil {
		return 0, false
	}
	n, err := strconv.Atoi(m[group])
	return n, err == nil
}

// ChapterPosition reads the position a chapter stem carries from the pieces
// ChapterGroupKey strips, never from a number left in the key:
//
//	"01 - My Book"          -> {Parts: [1]}
//	"Chapter 3 - 1984"      -> {Parts: [3]}      (1984 is the title)
//	"My Book 3 of 12"       -> {Parts: [3]}      (12 is the total)
//	"My Book Disc 2"        -> {Disc: 2}
//	"Disc 2 - My Story 07"  -> {Disc: 2, Parts: [7]}
//	"01 Genesis 001"        -> {Parts: [1, 1]}
//	"8-02 Rubicon — 02"     -> {Parts: [8, 2, 2]}
//	"02_Eldest_002_of_349"  -> {Parts: [2, 2]}
//
// ok is false when the stem carries no chapter numbering (ChapterKeyNone).
func ChapterPosition(stem string) (pos ChapterPos, ok bool) {
	s := strings.TrimSpace(stem)
	take := func(disc bool, n int) {
		if disc {
			pos.Disc = n
		} else {
			pos.Parts = append(pos.Parts, n)
		}
	}
	if loc := chapterLeadingMarkerRe.FindStringIndex(s); loc != nil {
		if disc, n, mok := markerPiece(s[:loc[1]]); mok {
			take(disc, n)
			ok = true
		}
		s = s[loc[1]:]
	} else if m := chapterLeadingPairRe.FindStringSubmatchIndex(s); m != nil {
		a, aerr := strconv.Atoi(s[m[2]:m[3]])
		b, berr := strconv.Atoi(s[m[4]:m[5]])
		if aerr == nil && berr == nil {
			take(false, a)
			take(false, b)
			ok = true
		}
		s = s[m[1]:]
	} else if loc := chapterLeadingNumRe.FindStringIndex(s); loc != nil {
		if n, nok := firstNum(chapterAnyNumRe, s[:loc[1]], 0); nok {
			take(false, n)
			ok = true
		}
		s = s[loc[1]:]
	}
	switch {
	case chapterTrailingMarkerRe.MatchString(s):
		piece := chapterTrailingMarkerRe.FindString(s)
		if disc, n, mok := markerPiece(piece); mok {
			take(disc, n)
			ok = true
		}
	case chapterTrailingOfRe.MatchString(s):
		if n, nok := firstNum(chapterOfNumRe, chapterTrailingOfRe.FindString(s), 1); nok {
			take(false, n)
			ok = true
		}
	case chapterTrailingNumRe.MatchString(s):
		rest := chapterTrailingNumRe.ReplaceAllString(s, "")
		if rest != "" && !chapterSeriesWordRe.MatchString(strings.TrimSpace(rest)) {
			if n, nok := firstNum(chapterAnyNumRe, chapterTrailingNumRe.FindString(s), 0); nok {
				take(false, n)
				ok = true
			}
		}
	}
	if !ok {
		return ChapterPos{}, false
	}
	return pos, true
}

// discFolderRe matches a disc folder name: "CD1", "Disc 2", "Disk_03",
// "My Book - CD 2". The rest before the marker is kept by DiscFolder.
var discFolderRe = regexp.MustCompile(`(?i)^(.*?)[\s\-–_.(\[]*\b(?:cd|disc|disk)[\s_\-.]*(\d{1,3})[)\]]?\s*$`)

// DiscFolder reports whether a folder name is a disc folder of a multi-disc
// set ("CD1", "Disc 2", "My Book - Disc 3"): the disc number and the name
// with the marker removed ("" for a bare "CD1").
func DiscFolder(name string) (rest string, disc int, ok bool) {
	m := discFolderRe.FindStringSubmatch(strings.TrimSpace(name))
	if m == nil {
		return "", 0, false
	}
	n, err := strconv.Atoi(m[2])
	if err != nil || n <= 0 {
		return "", 0, false
	}
	return strings.Trim(m[1], " -–_.([)]"), n, true
}
