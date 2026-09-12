// file: internal/util/natural_test.go
// version: 1.0.0
// guid: d3873230-5350-4d2b-9250-e262226581a1
// last-edited: 2026-09-12

package util

import (
	"slices"
	"strings"
	"testing"
)

func TestCompareNatural(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"2", "10", -1},
		{"10", "2", 1},
		{"Chapter 2.mp3", "Chapter 10.mp3", -1},
		{"Chapter 1.mp3", "Chapter 10.mp3", -1},
		{"Chapter 9.mp3", "Chapter 10.mp3", -1},
		// Leading zeros: equal value, so the byte-wise tie-break decides.
		{"01", "1", -1},
		{"1", "01", 1},
		{"Track 007", "Track 7", -1},
		{"Track 007", "Track 8", -1},
		{"Track 010", "Track 9", 1},
		// Mixed runs: the first differing run decides.
		{"Part 2 - Chapter 9", "Part 2 - Chapter 10", -1},
		{"Part 2 - Chapter 10", "Part 10 - Chapter 1", -1},
		{"Part 10 - Chapter 1", "Part 2 - Chapter 10", 1},
		// Digit runs longer than int64 compare by trimmed length, then digits.
		{"x" + strings.Repeat("9", 30), "x1" + strings.Repeat("0", 30), -1},
		{"x" + strings.Repeat("0", 40) + "5", "x6", -1},
		{"99999999999999999999999", "99999999999999999999998", 1},
		// Case-insensitive text, byte-wise tie-break when only case differs.
		{"alpha 2", "Alpha 10", -1},
		{"A", "a", -1},
		{"a", "A", 1},
		// Unicode text around numbers.
		{"Émile 2", "émile 10", -1},
		{"日本 2", "日本 10", -1},
		{"Über 1", "über 2", -1},
		{"über 10", "Über 9", 1},
		// Prefixes and empties.
		{"", "a", -1},
		{"Chapter", "Chapter 1", -1},
		{"same", "same", 0},
		{"", "", 0},
		// Digit vs non-digit falls back to rune order.
		{"1abc", "abc", -1},
	}
	for _, tc := range cases {
		if got := CompareNatural(tc.a, tc.b); got != tc.want {
			t.Errorf("CompareNatural(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
		// Antisymmetry: swapping the arguments must flip the sign.
		if got := CompareNatural(tc.b, tc.a); got != -tc.want {
			t.Errorf("CompareNatural(%q, %q) = %d, want %d (antisymmetry)", tc.b, tc.a, got, -tc.want)
		}
		if got := NaturalLess(tc.a, tc.b); got != (tc.want < 0) {
			t.Errorf("NaturalLess(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want < 0)
		}
	}
}

func TestCompareNatural_SortsChapterFiles(t *testing.T) {
	var in []string
	for _, n := range []string{"12", "3", "1", "10", "2", "11", "9", "4", "8", "5", "7", "6"} {
		in = append(in, "/lib/Book/Chapter "+n+".mp3")
	}
	slices.SortFunc(in, CompareNatural)
	for i, p := range in {
		want := "/lib/Book/Chapter " + itoa(i+1) + ".mp3"
		if p != want {
			t.Fatalf("position %d = %q, want %q (full order %q)", i, p, want, in)
		}
	}
}

func itoa(n int) string {
	if n < 10 {
		return string(rune('0' + n))
	}
	return itoa(n/10) + string(rune('0'+n%10))
}
