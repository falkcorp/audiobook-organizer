// file: internal/seqnum/seqnum.go
// version: 1.0.0
// guid: 5b0e7c2a-9d41-4f6e-8a13-c7d2e4f90b61
// last-edited: 2026-09-13

// Package seqnum extracts a volume / sequence number ("Big Cats 3", "Vol. III",
// "02 - Title", "Book 1.5") from free text, so a bulk metadata apply can refuse
// to put book 3's metadata on book 1.
//
// It is a leaf package on purpose: pure string in, number out, no database or
// metafetch types, so the extraction rules can be table-tested in isolation and
// every bulk-apply path (internal/applygate) consults the same rules.
//
// # What is NOT a sequence number
//
// These are stripped from the text before any pattern runs, on BOTH sides of a
// comparison, so a marker present on only one side can never manufacture a
// "book has a number, candidate has none" block:
//
//   - a year: any standalone 4-digit integer from 1000 to 2100 ("Dune (1965)");
//   - a bitrate / sample rate / size: "64kbps", "128k", "44.1kHz", "700MB";
//   - a disc / part / track / chapter marker: "Disc 2", "CD 1", "Part 1 of 3",
//     "Pt. II", "Track 04", "Chapter 6".
//
// The disc/part decision, stated explicitly: "Part N" and "Disc N" split ONE
// book into pieces, they do not place a book in a series. Treating them as
// sequence numbers would block every multi-part rip whose provider record has
// no part number, and would let "Part 2" vs "Part 3" block an otherwise
// identical match. So "Disc 2" vs "Disc 3" does NOT block, while "Book 2" vs
// "Book 3" does. A title whose only number is a part marker has no sequence
// number at all.
package seqnum

import (
	"regexp"
	"strconv"
	"strings"
)

// Number is an extracted sequence number. Value carries decimals so "1.5"
// (a novella between books 1 and 2) never compares equal to "1".
type Number struct {
	Value float64
	// Text is the canonical rendering ("2", "1.5"), for reasons and reports.
	Text string
	// Form names the pattern that matched ("marker", "bracket", "leading",
	// "trailing", "position"), so a report can say why a number was seen.
	Form string
}

// Equal reports whether two numbers name the same sequence position.
func Equal(a, b Number) bool { return a.Value == b.Value }

var (
	// Noise removed before extraction. Order matters: disc/part markers go
	// first so "Part 1 of 3" is removed whole rather than leaving "of 3".
	reDiscPart = regexp.MustCompile(`\b(?:disc|disk|cd|part|pt|track|trk|chapter|ch)\.?\s*(?:\d+|[ivxlc]+|one|two|three|four|five|six|seven|eight|nine|ten)\b(?:\s*(?:of|/)\s*\d+)?`)
	reBitrate  = regexp.MustCompile(`\b\d+(?:\.\d+)?\s*(?:kbps|kb/s|kbit/s|kbits|khz|hz|mb|gb|k)\b`)
	reYear     = regexp.MustCompile(`\b(?:1\d{3}|20\d{2}|2100)\b`)

	numAlt   = `(\d+(?:\.\d+)?|[ivxlc]+|one|two|three|four|five|six|seven|eight|nine|ten|eleven|twelve|thirteen|fourteen|fifteen|sixteen|seventeen|eighteen|nineteen|twenty)`
	reMarker = regexp.MustCompile(`(?:\b(?:book|bk|volume|vol|number|no)\.?|#)\s*#?\s*` + numAlt + `\b`)
	// A number alone inside brackets or parentheses: "Big Cats (2)", "[03]".
	reBracket = regexp.MustCompile(`[\[(]\s*(\d{1,3}(?:\.\d+)?)\s*[\])]`)
	// "02 - Title", "1.5. Title", "3) Title", "02 Title".
	reLeading = regexp.MustCompile(`^\s*(\d{1,3}(?:\.\d+)?)\s*[-._:)]?\s+\S`)
	// "Big Cats 1", "Big Cats, 2", "Big Cats - 3", "Catch-22".
	reTrailing = regexp.MustCompile(`(?:^|[\s,:\-])(\d{1,3}(?:\.\d+)?)\s*$`)
	// Trailing roman numerals, II..XX. Single letters (I, V, X) are excluded:
	// "Malcolm X" and "Plan B"-style titles are not volume numbers.
	reTrailingRoman = regexp.MustCompile(`\s(ii|iii|iv|vi|vii|viii|ix|xi|xii|xiii|xiv|xv|xvi|xvii|xviii|xix|xx)\s*$`)

	rePlainNumber = regexp.MustCompile(`^\d+(?:\.\d+)?$`)
	reRomanOnly   = regexp.MustCompile(`^[ivxlc]+$`)
)

