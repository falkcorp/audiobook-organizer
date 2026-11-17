// file: internal/metadata/asin_test.go
// version: 1.0.0
// guid: 2c9e4a17-b5d3-4860-8f1a-d07e3b6c5a94
// last-edited: 2026-11-16

package metadata

import "testing"

func TestCanonicalASIN(t *testing.T) {
	for _, tc := range []struct {
		name, in, want string
		ok             bool
	}{
		{"modern", "B0GOTHAM99", "B0GOTHAM99", true},
		{"trimmed", "  B0GOTHAM99\n", "B0GOTHAM99", true},
		{"lower case with digits is a real ASIN in the wrong case", "b0gotham99", "B0GOTHAM99", true},
		{"nine digits and a digit", "0123456789", "0123456789", true},
		{"nine digits and X", "012345678X", "012345678X", true},
		{"nine digits and lower x", "012345678x", "012345678X", true},
		{"an upper-case B word as written", "BATMOBILES", "BATMOBILES", true},
		{"a ten-letter word", "Batmobiles", "", false},
		{"a lower-case ten-letter word", "batmobiles", "", false},
		{"a ten-letter word not starting with B", "Gothamcity", "", false},
		{"a ten-letter upper-case word not starting with B", "GOTHAMCITY", "", false},
		{"too short", "B0GOTHAM9", "", false},
		{"too long", "B0GOTHAM999", "", false},
		{"punctuation", "B0GOTHAM9!", "", false},
		{"inner space", "B0GOTHA M9", "", false},
		{"X not last", "01234567X9", "", false},
		{"empty", "", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := CanonicalASIN(tc.in)
			if got != tc.want || ok != tc.ok {
				t.Fatalf("CanonicalASIN(%q) = %q, %v; want %q, %v", tc.in, got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestASINInText(t *testing.T) {
	for _, tc := range []struct {
		name, in, want string
	}{
		{"the whole text", "B0GOTHAM99", "B0GOTHAM99"},
		{"in a sentence", "Gotham Knights B0GOTHAM99 unabridged", "B0GOTHAM99"},
		{"in brackets", "Year One [B0GOTHAM99]", "B0GOTHAM99"},
		{"with punctuation", "(B0GOTHAM99),", "B0GOTHAM99"},
		// The failure that sent 800 title words to Audnexus.
		{"a ten-letter title word", "The Batmobiles Chronicle", ""},
		{"a ten-letter word that is the whole title", "Batmobiles", ""},
		{"an upper-case B word", "BATMOBILES", ""},
		{"an upper-case B word in a title", "THE BATMOBILES", ""},
		{"lower case is not an ASIN as written", "b0gotham99", ""},
		{"a ten-digit token is not an ASIN in free text", "Year 0123456789 Knights", ""},
		{"no token", "hello world", ""},
		{"empty", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ASINInText(tc.in); got != tc.want {
				t.Fatalf("ASINInText(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
