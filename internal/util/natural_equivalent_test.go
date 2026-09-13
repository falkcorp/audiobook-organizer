// file: internal/util/natural_equivalent_test.go
// version: 1.0.0
// guid: 7e4b2c19-5a63-4d8f-b1e0-9c36a2f7d584
// last-edited: 2026-09-13

package util

import "testing"

func TestEquivalentNatural(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"Part 1", "part 01", true},
		{"01", "1", true},
		{"Book", "BOOK", true},
		{"Book 2", "Book 10", false},
		{"01", "01 a", false},
		{"a", "b", false},
	}
	for _, c := range cases {
		if got := EquivalentNatural(c.a, c.b); got != c.want {
			t.Errorf("EquivalentNatural(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
		if got := EquivalentNatural(c.b, c.a); got != c.want {
			t.Errorf("EquivalentNatural(%q, %q) = %v, want %v (not symmetric)", c.b, c.a, got, c.want)
		}
		// CompareNatural still orders every distinct pair.
		if c.a != c.b && CompareNatural(c.a, c.b) == 0 {
			t.Errorf("CompareNatural(%q, %q) = 0 for distinct strings", c.a, c.b)
		}
	}
}
