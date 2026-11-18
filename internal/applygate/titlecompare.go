// file: internal/applygate/titlecompare.go
// version: 2.2.0
// guid: 8e41c7a2-5d09-4b3f-a6e8-2f9c0d71b5a3
// last-edited: 2026-11-17

package applygate

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"

	"github.com/falkcorp/audiobook-organizer/internal/seqnum"
)

// Title comparison for the evidence leg's title check.
//
// The check used to score agreement 1.0 when the candidate's main title (the
// part before ":") equalled ANY segment of the stored title. Two volumes of one
// storyline share that segment ("Hush: Part 1" and "Hush: Part 2", "Batman:
// Dark Victory" and "Batman: The Long Halloween"), so with the runtime inside
// 5% and the author agreeing the pair applied unattended. A first rewrite
// compared the whole title but decided agreement by REMOVING text (series
// names, credited names, "of N", a parenthetical, fuzzy word overlap), and
// every removal was a new way for two books to agree. This file and
// titleforms.go have one rule and remove nothing:
//
//   - AGREE (decided first, in titleforms.go and checkTitle): a whole form of
//     the candidate EQUALS a whole form of the book. A form is declared fields
//     JOINED, never trimmed: the title, the title and subtitle, each also behind
//     the series name, and for a candidate with no real subtitle the series,
//     position and credit forms titleforms.go lists, each in a fixed role. A
//     side that has a subtitle has no bare-title forms, so "Knightfall" against
//     "Knightfall" + subtitle "Book 2" is not an agreement. The book's file and
//     folder names are forms of the book only when its stored title is blank or
//     a placeholder. Equality is over a closed normalisation (formTokens) and
//     nothing else.
//   - DISAGREE: only from the two TITLE fields, only when no form agreed, and
//     only in two shapes: the titles share a whole segment and each carries a
//     segment the other lacks ("Batman: Dark Victory" / "Batman: The Long
//     Halloween"), or each states a number of the same KIND (a volume, a part;
//     seqnum.ReadStated) and the numbers differ ("Part 1" / "Part 2", "Hush 1" /
//     "Hush 2"). A number only one side states is not a conflict ("Who Am I" /
//     "Who Am I 2"). A series name, a credit, a path or a year never makes a
//     pair disagree: a year in brackets ("Knightfall (1993)"), a year only one
//     title carries and a leading list number ("08 - ") are set aside before the
//     conflict tests read the text (comparableText), and ordinals beside a
//     marker read as the cardinal ("Third Book" is "Book 3").
//   - everything else that is related is NEUTRAL: one title holds words or a
//     number the other lacks, so the pair is confirmed by nothing in the title.
//     A neutral title applies only with a runtime that agrees (EvidenceVerdict
//     tally).
//
// compareTitleFields is symmetric in its two texts EXCEPT for the last step:
// a stored title whose words are all in the candidate's title (the candidate
// adds a franchise or a subtitle) is neutral at any size, while a candidate
// that is a few words of a longer stored title must share half the words to be
// neutral, because it is another book of the same name's family. No remainder
// is ever excused by what the other fields say. The whole check is symmetric
// only in the declared title fields: the candidate-only forms (credit,
// position) and a self-titled candidate (checkTitle) are one-sided on purpose
// (titleforms.go).

// titleClass is the verdict on one pair of title fields.
type titleClass int

const (
	// classConflictNumber: the same words with numbers that differ ("Part 1"
	// / "Part 2", "Night of 7 Owls" / "Night of 5 Owls").
	classConflictNumber titleClass = iota
	// classConflictWords: a whole segment is shared and each title has a
	// segment the other lacks.
	classConflictWords
	// classUnrelated: little in common, no structure to compare.
	classUnrelated
	// classNeutral: related, but one side says something the other does not.
	classNeutral
	// classEqual: the two fields normalise to the same words.
	classEqual
)

// titleCmp is one pair's verdict with a short reason for the check detail.
type titleCmp struct {
	class titleClass
	note  string
}

