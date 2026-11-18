// file: internal/seqnum/stated.go
// version: 1.3.1
// guid: ef7d6ebc-a3a1-4738-bc60-512ed593d8eb
// last-edited: 2026-11-18

package seqnum

import (
	"strings"
)

// Stated numbers: every number a title says, with what it numbers.
//
// Parse answers one question for the sequence leg of the bulk-apply gate: which
// volume of a series does this text name? It deliberately does not see "Part 2"
// or "Disc 3" (they split ONE book into pieces and place nothing in a series),
// nor "N of M". The title check asks a different question: do two titles state
// the same number of the same kind? "Hush, Part 1 of 3" and "Hush, Part 3" state
// a part number each, and they differ. ReadStated answers it with the same
// number reader (the same word, roman and arabic numbers, the same ranges, the
// same noise stripped) instead of a second reader of its own, and leaves Parse
// and every caller of it untouched.
//
// Kinds:
//
//   - KindVolume: a place in a series. Everything Parse reads from a title
//     ("Book 2", "Vol. III", "#3", "02 - Title", "Big Cats 1", "Books 1-3"),
//     a number between title segments (ParseTitle), "N of M" ("Hush 1 of 2" is
//     position 1; the 2 is a total and states nothing), an ordinal word beside
//     a volume marker ("the Second Book", "Book the Third"), and a lone trailing
//     "I" ("Hush I") when ReadPair is told the other text ends in a numeral.
//   - KindPart: one piece of one book. "Part 2", "Pt. II", "Disc 3", "CD 1",
//     "(2 of 3)", "Part 1 of 3", an ordinal beside "part" ("the First Part"). A
//     marker that opens a range ("Disc 1-3") is every part, not one, and states
//     no number.
//
// A year, a bitrate and a chapter or track number are not stated numbers.

// Kind says what a stated number numbers.
type Kind int

const (
	// KindVolume is a place in a series.
	KindVolume Kind = iota
	// KindPart is one piece of a single book.
	KindPart
)

// Stated is one number a text states.
type Stated struct {
	Kind Kind
	// Number is the number as Parse reads it (Value, Last for a range, Text).
	Number Number
	// Of is the M of "N of M", 0 when the text gives none. It is a total, not
	// a number the text states.
	Of float64
	// Marker is the word a part was written with, normalised: "part" (part,
	// parts, pt), "disc" (disc, disk), "cd", or "of" for a bare "(2 of 3)".
	// A disc of a part and a part are different levels, and a caller that
	// compares two parts of one book needs to know which level each names.
	// Empty for a volume.
	Marker string
}

var (
	// A part marker with its number, an optional "of M", and the tail that
	// would make it a range of parts.
	rePartStated = guard(`\b(?:parts?|pts?|discs?|disks?|cds?)\.?\s*`+numAlt+`\b(?:\s*(?:of|/)\s*(\d+)\b)?(\s*(?:[-–—]|to|thru|through|&|and)\s*(?:\d+|[ivxlc]+)\b)?`,
		hasAny("part", "pt", "disc", "disk", "cd"))
	// "(2 of 3)": a parenthesised N of M is a part.
	reParenOf = guard(`\(\s*(\d+)\s+of\s+(\d+)\s*\)`, all(hasAny("("), hasAny("of"), hasDigit))
	// "1 of 2": a position in a set of M.
	reBareOf = guard(`\b(\d+)\s+of\s+(\d+)\b`, all(hasAny("of"), hasDigit))

	ordAlt       = `(first|second|third|fourth|fifth|sixth|seventh|eighth|ninth|tenth|eleventh|twelfth|thirteenth|fourteenth|fifteenth|sixteenth|seventeenth|eighteenth|nineteenth|twentieth|\d+(?:st|nd|rd|th))`
	reOrdBefore  = guard(`\b`+ordAlt+`\s+(book|volume|vol|part)\b`, hasAny("book", "vol", "part"))
	reOrdAfter   = guard(`\b(book|volume|vol|part)\s+(?:the\s+)?`+ordAlt+`\b`, hasAny("book", "vol", "part"))
	reLoneRomanI = guard(`\S\s+i\s*$`, hasAny("i"))
	// A numeral closing a text, bare or behind a hash or an opening bracket
	// ("Hush 2", "Hush #2", "Hush (2)").
	reTrailingNum = guard(`(?:^|[\s,:\-#(\[])(?:\d{1,3}(?:\.\d+)?|ii|iii|iv|vi|vii|viii|ix|xi|xii|xiii|xiv|xv|xvi|xvii|xviii|xix|xx)[)\]]?\s*$`,
		numeralEnds)
	// The marker word and the punctuation before it, between a title's words
	// and its numeral ("Hush, Book 2", "Hush Volume II", "Hush (Book 2)"); a
	// hash after the marker ("Hush, Book #2") leaves a space behind it.
	reNumeralMarker = guard(`[\s,:\-(\[]+(?:book|bk|volume|vol|no|number)\.?\s*$`, hasAny("book", "bk", "vol", "no", "number"))
	// A closing note with no number in it ("(Unabridged)", "[Dramatized]"): an
	// edition label, not a word of the title and not a statement of a number.
	reClosingNote = guard(`\s*[(\[][^()\[\]\d]*[)\]]\s*$`, all(hasAny("(", "["), hasAny(")", "]")))
)

