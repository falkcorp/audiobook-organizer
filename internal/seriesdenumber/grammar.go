// file: internal/seriesdenumber/grammar.go
// version: 1.0.0
// guid: 5e2c7a91-3b48-4d06-9a1f-c70d8e3b6f24
// last-edited: 2026-11-17

package seriesdenumber

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ─── THE POSITIVE GRAMMAR ───────────────────────────────────────────────────
//
// A numbered series name is eligible for an unattended apply only when it is
//
//	<base> <separator> <marker?> <number>
//
// exactly, and the base, separator, marker and number each pass the rules
// below. Everything else is HELD, with the rule that held it named. This
// replaced a deny-list ("matches a number pattern, minus the shapes already
// seen to be wrong"), which let through every wrong shape nobody had listed yet:
// the PR #4684 round-1 review found five in one pass, all of them names that
// matched the number pattern and none of them on the list.
//
// The grammar is read from the series NAME alone. It does not trust the
// planner's base or position (internal/metadata's matcher, a looser reader with
// a different job): Demote compares the two and holds a plan they disagree on.
//
//	number     one to three ASCII digits, value 1..99; leading zeros allowed
//	           ("05"). No decimal point, no range, no year, no roman numeral:
//	           a number the grammar cannot read plainly is held, not guessed.
//	marker     optional, one of Book, Bk, Vol, Volume, No (each with an optional
//	           ".") or "#", compared ignoring case. Any other word before the
//	           number is part of the base, and a base that ends in a word that
//	           names a volume in another language (VolumeWords) is held, not
//	           trusted: add the language to that list rather than guess.
//	separator  what lies between the base and the marker (or number): spaces, or
//	           spaces around one of "-", ":", ",", an en dash or an em dash. A
//	           number glued to a letter ("Hush2") has no separator and is held.
//	base       what is left. It must be non-empty and end in a letter; contain
//	           no digit anywhere (a second number means the name carries two);
//	           not end in a connector (of, to, &, and, ...); have at least
//	           MinBaseLetters letters; not be a bare structural word (chapter,
//	           track, ...); and the name as a whole must contain no part, format
//	           or edition word (Part, Unabridged, Disc, CD, Edition, ...).
//
// Holding is cheap (one manual approval in Repairs later); a wrong move is not.
// Corroboration (decide.go and RunCorroborated) is a separate, second condition.

// Grammar codes name the rule that held a name. Stable strings: they appear in
// the report and in the operator-facing summary.
const (
	CodeNoTrailingNumber  Code = "no_trailing_number"
	CodeNumberOutOfRange  Code = "number_out_of_range"
	CodeFractionalNumber  Code = "fractional_number"
	CodeNoSeparator       Code = "no_separator"
	CodeBaseEmpty         Code = "base_empty"
	CodeBaseNotClean      Code = "base_not_clean"
	CodeBaseHasDigit      Code = "base_has_digit"
	CodeBaseEndsConnector Code = "base_ends_in_connector"
	CodePartOrFormatWord  Code = "part_or_format_word"
	CodeVolumeWordInBase  Code = "volume_word_in_base"
	CodeTinyBase          Code = "base_too_short"
	CodeStructuralBase    Code = "base_is_structural_word"
	CodeNamesDisagree     Code = "planner_disagrees_with_name"
	CodeNoAuthor          Code = "no_author"
	CodePlaceholderAuthor Code = "placeholder_author"
	CodeAuthorBase        Code = "base_is_author_name"
)

// MinBaseLetters is the fewest letters a base name may have. One- and
// two-letter bases ("BW", "DG") are abbreviations too generic to name one
// series.
const MinBaseLetters = 3

// MaxPosition is the largest number a name may carry and still be read as a
// series position. A three-digit number in a series name is nearly always a
// count, a chapter or a track ("Name 201" for part 2, chapter 01).
const MaxPosition = 99

// Parsed is a name that passed the grammar.
type Parsed struct {
	// Base is the series name with the number, marker and separator removed,
	// whitespace collapsed.
	Base string
	// Marker is the marker as written ("Book", "Vol.", "#"), "" when none.
	Marker string
	// Digits is the number as written ("05").
	Digits string
	// N is the number's value, 1..MaxPosition.
	N int
}

