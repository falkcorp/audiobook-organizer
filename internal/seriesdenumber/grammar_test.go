// file: internal/seriesdenumber/grammar_test.go
// version: 1.1.0
// guid: 7ebf2f9c-4978-464b-9c4c-c768027dd6ce
// last-edited: 2026-11-17

package seriesdenumber

import (
	"math/rand"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func TestParseName_Accepts(t *testing.T) {
	cases := []struct {
		name   string
		base   string
		n      int
		marker string
	}{
		{"Court of Owls 05", "Court of Owls", 5, ""},
		{"Gotham Knights: Book 11", "Gotham Knights", 11, "Book"},
		{"Gotham Knights, Volume 3", "Gotham Knights", 3, "Volume"},
		{"Gotham Knights - 03", "Gotham Knights", 3, ""},
		{"Gotham Knights – 03", "Gotham Knights", 3, ""},
		{"Court of Owls #5", "Court of Owls", 5, "#"},
		{"Court of Owls # 5", "Court of Owls", 5, "#"},
		{"Court of Owls Vol. 2", "Court of Owls", 2, "Vol."},
		{"Court of Owls Vol.2", "Court of Owls", 2, "Vol."},
		{"Court of Owls No 7", "Court of Owls", 7, "No"},
		{"Court of Owls bk. 7", "Court of Owls", 7, "bk."},
		{"BAT 07", "BAT", 7, ""},
		{"BAT-05", "BAT", 5, ""},
		{"Arkham Asylum 99", "Arkham Asylum", 99, ""},
		{"Discord Knights 05", "Discord Knights", 5, ""},
		{"Spider-Man 03", "Spider-Man", 3, ""},
		{"  Gotham   Knights   005 ", "Gotham Knights", 5, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, v, ok := ParseName(tc.name)
			if !ok {
				t.Fatalf("held as %s (%s), want base %q at %d", v.Code, v.Reason, tc.base, tc.n)
			}
			if p.Base != tc.base || p.N != tc.n || p.Marker != tc.marker {
				t.Fatalf("got base %q marker %q n %d, want %q %q %d", p.Base, p.Marker, p.N, tc.base, tc.marker, tc.n)
			}
		})
	}
}