// numeralEnds is what reTrailingNum needs of the text: a digit, or the i or x
// every roman numeral from II to XX holds.
var numeralEnds = hasAnyOr(hasDigit, hasAny("i", "x"))

var ordinalNumbers = map[string]float64{
	"first": 1, "second": 2, "third": 3, "fourth": 4, "fifth": 5, "sixth": 6, "seventh": 7, "eighth": 8,
	"ninth": 9, "tenth": 10, "eleventh": 11, "twelfth": 12, "thirteenth": 13, "fourteenth": 14,
	"fifteenth": 15, "sixteenth": 16, "seventeenth": 17, "eighteenth": 18, "nineteenth": 19, "twentieth": 20,
}

func parseFloat(s string) (float64, bool) {
	n, ok := toNumber(s, "stated")
	if ok {
		return n.Value, true
	}
	// toNumber refuses a year; a total or a value written in Text may be one.
	return 0, false
}

// ordinalValue reads "second" or "2nd".
func ordinalValue(tok string) (float64, bool) {
	if v, ok := ordinalNumbers[tok]; ok {
		return v, true
	}
	if len(tok) > 2 {
		return parseFloat(tok[:len(tok)-2])
	}
	return 0, false
}

// ReadStated reads every number a text states, the volume first and then the
// parts in the order written. A lone trailing "I" is not read (see ReadPair).
func ReadStated(s string) []Stated { return readStated(s, false) }

// ReadPair reads two texts that are being compared. A lone trailing "I" ("Hush
// I") is a numeral only when the text is set beside another that ends in a
// numeral IN THE SAME PLACE: the words before the "I" are the words before the
// other text's numeral ("Hush I" beside "Hush II" or "Hush 2"; not "Who Am I"
// beside "Who Am I 2", where the "I" is a word of the title). On its own it is
// the pronoun or an initial, which is why Parse never reads it.
func ReadPair(a, b string) (sa, sb []Stated) {
	return readStatedIn(a, numeralBeside(a, b), true), readStatedIn(b, numeralBeside(b, a), true)
}

// numeralBeside reports whether other ends in a numeral whose preceding words
// are the words that precede the lone "I" ending s. The numeral may be bare
// ("Hush 2"), behind a volume marker ("Hush, Book 2", "Hush Volume II",
// "Hush, Book #2") or in the usual brackets ("Hush #2", "Hush (2)", "Hush (Book
// 2)"): the marker is not one of the words, and "Who Am I: Book 2" still does
// not match "Who Am I", whose words before the "I" are "who am". A closing
// edition note on either text ("Hush II (Unabridged)") is not one of the words
// either, but only when the text does not end in its numeral without it.
func numeralBeside(s, other string) bool {
	rest, _ := takeParts(strings.ToLower(strings.ReplaceAll(other, "_", " ")))
	c := clean(rest)
	ix := reTrailingNum.FindStringIndex(c)
	if ix == nil {
		c = withoutClosingNote(c)
		if ix = reTrailingNum.FindStringIndex(c); ix == nil {
			return false
		}
	}
	own, _ := takeParts(strings.ToLower(strings.ReplaceAll(s, "_", " ")))
	oc := clean(own)
	loc := reLoneRomanI.FindStringIndex(oc)
	if loc == nil {
		oc = withoutClosingNote(oc)
		if loc = reLoneRomanI.FindStringIndex(oc); loc == nil {
			return false
		}
	}
	return wordsBefore(oc[:loc[0]+1]) == wordsBefore(reNumeralMarker.ReplaceAllString(c[:ix[0]], ""))
}

// withoutClosingNote is s without the closing note that holds no number, if it
// ends in one.
func withoutClosingNote(s string) string {
	return strings.TrimSpace(reClosingNote.ReplaceAllString(s, ""))
}

// wordsBefore is the words of s with the punctuation that closes them dropped.
func wordsBefore(s string) string {
	return strings.Join(strings.Fields(strings.TrimRight(s, " ,:;-")), " ")
}

// takeParts removes the part statements from lower-cased text and returns them.
func takeParts(t string) (string, []Stated) {
	var parts []Stated
	t = replaceAllSub(t, rePartStated, func(m []string) string {
		if m[3] != "" { // a range of parts: every part, no one of them
			return " "
		}
		if n, ok := toNumber(m[1], "part"); ok {
			st := Stated{Kind: KindPart, Number: n, Marker: partMarker(m[0])}
			if of, ok := parseFloat(m[2]); ok {
				st.Of = of
			}
			parts = append(parts, st)
		}
		return " "
	})
	t = replaceAllSub(t, reParenOf, func(m []string) string {
		if n, ok := toNumber(m[1], "part"); ok {
			st := Stated{Kind: KindPart, Number: n, Marker: "of"}
			if of, ok := parseFloat(m[2]); ok {
				st.Of = of
			}
			parts = append(parts, st)
		}
		return " "
	})
	return t, parts
}

