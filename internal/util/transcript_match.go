// file: internal/util/transcript_match.go
// version: 1.0.0
// guid: 5c1e8f27-9a43-4d6b-b0e2-7f3a91c4d856
// last-edited: 2026-09-13

package util

import (
	"strings"
	"unicode"
)

// This file is the ONE rule for "does a metadata candidate agree with what
// Whisper heard at the start of the audio". Before 2026-09-13 there were three
// rules for that question and they disagreed:
//
//   - applygate.TranscriptionConfirms demanded exact lower-cased title
//     equality and then required the noisy transcribed author to sit INSIDE
//     the clean candidate author. Whisper appends credit text ("Andrzej
//     Sapkowski Translated from the Polish") and misspells names ("R.A.
//     Salvator"), so that direction refused every real book the owner tried
//     to apply: 11 of 11 on the review lane.
//   - metafetch.transcribedTitleAgrees accepted a raw substring either way,
//     so a one-word title like "It" matched any long transcript.
//   - ApplyMetadataCandidate's audio_confirmed marker had a third inline copy
//     of the first rule.
//
// It lives in util because applygate imports metafetch (and must never be
// imported by it), so the shared code has to be a leaf both already use.

// titleNoise are tokens dropped from titles before comparison: they describe
// the edition, not the book.
var titleNoise = map[string]bool{"unabridged": true}

// stopTokens carry no identity on their own. A token is SIGNIFICANT when it is
// not one of these and is not purely numeric.
var stopTokens = map[string]bool{
	"a": true, "an": true, "the": true, "of": true, "in": true, "to": true,
	"and": true, "on": true, "for": true, "at": true, "by": true, "or": true,
}

// nameSuffixes are ignored when picking an author's surname.
var nameSuffixes = map[string]bool{"jr": true, "sr": true, "ii": true, "iii": true, "iv": true}

// introWindow is how much of the raw intro transcript is searched for an
// author's surname: the credits are read in the first sentences.
const introWindow = 500

// matchTokens lower-cases s, turns every non-letter/non-digit into a space
// and splits. Apostrophes split too ("Sorcerer's" -> "sorcerer", "s"), which
// is harmless because both sides are tokenized the same way.
func matchTokens(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

func titleTokens(s string) []string {
	var out []string
	for _, t := range matchTokens(s) {
		if !titleNoise[t] {
			out = append(out, t)
		}
	}
	return out
}

func isNumeric(t string) bool {
	for _, r := range t {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return t != ""
}

// significant reports whether a token carries identity (see stopTokens).
func significant(t string) bool {
	return !stopTokens[t] && !isNumeric(t)
}

// tokenEqual is token equality with one typo of slack for tokens of five or
// more letters: "naves" ~ "knaves", "salvator" ~ "salvatore". Short tokens
// and numbers must match exactly, so "1" never equals "3".
func tokenEqual(a, b string) bool {
	if a == b {
		return true
	}
	ra, rb := []rune(a), []rune(b)
	if len(ra) < 5 || len(rb) < 5 || isNumeric(a) || isNumeric(b) {
		return false
	}
	return withinOneEdit(ra, rb)
}

// withinOneEdit reports whether a and b differ by at most one insertion,
// deletion or substitution.
func withinOneEdit(a, b []rune) bool {
	if len(a) > len(b) {
		a, b = b, a
	}
	if len(b)-len(a) > 1 {
		return false
	}
	i, j, edits := 0, 0, 0
	for i < len(a) && j < len(b) {
		if a[i] == b[j] {
			i++
			j++
			continue
		}
		edits++
		if edits > 1 {
			return false
		}
		if len(a) == len(b) {
			i++
		}
		j++
	}
	return edits+(len(b)-j)+(len(a)-i) <= 1
}

// seqEqual compares two token sequences with tokenEqual.
func seqEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !tokenEqual(a[i], b[i]) {
			return false
		}
	}
	return true
}

// containsSeq reports whether needle appears contiguously in hay.
func containsSeq(hay, needle []string) bool {
	if len(needle) == 0 || len(needle) > len(hay) {
		return false
	}
	for i := 0; i+len(needle) <= len(hay); i++ {
		if seqEqual(hay[i:i+len(needle)], needle) {
			return true
		}
	}
	return false
}