// normalizeName trims s and collapses every run of whitespace to one space.
func normalizeName(s string) string { return strings.Join(strings.Fields(s), " ") }

// markers are the closed list of position markers, lower-case, without the
// optional trailing ".".
var markers = map[string]bool{"book": true, "bk": true, "vol": true, "volume": true, "no": true}

// partFormatWords are words of a file or an edition, never of a series. Whole
// words only, so "Discord Knights" and "Cdrom Knights" are not held.
var partFormatWords = map[string]bool{
	"part": true, "parts": true, "pt": true,
	"unabridged": true, "abridged": true,
	"disc": true, "disk": true, "cd": true, "track": true, "tracks": true,
	"file": true, "mp3": true, "m4b": true, "audio": true, "side": true,
	"edition": true, "version": true,
	"episode": true, "chapter": true, "chap": true, "section": true,
}

// VolumeWords are words that mean "volume" or "book" in another language, and
// the English ones the marker list does not take as a marker. A base that ends
// in one is held: "Gotham Knights Tome 1" has the base "Gotham Knights Tome",
// and the next language to appear is not guessed at. Extend the list when a
// dry run shows another.
var VolumeWords = map[string]bool{
	"tome": true, "tomo": true, "tom": true, "band": true, "libro": true, "livre": true,
	"teil": true, "buch": true, "deel": true, "volumen": true, "kniha": true, "bok": true,
	"bog": true, "kirja": true, "volym": true, "numero": true, "number": true,
	"book": true, "bk": true, "vol": true, "volume": true, "no": true,
}

// connectorWords end a base that was cut mid-phrase: "Name 01 of" from
// "Name 01 of 03".
var connectorWords = map[string]bool{
	"of": true, "to": true, "and": true, "or": true, "in": true, "on": true, "at": true,
	"for": true, "from": true, "with": true, "by": true, "vs": true, "the": true, "a": true, "an": true,
}

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

