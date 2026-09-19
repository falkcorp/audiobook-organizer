// file: internal/scanner/chapter_sequence.go
// version: 1.1.0
// guid: 6b0e2d47-93c1-4f8a-b5e6-1d7a4c9f2e38
// last-edited: 2026-09-19

package scanner

import (
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Sequence-marker shapes: how a chapter's position is written in a title or
// file stem.
const (
	SeqShapeBare      = "bare"       // "157", "012"
	SeqShapeNofM      = "n_of_m"     // "108 of 310", "3/12"
	SeqShapePrefix    = "prefix"     // "006_Title", "102 - Title", "02 Title"
	SeqShapeDiscTrack = "disc_track" // "2-05 Title" (iTunes disc-track)
	SeqShapeToken     = "token"      // "Part 3", "Chapter 12", "Track 07"
	SeqShapeTrailing  = "trailing"   // file stems only: "Title - 157"
)

// SequenceMarker is a title or file stem read as "position N of a larger
// book": the disc (0 when none), the index, the declared total (0 when
// none), and the residual text once the position is removed (the book's
// name, a chapter's name, or nothing).
type SequenceMarker struct {
	Shape    string
	Disc     int
	Index    int
	Total    int
	Residual string
}

var (
	seqBareRe      = regexp.MustCompile(`^(\d{1,4})$`)
	seqNofMRe      = regexp.MustCompile(`(?i)^(\d{1,4})\s*(?:of|/)\s*(\d{1,4})$`)
	seqDiscTrackRe = regexp.MustCompile(`^(\d{1,2})-(\d{2,3})(?:[\s_.]+(.*))?$`)
	seqTokenRe     = regexp.MustCompile(`(?i)^(?:part|pt\.?|chapter|chap\.?|ch\.?|track|trk|section)\s*[-_.]?\s*(\d{1,4})\b(?:\s*(?:of|/)\s*(\d{1,4})\b)?\s*[-_:.,]*\s*(.*)$`)
	// seqPrefixRe needs a real separator after the number: an underscore, a
	// dash with whitespace on at least one side, a colon or ')', a dot, or
	// whitespace. "86-Neon" (a dash joining a word) and "84K" are titles, not
	// positions; "185917" has no separator inside four digits.
	seqPrefixRe = regexp.MustCompile(`^(\d{1,4})(\s*_+\s*|\s+[-–—]+\s*|[-–—]+\s+|\s*[:)]\s*|\.\s*|\s+)(\S.*)$`)
	// seqTrailingIndexRe finds a trailing repeat of the index, optionally
	// with a total: "Nightnet - 5", "Heir of Ash 40-65".
	seqTrailingIndexRe = regexp.MustCompile(`(?i)^(.*?\S)(\s*[-_]\s*|\s+)(\d{1,4})(?:\s*(?:-|of|/)\s*(\d{1,4}))?$`)
	// seqStemTrailingRe is a file stem ending in its index: "Title - 157",
	// "Title_009". A bare trailing number after a space ("Wheel 01") is a
	// series number, not a position, and does not match.
	seqStemTrailingRe = regexp.MustCompile(`^(.*?\S)\s*[-_]\s*(\d{1,4})$`)
	// seqChapterWordRe marks where a chapter's own name starts in a
	// residual: "Chapter One - The End", "Chapter 3 - The Hunter".
	seqChapterWordRe = regexp.MustCompile(`(?i)\b(?:chapter|chap|ch|part|pt|track|section)\b[\s\-_.]*(?:\d+|one|two|three|four|five|six|seven|eight|nine|ten|eleven|twelve|thirteen|fourteen|fifteen|sixteen|seventeen|eighteen|nineteen|twenty|thirty|forty|fifty|sixty|seventy|eighty|ninety)\b`)
	seqNoiseRe       = regexp.MustCompile(`(?i)[\s\-_]*[(\[]?\b(?:un)?abridged\b[)\]]?`)
)

// frontMatterKeys are residuals that name a track's role, not a book:
// "03_Intro" and "04_Chapter 01" are both tracks of one book.
var frontMatterKeys = map[string]bool{
	"prologue": true, "epilogue": true, "intro": true, "introduction": true,
	"preface": true, "foreword": true, "afterword": true, "credits": true,
	"opening credits": true, "end credits": true, "closing credits": true,
	"acknowledgments": true, "acknowledgements": true, "dedication": true,
	"interlude": true, "appendix": true, "outro": true, "author s note": true,
}

func seqAtoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

// ParseSequenceMarker reads a TITLE as a sequence marker: a bare number (1-4
// digits, leading zeros allowed), "N of M", "NNN_Title" / "NNN - Title" /
// "NNN Title", "D-NN Title" (disc-track), or "Part/Chapter/Track N". It
// reports false for anything else, including "11/22/63", "84K", "86-Neon",
// a decimal series position ("11.3 - Title"), "Book 2" (a series number) and
// numbers of five or more digits (timestamps).
//
// A legit book titled "1984" or "2001: A Space Odyssey" parses as a marker:
// the SHAPE is the same. What keeps it out of a chapter group is grouping
// (one record in its folder, or siblings with different residuals).
func ParseSequenceMarker(s string) (SequenceMarker, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return SequenceMarker{}, false
	}
	if m := seqBareRe.FindStringSubmatch(s); m != nil {
		return SequenceMarker{Shape: SeqShapeBare, Index: seqAtoi(m[1])}, true
	}
	if m := seqNofMRe.FindStringSubmatch(s); m != nil {
		return SequenceMarker{Shape: SeqShapeNofM, Index: seqAtoi(m[1]), Total: seqAtoi(m[2])}, true
	}
	if m := seqDiscTrackRe.FindStringSubmatch(s); m != nil {
		sm := SequenceMarker{Shape: SeqShapeDiscTrack, Disc: seqAtoi(m[1]), Index: seqAtoi(m[2])}
		sm.Residual, sm.Total = seqStripTrailingIndex(strings.TrimSpace(m[3]), sm.Index)
		return sm, true
	}
	if m := seqTokenRe.FindStringSubmatch(s); m != nil {
		// "Part 2 of 3" declares the total.
		return SequenceMarker{Shape: SeqShapeToken, Index: seqAtoi(m[1]), Total: seqAtoi(m[2]), Residual: strings.TrimSpace(m[3])}, true
	}
	if m := seqPrefixRe.FindStringSubmatch(s); m != nil {
		rest := m[3]
		if strings.HasPrefix(m[2], ".") && rest[0] >= '0' && rest[0] <= '9' {
			return SequenceMarker{}, false // "11.3 - Title": a decimal, not a position
		}
		sm := SequenceMarker{Shape: SeqShapePrefix, Index: seqAtoi(m[1])}
		sm.Residual, sm.Total = seqStripTrailingIndex(strings.TrimSpace(rest), sm.Index)
		return sm, true
	}
	return SequenceMarker{}, false
}