var wordNumbers = map[string]float64{
	"one": 1, "two": 2, "three": 3, "four": 4, "five": 5, "six": 6, "seven": 7,
	"eight": 8, "nine": 9, "ten": 10, "eleven": 11, "twelve": 12, "thirteen": 13,
	"fourteen": 14, "fifteen": 15, "sixteen": 16, "seventeen": 17, "eighteen": 18,
	"nineteen": 19, "twenty": 20,
}

// clean lowercases and strips everything the package doc says is not a
// sequence number. Both sides of every comparison go through it.
func clean(s string) string {
	s = strings.ToLower(s)
	s = strings.NewReplacer("_", " ").Replace(s)
	s = reDiscPart.ReplaceAllString(s, " ")
	s = reBitrate.ReplaceAllString(s, " ")
	s = reYear.ReplaceAllString(s, " ")
	// What stripping leaves behind — "big cats 3 ( )", "big cats 2 - " — must
	// not stop the trailing pattern from seeing the number before it.
	s = reEmptyBrackets.ReplaceAllString(s, " ")
	s = strings.Join(strings.Fields(s), " ")
	return strings.TrimRight(s, " -,:;.")
}

var reEmptyBrackets = regexp.MustCompile(`[\[(]\s*[\])]`)

// Parse extracts a sequence number from free text — a title, a file name, a
// folder name. Patterns are tried from most to least explicit: an explicit
// marker ("Book 2", "Vol. III", "#3"), a bracketed number, a leading number
// ("02 - Title"), then a trailing number ("Big Cats 1") or trailing roman
// numeral.
func Parse(s string) (Number, bool) {
	c := clean(s)
	if c == "" {
		return Number{}, false
	}
	if m := reMarker.FindStringSubmatch(c); m != nil {
		if n, ok := toNumber(m[1], "marker"); ok {
			return n, true
		}
	}
	if m := reBracket.FindStringSubmatch(c); m != nil {
		if n, ok := toNumber(m[1], "bracket"); ok {
			return n, true
		}
	}
	if m := reLeading.FindStringSubmatch(c); m != nil {
		if n, ok := toNumber(m[1], "leading"); ok {
			return n, true
		}
	}
	if m := reTrailing.FindStringSubmatch(c); m != nil {
		if n, ok := toNumber(m[1], "trailing"); ok {
			return n, true
		}
	}
	if m := reTrailingRoman.FindStringSubmatch(c); m != nil {
		if n, ok := toNumber(m[1], "trailing"); ok {
			return n, true
		}
	}
	return Number{}, false
}

// ParsePosition extracts a number from a series-position FIELD ("3", "1.5",
// "III", "Book 3"). A bare number or roman numeral is the common case; anything
// else falls back to Parse.
func ParsePosition(s string) (Number, bool) {
	t := strings.ToLower(strings.TrimSpace(s))
	if t == "" {
		return Number{}, false
	}
	if rePlainNumber.MatchString(t) || reRomanOnly.MatchString(t) {
		return toNumber(t, "position")
	}
	n, ok := Parse(s)
	if ok {
		n.Form = "position"
	}
	return n, ok
}

// toNumber converts one matched token. A year that survived clean() (it can
// arrive through ParsePosition) is still refused.
func toNumber(tok, form string) (Number, bool) {
	var v float64
	switch {
	case rePlainNumber.MatchString(tok):
		f, err := strconv.ParseFloat(tok, 64)
		if err != nil {
			return Number{}, false
		}
		v = f
	case wordNumbers[tok] > 0:
		v = wordNumbers[tok]
	case reRomanOnly.MatchString(tok):
		r, ok := romanValue(tok)
		if !ok {
			return Number{}, false
		}
		v = float64(r)
	default:
		return Number{}, false
	}
	if v == float64(int64(v)) && v >= 1000 && v <= 2100 {
		return Number{}, false
	}
	return Number{Value: v, Text: strconv.FormatFloat(v, 'f', -1, 64), Form: form}, true
}

// romanValue parses a well-formed roman numeral up to C-range values. It
// rejects malformed strings ("iiii", "vv", "ic") so an ordinary word made of
// the letters i/v/x/l/c ("civic", "ill") is not read as a number.
func romanValue(s string) (int, bool) {
	vals := map[byte]int{'i': 1, 'v': 5, 'x': 10, 'l': 50, 'c': 100}
	total := 0
	for i := 0; i < len(s); i++ {
		v := vals[s[i]]
		if i+1 < len(s) && v < vals[s[i+1]] {
			total -= v
		} else {
			total += v
		}
	}
	if total <= 0 || toRoman(total) != s {
		return 0, false
	}
	return total, true
}

func toRoman(n int) string {
	type pair struct {
		v int
		s string
	}
	table := []pair{{100, "c"}, {90, "xc"}, {50, "l"}, {40, "xl"}, {10, "x"}, {9, "ix"}, {5, "v"}, {4, "iv"}, {1, "i"}}
	var b strings.Builder
	for _, p := range table {
		for n >= p.v {
			b.WriteString(p.s)
			n -= p.v
		}
	}
	return b.String()
}
