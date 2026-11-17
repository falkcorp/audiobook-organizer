// file: internal/seqnum/range_test.go
// version: 1.3.0
// guid: 7b2d9e41-5a63-4c08-b1f7-d4e08a36c915
// last-edited: 2026-11-17

package seqnum

import (
	"strings"
	"testing"
)

// rng renders a Number the way a reader of a report sees it: the canonical
// text, with "(range)" when it covers several volumes.
func rng(n Number, ok bool) string {
	if !ok {
		return ""
	}
	if n.IsRange() {
		return n.Text + " (range)"
	}
	return n.Text
}

func TestParse_Ranges(t *testing.T) {
	cases := []struct {
		in   string
		want string // "" = no number
	}{
		// Every collected-edition shape in free text.
		{"The Dark Knight Archive, Books 1-3", "1-3 (range)"},
		{"The Dark Knight Archive, Book 1-3", "1-3 (range)"},
		{"The Dark Knight Archive: Vols 1–3", "1-3 (range)"},  // en dash
		{"The Dark Knight Archive: Vols. 1—3", "1-3 (range)"}, // em dash
		{"The Dark Knight Archive: Volumes 4 - 6", "4-6 (range)"},
		{"The Dark Knight Archive Vol. 01-02", "1-2 (range)"},
		{"The Dark Knight Archive, Books 1 to 3", "1-3 (range)"},
		{"The Dark Knight Archive, Books 1 through 3", "1-3 (range)"},
		{"The Dark Knight Archive, Books 1 thru 3", "1-3 (range)"},
		{"The Dark Knight Archive, Books 1 & 2", "1-2 (range)"},
		{"The Dark Knight Archive, Books 1 and 2", "1-2 (range)"},
		{"The Dark Knight Archive, Books 1, 2 & 3", "1-3 (range)"},
		{"The Dark Knight Archive, Books 1, 2, 3", "1-3 (range)"},
		{"The Dark Knight Archive, Books I-III", "1-3 (range)"},
		{"The Dark Knight Archive, Books one to three", "1-3 (range)"},
		{"The Dark Knight Archive, #1-3", "1-3 (range)"},
		{"The Dark Knight Archive, Books 1.5-2", "1.5-2 (range)"},
		{"The Dark Knight Archive, Books 1-3 (2015)", "1-3 (range)"},
		{"Omnibus (Books 1-3)", "1-3 (range)"},
		// A range wins over the trailing number the old reader returned.
		{"The Dark Knight Archive, Books 1-3 - Unabridged", "1-3 (range)"},
		// A gappy list is one Number that equals only itself.
		{"The Dark Knight Archive, Books 1, 3", "1,3 (range)"},
		// Not ranges: no keyword, reversed, one volume, years, discs.
		{"Batman-22", "22"},
		{"Batman 9-1-1", "1"},
		{"The Dark Knight Archive, Books 3-1", "1"},
		{"The Dark Knight Archive, Books 2-2", "2"},
		{"The Dark Knight Archive 1-3", "3"},
		{"The Dark Knight Archive (1939-1945)", ""},
		{"The Dark Knight Archive Disc 1-3", "3"},
		{"The Dark Knight Archive, Book 2", "2"},
		{"The Dark Knight Archive, Book 2 and the Joker", "2"},
	}
	for _, tc := range cases {
		n, ok := Parse(tc.in)
		if got := rng(n, ok); got != tc.want {
			t.Errorf("Parse(%q) = %q (form %q), want %q", tc.in, got, n.Form, tc.want)
		}
	}
}