// ParseFilenameSequence reads a FILE STEM: every title shape, plus a stem
// that ends in its index ("Title - 157", "Title - Author - 010").
func ParseFilenameSequence(stem string) (SequenceMarker, bool) {
	stem = strings.TrimSpace(stem)
	if m, ok := ParseSequenceMarker(stem); ok {
		return m, true
	}
	if m := seqStemTrailingRe.FindStringSubmatch(stem); m != nil {
		return SequenceMarker{Shape: SeqShapeTrailing, Index: seqAtoi(m[2]), Residual: strings.TrimSpace(m[1])}, true
	}
	return SequenceMarker{}, false
}

// seqStripTrailingIndex removes a trailing repeat of index from a residual:
// "Nightnet - 5" (index 5) -> "Nightnet"; "Heir of Ash 40-65" (index 40) ->
// "Heir of Ash", total 65. A repeat after bare whitespace with no total
// ("Wheel of Time 01") is kept: that shape is a series number.
func seqStripTrailingIndex(res string, index int) (string, int) {
	m := seqTrailingIndexRe.FindStringSubmatch(res)
	if m == nil || seqAtoi(m[3]) != index {
		return res, 0
	}
	hasDash := strings.ContainsAny(m[2], "-_")
	if m[4] == "" && !hasDash {
		return res, 0
	}
	return strings.TrimSpace(m[1]), seqAtoi(m[4])
}

// sequenceResidualDisplay is a residual cleaned for display as a book title:
// within-book position tokens and "(Unabridged)" removed.
func sequenceResidualDisplay(res string) string {
	res = seqNoiseRe.ReplaceAllString(res, "")
	return strings.Trim(chapterTokenRe.ReplaceAllString(res, ""), " -_.,:")
}

// sequenceResidualKey is the grouping key of a residual: normalised, without
// position tokens or "(Unabridged)", and "" for a residual that only names a
// track's role (Prologue, Intro, Credits) or nothing at all.
func sequenceResidualKey(res string) string {
	k := normForCompare(sequenceResidualDisplay(res))
	if frontMatterKeys[k] {
		return ""
	}
	return k
}

// sequenceResidualBase is the part of a residual before a chapter token --
// the book's name when the rest is a chapter's own name ("Tunnels - Chapter
// One - The End" -> "Tunnels"; "Chapter 4 - The Hunter" -> "").
func sequenceResidualBase(res string) (string, bool) {
	loc := seqChapterWordRe.FindStringIndex(res)
	if loc == nil {
		return res, false
	}
	return strings.Trim(res[:loc[0]], " -_.,:"), true
}

var (
	seqFolderLeadRe   = regexp.MustCompile(`^\d{1,4}\s*[-_.)]+\s*`)
	seqFolderSeriesRe = regexp.MustCompile(`^.+?\s+-\s+\d{1,3}(?:\.\d+)?\s+-\s+`)
)

// chapterFolderDisplay is a folder's name read as a book title: a leading
// number ("06_Title") and a "Series - N - " prefix removed, "(Unabridged)"
// dropped.
func chapterFolderDisplay(dir string) string {
	name := filepath.Base(dir)
	name = seqFolderSeriesRe.ReplaceAllString(name, "")
	name = seqFolderLeadRe.ReplaceAllString(name, "")
	return sequenceResidualDisplay(strings.TrimSpace(name))
}

// chapterFolderKey is chapterFolderDisplay normalised for comparison.
func chapterFolderKey(dir string) string {
	return sequenceResidualKey(chapterFolderDisplay(dir))
}