// partMarker normalises the marker word a rePartStated match opens with.
func partMarker(match string) string {
	switch {
	case strings.HasPrefix(match, "cd"):
		return "cd"
	case strings.HasPrefix(match, "dis"):
		return "disc"
	default:
		return "part"
	}
}

func readStated(s string, loneI bool) []Stated { return readStatedIn(s, loneI, false) }

// readStatedIn is readStated; paired says the text is read beside another
// (ReadPair), where a closing note with no number ("Hush II (Unabridged)") is
// set aside when the text states no volume with it in place.
func readStatedIn(s string, loneI, paired bool) []Stated {
	t := strings.ToLower(strings.ReplaceAll(s, "_", " "))
	t, parts := takeParts(t)

	var volume *Stated
	// An ordinal beside a marker: "the Second Book", "Part the First".
	t = replaceAllSub(t, reOrdBefore, func(m []string) string {
		return ordinalStated(&volume, &parts, m[2], m[1])
	})
	t = replaceAllSub(t, reOrdAfter, func(m []string) string {
		return ordinalStated(&volume, &parts, m[1], m[2])
	})
	// "1 of 2": position 1 of a set of 2.
	t = replaceAllSub(t, reBareOf, func(m []string) string {
		if volume == nil {
			if n, ok := toNumber(m[1], "of"); ok {
				st := Stated{Kind: KindVolume, Number: n}
				if of, ok := parseFloat(m[2]); ok {
					st.Of = of
				}
				volume = &st
			}
		}
		return " "
	})
	volume = volumeIn(t, volume, loneI)
	if volume == nil && paired {
		if t2 := withoutClosingNote(t); t2 != t {
			volume = volumeIn(t2, nil, loneI)
		}
	}
	var out []Stated
	if volume != nil {
		out = append(out, *volume)
	}
	return append(out, parts...)
}

// volumeIn is the volume t states, given the one an ordinal or an "N of M"
// already said (nil when none did): a range, a bare span, the title's own
// number, or a lone trailing "I" when loneI says it is a numeral.
func volumeIn(t string, volume *Stated, loneI bool) *Stated {
	if n, ok := ParseRange(t); ok {
		return &Stated{Kind: KindVolume, Number: n}
	}
	if n, ok := trailingSpan(t); ok {
		return &Stated{Kind: KindVolume, Number: n}
	}
	if n, ok := ParseTitle(t); ok {
		st := Stated{Kind: KindVolume, Number: n}
		if volume != nil {
			st = *volume // an "N of M" or an ordinal said it first
		}
		return &st
	}
	if volume == nil && loneI {
		if c := clean(t); reLoneRomanI.MatchString(c) || reLoneRomanI.MatchString(withoutClosingNote(c)) {
			return &Stated{Kind: KindVolume, Number: Number{Value: 1, Text: "1", Form: "trailing"}}
		}
	}
	return volume
}

// reTrailingSpan is a bare range closing a title: "Hush 1-3". Parse reads the
// 3 of it (a keyword-less range is ambiguous to a sequence reader, which wants
// one volume), but a title that says "1-3" does not say volume 3: it says the
// first three. The ends must be short and ordered so a year span or a phone
// number is not one.
var reTrailingSpan = guard(`(?:^|\s)(\d{1,3}\s*[-–—]\s*\d{1,3})\s*$`, all(hasDigit, hasDash))

func trailingSpan(t string) (Number, bool) {
	m := reTrailingSpan.FindStringSubmatch(clean(t))
	if m == nil {
		return Number{}, false
	}
	return parsePositionRange(m[1])
}

// ordinalStated records "second book" / "part the first" and returns the text
// it leaves behind.
func ordinalStated(volume **Stated, parts *[]Stated, marker, ord string) string {
	v, ok := ordinalValue(ord)
	if !ok {
		return " " + marker + " " + ord + " "
	}
	n := Number{Value: v, Text: ftoa(v), Form: "ordinal"}
	if marker == "part" {
		*parts = append(*parts, Stated{Kind: KindPart, Number: n, Marker: "part"})
	} else if *volume == nil {
		*volume = &Stated{Kind: KindVolume, Number: n}
	}
	return " "
}

// replaceAllSub replaces every match of re in s by f(submatches), which is
// handed the whole match at index 0.
func replaceAllSub(s string, re *guardedRe, f func(m []string) string) string {
	var b strings.Builder
	last := 0
	for _, ix := range re.FindAllStringSubmatchIndex(s, -1) {
		b.WriteString(s[last:ix[0]])
		m := make([]string, len(ix)/2)
		for i := range m {
			if ix[2*i] >= 0 {
				m[i] = s[ix[2*i]:ix[2*i+1]]
			}
		}
		b.WriteString(f(m))
		last = ix[1]
	}
	b.WriteString(s[last:])
	return b.String()
}
