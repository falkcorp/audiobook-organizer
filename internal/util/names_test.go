// file: internal/util/names_test.go
// version: 1.0.0
// guid: 6c05f39a-71b4-4e28-9d1a-3f8b0c27e5d6
// last-edited: 2026-08-17

package util_test

import (
	"reflect"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/util"
)

func TestSplitCreditNames(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		// The case that motivated this: " & " was the ONLY separator, so every
		// comma-separated credit stayed one "narrator".
		{"comma", "Kate Reading, Michael Kramer", []string{"Kate Reading", "Michael Kramer"}},
		{"ampersand", "Michael Kramer & Kate Reading", []string{"Michael Kramer", "Kate Reading"}},
		{"and", "Michael Kramer and Kate Reading", []string{"Michael Kramer", "Kate Reading"}},
		{"semicolon", "Michael Kramer; Kate Reading", []string{"Michael Kramer", "Kate Reading"}},
		{"with", "Michael Kramer with Kate Reading", []string{"Michael Kramer", "Kate Reading"}},
		{"slash", "Michael Kramer/Kate Reading", []string{"Michael Kramer", "Kate Reading"}},
		{"bare ampersand", "Michael Kramer&Kate Reading", []string{"Michael Kramer", "Kate Reading"}},
		{"three", "A Adams, B Brown & C Clark", []string{"A Adams", "B Brown", "C Clark"}},

		// Must NOT split: one person written surname-first.
		{"surname first", "Smith, John", []string{"Smith, John"}},
		{"surname first spaced", "Le Guin, Ursula", []string{"Le Guin, Ursula"}},
		// Errs toward NOT splitting: a list ending in a mononym stays compound.
		// Merging two names is easier to spot and undo than shredding one person
		// into two phantom narrators.
		{"mononym tail stays compound", "Kate Reading, Cher", []string{"Kate Reading, Cher"}},

		// Single names pass through untouched.
		{"single", "Michael Kramer", []string{"Michael Kramer"}},
		{"empty", "", []string{""}},

		// Duplicates collapse rather than creating two identical rows.
		{"dupe", "Kate Reading & Kate Reading", []string{"Kate Reading"}},
		{"dupe case", "Kate Reading & KATE READING", []string{"Kate Reading"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := util.SplitCreditNames(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("SplitCreditNames(%q) = %#v, want %#v", tc.in, got, tc.want)
			}
		})
	}
}

// The old gate was strings.Contains(name, " & "), which meant the splitter never
// even ran for comma-separated credits — the bug behind the bug.
func TestIsCompoundCreditName(t *testing.T) {
	compound := []string{
		"Kate Reading, Michael Kramer",
		"Michael Kramer & Kate Reading",
		"Michael Kramer and Kate Reading",
		"A; B",
	}
	for _, s := range compound {
		if !util.IsCompoundCreditName(s) {
			t.Errorf("%q must be treated as compound", s)
		}
	}
	simple := []string{"Michael Kramer", "Smith, John", "", "Le Guin, Ursula"}
	for _, s := range simple {
		if util.IsCompoundCreditName(s) {
			t.Errorf("%q must NOT be treated as compound", s)
		}
	}
}