func TestParseName_Holds(t *testing.T) {
	cases := []struct {
		name string
		want Code
	}{
		// B1: a Part in any punctuation, or beside a format word.
		{"Hush Part: 2", CodePartOrFormatWord},
		{"Hush Part #2", CodePartOrFormatWord},
		{"Hush Part 1 - Unabridged", CodePartOrFormatWord},
		{"Hush Pt. 2", CodePartOrFormatWord},
		{"Hush Part 2", CodePartOrFormatWord},
		{"Hush Part.2", CodePartOrFormatWord},
		{"Year One Unabridged Part 1", CodePartOrFormatWord},
		{"Hush Disc 2", CodePartOrFormatWord},
		{"Hush CD 3", CodePartOrFormatWord},
		{"Audio 01", CodePartOrFormatWord},
		{"Chapter 4", CodePartOrFormatWord},
		{"chap-01", CodePartOrFormatWord},
		{"Knightfall (Unabridged) 3", CodePartOrFormatWord},
		{"Gotham Knights Special Edition 3", CodePartOrFormatWord},
		// B2: a base that still carries a number or a cut-off phrase.
		{"Gotham Knights 01 of 03", CodeBaseHasDigit},
		{"Gotham Knights Part 02 of 05", CodePartOrFormatWord},
		{"Gotham Knights 01 & 02", CodeBaseHasDigit},
		{"Gotham Knights 01 to 03", CodeBaseHasDigit},
		{"Gotham Knights 1-03", CodeBaseHasDigit},
		{"No Mans Land 2-1", CodeBaseHasDigit},
		{"Dark Victory.1 04", CodeBaseHasDigit},
		{"01 Knightfall 04", CodeBaseHasDigit},
		{"Batman Vol. 2: Knightfall Book 3", CodeBaseHasDigit},
		{"Gotham Knights 5 6", CodeBaseHasDigit},
		{"Gotham Knights of 3", CodeBaseEndsConnector},
		{"Gotham Knights & 3", CodeBaseEndsConnector},
		{"Gotham Knights and 3", CodeBaseEndsConnector},
		{"Gotham Knights to 3", CodeBaseEndsConnector},
		// A base made only of structural words is a slice of a file, not a series
		// (the words the part/format list and the volume list do not already hold).
		{"Chapters 03", CodeStructuralBase},
		{"Episodes 04", CodeStructuralBase},
		{"Chapters Episodes 03", CodeStructuralBase},
		// B3: a decimal point.
		{"Gotham Knights Book .5", CodeFractionalNumber},
		{"Gotham Knights #.5", CodeFractionalNumber},
		{"Gotham Knights Book 0.5", CodeFractionalNumber},
		{"Gotham Knights 2.5", CodeFractionalNumber},
		{"Gotham Knights, Book 2.5", CodeFractionalNumber},
		// Numbers the grammar will not read.
		{"Knightfall 100", CodeNumberOutOfRange},
		{"Knightfall 201", CodeNumberOutOfRange},
		{"Gotham Central 2099", CodeNumberOutOfRange},
		{"Batman 1989", CodeNumberOutOfRange},
		{"Gotham Knights Book 0", CodeNumberOutOfRange},
		{"Batman '66", CodeNoSeparator},
		{"Hush2", CodeNoSeparator},
		{"Hush", CodeNoTrailingNumber},
		{"Gotham Knights Book IV", CodeNoTrailingNumber},
		{"Gotham Knights Book One", CodeNoTrailingNumber},
		{"", CodeNoTrailingNumber},
		// Base shapes.
		{"Book 5", CodeBaseEmpty},
		{"Gotham Knights (Special) 3", CodeBaseNotClean},
		{"Gotham Knights Tome 1", CodeVolumeWordInBase},
		{"Gotham Knights Band 1", CodeVolumeWordInBase},
		{"Gotham Knights Libro 1", CodeVolumeWordInBase},
		{"Gotham Knights Teil 1", CodeVolumeWordInBase},
		{"Gotham Knights Book Book 3", CodeVolumeWordInBase},
		{"B 05", CodeTinyBase},
		{"BM 05", CodeTinyBase},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, v, ok := ParseName(tc.name)
			if ok {
				t.Fatalf("parsed %+v, want it held as %s", p, tc.want)
			}
			if v.Code != tc.want {
				t.Fatalf("held as %s (%s), want %s", v.Code, v.Reason, tc.want)
			}
			if v.Reason == "" {
				t.Fatalf("held as %s with no reason", v.Code)
			}
		})
	}
}

func TestRunCorroborated(t *testing.T) {
	cases := []struct {
		name string
		n    int
		sib  []int
		want bool
	}{
		{"three in a row, n first", 1, []int{2, 3}, true},
		{"three in a row, n last", 4, []int{2, 3}, true},
		{"three in a row, n in the middle", 3, []int{2, 4}, true},
		{"long run", 7, []int{5, 6, 8, 9, 10}, true},
		{"one sibling is not enough", 2, []int{3}, false},
		{"no siblings", 5, nil, false},
		{"two unrelated numbers", 66, []int{89}, false},
		{"n joins none of two siblings", 66, []int{89, 90}, false},
		{"n adjacent to one but the run is two long", 5, []int{6, 9}, false},
		{"a gap breaks the run", 1, []int{2, 4}, false},
		{"duplicate sibling numbers count once", 1, []int{2, 2}, false},
		{"duplicates plus a real run", 1, []int{2, 2, 3}, true},
	}
	for _, tc := range cases {
		if got := RunCorroborated(tc.n, tc.sib); got != tc.want {
			t.Errorf("%s: RunCorroborated(%d, %v) = %v, want %v", tc.name, tc.n, tc.sib, got, tc.want)
		}
	}
}

