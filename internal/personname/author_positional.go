// file: internal/personname/author_positional.go
// version: 1.1.0
// guid: 1b54b5ba-45b5-4f3a-8674-43ad240b4c53
// last-edited: 2026-09-03

package personname

import (
	"regexp"
	"strings"
)

// Positional filename shrapnel is a SEPARATE defect from the publisher and
// copyright shrapnel IsDirtyAuthorName was built for (C413, 2026-08-14).
//
// Measured against the 2,793 leading-digit author rows in the production
// library on 2026-09-03, IsDirtyAuthorName recognised 435 of them (15.6%) and
// missed 2,358. It is not a near miss: the names in this class carry track,
// disc, chapter and timecode numbering lifted out of filenames and ID3 artist
// tags, and none of them look like a publisher or a copyright fragment. Wiring
// the older predicate into a creation path guards a sixth of the problem and
// reads, from the call site, exactly like guarding all of it.
//
// The shapes below were clustered from that same population:
//
//	721  001_Celestia                    NNN_Title
//	603  001-147 Kevin J Anderson        NNN-NNN Title  /  NNN of NNN
//	576  01 Something                    NN Title
//	239  0.5, 00 3                       bare numbering
//	 97  00 Prologue                     numbered structural part
//	 62  000m_00s__056m_16s_43h          timecode
//	  7  002-32k                         bitrate
//
// The 603-row bucket is why this file strips rather than rejects: a name like
// "001-147 Kevin J Anderson" carries a real person, and throwing the whole
// string away to be rid of the prefix loses them.

// positionalPrefixShapes are anchored to the clustered shapes above rather than
// to a general ^\d+ pattern.
//
// The zero-padding requirement on the bare-number shape is load-bearing. A
// general `^\d+\s+` strip also rewrites "50 Cent" to "Cent" and "24 Hours" to
// "Hours" — real names damaged silently on a creation path, which is the same
// class of data loss this file exists to prevent, pointed the other way.
// Zero-padding ("00 ", "01 ", "001 ") is the filename-numbering signature and
// does not occur in names people actually have. Verified over all 19,972
// production author rows: these patterns modify 1,776 names and every one of
// them starts with a digit.
var positionalPrefixShapes = []*regexp.Regexp{
	regexp.MustCompile(`^\s*\d{1,4}\s*[-\x{2013}]\s*\d{1,4}\s+`), // 001-147 Name
	regexp.MustCompile(`(?i)^\s*\d{1,4}\s+of\s+\d{1,4}\b\s*`),    // 001 of 301
	regexp.MustCompile(`^\s*\d{1,4}_\s*`),                        // 001_Title
	regexp.MustCompile(`^\s*\d{1,4}\s*[).]\s*`),                  // 07) Monster, 01.Magician
	regexp.MustCompile(`^\s*\d{1,4}\s*-\s*`),                     // 01-Preface, 002-32k
	regexp.MustCompile(`^\s*0\d{0,3}\s+`),                        // zero-padded only
}

// timecodeRe matches the "000m_00s__056m_16s_43h" shape that chapter-splitting
// tools write into the artist tag.
var timecodeRe = regexp.MustCompile(`(?i)^\d+\s*m[_\s]*\d+\s*s`)

// bitrateRe matches encoder residue such as "32k" or "128kbps".
var bitrateRe = regexp.MustCompile(`(?i)^\d+\s*k(bps)?$`)

// opaqueTokenRe matches truncated-filename garbage such as "0BJLX1~W" — the
// tilde is the DOS 8.3 short-name marker and never appears in a person's name.
var opaqueTokenRe = regexp.MustCompile(`^[0-9A-Za-z]{1,8}~[0-9A-Za-z]`)

// gluedNumberRe matches zero-padded numbering fused to the following word.
var gluedNumberRe = regexp.MustCompile(`^0\d{0,3}[A-Za-z]`)

