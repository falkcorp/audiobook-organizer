// file: internal/seqnum/stated_test.go
// version: 1.1.0
// guid: e20b2f44-17f0-486d-b69d-66acf91c7b19
// last-edited: 2026-11-17

package seqnum

import (
	"fmt"
	"strings"
	"testing"
)

func statedText(ss []Stated) string {
	var parts []string
	for _, s := range ss {
		kind := "volume"
		if s.Kind == KindPart {
			kind = "part"
		}
		p := fmt.Sprintf("%s %s", kind, s.Number.Text)
		if s.Of > 0 {
			p += fmt.Sprintf(" of %g", s.Of)
		}
		parts = append(parts, p)
	}
	return strings.Join(parts, "; ")
}

func TestReadStated(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"Hush", ""},
		{"Hush 2", "volume 2"},
		{"Hush, Book 2", "volume 2"},
		{"Hush: Part 2", "part 2"},
		{"Hush: Part II", "part 2"},
		{"Hush Pt. 3", "part 3"},
		{"Hush - Disc 2", "part 2"},
		{"Hush, Part 1 of 3", "part 1 of 3"},
		{"Hush (2 of 3)", "part 2 of 3"},
		{"Hush 1 of 2", "volume 1 of 2"},
		{"Hush, Book 1 of 3", "volume 1 of 3"},
		{"Hush Books 1-3", "volume 1-3"},
		{"Hush: Disc 1-3", ""},
		{"Hush: Parts 1 & 2", ""},
		{"Hush, Book 2: Part 1 of 2", "volume 2; part 1 of 2"},
		{"The Second Book of Hush", "volume 2"},
		{"Hush, Book the Third", "volume 3"},
		{"Hush: The First Part", "part 1"},
		{"Second Gotham", ""},
		{"First Strike", ""},
		{"Hush (1965)", ""},
		{"Night of 7 Owls", ""},
		{"Hush: Chapter 4", ""},
		{"Hush 64kbps", ""},
		{"Gotham 1 - Gotham", "volume 1"},
		// A lone trailing I is never read on its own.
		{"Hush I", ""},
		{"Robin X", ""},
	} {
		if got := statedText(ReadStated(tc.in)); got != tc.want {
			t.Errorf("ReadStated(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestReadPairLoneRoman(t *testing.T) {
	for _, tc := range []struct{ a, b, wantA, wantB string }{
		{"Hush I", "Hush II", "volume 1", "volume 2"},
		{"Hush I", "Hush 2", "volume 1", "volume 2"},
		{"Hush I", "Hush: Part 2", "", "part 2"},
		{"Hush I", "Hush", "", ""},
		{"Hush I", "Hush: Dark Victory", "", ""},
		{"Robin X", "Robin X", "", ""},
		{"Who Am I", "Who Am I 2", "", "volume 2"},
		{"Who Am I 2", "Who Am I", "volume 2", ""},
		{"Hush I", "Hush: Dark Victory II", "", "volume 2"},
		// The numeral beside the I may sit behind a volume marker or in the
		// usual brackets; the marker is not one of the title's words.
		{"Hush I", "Hush, Book 2", "volume 1", "volume 2"},
		{"Hush, Book 2", "Hush I", "volume 2", "volume 1"},
		{"Hush I", "Hush Volume II", "volume 1", "volume 2"},
		{"Hush I", "Hush #2", "volume 1", "volume 2"},
		{"Hush I", "Hush (2)", "volume 1", "volume 2"},
		// ... but only when the words before the I are the same words.
		{"Who Am I", "Who Am I: Book 2", "", "volume 2"},
		{"Who Am I", "Who Am I #2", "", "volume 2"},
		{"Who Am I", "Who Am I (2)", "", "volume 2"},
		{"Hush I", "Gotham, Book 2", "", "volume 2"},
	} {
		sa, sb := ReadPair(tc.a, tc.b)
		if got := statedText(sa); got != tc.wantA {
			t.Errorf("ReadPair(%q, %q) a = %q, want %q", tc.a, tc.b, got, tc.wantA)
		}
		if got := statedText(sb); got != tc.wantB {
			t.Errorf("ReadPair(%q, %q) b = %q, want %q", tc.a, tc.b, got, tc.wantB)
		}
	}
}

func TestReadStatedBareSpan(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"Hush 1-3", "volume 1-3"},
		{"Hush 3", "volume 3"},
		{"Hush Books 1-3", "volume 1-3"},
	} {
		if got := statedText(ReadStated(tc.in)); got != tc.want {
			t.Errorf("ReadStated(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