var (
	// titleTokenRe keeps "1.5" whole and splits everything else on
	// non-alphanumerics.
	titleTokenRe = regexp.MustCompile(`\d+\.\d+|[\p{L}\p{N}]+`)
	// romanRe is I..XXXIX over the letters I, V, X only: "mix" and "civil" are
	// words, "vi" and "xii" are numerals.
	romanRe = regexp.MustCompile(`^(?:x{0,3})(?:ix|iv|v?i{0,3})$`)
	// ordinalDigitRe is "1st", "22nd", "3rd", "4th".
	ordinalDigitRe = regexp.MustCompile(`^(\d+)(?:st|nd|rd|th)$`)

	unitWords = map[string]int{
		"one": 1, "two": 2, "three": 3, "four": 4, "five": 5, "six": 6, "seven": 7, "eight": 8, "nine": 9,
		"ten": 10, "eleven": 11, "twelve": 12, "thirteen": 13, "fourteen": 14, "fifteen": 15,
		"sixteen": 16, "seventeen": 17, "eighteen": 18, "nineteen": 19,
	}
	tensWords = map[string]int{
		"twenty": 20, "thirty": 30, "forty": 40, "fifty": 50, "sixty": 60, "seventy": 70, "eighty": 80, "ninety": 90,
	}
	ordinalWords = map[string]int{
		"first": 1, "second": 2, "third": 3, "fourth": 4, "fifth": 5, "sixth": 6, "seventh": 7, "eighth": 8,
		"ninth": 9, "tenth": 10, "eleventh": 11, "twelfth": 12, "thirteenth": 13, "fourteenth": 14,
		"fifteenth": 15, "sixteenth": 16, "seventeenth": 17, "eighteenth": 18, "nineteenth": 19, "twentieth": 20,
	}
	// numberMarkers are the words a single I, V or X is read as a numeral
	// behind ("Part I"); anywhere else a lone I is the pronoun, a lone V or X
	// an initial.
	numberMarkers = map[string]bool{
		"part": true, "pt": true, "book": true, "bk": true, "volume": true, "vol": true, "chapter": true,
		"ch": true, "disc": true, "disk": true, "cd": true, "track": true, "episode": true, "act": true,
		"season": true, "no": true, "number": true,
	}
	// ordinalMarkers are the markers an ordinal beside them is a number of
	// ("the Third Book", "Part the First"); seqnum's ordinal reader has the same
	// four.
	ordinalMarkers = map[string]bool{"book": true, "volume": true, "vol": true, "part": true}
	// leadingArticles are dropped from the front of a segment.
	leadingArticles = map[string]bool{"the": true, "a": true, "an": true}
	// editionPhrases is the closed list of words that describe the recording,
	// not the book, removed wherever they stand. "abridged" is NOT on it: an
	// abridged recording is a different edition.
	editionPhrases = [][]string{{"a", "novel"}, {"the", "audiobook"}, {"audiobook"}, {"unabridged"}, {"novel"},
		// The two placeholder phrases a tagger writes where a credit would be.
		{"unknown", "author"}, {"read", "by", "narrator"}}
	// fieldStop are the words a remainder is not counted for: they join a title
	// together and carry no identity.
	fieldStop = map[string]bool{"the": true, "a": true, "an": true, "of": true, "and": true}
)

// cardTok is the token for a cardinal number; ordTok for an ordinal. They never
// collide with a word token (the tokenizer cannot produce '#' or '@') and an
// ordinal is never equal to a cardinal.
func cardTok(v string) string { return "#" + v }
func ordTok(v int) string     { return "@" + strconv.Itoa(v) }

func isNumTok(t string) bool { return strings.HasPrefix(t, "#") }

