// file: internal/seqnum/range_test.go
// version: 1.0.0
// guid: 7b2d9e41-5a63-4c08-b1f7-d4e08a36c915
// last-edited: 2026-11-16

package seqnum

import "testing"

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
		{"Catch-22", "22"},
		{"9-1-1", "1"},
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
	for _, in := range []string{"Books 1-3", "Books 1, 3", "Books 1.5-2", "Books 1 & 2"} {
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
	if _, ok := ParseRange("Catch-22"); ok {
		t.Error("an unkeyworded dash is a name, not a range")
	}
	if _, ok := ParseRange("1-3"); ok {
		t.Error("a bare 1-3 in free text is not a range (it is one only in a position field)")
	}
	if n, ok := ParseRange("Books 1-3"); !ok || n.Value != 1 || n.Last != 3 || !n.IsRange() {
		t.Errorf("ParseRange(Books 1-3) = %+v ok=%v", n, ok)
	}
}
