// file: internal/metadata/chapter_group_key.go
// version: 1.0.0
// guid: 7b1e4c2a-9d53-4f8e-a6b0-3c5d8e2f1a94
// last-edited: 2026-09-28

package metadata

import (
	"regexp"
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
	// chapterLeadingMarkerRe: "Chapter 01 - Title", "Disc 2 - Title". The
	// marker word must be followed by a number, so "Discworld - Mort" is not
	// a disc marker.
	chapterLeadingMarkerRe = regexp.MustCompile(`(?i)^(?:chapter|chap|ch|part|pt|disc|disk|cd|track)\.?[\s_]*\d+(?:\s*of\s*\d+)?(?:[\s\-–._:]+|$)`)
	// chapterTrailingMarkerRe: "Title - Chapter 12", "Title Part 3",
	// "Title_Disc2", "Title (CD 1)", "Title Part 3 of 12". The marker must
	// start a word (start of stem or after a separator) and be followed by a
	// number, so "Discworld 5" is NOT a disc marker.
	chapterTrailingMarkerRe = regexp.MustCompile(`(?i)(?:^|[\s\-–_.,(\[]+)(?:chapter|chap|ch|part|pt|disc|disk|cd|track)\.?[\s_\-]*\d+(?:\s*of\s*\d+)?[)\]]?\s*$`)
	// chapterTrailingOfRe: "Title 3 of 12", "Title (03 of 12)". Captures the
	// total, which is part of the grouping key.
	chapterTrailingOfRe = regexp.MustCompile(`(?i)(?:^|[\s\-–_.,(\[]+)\d+\s*of\s*(\d+)[)\]]?\s*$`)
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
