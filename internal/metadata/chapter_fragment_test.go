// file: internal/metadata/chapter_fragment_test.go
// version: 1.0.0
// guid: 3a9c0e21-6f48-4b7d-95a2-1c8f0d4e7b52
// last-edited: 2026-06-21

package metadata

import "testing"

func TestIsLikelyChapterFragment(t *testing.T) {
	cases := []struct {
		name  string
		title string
		want  bool
	}{
		// --- Chapter fragments: must be TRUE ---
		{"number then chapter", "06 Chapter 6", true},
		{"bare chapter", "Chapter 6", true},
		{"number dash track", "01 - Track 1", true},
		{"number dot track", "01. Track 1", true},
		{"disc with number", "Disc 2", true},
		{"cd with number", "CD 3", true},
		{"section with number", "Section 4", true},
		{"part with number", "Part 2", true},
		{"number then part", "12 Part 3", true},
		{"zero padded two digit", "06", true},
		{"zero padded one digit", "01", true},
		{"zero padded three digit", "012", true},
		{"lowercase chapter", "chapter 12", true},

		// --- Real books: must be FALSE ---
		{"real title moons", "The Moons of Barsk", false},
		{"real title metro", "Metro 2034", false},
		{"year title 1984", "1984", false},
		{"year title 2001", "2001", false},
		{"bare number 451", "451", false},
		{"part of your world", "Part of Your World", false},
		{"discworld", "Discworld", false},
		{"catch 22", "Catch-22", false},
		{"empty", "", false},
		{"whitespace only", "   ", false},
		{"chapter no number", "Chapter House Dune", false},
		{"section no number", "Sectional Sofas", false},
		{"track no number", "Off the Beaten Track", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsLikelyChapterFragment(tc.title); got != tc.want {
				t.Errorf("IsLikelyChapterFragment(%q) = %v, want %v", tc.title, got, tc.want)
			}
		})
	}
}