// ParseName reads name against the grammar. On success it returns the parts; on
// failure it returns the rule that held the name.
func ParseName(name string) (Parsed, Verdict, bool) {
	hold := func(c Code, why string) (Parsed, Verdict, bool) {
		return Parsed{}, Verdict{Code: c, Reason: why}, false
	}
	s := normalizeName(name)
	if s == "" {
		return hold(CodeNoTrailingNumber, "the name is empty")
	}

	// A part, format or edition word anywhere in the name marks an audio file
	// or an edition, not a series, whatever punctuation the number sits in.
	for _, w := range words(s) {
		if partFormatWords[w] {
			return hold(CodePartOrFormatWord, "the name carries the part, format or edition word \""+w+"\", which numbers the files or editions of one book, not the volumes of a series")
		}
	}

	// The number: the trailing run of ASCII digits.
	end := len(s)
	start := end
	for start > 0 && s[start-1] >= '0' && s[start-1] <= '9' {
		start--
	}
	if start == end {
		return hold(CodeNoTrailingNumber, "the name does not end in a number")
	}
	digits := s[start:end]
	n := 0
	if len(digits) <= 3 {
		n, _ = strconv.Atoi(digits)
	}
	if len(digits) > 3 || n < 1 || n > MaxPosition {
		return hold(CodeNumberOutOfRange, "the number "+digits+" is outside 1 to "+strconv.Itoa(MaxPosition)+", so it reads as a count, a chapter or a year, not a volume")
	}
	rest := s[:start]

	// A decimal point (".5", "2.5") is a fraction, not the position 5.
	if strings.HasSuffix(rest, ".") {
		if _, glued := markerWord(strings.TrimSuffix(rest, ".")); !glued {
			return hold(CodeFractionalNumber, "a decimal point sits before the number, so it is a fraction, not a position")
		}
	}

	// Optional marker, then the separator.
	pre := strings.TrimRight(rest, " ")
	gap := rest[len(pre):]
	marker := ""
	separated := gap != "" // a space between the base and the number
	if strings.HasSuffix(pre, "#") {
		marker = "#"
		pre = strings.TrimSuffix(pre, "#")
		separated = pre != strings.TrimRight(pre, " ")
	} else if mw, ok := markerWord(strings.TrimSuffix(pre, ".")); ok {
		cut := len(strings.TrimSuffix(pre, ".")) - len(mw)
		marker = pre[cut:]
		pre = pre[:cut]
		separated = pre != strings.TrimRight(pre, " ")
	}
	before := strings.TrimRight(pre, " ")

	// What precedes the marker (or the number) must be a separator: a space, or
	// a space-padded "-", ":", ",", en dash or em dash.
	if r, size := utf8.DecodeLastRuneInString(before); size > 0 && strings.ContainsRune("-:,\u2013\u2014", r) {
		separated = true
		before = strings.TrimRight(before[:len(before)-size], " ")
	}
	if !separated && before != "" {
		return hold(CodeNoSeparator, "the number is glued to the word before it, with no space or punctuation between")
	}

	base := before
	if base == "" {
		return hold(CodeBaseEmpty, "nothing is left of the name once the number is taken off")
	}
	if strings.ContainsFunc(base, unicode.IsDigit) {
		return hold(CodeBaseHasDigit, "the base \""+base+"\" still contains a number, so the name carries two numbers")
	}
	if r, _ := utf8.DecodeLastRuneInString(base); !unicode.IsLetter(r) {
		if r == '&' {
			return hold(CodeBaseEndsConnector, "the base ends in \"&\", so the name was cut mid-phrase")
		}
		return hold(CodeBaseNotClean, "the base \""+base+"\" does not end in a letter")
	}
	bw := words(base)
	if len(bw) > 0 {
		last := bw[len(bw)-1]
		if connectorWords[last] {
			return hold(CodeBaseEndsConnector, "the base ends in the connector \""+last+"\", so the name was cut mid-phrase")
		}
		if VolumeWords[last] {
			return hold(CodeVolumeWordInBase, "the base ends in the volume word \""+last+"\", which is not a marker this grammar reads")
		}
	}
	if letterCount(base) < MinBaseLetters {
		return hold(CodeTinyBase, "the base is an abbreviation of one or two letters, too generic to name one series")
	}
	if isStructural(base) {
		return hold(CodeStructuralBase, "the base is a bare structural word (chap, part, track), not a series")
	}
	return Parsed{Base: base, Marker: marker, Digits: digits, N: n}, Verdict{}, true
}

// markerWord reports whether s ends in a marker word standing alone as a token
// (preceded by a space, punctuation or the start), and returns it.
func markerWord(s string) (string, bool) {
	end := len(s)
	i := end
	for i > 0 {
		r, size := utf8.DecodeLastRuneInString(s[:i])
		if !unicode.IsLetter(r) {
			break
		}
		i -= size
	}
	w := s[i:end]
	if w == "" || !markers[strings.ToLower(w)] {
		return "", false
	}
	return w, true
}

// words splits s into lower-case letter-and-digit tokens.
func words(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
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
	ws := words(base)
	if len(ws) == 0 {
		return false
	}
	for _, w := range ws {
		if !structuralWords[w] {
			return false
		}
	}
	return true
}

// RunCorroborated reports whether the numbers of sibling series support n: the
// run of consecutive whole numbers that contains n, built from n and the
// siblings' numbers, has at least RunMin members, and at least two siblings
// stand behind it. A sibling is another series of the SAME author that passes
// the grammar with the same base and holds at least one book: the caller
// filters, this only reads the numbers. Two unrelated numbers under one base
// ("Name 66", "Name 89") are not a run.
func RunCorroborated(n int, siblings []int) bool {
	have := map[int]bool{n: true}
	others := 0
	for _, s := range siblings {
		if !have[s] {
			others++
		}
		have[s] = true
	}
	if others < 2 {
		return false
	}
	run := 1
	for k := n - 1; have[k]; k-- {
		run++
	}
	for k := n + 1; have[k]; k++ {
		run++
	}
	return run >= RunMin
}

// RunMin is the fewest members of a run of consecutive numbers (n included)
// that corroborates a position by its siblings.
const RunMin = 3
