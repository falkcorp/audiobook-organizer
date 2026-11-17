// file: internal/transcribe/numbers_test.go
// version: 1.0.0
// guid: 41d8c6b2-0a97-4e53-9f1c-8b25e7a0d364
// last-edited: 2026-11-17

package transcribe

import "testing"

// TestScanNumber pins the number grammar a structural marker reads after its
// keyword. Every row is invented; names and titles are Batman's.
func TestScanNumber(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want int
		used int
		ok   bool
	}{
		// digits
		{"digits", "12. The Batcave", 12, 2, true},
		{"zero digit", "0", 0, 1, true},
		{"three digits", "105 Arkham", 105, 3, true},
		{"four digits is a year", "1939 Detective Comics", 0, 0, false},
		{"digits glued to letters", "3rd Gotham", 0, 0, false},

		// roman numerals: uppercase, canonical, clause-terminating
		{"roman IV end", "IV", 4, 2, true},
		{"roman before capital", "XV THE QUEEN", 15, 2, true},
		{"roman before punctuation", "IX. Gotham", 9, 2, true},
		{"roman XXXIX", "XXXIX", 39, 5, true},
		{"roman I before lowercase is a pronoun", "I wanted the cowl", 0, 0, false},
		{"roman lowercase refused", "iv", 0, 0, false},
		{"roman non-canonical", "IIII", 0, 0, false},
		{"roman glued to a word", "Vivid", 0, 0, false},

		// spelled cardinals
		{"spelled one", "one of the", 1, 3, true},
		{"spelled fifteen", "Fifteen", 15, 7, true},
		{"spelled sixteen", "sixteen", 16, 7, true},
		{"spelled twenty", "twenty", 20, 6, true},
		{"spelled compound space", "twenty one", 21, 10, true},
		{"spelled compound hyphen", "thirty-three", 33, 12, true},
		{"spelled hundred", "one hundred", 100, 11, true},
		{"spelled hundred and", "one hundred and five", 105, 20, true},
		{"spelled hundred twenty", "one hundred twenty three", 123, 24, true},
		{"spelled stops after unit", "one two three", 1, 3, true},
		{"spelled ten hundred refused", "ten hundred", 10, 3, true},
		{"spelled zero", "zero", 0, 4, true},
		{"spelled non number", "wanted", 0, 0, false},
		{"and without a number after hundred", "one hundred and nothing", 100, 11, true},

		// ordinals need "the"
		{"the first", "the first", 1, 9, true},
		{"the twelfth", "The Twelfth", 12, 11, true},
		{"the twenty-first", "the twenty-first", 21, 16, true},
		{"the one hundred first", "the one hundred first", 101, 21, true},
		{"the hundredth", "the one hundredth", 100, 17, true},
		{"the plus cardinal refused", "the one", 0, 0, false},
		{"the plus word refused", "the best", 0, 0, false},
		{"ordinal without the refused", "first", 0, 0, false},
		{"the plus digits refused", "the 5", 0, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, used, ok := scanNumber(tc.in)
			if ok != tc.ok || (ok && (got != tc.want || used != tc.used)) {
				t.Fatalf("scanNumber(%q) = (%d, %d, %v), want (%d, %d, %v)", tc.in, got, used, ok, tc.want, tc.used, tc.ok)
			}
		})
	}
}

func TestParseSpokenNumberWholeToken(t *testing.T) {
	cases := map[string]int{
		"12": 12, "twelve": 12, "Twenty-One": 21, "twenty one": 21, "one hundred and five": 105,
		"": 0, "twelve monkeys": 0, "banana": 0,
	}
	for in, want := range cases {
		if got := parseSpokenNumber(in); got != want {
			t.Errorf("parseSpokenNumber(%q) = %d, want %d", in, got, want)
		}
	}
}