func TestParsePosition_Ranges(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"1-3", "1-3 (range)"},
		{"1 - 3", "1-3 (range)"},
		{"1–2", "1-2 (range)"},
		{"3 to 4", "3-4 (range)"},
		{"1 & 2", "1-2 (range)"},
		{"1, 2, 3", "1-3 (range)"},
		{"1.5-2", "1.5-2 (range)"},
		{"Books 1-3", "1-3 (range)"},
		{"Book 3", "3"},
		{"1, 3", "1,3 (range)"},
		// Not ranges.
		{"3-1", "1"},
		{"2-2", "2"},
		{"3", "3"},
		{"1.5", "1.5"},
		{"2019-2020", ""},
	}
	for _, tc := range cases {
		n, ok := ParsePosition(tc.in)
		if got := rng(n, ok); got != tc.want {
			t.Errorf("ParsePosition(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// The reason for the type: one collected edition written five ways reads the
// same number, wherever it is written (a title, a subtitle, a position field).
func TestRange_SameEditionReadsTheSameEverywhere(t *testing.T) {
	forms := []struct {
		name string
		read func(string) (Number, bool)
		in   string
	}{
		{"title plural", Parse, "Archive, Books 1-3"},
		{"title singular", Parse, "Archive, Book 1-3"},
		{"title en dash", Parse, "Archive, Vols 1–3"},
		{"title to", Parse, "Archive, Books 1 to 3"},
		{"title list", Parse, "Archive, Books 1, 2 & 3"},
		{"position bare", ParsePosition, "1-3"},
		{"position spaced", ParsePosition, "1 – 3"},
		{"position keyworded", ParsePosition, "Books 1-3"},
		{"position list", ParsePosition, "1, 2, 3"},
	}
	var first Number
	for i, f := range forms {
		n, ok := f.read(f.in)
		if !ok || !n.IsRange() {
			t.Fatalf("%s: %q read %+v ok=%v, want a range", f.name, f.in, n, ok)
		}
		if i == 0 {
			first = n
			continue
		}
		if !Equal(first, n) {
			t.Errorf("%s: %q read %q, want it Equal to %q", f.name, f.in, n.Text, first.Text)
		}
	}
}

func TestEqual_Ranges(t *testing.T) {
	must := func(read func(string) (Number, bool), s string) Number {
		t.Helper()
		n, ok := read(s)
		if !ok {
			t.Fatalf("%q did not parse", s)
		}
		return n
	}
	cases := []struct {
		name string
		a, b Number
		want bool
	}{
		{"same range", must(Parse, "Books 1-3"), must(ParsePosition, "1-3"), true},
		{"range vs its first volume", must(ParsePosition, "1-3"), must(ParsePosition, "1"), false},
		{"range vs its last volume", must(ParsePosition, "1-3"), must(ParsePosition, "3"), false},
		{"range vs an inner volume", must(ParsePosition, "1-3"), must(ParsePosition, "2"), false},
		{"longer range", must(Parse, "Books 1-3"), must(Parse, "Books 1-5"), false},
		{"shifted range", must(Parse, "Books 1-3"), must(Parse, "Books 2-4"), false},
		{"gappy list vs span with the same ends", must(Parse, "Books 1, 3"), must(Parse, "Books 1-3"), false},
		{"same gappy list", must(Parse, "Books 1, 3"), must(ParsePosition, "1, 3"), true},
		{"single equals single", must(ParsePosition, "03"), must(ParsePosition, "3"), true},
		// A Number built without Last is a single position.
		{"zero Last is single", Number{Value: 3, Text: "3"}, must(ParsePosition, "3"), true},
		{"zero Last vs range", Number{Value: 1, Text: "1"}, must(ParsePosition, "1-3"), false},
	}
	for _, tc := range cases {
		if got := Equal(tc.a, tc.b); got != tc.want {
			t.Errorf("%s: Equal(%+v, %+v) = %v, want %v", tc.name, tc.a, tc.b, got, tc.want)
		}
		if got := Equal(tc.b, tc.a); got != tc.want {
			t.Errorf("%s (swapped): Equal = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// Text must read back to an Equal Number, because reports and JSON carry the
// text and a reader (applygate's agree) may parse it again.
func TestRange_TextRoundTrips(t *testing.T) {
	for _, in := range []string{"Books 1-3", "Books 1, 3", "Books 1.5-2", "Books 1 & 2", "Books 1-3 & 5", "Books 1-2 & 4-5"} {
		n, ok := Parse(in)
		if !ok {
			t.Fatalf("%q did not parse", in)
		}
		back, ok := ParsePosition(n.Text)
		if !ok || !Equal(n, back) {
			t.Errorf("%q -> text %q -> %+v (ok=%v), not Equal to %+v", in, n.Text, back, ok, n)
		}
	}
}

func TestParseRange(t *testing.T) {
	if _, ok := ParseRange("Batman-22"); ok {
		t.Error("an unkeyworded dash is a name, not a range")
	}
	if _, ok := ParseRange("1-3"); ok {
		t.Error("a bare 1-3 in free text is not a range (it is one only in a position field)")
	}
	if n, ok := ParseRange("Books 1-3"); !ok || n.Value != 1 || n.Last != 3 || !n.IsRange() {
		t.Errorf("ParseRange(Books 1-3) = %+v ok=%v", n, ok)
	}
}

// A second end that is the first word of a title is not a second end, and a
// range with a tail is a list, not the bare range.
func TestParse_RangeOverReads(t *testing.T) {
	cases := []struct {
		in   string
		want string // "" = no number
	}{
		// Single volumes whose title starts with a number word, a roman token or a number.
		{"Batman, Book 2 - Three Jokers", "2"},
		{"Batman, Book 2 - Ten Nights of the Beast", "2"},
		{"Batman, Book 1 and Two-Face", "1"},
		{"Batman, Book 1, Two-Face", "1"},
		{"Batman, Book 1 - V for Victory", "1"},
		{"Batman, Book 1 - X Marks the Spot", "1"},
		{"Batman, Book 1 - L.A. Confidential", "1"},
		{"Batman, Book 1 - C Is for Catwoman", "1"},
		{"Batman, Book 2 to Three Jokers", "2"},
		{"Batman, Book 2, 10 Nights of the Beast", "2"},
		{"Batman, Book 3 and 4 Horsemen", "3"},
		{"Batman, Books 3, 4 and 5", "3-5 (range)"},
		{"Batman, Books 3, 4 and 5: Unabridged", "3-5 (range)"},
		{"Batman, Book 4 - 52", "4"},
		{"Batman, Book 2 - 3 Jokers", "2"},
		{"Batman, Vol. 2 - Three Jokers", "2"},
		{"Batman, Book 1 and Ten Thousand Fists", "1"},
		// Real ranges still read, in every spelling that is safe.
		{"Batman, Books 1 - 3", "1-3 (range)"},
		{"Batman, Vols 4 – 6", "4-6 (range)"},
		{"Batman, Book 1-3", "1-3 (range)"},
		{"Batman, Book 1 to 3", "1-3 (range)"},
		{"Batman, Books one to three", "1-3 (range)"},
		{"Batman, Books I-III", "1-3 (range)"},
		{"Batman, Books I to III: Unabridged", "1-3 (range)"},
		{"Batman, Books one-three (Unabridged)", "1-3 (range)"},
		{"Batman, Books 1 and 2", "1-2 (range)"},
		{"Batman, Books One and Two", "1-2 (range)"},
		{"Batman, Books 1-3 Boxed Set", "1-3 (range)"},
		// A range with a tail is a list.
		{"Batman, Books 1-3 & 5", "1-3,5 (range)"},
		{"Batman, Books 1-3, 5", "1-3,5 (range)"},
		{"Batman, Books 1-3 and 5", "1-3,5 (range)"},
		{"Batman, Books 1-3 and 4-6", "1-6 (range)"},
		{"Batman, Books 1-3 & 4", "1-4 (range)"},
		{"Batman, Books 1-2 & 4-5", "1-2,4-5 (range)"},
	}
	for _, tc := range cases {
		n, ok := Parse(tc.in)
		got := rng(n, ok)
		if got != tc.want {
			t.Errorf("Parse(%q) = %q (form %q), want %q", tc.in, got, n.Form, tc.want)
		}
	}
	// A tail that overlaps the span is no list and no gappy range.
	if n, ok := Parse("Batman, Books 1-3 & 3"); ok && n.IsRange() && strings.Contains(n.Text, ",") {
		t.Errorf("an overlapping tail read as the list %q", n.Text)
	}
	// "Books 1-3 & 5" is never equal to the bare "Books 1-3" or to "Books 1-5".
	list, _ := Parse("Batman, Books 1-3 & 5")
	for _, other := range []string{"Batman, Books 1-3", "Batman, Books 1-5", "Batman, Books 1-4"} {
		o, _ := Parse(other)
		if Equal(list, o) || Equal(o, list) {
			t.Errorf("%q is Equal to %q", "Books 1-3 & 5", other)
		}
	}
	// The position field reads the same list the same way.
	pos, ok := ParsePosition("1-3 & 5")
	if !ok || !Equal(pos, list) {
		t.Errorf("ParsePosition(1-3 & 5) = %+v ok=%v, want Equal to the title's list %+v", pos, ok, list)
	}
	if bare, _ := ParsePosition("1-3"); Equal(pos, bare) {
		t.Error("the position list 1-3 & 5 must not equal 1-3")
	}
}

// ParsePositionNoRange is the reading a numeric field keeps: it never returns
// a range, whatever the text says, and agrees with ParsePosition on every
// single number.
func TestParsePositionNoRange(t *testing.T) {
	for _, in := range []string{"1-3", "Books 1-3", "1 & 2", "Books 1, 2 & 3", "1, 3", "Books 1-3 & 5", "Vols 4 – 6", "1.5-2"} {
		if n, ok := ParsePositionNoRange(in); ok && n.IsRange() {
			t.Errorf("ParsePositionNoRange(%q) = range %q", in, n.Text)
		}
	}
	for _, in := range []string{"3", "03", "Book 3", "Batman Book 3", "III", "2.5", "08 - Hush"} {
		a, aok := ParsePositionNoRange(in)
		b, bok := ParsePosition(in)
		if aok != bok || a.Value != b.Value || b.IsRange() {
			t.Errorf("single %q: NoRange %+v/%v, ParsePosition %+v/%v", in, a, aok, b, bok)
		}
	}
	// The values the catalog stored before ranges existed.
	for in, want := range map[string]float64{"1 & 2": 1, "Books 1 and 2": 2, "Books 1, 2 & 3": 3, "1, 3": 3} {
		if n, ok := ParsePositionNoRange(in); !ok || n.Value != want {
			t.Errorf("ParsePositionNoRange(%q) = %v ok=%v, want %v", in, n.Value, ok, want)
		}
	}
}
