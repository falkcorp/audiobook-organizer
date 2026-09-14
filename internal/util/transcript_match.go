// file: internal/util/transcript_match.go
// version: 2.0.0
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
// The rule is deliberately STRICT, because the unreviewed paths (auto-fetch,
// metadata.upgrade, an unpinned batch apply) trust it without a human. A
// first version accepted word-boundary containment and found the author's
// surname anywhere in the intro; a review measured what that let through:
// "Mistborn: The Hero of Ages" ~ "Mistborn", "Foundation" ~ "Foundation and
// Empire", "The Witches" ~ "The Witcher", Stephen King ~ "Owen King", and a
// surname read out of "Return of the King". All of those refuse now. A book
// the strict rule cannot confirm is not lost: an owner clicking Apply on its
// review row applies it (applygate.OwnerReviewOverridable).
//
// It lives in util because applygate imports metafetch (and must never be
// imported by it), so the shared code has to be a leaf both already use.

// titleNoise are tokens dropped from titles before comparison: they describe
// the edition, not the book.
var titleNoise = map[string]bool{"unabridged": true}

// numberWords are read as their digits, so "book one" and "book 1" are the
// same title and "Book One" never agrees with "Book 3".
var numberWords = map[string]string{
	"one": "1", "two": "2", "three": "3", "four": "4", "five": "5",
	"six": "6", "seven": "7", "eight": "8", "nine": "9", "ten": "10",
	"eleven": "11", "twelve": "12", "thirteen": "13", "fourteen": "14",
	"fifteen": "15", "sixteen": "16", "seventeen": "17", "eighteen": "18",
	"nineteen": "19", "twenty": "20",
}

// stopTokens carry no identity on their own.
var stopTokens = map[string]bool{
	"a": true, "an": true, "the": true, "of": true, "in": true, "to": true,
	"and": true, "on": true, "for": true, "at": true, "by": true, "or": true,
	"with": true, "from": true,
}

// creditTokens are words Whisper's author field carries around a name
// ("Written by", "Translated from the Polish", "assisted"). They end a name.
var creditTokens = map[string]bool{
	"written": true, "read": true, "narrated": true, "translated": true,
	"author": true, "authors": true, "assisted": true, "presented": true,
	"performed": true, "introduced": true, "edited": true, "copyright": true,
}

// nameSuffixes are ignored when picking an author's surname.
var nameSuffixes = map[string]bool{"jr": true, "sr": true, "ii": true, "iii": true, "iv": true}

// silentPrefixes are onsets whose first letter is silent. Whisper transcribes
// by sound, so "Knaves" comes back as "Naves"; see titleTokenFuzzy.
var silentPrefixes = []string{"kn", "wr", "gn", "pn", "ps"}

