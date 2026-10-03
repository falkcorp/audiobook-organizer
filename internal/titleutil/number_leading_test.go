// file: internal/titleutil/number_leading_test.go
// version: 1.0.0
// guid: 66f3802d-360e-4363-a3f7-5133246b0f32
// last-edited: 2026-10-03

package titleutil_test

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/titleutil"
)

func TestIsNumberLeadingTitle(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		// Stray track-number titles: the thing the gauge counts.
		{"01 The Storm", true},
		{"3 - Chapter Three", true},
		{"  12 Anything  ", true},
		{"1Q85", true}, // near-miss of an allowlisted title is NOT allowlisted
		{"1984 Audiobook", true},
		{"20000 Leagues Under the Sea", true}, // allowlist is exact, not fuzzy

		// Allowlisted real titles, exact and case-insensitive after trimming.
		{"1984", false},
		{"  1984  ", false},
		{"11/22/63", false},
		{"11.22.63", false},
		{"2001: A Space Odyssey", false},
		{"2001: a space odyssey", false},
		{"2010: Odyssey Two", false},
		{"1q84", false},
		{"1491", false},
		{"1493", false},
		{"1776", false},
		{"12 RULES FOR LIFE", false},
		{"13 Reasons Why", false},
		{"20,000 Leagues Under the Sea", false},
		{"1632", false},
		{"1635", false},
		{"2312", false},
		{"1066", false},
		{"84k", false},

		// Not digit-leading at all.
		{"", false},
		{"   ", false},
		{"The Hobbit", false},
		{"#1 Bestseller", false},
		{"(1/8) Tarkin", false},
		{"Ⅳ Roman Numeral", false},
	}
	for _, tc := range cases {
		if got := titleutil.IsNumberLeadingTitle(tc.in); got != tc.want {
			t.Errorf("IsNumberLeadingTitle(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// TestLegitNumberTitles_AllStartWithDigit guards the allowlist's own invariant:
// an entry that does not start with a digit can never be reached by the
// predicate and is a typo.
func TestLegitNumberTitles_AllStartWithDigit(t *testing.T) {
	if len(titleutil.LegitNumberTitles) != 17 {
		t.Errorf("LegitNumberTitles has %d entries, want 17 (update this pin when the list changes on purpose)", len(titleutil.LegitNumberTitles))
	}
	for _, title := range titleutil.LegitNumberTitles {
		if title == "" || title[0] < '0' || title[0] > '9' {
			t.Errorf("allowlist entry %q does not start with a digit", title)
		}
		if titleutil.IsNumberLeadingTitle(title) {
			t.Errorf("allowlist entry %q is reported as number-leading", title)
		}
	}
}