// segTokens normalises ONE segment of a title into tokens: NFKC (so a unicode
// roman numeral "Ⅱ" is "II"), lower case, diacritics and apostrophes removed,
// "&" read as "and", numbers read as values, and a leading article dropped.
// strip also removes the edition words (editionPhrases); a segment that is only
// edition words ("(Unabridged)") is then empty.
func segTokens(s string, strip bool) []string {
	s = strings.ToLower(norm.NFKC.String(s))
	s = strings.NewReplacer("&", " and ", "#", " ", "'", "", "\u2019", "").Replace(s)
	var b strings.Builder
	for _, r := range norm.NFD.String(s) {
		if !unicode.Is(unicode.Mn, r) {
			b.WriteRune(r)
		}
	}
	raw := titleTokenRe.FindAllString(b.String(), -1)
	out := make([]string, 0, len(raw))
	for i := 0; i < len(raw); i++ {
		t := raw[i]
		switch {
		case isDecimalOrDigits(t):
			if v, err := strconv.ParseFloat(t, 64); err == nil {
				out = append(out, cardTok(strconv.FormatFloat(v, 'f', -1, 64)))
				continue
			}
		case ordinalDigitRe.MatchString(t) || ordinalWords[t] > 0:
			v := ordinalWords[t]
			if v == 0 {
				v, _ = strconv.Atoi(ordinalDigitRe.FindStringSubmatch(t)[1])
			}
			// "the Third Book" says what "Book 3" says: an ordinal beside a
			// volume or part marker is that marker's number (seqnum reads it
			// the same way), so the two spellings are one token sequence.
			if i+1 < len(raw) && ordinalMarkers[raw[i+1]] {
				out = append(out, raw[i+1], cardTok(strconv.Itoa(v)))
				i++
				continue
			}
			out = append(out, ordTok(v))
			continue
		case ordinalMarkers[t] && i+2 < len(raw) && raw[i+1] == "the" && (ordinalWords[raw[i+2]] > 0 || ordinalDigitRe.MatchString(raw[i+2])):
			// "Book the Third".
			v := ordinalWords[raw[i+2]]
			if v == 0 {
				v, _ = strconv.Atoi(ordinalDigitRe.FindStringSubmatch(raw[i+2])[1])
			}
			out = append(out, t, cardTok(strconv.Itoa(v)))
			i += 2
			continue
		case tensWords[t] > 0:
			v := tensWords[t]
			// "twenty-one" / "twenty one": a tens word and a unit word are one
			// number.
			if i+1 < len(raw) && unitWords[raw[i+1]] >= 1 && unitWords[raw[i+1]] <= 9 {
				v += unitWords[raw[i+1]]
				i++
			}
			out = append(out, cardTok(strconv.Itoa(v)))
			continue
		case unitWords[t] > 0:
			out = append(out, cardTok(strconv.Itoa(unitWords[t])))
			continue
		case len(t) >= 2 && romanRe.MatchString(t):
			out = append(out, cardTok(strconv.Itoa(romanValue(t))))
			continue
		case (t == "i" || t == "v" || t == "x") && len(out) > 0 && numberMarkers[out[len(out)-1]]:
			out = append(out, cardTok(strconv.Itoa(romanValue(t))))
			continue
		}
		out = append(out, t)
	}
	if strip {
		out = stripEdition(out)
	}
	if len(out) > 1 && leadingArticles[out[0]] {
		out = out[1:]
	}
	return out
}

// stripEdition removes the edition phrases wherever they stand.
func stripEdition(toks []string) []string {
	for changed := true; changed; {
		changed = false
		for _, ph := range editionPhrases {
			for i := 0; i+len(ph) <= len(toks); i++ {
				if slices.Equal(toks[i:i+len(ph)], ph) {
					toks = append(slices.Clone(toks[:i]), toks[i+len(ph):]...)
					changed = true
					break
				}
			}
		}
	}
	return toks
}

func isDecimalOrDigits(s string) bool {
	dot := false
	for i, r := range s {
		switch {
		case unicode.IsDigit(r):
		case r == '.' && !dot && i > 0:
			dot = true
		default:
			return false
		}
	}
	return s != ""
}

func romanValue(s string) int {
	vals := map[byte]int{'i': 1, 'v': 5, 'x': 10}
	total := 0
	for i := 0; i < len(s); i++ {
		v := vals[s[i]]
		if i+1 < len(s) && v < vals[s[i+1]] {
			total -= v
		} else {
			total += v
		}
	}
	return total
}

