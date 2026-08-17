// file: internal/util/names_test.go
// version: 1.1.0
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

// 🔴 THE OXFORD COMMA LEAKS PUNCTUATION INTO A PERSON'S NAME.
//
// Measured on the live library 2026-08-17, AFTER the splitter shipped: the
// narrator list contained BOTH "Alan Barnes" (14 books) and "Alan Barnes," (1
// book) as separate people, and 8 such pairs in total across 3,289 entries. The
// trailing comma is not in the source data — the splitter creates it.
//
// Why: creditSeparators applies " & " BEFORE ", ". The real credit below splits on
// " & " into "Lance Parkin, Stephen Cole, Alan Barnes," + "Jonathan Morris"; the
// later ", " pass then cannot reach the final comma, because it is now at the end
// of the string with no space after it and so matches no separator. TrimSpace
// removes whitespace only, and the comma rides through into the narrator table.
func TestSplitCreditNames_OxfordCommaDoesNotLeakPunctuation(t *testing.T) {
	// The literal narratorName of a real book in the production library. A
	// constructed input would have verified my model of the bug rather than the
	// bug.
	const real = "Lance Parkin, Stephen Cole, Alan Barnes, & Jonathan Morris"
	want := []string{"Lance Parkin", "Stephen Cole", "Alan Barnes", "Jonathan Morris"}
	if got := util.SplitCreditNames(real); !reflect.DeepEqual(got, want) {
		t.Errorf("SplitCreditNames(%q) =\n  %#v\nwant\n  %#v", real, got, want)
	}
}

// Once the punctuation is trimmed, the two spellings must collapse to ONE entry
// rather than to two that merely look alike. Pinned separately because a fix that
// trimmed but ran before the de-dup pass would still emit a duplicate.
func TestSplitCreditNames_TrimmedFormsDeduplicate(t *testing.T) {
	got := util.SplitCreditNames("Alan Barnes, & Alan Barnes")
	if want := []string{"Alan Barnes"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v, want %#v — the trimmed forms are the same person", got, want)
	}
}

// 🔴 TRIM SEPARATORS, NOT PUNCTUATION IN GENERAL. A period and a hyphen are part of
// real names; stripping them would mint a NEW phantom duplicate ("Sammy Davis Jr"
// alongside "Sammy Davis Jr.") — the same defect this fix exists to remove, one
// character class over.
func TestSplitCreditNames_KeepsPunctuationThatBelongsToTheName(t *testing.T) {
	keep := []string{"Sammy Davis Jr.", "Alex Hill-Knight", "E. E. Knight", "Smith, John"}
	for _, s := range keep {
		if got := util.SplitCreditNames(s); len(got) != 1 || got[0] != s {
			t.Errorf("SplitCreditNames(%q) = %#v, want it left exactly alone", s, got)
		}
	}
}
