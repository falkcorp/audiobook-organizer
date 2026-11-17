// file: internal/seqnum/stated.go
// version: 1.0.0
// guid: ef7d6ebc-a3a1-4738-bc60-512ed593d8eb
// last-edited: 2026-11-17

package seqnum

import (
	"regexp"
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

func (k Kind) String() string {
	if k == KindPart {
		return "part"
	}
	return "volume"
}

// Stated is one number a text states.
type Stated struct {
	Kind Kind
	// Number is the number as Parse reads it (Value, Last for a range, Text).
	Number Number
	// Of is the M of "N of M", 0 when the text gives none. It is a total, not
	// a number the text states.
	Of float64
}

var (
	// A part marker with its number, an optional "of M", and the tail that
	// would make it a range of parts.
	rePartStated = regexp.MustCompile(`\b(?:parts?|pts?|discs?|disks?|cds?)\.?\s*` + numAlt + `\b(?:\s*(?:of|/)\s*(\d+)\b)?(\s*(?:[-–—]|to|thru|through|&|and)\s*(?:\d+|[ivxlc]+)\b)?`)
	// "(2 of 3)": a parenthesised N of M is a part.
	reParenOf = regexp.MustCompile(`\(\s*(\d+)\s+of\s+(\d+)\s*\)`)
	// "1 of 2": a position in a set of M.
	reBareOf = regexp.MustCompile(`\b(\d+)\s+of\s+(\d+)\b`)

	ordAlt        = `(first|second|third|fourth|fifth|sixth|seventh|eighth|ninth|tenth|eleventh|twelfth|thirteenth|fourteenth|fifteenth|sixteenth|seventeenth|eighteenth|nineteenth|twentieth|\d+(?:st|nd|rd|th))`
	reOrdBefore   = regexp.MustCompile(`\b` + ordAlt + `\s+(book|volume|vol|part)\b`)
	reOrdAfter    = regexp.MustCompile(`\b(book|volume|vol|part)\s+(?:the\s+)?` + ordAlt + `\b`)
	reLoneRomanI  = regexp.MustCompile(`\S\s+i\s*$`)
	reTrailingNum = regexp.MustCompile(`(?:^|[\s,:\-])(?:\d{1,3}(?:\.\d+)?|ii|iii|iv|vi|vii|viii|ix|xi|xii|xiii|xiv|xv|xvi|xvii|xviii|xix|xx)\s*$`)
)

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
	return readStated(a, numeralBeside(a, b)), readStated(b, numeralBeside(b, a))
}

// numeralBeside reports whether other ends in a numeral whose preceding words
// are the words that precede the lone "I" ending s.
func numeralBeside(s, other string) bool {
	rest, _ := takeParts(strings.ToLower(strings.ReplaceAll(other, "_", " ")))
	c := clean(rest)
	ix := reTrailingNum.FindStringIndex(c)
	if ix == nil {
		return false
	}
	own, _ := takeParts(strings.ToLower(strings.ReplaceAll(s, "_", " ")))
	oc := clean(own)
	loc := reLoneRomanI.FindStringIndex(oc)
	if loc == nil {
		return false
	}
	return strings.Join(strings.Fields(oc[:loc[0]+1]), " ") == strings.Join(strings.Fields(c[:ix[0]]), " ")
}

// takeParts removes the part statements from lower-cased text and returns them.
func takeParts(t string) (string, []Stated) {
	var parts []Stated
	t = replaceAllSub(t, rePartStated, func(m []string) string {
		if m[3] != "" { // a range of parts: every part, no one of them
			return " "
		}
		if n, ok := toNumber(m[1], "part"); ok {
			st := Stated{Kind: KindPart, Number: n}
			if of, ok := parseFloat(m[2]); ok {
				st.Of = of
			}
			parts = append(parts, st)
		}
		return " "
	})
	t = replaceAllSub(t, reParenOf, func(m []string) string {
		if n, ok := toNumber(m[1], "part"); ok {
			st := Stated{Kind: KindPart, Number: n}
			if of, ok := parseFloat(m[2]); ok {
				st.Of = of
			}
			parts = append(parts, st)
		}
		return " "
	})
	return t, parts
}

func readStated(s string, loneI bool) []Stated {
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
	if n, ok := ParseRange(t); ok {
		st := Stated{Kind: KindVolume, Number: n}
		volume = &st
	} else if n, ok := trailingSpan(t); ok {
		st := Stated{Kind: KindVolume, Number: n}
		volume = &st
	} else if n, ok := ParseTitle(t); ok {
		st := Stated{Kind: KindVolume, Number: n}
		if volume != nil {
			st = *volume // an "N of M" or an ordinal said it first
		}
		volume = &st
	} else if volume == nil && loneI {
		if c := clean(t); reLoneRomanI.MatchString(c) {
			volume = &Stated{Kind: KindVolume, Number: Number{Value: 1, Text: "1", Form: "trailing"}}
		}
	}
	var out []Stated
	if volume != nil {
		out = append(out, *volume)
	}
	return append(out, parts...)
}

// reTrailingSpan is a bare range closing a title: "Hush 1-3". Parse reads the
// 3 of it (a keyword-less range is ambiguous to a sequence reader, which wants
// one volume), but a title that says "1-3" does not say volume 3: it says the
// first three. The ends must be short and ordered so a year span or a phone
// number is not one.
var reTrailingSpan = regexp.MustCompile(`(?:^|\s)(\d{1,3}\s*[-–—]\s*\d{1,3})\s*$`)

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
		*parts = append(*parts, Stated{Kind: KindPart, Number: n})
	} else if *volume == nil {
		*volume = &Stated{Kind: KindVolume, Number: n}
	}
	return " "
}

// replaceAllSub replaces every match of re in s by f(submatches), which is
// handed the whole match at index 0.
func replaceAllSub(s string, re *regexp.Regexp, f func(m []string) string) string {
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
