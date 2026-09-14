// file: internal/util/transcript_match.go
// version: 2.2.1
// guid: 5c1e8f27-9a43-4d6b-b0e2-7f3a91c4d856
// last-edited: 2026-09-14

package util

import (
	"regexp"
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
//
// number is the first number in the removed phrase ("1" for "book one of
// ..."), "" when it had none. The heard volume number is identity, not noise:
// "Dune, book two of the Dune Chronicles" is Dune Messiah, not Dune, so the
// caller must check it against the candidate's series position.
func stripTranscribedTrailer(toks []string) (kept []string, number string) {
	for i := 1; i < len(toks); i++ {
		cut := volumeWords[toks[i]] && i+1 < len(toks) && isNumeric(toks[i+1])
		for _, p := range trailerPhrases {
			if !cut && hasPrefixSeq(toks[i:], p) {
				cut = true
			}
		}
		if cut {
			for _, t := range toks[i:] {
				if isNumeric(t) {
					return toks[:i], t
				}
			}
			return toks[:i], ""
		}
	}
	return toks, ""
}

// seriesNumberRe finds the first number (optionally decimal) in a series
// position ("1", "1.0", "Book 4", "#4", "2.5").
var seriesNumberRe = regexp.MustCompile(`[0-9]+(\.[0-9]+)?`)

// numberWordRe matches a whole number word (numberWords' keys).
var numberWordRe = regexp.MustCompile(`(?i)\b(one|two|three|four|five|six|seven|eight|nine|ten|eleven|twelve|thirteen|fourteen|fifteen|sixteen|seventeen|eighteen|nineteen|twenty)\b`)

// canonicalSeriesNumber reduces a number to a comparable form: leading zeros
// and a zero fraction dropped ("01" -> "1", "1.0" -> "1"), a real fraction
// kept ("2.5" stays "2.5", so it never equals a heard "2"). "" when s has no
// number. Number words are read as digits first ("Book Four" -> "4").
func canonicalSeriesNumber(s string) string {
	s = numberWordRe.ReplaceAllStringFunc(s, func(w string) string { return numberWords[strings.ToLower(w)] })
	m := seriesNumberRe.FindString(s)
	if m == "" {
		return ""
	}
	whole, frac, _ := strings.Cut(m, ".")
	whole = strings.TrimLeft(whole, "0")
	if whole == "" {
		whole = "0"
	}
	if frac = strings.TrimRight(frac, "0"); frac != "" {
		return whole + "." + frac
	}
	return whole
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
//
// When the stripped trailer named a volume ("Harry Potter book 2"), that
// number must equal candidateSeriesPosition; a candidate with no position
// does not agree, because nothing says the heard volume is this record.
//
// This is the matcher an OWNER-REVIEWED row apply may annotate with. Paths
// that apply with nobody reviewing must also pass MainTranscriptionConfirms
// (see applygate.TranscriptionConfirms), so they are never looser than the
// rule on origin/main before 2026-09-13.
func TitleAgrees(candidateTitle, candidateSeriesPosition, transcribedTitle string) bool {
	c, t := titleTokens(candidateTitle), titleTokens(transcribedTitle)
	if len(c) == 0 || len(t) == 0 || numbersConflict(c, t) {
		return false
	}
	if titleSeqEqual(c, t) {
		return true
	}
	kept, heardNumber := stripTranscribedTrailer(t)
	if len(kept) == len(t) || !titleSeqEqual(c, kept) {
		return false
	}
	if heardNumber == "" {
		return true
	}
	pos := canonicalSeriesNumber(candidateSeriesPosition)
	return pos != "" && pos == canonicalSeriesNumber(heardNumber)
}

// MainTranscriptionConfirms is applygate.TranscriptionConfirms as it stood on
// origin/main before 2026-09-13 (normalized title equality, then, when the
// transcribed author is longer than three characters, the normalized
// transcribed author as a substring of the normalized candidate author) with
// ONE owner-approved change (2026-09-14): both authors go through foldInitials
// first, so initials compare equal however they are spaced or punctuated.
// "R.A. Salvator" (Whisper) against "R. A. Salvatore" (provider) is the
// motivating case: Sojourn was refused only because "r.a." != "r. a.". The
// folded comparison is a substring test anchored at a token start, so the
// surname tolerance is unchanged and folded initials never match mid-word.
//
// It is the UPPER BOUND for every unreviewed use: metadata.upgrade, an
// unpinned batch apply and the certainty gate itself AND it with the shared
// matcher, so they refuse every pair this refuses, by construction.
func MainTranscriptionConfirms(candidateTitle, candidateAuthor, transcribedTitle, transcribedAuthor string) bool {
	if transcribedTitle == "" || NormalizeTitle(candidateTitle) != NormalizeTitle(transcribedTitle) {
		return false
	}
	if len(transcribedAuthor) <= 3 {
		return true
	}
	// Leg 1 is origin/main verbatim, so nothing it confirmed can refuse.
	if strings.Contains(NormalizeAuthor(candidateAuthor), NormalizeAuthor(transcribedAuthor)) {
		return true
	}
	// Leg 2: the folded forms, anchored at a token boundary. Without the
	// anchor a heard "A. Smith" ("a. smith") would sit inside "R.A. Smith"
	// ("r.a. smith"), a different author main refused.
	return containsAtTokenStart(foldInitials(candidateAuthor), foldInitials(transcribedAuthor))
}

// containsAtTokenStart reports whether sub occurs in s starting at the
// beginning of s or right after a space. Every occurrence is tried.
func containsAtTokenStart(s, sub string) bool {
	if sub == "" {
		return false
	}
	for off := 0; off <= len(s)-len(sub); {
		i := strings.Index(s[off:], sub)
		if i < 0 {
			return false
		}
		at := off + i
		if at == 0 || s[at-1] == ' ' {
			return true
		}
		off = at + 1
	}
	return false
}

// foldInitials is NormalizeAuthor with every run of initials written as one
// canonical token, each letter followed by one dot: "R.A." = "R. A." = "R A"
// = "RA" all become "r.a.", so "R.A. Salvator" folds to "r.a. salvator" and
// "R. A. Salvatore" to "r.a. salvatore". The dots keep a folded run distinct
// from a word: "Ra Salvatore" folds to "ra salvatore", which is not
// "r.a. salvatore". Adjacent initial tokens are joined with no space. Every
// other token is left exactly as NormalizeAuthor writes it (lower-cased,
// punctuation included), so nothing but initials spacing and punctuation is
// forgiven.
//
// Classification rule. A whitespace-separated token is initials only when
// its RAW text, BEFORE lowercasing, is one of:
//   - a token containing dots whose letters are each single and followed by
//     a dot or the end of the token ("R.", "R.A.", "R.A", "r.a.");
//   - a single letter ("R", "r");
//   - a dotless run of two or more letters that are ALL uppercase ("RA",
//     "JRR"), and only when the whole name has a lowercase letter somewhere
//     (in an all-caps "KIM STANLEY" case says nothing, so "KIM" is a word).
//
// A dotless mixed- or lower-case token ("Ra", "Ed", "Jo", "Al", "Kim") is a
// first name, never initials: "Ra Salvatore" is not "R. A. Salvatore", and
// "K. I. M. Stanley" is not "Kim Stanley". The classification must come from
// the raw text because lowercasing erases the only signal that separates
// "RA" from "Ra". Hyphen, apostrophe and any non-ASCII dot ("R-A", "R'A",
// "R․A") make a token a word, so it stays unfolded. The transcribed side
// keeps Whisper's casing all the way here (transcribe.ClassifyIntro ->
// truncateName -> clean never change case), so the rule works on both sides.
//
// Comparison only. NormalizeAuthor keys the Pebble name indexes and must never
// change; do not use this for a key.
func foldInitials(s string) string {
	raw := CollapseSpaces(s)
	if raw == "" {
		return ""
	}
	hasLower := strings.ToUpper(raw) != raw
	toks := strings.Split(raw, " ")
	out := make([]string, 0, len(toks))
	inRun := false
	for _, t := range toks {
		letters, ok := initialLetters(t, hasLower)
		if !ok {
			out = append(out, strings.ToLower(t))
			inRun = false
			continue
		}
		// Canonical initials: every letter followed by one dot ("r.a."). A
		// word never folds to this shape (any raw token of that shape IS
		// initials), so "Ra" ("ra") can never equal "R. A." ("r.a.").
		var b strings.Builder
		for _, r := range strings.ToLower(letters) {
			b.WriteRune(r)
			b.WriteByte('.')
		}
		letters = b.String()
		if inRun {
			out[len(out)-1] += letters
		} else {
			out = append(out, letters)
		}
		inRun = true
	}
	return strings.Join(out, " ")
}

// initialLetters reports whether the RAW (not lower-cased) token t is a token
// of initials under foldInitials' classification rule, and returns its
// letters without the dots ("R.A." -> "RA"). allowUpperRun is false when the
// whole name is written without a lowercase letter; then a dotless run of
// capitals is a word, not initials.
func initialLetters(t string, allowUpperRun bool) (string, bool) {
	rs := []rune(t)
	if len(rs) == 0 {
		return "", false
	}
	for _, r := range rs {
		if r != '.' && !unicode.IsLetter(r) {
			return "", false
		}
	}
	if len(rs) == 1 {
		return t, unicode.IsLetter(rs[0])
	}
	if !strings.Contains(t, ".") {
		// Dotless, two or more letters: initials only if every letter is
		// uppercase ("RA", "JRR"). "Ra", "Ed", "Kim" are first names.
		if !allowUpperRun {
			return "", false
		}
		for _, r := range rs {
			if !unicode.IsUpper(r) {
				return "", false
			}
		}
		return t, true
	}
	// Dotted: each letter single, followed by a dot or the end ("R.A.",
	// "R.A"). "Jr." has two letters in a row, so it is a word.
	var b strings.Builder
	for i := 0; i < len(rs); i++ {
		if !unicode.IsLetter(rs[i]) {
			return "", false
		}
		b.WriteRune(rs[i])
		switch {
		case i+1 == len(rs):
			// The last letter of "R.A".
		case rs[i+1] == '.':
			i++
		default:
			return "", false
		}
	}
	return b.String(), true
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