// titleSegments splits a title field on ":", a spaced dash and brackets and
// normalises each piece. Empty pieces are dropped. When every piece is
// edition words the title is kept as written rather than emptied.
func titleSegments(s string) [][]string {
	parts := segSplitRe.Split(s, -1)
	for _, strip := range []bool{true, false} {
		var out [][]string
		for _, part := range parts {
			if toks := segTokens(part, strip); len(toks) > 0 {
				out = append(out, toks)
			}
		}
		// A title that is nothing but an edition word ("Novel") keeps it.
		if len(out) > 0 {
			return out
		}
	}
	return nil
}

// formTokens is the normalised word sequence of a whole title field: its
// segments in order.
func formTokens(s string) []string {
	var out []string
	for _, seg := range titleSegments(s) {
		out = append(out, seg...)
	}
	return out
}

// fieldText is a title field and its subtitle as one text, the subtitle a
// segment of its own.
func fieldText(title, subtitle string) string {
	if strings.TrimSpace(subtitle) == "" {
		return title
	}
	return title + ": " + subtitle
}

// words returns the identity words (not numbers, not joining words) and the
// sorted cardinal numbers of a token sequence.
func wordsAndNums(toks []string) (map[string]bool, []string) {
	words := map[string]bool{}
	var nums []string
	for _, t := range toks {
		switch {
		case isNumTok(t):
			nums = append(nums, t)
		case !fieldStop[t]:
			words[t] = true
		}
	}
	slices.SortFunc(nums, func(a, b string) int {
		x, _ := strconv.ParseFloat(a[1:], 64)
		y, _ := strconv.ParseFloat(b[1:], 64)
		switch {
		case x < y:
			return -1
		case x > y:
			return 1
		}
		return 0
	})
	return words, nums
}

func subsetOf(a, b map[string]bool) bool {
	for w := range a {
		if !b[w] {
			return false
		}
	}
	return true
}

func intersects(a, b map[string]bool) bool {
	for w := range a {
		if b[w] {
			return true
		}
	}
	return false
}

// compareTitleFields classifies two TITLE fields (each a title and, when the
// side has one, its subtitle): a is the candidate's, b the stored title. The
// result is the same with the two swapped except for the subset bound at the
// end (see the header). It decides nothing about agreement beyond classEqual: the check agrees by
// form equality (checkTitle), and this only separates a conflict from a pair
// that merely is not confirmed.
func compareTitleFields(a, b string) titleCmp {
	a, b = comparableText(a), comparableText(b)
	sa, sb := titleSegments(a), titleSegments(b)
	if len(sa) == 0 || len(sb) == 0 {
		return titleCmp{class: classUnrelated}
	}
	fa, fb := formTokens(a), formTokens(b)
	if slices.Equal(fa, fb) {
		return titleCmp{class: classEqual}
	}
	wa, na := wordsAndNums(fa)
	wb, nb := wordsAndNums(fb)

	// The same words with numbers that differ: another book of the same name.
	// The numbers are read by the one sequence number reader (seqnum), which
	// says what each number numbers; the title conflicts when both sides state a
	// number of the SAME kind and the two differ. Nothing excuses it: "Part 1 of
	// 3" and "Part 3" state part 1 and part 3, "Books 1-3" and "Book 1" state a
	// range and a volume, and one side holding the other's numbers among more
	// of its own settles nothing. A number only one side states is not compared
	// (a volume the book's own title leaves out is the sequence leg's to check).
	related := intersects(wa, wb) || (len(wa) == 0 && len(wb) == 0)
	if related {
		if note := numberConflict(a, b, na, nb); note != "" {
			return titleCmp{class: classConflictNumber, note: note}
		}
	}

	// A whole segment in common and a segment of its own on each side.
	remA, remB, shared := unmatchedSegments(sa, sb)
	if shared && len(remA) > 0 && len(remB) > 0 {
		return titleCmp{class: classConflictWords, note: "both share a segment, then continue differently: " +
			strconv.Quote(strings.Join(remA[0], " ")) + " vs " + strconv.Quote(strings.Join(remB[0], " "))}
	}
	if shared {
		return titleCmp{class: classNeutral, note: "one title continues past the text both share"}
	}

	switch {
	// b is the stored title: a candidate that ADDS words to the stored title
	// (a franchise prefix, a subtitle the stored one lacks) is neutral at any
	// size. One that is a few words OF a longer stored title is another book
	// of the same name's family, and must share half the words (below): a
	// candidate "Gotham" for a stored "Gotham Knights Detective Tales" is not
	// confirmed by it.
	case len(wa) > 0 && len(wb) > 0 && subsetOf(wb, wa):
		return titleCmp{class: classNeutral, note: "the candidate holds words or a number the stored title lacks"}
	case tokenOverlap(fa, fb) >= 0.5:
		return titleCmp{class: classNeutral, note: "the titles share part of their words"}
	}
	return titleCmp{class: classUnrelated}
}