// strongEnoughToContain is the guard on containment: the contained side needs
// two significant tokens or six letters, so "It" does not match every long
// transcript that happens to contain the word.
func strongEnoughToContain(tokens []string) bool {
	sig, letters := 0, 0
	for _, t := range tokens {
		if significant(t) {
			sig++
		}
		for _, r := range t {
			if unicode.IsLetter(r) {
				letters++
			}
		}
	}
	return sig >= 2 || letters >= 6
}

// numbersConflict is true when both sides carry numbers and share none:
// "Big Cats 1" is not "Big Cats 3" however similar the words are.
func numbersConflict(a, b []string) bool {
	var na, nb []string
	for _, t := range a {
		if isNumeric(t) {
			na = append(na, t)
		}
	}
	for _, t := range b {
		if isNumeric(t) {
			nb = append(nb, t)
		}
	}
	if len(na) == 0 || len(nb) == 0 {
		return false
	}
	for _, x := range na {
		for _, y := range nb {
			if strings.TrimLeft(x, "0") == strings.TrimLeft(y, "0") {
				return false
			}
		}
	}
	return true
}

// TitleAgrees reports whether a candidate title matches a transcribed
// (audio-derived) title. Both are lower-cased, stripped of punctuation and of
// "unabridged", and tokenized. They agree when the token sequences are equal,
// or when one side's sequence appears contiguously, on word boundaries, in the
// other's ("A Cry of Honor" in "A Cry of Honor (Book #4 in the Sorcerer's
// Ring)", "Witness to a Trial" in "Witness to a Trial A short story prequel
// to The Whistler"). Tokens of five or more letters tolerate one typo. The
// contained side must pass strongEnoughToContain, and titles whose numbers
// disagree never match.
func TitleAgrees(candidateTitle, transcribedTitle string) bool {
	c, t := titleTokens(candidateTitle), titleTokens(transcribedTitle)
	if len(c) == 0 || len(t) == 0 || numbersConflict(c, t) {
		return false
	}
	if seqEqual(c, t) {
		return true
	}
	if len(c) < len(t) {
		return strongEnoughToContain(c) && containsSeq(t, c)
	}
	return strongEnoughToContain(t) && containsSeq(c, t)
}

// splitAuthors splits a candidate author field into individual authors on
// "," "&" ";" and " and ".
func splitAuthors(s string) []string {
	s = strings.NewReplacer("&", ",", ";", ",").Replace(s)
	var out []string
	for _, part := range strings.Split(s, ",") {
		for _, name := range splitOnAnd(part) {
			if name = strings.TrimSpace(name); name != "" {
				out = append(out, name)
			}
		}
	}
	return out
}

func splitOnAnd(s string) []string {
	var out []string
	lower := strings.ToLower(s)
	for {
		i := strings.Index(lower, " and ")
		if i < 0 {
			return append(out, s)
		}
		out = append(out, s[:i])
		s, lower = s[i+5:], lower[i+5:]
	}
}

// surname is the last significant token of one author's name, ignoring
// jr/sr/ii/iii/iv and single-letter initials. "" when the name has none.
func surname(name string) string {
	toks := matchTokens(name)
	for i := len(toks) - 1; i >= 0; i-- {
		t := toks[i]
		if nameSuffixes[t] || len([]rune(t)) < 2 || !significant(t) {
			continue
		}
		return t
	}
	return ""
}

// AuthorAgrees reports whether any author in candidateAuthor matches the
// transcribed author: the candidate author's surname must fuzzily equal a
// token of the transcribed author ("R.A. Salvator" confirms "R. A.
// Salvatore"; "Andrzej Sapkowski Translated from the Polish" confirms
// "Andrzej Sapkowski"). When introTranscription is non-empty, the surname
// appearing in its first 500 characters also counts.
//
// A transcribed author of three characters or fewer (after trimming) carries
// too little to judge and agrees by default, as the rule always has: the
// title match then stands alone.
func AuthorAgrees(candidateAuthor, transcribedAuthor, introTranscription string) bool {
	if len(strings.TrimSpace(transcribedAuthor)) <= 3 {
		return true
	}
	heard := matchTokens(transcribedAuthor)
	var intro []string
	if introTranscription != "" {
		r := []rune(introTranscription)
		if len(r) > introWindow {
			r = r[:introWindow]
		}
		intro = matchTokens(string(r))
	}
	for _, name := range splitAuthors(candidateAuthor) {
		sn := surname(name)
		if sn == "" {
			continue
		}
		for _, t := range heard {
			if tokenEqual(sn, t) {
				return true
			}
		}
		for _, t := range intro {
			if tokenEqual(sn, t) {
				return true
			}
		}
	}
	return false
}
