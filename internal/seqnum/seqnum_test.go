// file: internal/seqnum/seqnum_test.go
// version: 1.0.0
// guid: 9e3c1f7b-2a6d-4c80-b5e4-1d8f0a7c3e92
// last-edited: 2026-09-13

package seqnum

import "testing"

func TestParse(t *testing.T) {
	cases := []struct {
		in   string
		want string // "" = no number
	}{
		// The owner's case, verbatim shapes.
		{"Big Cats 1", "1"},
		{"big cats 2", "2"},
		{"Big Cats 3", "3"},
		{"Big Cats #3", "3"},
		{"Big Cats, Book 2", "2"},
		{"Big Cats - 3", "3"},
		{"Book 2", "2"},
		{"Vol. 3", "3"},
		{"Volume III", "3"},
		{"The Expanse, Vol. IV", "4"},
		{"Big Cats Book Two", "2"},
		{"Big Cats II", "2"},
		{"02 - Title", "2"},
		{"02. Title", "2"},
		{"1.5 - The Novella", "1.5"},
		{"Big Cats 1.5", "1.5"},
		{"Big Cats (2)", "2"},
		{"Big Cats [3]", "3"},
		{"Big_Cats_3", "3"},
		// Not sequence numbers.
		{"Dune (1965)", ""},
		{"1984", ""},
		{"Big Cats 2019", ""},
		{"Big Cats 64kbps", ""},
		{"Big Cats 128k", ""},
		{"Big Cats 44.1kHz", ""},
		{"Big Cats Part 1 of 3", ""},
		{"Big Cats Disc 2", ""},
		{"Big Cats CD 1", ""},
		{"Big Cats Pt. II", ""},
		{"Big Cats Track 04", ""},
		{"Malcolm X", ""},
		{"A Civic Duty", ""},
		{"No Country for Old Men", ""},
		{"Big Cats", ""},
		{"", ""},
		// A year and a part marker must not hide a real number next to them.
		{"Big Cats 3 (2019)", "3"},
		{"Big Cats Book 2 Part 1 of 3", "2"},
		{"Big Cats 2 - Disc 1", "2"},
	}
	for _, tc := range cases {
		n, ok := Parse(tc.in)
		got := ""
		if ok {
			got = n.Text
		}
		if got != tc.want {
			t.Errorf("Parse(%q) = %q (ok=%v, form=%s), want %q", tc.in, got, ok, n.Form, tc.want)
		}
	}
}

func TestParsePosition(t *testing.T) {
	cases := []struct{ in, want string }{
		{"3", "3"},
		{"03", "3"},
		{"1.5", "1.5"},
		{"III", "3"},
		{"Book 3", "3"},
		{" 2 ", "2"},
		{"", ""},
		{"2019", ""},
		{"abc", ""},
	}
	for _, tc := range cases {
		n, ok := ParsePosition(tc.in)
		got := ""
		if ok {
			got = n.Text
		}
		if got != tc.want {
			t.Errorf("ParsePosition(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestEqual pins the comparison the guard is built on: a decimal is not its
// integer part, and zero-padded numbers are the same number.
func TestEqual(t *testing.T) {
	mustParse := func(s string) Number {
		t.Helper()
		n, ok := Parse(s)
		if !ok {
			t.Fatalf("Parse(%q) found no number", s)
		}
		return n
	}
	if Equal(mustParse("Big Cats 1"), mustParse("Big Cats 3")) {
		t.Error("Big Cats 1 must not equal Big Cats 3")
	}
	if Equal(mustParse("Big Cats 1"), mustParse("Big Cats 1.5")) {
		t.Error("1 must not equal 1.5")
	}
	if !Equal(mustParse("02 - Big Cats"), mustParse("Big Cats 2")) {
		t.Error("02 must equal 2")
	}
	if !Equal(mustParse("Big Cats Volume III"), mustParse("Big Cats #3")) {
		t.Error("III must equal 3")
	}
}