var (
	// yearTagRe is a year alone in brackets or parentheses, the way a tagger or
	// a folder writes a release year: "Knightfall (1993)".
	yearTagRe = regexp.MustCompile(`\s*[(\[]\s*(?:1\d{3}|20\d{2}|2100)\s*[)\]]`)
)

// comparableText is a title field as the conflict tests read it: without a year
// in brackets and without a leading list number ("08 - "). Both are a file's
// or a tagger's, not the book's: seqnum's own reader already sets the year aside
// and reads the list number as the weak number it is, and the conflict tests
// must not see more than it does. Only the conflict tests read this text; the
// forms (titleforms.go) are built from the fields as declared.
func comparableText(s string) string {
	s = yearTagRe.ReplaceAllString(s, "")
	if n, ok := seqnum.Parse(s); ok && n.Form == "leading" {
		s = leadingNumberRe.ReplaceAllString(s, "")
	}
	return s
}

// yearLike reports whether a cardinal token is a four-digit year (1000-2100).
func yearLike(t string) bool {
	v, err := strconv.ParseFloat(t[1:], 64)
	return err == nil && v == float64(int(v)) && v >= 1000 && v <= 2100
}

// withoutOneSidedYears drops the year-like numbers of the side that has them
// when the other side has none: a year only one title carries is a release
// date or a tag, not a number that differs. Two titles that each carry one
// ("Metro 2033" / "Metro 2034") are numbered by them, and the pair stays a
// conflict.
func withoutOneSidedYears(na, nb []string) ([]string, []string) {
	years := func(ns []string) bool { return slices.ContainsFunc(ns, yearLike) }
	if ya, yb := years(na), years(nb); ya != yb {
		drop := func(ns []string) []string {
			return slices.DeleteFunc(slices.Clone(ns), yearLike)
		}
		if ya {
			na = drop(na)
		} else {
			nb = drop(nb)
		}
	}
	return na, nb
}

// tokenOverlap is the share of identity tokens two normalised token sequences
// have in common: |A and B| / max(|A|, |B|) over the tokens that are not
// titleStop words, 0 when they share only numbers and either side has a word
// ("Book 1" / "Part 1" share the 1 and nothing else). It reads the tokens
// formTokens made, so a number is the same token however it is spelled ("one",
// "I" and "1"); titleSim, which reads raw text, scored "Year One" against "Year
// I" differently from "Year 1" and made the class depend on the spelling.
func tokenOverlap(a, b []string) float64 {
	set := func(toks []string) map[string]bool {
		m := map[string]bool{}
		for _, t := range toks {
			if !titleStop[t] {
				m[t] = true
			}
		}
		return m
	}
	A, B := set(a), set(b)
	if len(A) == 0 || len(B) == 0 {
		return 0
	}
	inter, shareWord := 0, false
	for t := range A {
		if B[t] {
			inter++
			if !isNumTok(t) {
				shareWord = true
			}
		}
	}
	if !shareWord {
		// Checked before the stop words are dropped, as titleSim does: "Book 1"
		// has a word (book), so sharing only the 1 with "Part 1" is no match.
		for _, toks := range [][]string{a, b} {
			for _, t := range toks {
				if !isNumTok(t) {
					return 0
				}
			}
		}
	}
	return float64(inter) / float64(max(len(A), len(B)))
}