// oracle is an independent reading of the grammar: regular expressions over the
// whitespace-collapsed name that share no code with ParseName.
var (
	oracle      = regexp.MustCompile(`(?i)^(.+?)(?:\s*[-:,\x{2013}\x{2014}]\s*|\s+)(?:(?:book|bk|vol|volume|no)\.?\s*|#\s*)?(\d{1,3})$`)
	bannedWords = regexp.MustCompile(`(?i)\b(?:part|parts|pt|unabridged|abridged|disc|disk|cd|track|tracks|file|mp3|m4b|audio|side|edition|version|episode|chapter|chap|section)\b`)
	connectorAt = regexp.MustCompile(`(?i)(?:^|\s)(?:of|to|and|or|in|on|at|for|from|with|by|vs|the|a|an|&)$`)
)

// Names are generated from a pool that mixes markers, digits, connectors and
// format words. For every name the grammar accepts, the oracle must rebuild it:
// base + separator + marker + number reproduces the name, the base has no digit
// and does not end in a connector, the name has no part, format or edition
// word, and N is the number the oracle reads, within 1..MaxPosition.
func TestParseName_PropertyAgainstIndependentOracle(t *testing.T) {
	pool := []string{
		"Gotham", "Knights", "Court", "of", "Owls", "Part", "Pt.", "Unabridged", "Disc", "CD", "Book", "Vol.", "Volume",
		"No", "#", "Tome", "Band", "and", "&", "to", "-", ":", ",", "01", "2", "05", "66", "1989", ".5", "2.5", "(Special)",
		"'", "III", "Bat", "BW", "Edition", "99", "100", "0", "–",
	}
	rng := rand.New(rand.NewSource(20261117))
	accepted := 0
	for i := 0; i < 60000; i++ {
		var b strings.Builder
		for j, n := 0, 1+rng.Intn(6); j < n; j++ {
			if j > 0 && rng.Intn(6) != 0 {
				b.WriteByte(' ')
			}
			b.WriteString(pool[rng.Intn(len(pool))])
		}
		name := b.String()
		norm := strings.Join(strings.Fields(name), " ")
		p, _, ok := ParseName(name)
		if !ok {
			continue
		}
		accepted++
		m := oracle.FindStringSubmatch(norm)
		if m == nil {
			t.Fatalf("%q was accepted but the oracle cannot read it as base + separator + marker + number", name)
		}
		if bannedWords.MatchString(norm) {
			t.Fatalf("%q was accepted with a part, format or edition word", name)
		}
		if !strings.EqualFold(p.Base, m[1]) {
			t.Fatalf("%q: base %q, oracle base %q", name, p.Base, m[1])
		}
		if strings.ContainsAny(p.Base, "0123456789") {
			t.Fatalf("%q was accepted with a digit in the base %q", name, p.Base)
		}
		if connectorAt.MatchString(p.Base) {
			t.Fatalf("%q was accepted with a connector ending the base %q", name, p.Base)
		}
		want, _ := strconv.Atoi(m[2])
		if p.N != want || p.N < 1 || p.N > MaxPosition {
			t.Fatalf("%q: n %d, oracle %d (range 1..%d)", name, p.N, want, MaxPosition)
		}
		// Rebuild: base, then separator, then the marker as written, then the
		// digits as written, reproduces the name exactly.
		tail := norm[len(p.Base):]
		rebuild := regexp.MustCompile(`^(?:\s*[-:,\x{2013}\x{2014}]\s*|\s+)` + regexp.QuoteMeta(p.Marker) + `\s*` + regexp.QuoteMeta(p.Digits) + `$`)
		if p.Marker == "" {
			rebuild = regexp.MustCompile(`^(?:\s*[-:,\x{2013}\x{2014}]\s*|\s+)` + regexp.QuoteMeta(p.Digits) + `$`)
		}
		if !rebuild.MatchString(tail) {
			t.Fatalf("%q: base %q + separator + marker %q + digits %q does not rebuild the name (tail %q)", name, p.Base, p.Marker, p.Digits, tail)
		}
	}
	if accepted < 300 {
		t.Fatalf("only %d of 60000 generated names were accepted: the generator no longer exercises the accept path", accepted)
	}
	t.Logf("%d of 60000 generated names accepted", accepted)
}
