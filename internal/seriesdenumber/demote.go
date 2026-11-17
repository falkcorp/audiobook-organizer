// file: internal/seriesdenumber/demote.go
// version: 1.0.2
// guid: 609bfb39-82de-4bb5-b398-b84db96913fa
// last-edited: 2026-11-16

// Package seriesdenumber holds the pure decisions behind the
// maintenance.series-denumber op: which de-numbering plans may be applied
// unattended (demote.go), which books may actually move once the target series
// is known (decide.go), how an apply is ordered and scoped for a canary
// (select.go) and the undo-journal rows a move writes (journal.go).
//
// It is a package of its own, and not part of internal/plugins/maintenance,
// so these decisions have table tests that compile and run on their own: the
// maintenance test binary is too large to build per change. Nothing here reads
// a store.
package seriesdenumber

import (
	"regexp"
	"strings"
	"unicode"
)

// Code names why a plan was held out of the apply set. The codes are stable
// strings: they appear in the report and in the operator-facing summary.
type Code string

const (
	CodeNoAuthor          Code = "no_author"
	CodeFilePart          Code = "file_part_marker"
	CodeFormatWord        Code = "format_word_in_base"
	CodeHighPosition      Code = "position_100_or_more"
	CodeAuthorBase        Code = "base_is_author_name"
	CodeBaseTrailingDigit Code = "base_ends_in_number"
	CodeTinyBase          Code = "base_too_short"
	CodeStructuralBase    Code = "base_is_structural_word"
	CodeDigitLeadingBase  Code = "base_starts_with_digit"
)

// MaxTrustedPosition is the largest series position a name may carry and still
// be applied unattended. A three-digit number in a series name is nearly always
// a count, a chapter or a track ("Name 201" for part 2, chapter 01), not the
// 100th volume of a series.
const MaxTrustedPosition = 99

// MinBaseLetters is the fewest letters a base name may have. One- and
// two-letter bases ("AM", "RH") are abbreviations too generic to name one
// series.
const MinBaseLetters = 3

// Candidate is what the demotion checks read about one plan.
type Candidate struct {
	// FromName is the numbered series' full name, as stored.
	FromName string
	// Base is the name with the position stripped.
	Base string
	// Position is the parsed position.
	Position int
	// AuthorID is the numbered series' author row, 0 when it has none.
	AuthorID int
	// AuthorName is that author's name, "" when unknown. The base-equals-author
	// check is skipped without it.
	AuthorName string
	// Authors is every author name in the library. A base equal to ANY of them is
	// held, not only the series' own author: "Name 05" under a different author
	// row for the same person (a pen name, a duplicate) is the same mistake. nil
	// skips the check.
	Authors AuthorSet
}

// AuthorSet is a set of author names compared by their letters and digits only,
// so case, spacing and punctuation do not hide a match.
type AuthorSet map[string]struct{}

// NewAuthorSet builds an AuthorSet from names; blank names are skipped.
func NewAuthorSet(names []string) AuthorSet {
	set := make(AuthorSet, len(names))
	for _, n := range names {
		if k := alnum(n); k != "" {
			set[k] = struct{}{}
		}
	}
	return set
}

// Has reports whether name is in the set.
func (a AuthorSet) Has(name string) bool {
	k := alnum(name)
	if k == "" {
		return false
	}
	_, ok := a[k]
	return ok
}

// Verdict is a reason a plan must not be applied.
type Verdict struct {
	Code   Code
	Reason string
}

// formatWord matches vocabulary of an audio file or an edition, never of a
// series: "Unabridged", "Disc 2", "CD1", "Track", "Audio". Whole words only, so
// "Discworld" and "Cdr" do not match.
var formatWord = regexp.MustCompile(`(?i)\b(?:unabridged|abridged|disc|disk|cd|track|file|mp3|m4b|audio|side)\b`)

// partSuffix matches a trailing "Part N" / "Pt N" marker.
var partSuffix = regexp.MustCompile(`(?i)\b(?:part|pt)\s*\.?\s*\d{1,3}\s*$`)

// structuralWords are words that, standing alone as a base, name a slice of a
// file or a book and not a series: the base of "chap-01" is "chap".
var structuralWords = map[string]bool{
	"chap": true, "chapter": true, "chapters": true, "ch": true,
	"ep": true, "episode": true, "episodes": true,
	"part": true, "parts": true, "pt": true,
	"track": true, "tracks": true, "disc": true, "disk": true, "cd": true,
	"file": true, "side": true, "section": true,
	"volume": true, "vol": true, "book": true, "bk": true,
	"no": true, "number": true,
	"audio": true, "unabridged": true, "abridged": true,
}

// Demote returns a verdict when the plan describes a shape that reads as a
// series position but is not one, or that cannot be applied safely. The first
// matching check wins; they are ordered most specific first.
//
// Each check is a shape found in the 2026-11-16 production dry run, where the
// "high" tier carried them all. A plan that passes is not thereby right: it is
// only not one of the known wrong shapes.
func Demote(c Candidate) (Verdict, bool) {
	base := strings.TrimSpace(c.Base)
	switch {
	case c.AuthorID == 0:
		return Verdict{CodeNoAuthor, "the series has no author, so every no-author series of this name would fold into one base"}, true

	case partSuffix.MatchString(c.FromName):
		return Verdict{CodeFilePart, "a trailing \"Part N\" numbers the audio files of one book, not the volumes of a series"}, true

	case formatWord.MatchString(base):
		return Verdict{CodeFormatWord, "the base still carries a format or edition word (Unabridged, Disc, CD, Audio)"}, true

	case c.Position > MaxTrustedPosition:
		return Verdict{CodeHighPosition, "a position of 100 or more reads as a count or chapter, not a volume"}, true

	case sameName(base, c.AuthorName) || c.Authors.Has(base):
		return Verdict{CodeAuthorBase, "the base is an author's name, not a series"}, true

	case endsInDigit(base):
		return Verdict{CodeBaseTrailingDigit, "the base still ends in a number, so the name carries two numbers"}, true

	case letterCount(base) < MinBaseLetters:
		return Verdict{CodeTinyBase, "the base is an abbreviation of one or two letters, too generic to name one series"}, true

	case isStructural(base):
		return Verdict{CodeStructuralBase, "the base is a bare structural word (chap, part, track), not a series"}, true

	case startsWithDigit(base):
		return Verdict{CodeDigitLeadingBase, "the base starts with a digit, so a leading track number survived in it"}, true
	}
	return Verdict{}, false
}

// sameName reports whether a and b are the same words ignoring case,
// punctuation and spacing. An empty b never matches.
func sameName(a, b string) bool {
	na, nb := alnum(a), alnum(b)
	return nb != "" && na == nb
}

// alnum lowercases s and keeps only its letters and digits.
func alnum(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(unicode.ToLower(r))
		}
	}
	return b.String()
}

func endsInDigit(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	runes := []rune(s)
	return unicode.IsDigit(runes[len(runes)-1])
}

func startsWithDigit(s string) bool {
	for _, r := range s {
		if unicode.IsSpace(r) {
			continue
		}
		return unicode.IsDigit(r)
	}
	return false
}

func letterCount(s string) int {
	n := 0
	for _, r := range s {
		if unicode.IsLetter(r) {
			n++
		}
	}
	return n
}

// isStructural reports whether every word of base is a structural word.
func isStructural(base string) bool {
	words := strings.FieldsFunc(strings.ToLower(base), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	if len(words) == 0 {
		return false
	}
	for _, w := range words {
		if !structuralWords[w] {
			return false
		}
	}
	return true
}