// unmatchedSegments pairs equal segments of a and b (each used once) and
// returns what is left on each side. shared reports whether any pair matched.
func unmatchedSegments(a, b [][]string) (remA, remB [][]string, shared bool) {
	used := make([]bool, len(b))
	for _, x := range a {
		hit := false
		for j, y := range b {
			if !used[j] && slices.Equal(x, y) {
				used[j], hit, shared = true, true, true
				break
			}
		}
		if !hit {
			remA = append(remA, x)
		}
	}
	for j, y := range b {
		if !used[j] {
			remB = append(remB, y)
		}
	}
	return remA, remB, shared
}

// statedClaims splits one text's stated numbers (seqnum) by kind.
type statedClaims struct {
	volume, part []seqnum.Number
}

func claimsOf(stated []seqnum.Stated) statedClaims {
	var c statedClaims
	for _, st := range stated {
		if st.Kind == seqnum.KindPart {
			c.part = append(c.part, st.Number)
		} else {
			c.volume = append(c.volume, st.Number)
		}
	}
	return c
}

// sameNumbers reports whether two lists name the same numbers, in any order.
func sameNumbers(a, b []seqnum.Number) bool {
	if len(a) != len(b) {
		return false
	}
	used := make([]bool, len(b))
	for _, x := range a {
		hit := false
		for j, y := range b {
			if !used[j] && seqnum.Equal(x, y) {
				used[j], hit = true, true
				break
			}
		}
		if !hit {
			return false
		}
	}
	return true
}

func numberTexts(ns []seqnum.Number) string {
	parts := make([]string, len(ns))
	for i, n := range ns {
		parts[i] = n.Text
	}
	return strings.Join(parts, ",")
}

// subMultiset reports whether every element of a is in b, counting repeats.
func subMultiset(a, b []string) bool {
	count := map[string]int{}
	for _, x := range b {
		count[x]++
	}
	for _, x := range a {
		if count[x] == 0 {
			return false
		}
		count[x]--
	}
	return true
}

// numberConflict says why two title fields state numbers that differ ("" when
// they do not). Two checks, in this order:
//
//  1. Both state a number of one kind (a volume, a part) and the two differ.
//     Nothing excuses it: one side holding the other's numbers among more of
//     its own settles nothing ("Part 1 of 3" against "Part 3").
//  2. The numbers neither reader recognises as stated (the "One" of a name,
//     "Night of 7 Owls", a number the reader does not see in "Court 1 (Long)")
//     are compared as they always were: they conflict when both sides have
//     some and neither side's holds the other's, since a title that adds words
//     adds their numbers. A year-like number (1000-2100) that only one side
//     carries is a release date or a tag, not a number that differs
//     ("Knightfall 1993" / "Knightfall: Book 1"); two titles that each carry
//     one are numbered by them ("Gotham 2033" / "Gotham 2034").
//
// A number only one side states (a volume the book's own title leaves out) is
// not compared: that is the sequence leg's to check.
func numberConflict(a, b string, na, nb []string) string {
	sa, sb := seqnum.ReadPair(norm.NFKC.String(a), norm.NFKC.String(b))
	ca, cb := claimsOf(sa), claimsOf(sb)
	switch {
	case len(ca.volume) > 0 && len(cb.volume) > 0 && !sameNumbers(ca.volume, cb.volume):
		return "same words, different volumes (" + numberTexts(ca.volume) + " vs " + numberTexts(cb.volume) + ")"
	case len(ca.part) > 0 && len(cb.part) > 0 && !sameNumbers(ca.part, cb.part):
		return "same words, different parts (" + numberTexts(ca.part) + " vs " + numberTexts(cb.part) + ")"
	}
	na, nb = withoutOneSidedYears(na, nb)
	if len(na) > 0 && len(nb) > 0 && !subMultiset(na, nb) && !subMultiset(nb, na) {
		return "same words, different numbers (" + numsText(na) + " vs " + numsText(nb) + ")"
	}
	return ""
}

func numsText(ns []string) string {
	parts := make([]string, len(ns))
	for i, n := range ns {
		parts[i] = n[1:]
	}
	return strings.Join(parts, ",")
}