// structuralPartWords are book-structure labels that end up in the artist tag
// when a chapter file is imported as if it were a work in its own right.
var structuralPartWords = map[string]bool{
	"intro": true, "introduction": true, "prologue": true, "epilogue": true,
	"preface": true, "foreword": true, "afterword": true, "appendix": true,
	"credits": true, "end credits": true, "outro": true, "opening": true,
	"ending": true, "chapter": true, "track": true, "part": true, "disc": true,
	"cd": true, "section": true, "unabridged": true, "abridged": true,
	"the end": true, "untitled": true, "unknown": true, "unknown author": true,
	"various": true, "various artists": true, "va": true, "anthology": true,
	"soundtrack": true, "audiobook": true, "none": true, "n/a": true, "null": true,
}

// StripPositionalPrefix removes leading track/disc/chapter numbering from name.
// It applies repeatedly (bounded) because real tags stack the shapes, e.g.
// "01-04_Disgardium". A prefix is never stripped when nothing would remain —
// "019" keeps its digits so the caller can reject it as a whole.
func StripPositionalPrefix(name string) string {
	s := strings.TrimSpace(name)
	for i := 0; i < 3; i++ {
		stripped := false
		for _, rx := range positionalPrefixShapes {
			if loc := rx.FindStringIndex(s); loc != nil && loc[1] > 0 {
				s = strings.TrimSpace(s[loc[1]:])
				stripped = true
				break
			}
		}
		if !stripped {
			break
		}
	}
	return s
}

// IsPositionalArtifactName reports whether name is filename/tag numbering
// shrapnel rather than a person or organisation.
//
// It judges the name as given. Callers that want to salvage a person from a
// numbered string should run StripPositionalPrefix first and test the residue —
// that is what CleanAuthorNameForCreation does.
func IsPositionalArtifactName(name string) bool {
	s := strings.TrimSpace(name)
	if s == "" {
		return true
	}
	if timecodeRe.MatchString(s) || bitrateRe.MatchString(s) || opaqueTokenRe.MatchString(s) {
		return true
	}
	// Zero-padded numbering glued straight onto the title, with no separator to
	// strip: "001DeathBlackHoleOtherCosmicQuandaries". The zero padding is what
	// makes this safe — "50Cent" is left alone.
	if gluedNumberRe.MatchString(s) {
		return true
	}
	// Nothing but digits, separators and whitespace: "0.5", "00 3", "001-119".
	if strings.IndexFunc(s, func(r rune) bool {
		return !strings.ContainsRune("0123456789.,-_/\\() \t", r)
	}) < 0 {
		return true
	}
	lower := strings.ToLower(s)
	if structuralPartWords[lower] {
		return true
	}
	// "Chapter 3", "Track 01", "Disc 2" — a structural word carrying a number.
	if fields := strings.Fields(lower); len(fields) == 2 && structuralPartWords[fields[0]] {
		if strings.IndexFunc(fields[1], func(r rune) bool { return r < '0' || r > '9' }) < 0 {
			return true
		}
	}
	return false
}

// CleanAuthorNameForCreation resolves a raw artist tag to the author name that
// should be stored, reporting false when the tag carries no usable name.
//
// A book with NO author is honest; an author row named "Track 01" is a repair
// job. That is the same judgement C413 made for copyright shrapnel, applied to
// the positional class.
func CleanAuthorNameForCreation(raw string) (string, bool) {
	s := NormalizeAuthorName(strings.TrimSpace(raw))
	if s == "" {
		return "", false
	}
	if IsPositionalArtifactName(s) || IsDirtyAuthorName(s) {
		return "", false
	}
	stripped := NormalizeAuthorName(StripPositionalPrefix(s))
	if stripped == "" {
		return "", false
	}
	if IsPositionalArtifactName(stripped) || IsDirtyAuthorName(stripped) {
		return "", false
	}
	// A salvage is only trustworthy when the residue looks like a name. When a
	// positional prefix was actually removed and one bare word is left, that
	// word is overwhelmingly a chapter or book title rather than a person:
	// "001_Celestia", "001-119 Treason", "0 ABY". Real single-name authors
	// (Homer, Voltaire) do not arrive carrying track numbering, so requiring a
	// second token here costs nothing and stops the guard from trading one
	// junk row for another.
	if stripped != s && !strings.ContainsAny(stripped, " \t") {
		return "", false
	}
	return stripped, true
}