// matchTokens lower-cases s, turns every non-letter/non-digit into a space
// and splits. Apostrophes split too ("Sorcerer's" -> "sorcerer", "s"), which
// is harmless because both sides are tokenized the same way.
func matchTokens(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

// titleTokens is matchTokens without titleNoise and with number words as
// digits.
func titleTokens(s string) []string {
	var out []string
	for _, t := range matchTokens(s) {
		if titleNoise[t] {
			continue
		}
		if d, ok := numberWords[t]; ok {
			t = d
		}
		out = append(out, t)
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

// dropSilent removes the silent first letter of a silentPrefixes onset.
func dropSilent(t string) string {
	for _, p := range silentPrefixes {
		if strings.HasPrefix(t, p) {
			return t[1:]
		}
	}
	return t
}

// titleTokenFuzzy reports whether two unequal title tokens are the same word
// misheard. Two shapes only:
//
//   - a silent first letter: "knaves" ~ "naves", "wrath" ~ "rath" (both
//     tokens at least 4 letters once the silent letter is gone). Whisper
//     cannot hear the letter, so this is the one case where the first letter
//     may differ.
//   - one edit inside a long word: both tokens 7+ letters, the same first
//     letter AND the same last letter. The last-letter condition is what
//     keeps "witches" != "witcher" and "kingdom" != "kingdoms": a changed
//     ending is usually a different word, not a typo.
//
// Numbers never fuzz.
func titleTokenFuzzy(a, b string) bool {
	if isNumeric(a) || isNumeric(b) {
		return false
	}
	if sa, sb := dropSilent(a), dropSilent(b); (sa != a || sb != b) && sa == sb && len([]rune(sa)) >= 4 {
		return true
	}
	ra, rb := []rune(a), []rune(b)
	if len(ra) < 7 || len(rb) < 7 || ra[0] != rb[0] || ra[len(ra)-1] != rb[len(rb)-1] {
		return false
	}
	return withinOneEdit(ra, rb)
}

// titleSeqEqual is full-sequence equality with at most ONE fuzzy token, and
// fuzz only in titles of three or more tokens: a one- or two-word title has
// too little context to forgive a misheard word ("The Night" is not "The
// Knight").
func titleSeqEqual(a, b []string) bool {
	if len(a) != len(b) || len(a) == 0 {
		return false
	}
	fuzzy := 0
	for i := range a {
		if a[i] == b[i] {
			continue
		}
		if len(a) < 3 || !titleTokenFuzzy(a[i], b[i]) {
			return false
		}
		fuzzy++
		if fuzzy > 1 {
			return false
		}
	}
	return true
}

// hasPrefixSeq reports whether toks starts with p.
func hasPrefixSeq(toks, p []string) bool {
	if len(p) > len(toks) {
		return false
	}
	for i := range p {
		if toks[i] != p[i] {
			return false
		}
	}
	return true
}

// volumeWords introduce a trailing volume phrase when a number follows.
var volumeWords = map[string]bool{"book": true, "volume": true, "vol": true, "part": true}

// trailerPhrases introduce a trailing description of the book.
var trailerPhrases = [][]string{
	{"a", "short", "story"},
	{"a", "prequel"},
	{"prequel", "to"},
	{"a", "novella"},
}

// stripTranscribedTrailer removes a trailing series/volume phrase Whisper
// read after the title: "This Gilded Abyss, book one of the Gilded Abyss
// trilogy" -> "this gilded abyss"; "Witness to a Trial A short story prequel
// to The Whistler" -> "witness to a trial". It cuts at the first such phrase
// after the first token, so a title cannot be stripped to nothing. Applied to
// the TRANSCRIBED side only: a candidate's title is the provider's record,
// and its own "(Book #4 in the ...)" suffix is not something Whisper said.
func stripTranscribedTrailer(toks []string) []string {
	for i := 1; i < len(toks); i++ {
		if volumeWords[toks[i]] && i+1 < len(toks) && isNumeric(toks[i+1]) {
			return toks[:i]
		}
		for _, p := range trailerPhrases {
			if hasPrefixSeq(toks[i:], p) {
				return toks[:i]
			}
		}
	}
	return toks
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
// (audio-derived) title. Both sides are lower-cased, stripped of punctuation
// and "unabridged", number words read as digits, and tokenized. The
// transcribed side may additionally lose a trailing series/volume phrase
// (stripTranscribedTrailer). They agree only when the WHOLE token sequences
// are equal, with at most one misheard word (titleTokenFuzzy) in a title of
// three or more words. There is no containment: "Mistborn" does not agree
// with "Mistborn: The Hero of Ages", nor "Foundation" with "Foundation and
// Empire". Titles whose numbers disagree never agree.
func TitleAgrees(candidateTitle, transcribedTitle string) bool {
	c, t := titleTokens(candidateTitle), titleTokens(transcribedTitle)
	if len(c) == 0 || len(t) == 0 || numbersConflict(c, t) {
		return false
	}
	return titleSeqEqual(c, t) || titleSeqEqual(c, stripTranscribedTrailer(t))
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

// nameLike is a token that can be part of a person's name.
func nameLike(t string) bool {
	return !stopTokens[t] && !creditTokens[t] && !isNumeric(t)
}

// splitName returns one author's surname (the last name token, ignoring
// jr/sr/ii/iii/iv and initials) and first given name or initial ("" when the
// name has none). "R. A. Salvatore" -> ("salvatore", "r").
func splitName(name string) (surname, given string) {
	toks := matchTokens(name)
	si := -1
	for i := len(toks) - 1; i >= 0; i-- {
		t := toks[i]
		if nameSuffixes[t] || len([]rune(t)) < 2 || !nameLike(t) {
			continue
		}
		si = i
		break
	}
	if si < 0 {
		return "", ""
	}
	for _, t := range toks[:si] {
		if nameLike(t) && !nameSuffixes[t] {
			return toks[si], t
		}
	}
	return toks[si], ""
}

// surnameEqual allows one typo only in surnames of six or more letters with
// the same first letter: "salvatore" ~ "salvator", "gaiman" ~ "gayman", but
// "king" never ~ "kind" and "weir" never ~ "weil".
func surnameEqual(a, b string) bool {
	if a == b {
		return true
	}
	ra, rb := []rune(a), []rune(b)
	if len(ra) < 6 || len(rb) < 6 || ra[0] != rb[0] {
		return false
	}
	return withinOneEdit(ra, rb)
}

// givenEqual compares first names: equal, an initial against a name with the
// same first letter ("r" ~ "robert"), or one typo in names of six or more
// letters with the same first letter.
func givenEqual(a, b string) bool {
	if a == b {
		return true
	}
	ra, rb := []rune(a), []rune(b)
	if len(ra) == 1 || len(rb) == 1 {
		return ra[0] == rb[0]
	}
	return surnameEqual(a, b)
}

// AuthorAgrees reports whether any author in candidateAuthor matches the
// transcribed author field (and ONLY that field: never the intro transcript,
// which also names narrators, translators and titles like "Return of the
// King"). For some candidate author:
//
//   - their surname must equal a token of the transcribed author
//     (surnameEqual), and
//   - when both sides carry a given name or initial, the first one must agree
//     too (givenEqual): Stephen King does not agree with "Owen King". The
//     transcribed given name is the first name-like token of the run of
//     name-like tokens ending at the surname, so "Written by Stephen King"
//     reads "stephen".
//   - when the transcription has no given name before the surname, the
//     surname must END a name (be followed by nothing or a credit/stop word):
//     "Patterson Joseph" is Joseph Patterson's first name read backwards or a
//     different person, not a surname match for James Patterson.
//
// A transcribed author of three characters or fewer (after trimming) carries
// too little to judge and agrees by default, as the rule always has: the
// title match then stands alone.
func AuthorAgrees(candidateAuthor, transcribedAuthor string) bool {
	if len(strings.TrimSpace(transcribedAuthor)) <= 3 {
		return true
	}
	heard := matchTokens(transcribedAuthor)
	for _, name := range splitAuthors(candidateAuthor) {
		sn, given := splitName(name)
		if sn == "" {
			continue
		}
		for j, h := range heard {
			if !surnameEqual(sn, h) {
				continue
			}
			start := j
			for start > 0 && nameLike(heard[start-1]) {
				start--
			}
			if start == j {
				if j+1 < len(heard) && nameLike(heard[j+1]) {
					continue
				}
				return true
			}
			if given == "" || givenEqual(given, heard[start]) {
				return true
			}
		}
	}
	return false
}
