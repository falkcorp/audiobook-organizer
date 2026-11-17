// file: internal/metafetch/collected_edition_test.go
// version: 1.0.0
// guid: 0e7b3a95-2c48-4f16-b9d3-5a81c7e4f620
// last-edited: 2026-11-16

package metafetch

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

// SameCollectedEdition decides whether the compilation penalty is lifted. The
// default is "penalise"; every row that is not an exact collected-edition
// match must be false.
func TestSameCollectedEdition(t *testing.T) {
	cases := []struct {
		name                     string
		book, title, sub, series string
		want                     bool
	}{
		// Lifted: the same collected edition.
		{"same range in title and subtitle", "Knightfall, Books 1-3", "Knightfall", "Books 1-3", "", true},
		{"same range, different spelling", "Knightfall, Books 1-3", "Knightfall", "Vols 1 to 3", "", true},
		{"same range from the series position", "Knightfall Omnibus, Books 1-3", "Knightfall Omnibus", "", "1-3", true},
		{"same range, book side only in title", "Year One Box Set, Books 1-2", "Year One Box Set", "Books 1-2", "", true},
		{"same wording, neither numbered", "Knightfall Omnibus", "Knightfall Omnibus Edition", "", "", true},
		{"same pack number", "Publisher's Pack 2", "Publisher's Pack 2", "", "", true},

		// Penalised: the safety refusals.
		{"different range", "Knightfall, Books 1-3", "Knightfall", "Books 1-5", "", false},
		{"different range end", "Knightfall, Books 1-3", "Knightfall", "Books 2-3", "", false},
		{"book is a range, candidate is not", "Knightfall, Books 1-3", "Knightfall Omnibus", "", "", false},
		{"candidate is a range, book is not", "Knightfall Omnibus", "Knightfall", "Books 1-3", "", false},
		{"single volume vs a compilation", "Knightfall", "Knightfall Omnibus", "", "", false},
		{"single volume numbered vs a range", "Knightfall, Book 1", "Knightfall", "Books 1-3", "", false},
		{"empty book title", "", "Knightfall Omnibus", "", "", false},
		{"blank book title", "   ", "Knightfall Omnibus", "", "", false},
		{"different wording", "Knightfall Omnibus", "Knightfall Box Set", "", "", false},
		{"different pack numbers", "Publisher's Pack 2", "Publisher's Pack 3", "", "", false},
		{"one side numbered, same wording", "Publisher's Pack 2", "Publisher's Pack", "", "", false},
		{"the other side numbered, same wording", "Publisher's Pack", "Publisher's Pack 2", "", "", false},
		{"different counts of books", "Knightfall 3 books", "Knightfall 5 books", "", "", false},
		{"a gappy list is not the span", "Knightfall, Books 1, 3", "Knightfall", "Books 1-3", "", false},
		// Word boundaries: "recollection" is not "collection".
		{"substring is not wording", "A Recollection", "A Recollection", "", "", false},
		{"substring on one side only", "Collection of Shadows", "Recollection of Shadows", "", "", false},
		{"no wording at all", "Knightfall", "Knightfall", "", "", false},
		// A plain dash in a title is not a range.
		{"hyphenated title", "Catch-22", "Catch-22 Omnibus", "", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := SameCollectedEdition(c.book, c.title, c.sub, c.series); got != c.want {
				t.Fatalf("SameCollectedEdition(%q, %q, %q, %q) = %v, want %v", c.book, c.title, c.sub, c.series, got, c.want)
			}
		})
	}
}

func TestCollectedWordingClasses_WordBoundaries(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"Knightfall Omnibus", []string{"omnibus"}},
		{"Recollection", nil},
		{"Collection of Shadows", []string{"collection"}},
		{"Boxed Set", []string{"box set"}},
		{"Box-Set Edition", []string{"box set"}},
		{"The Complete Series", []string{"complete"}},
		{"Anthology", []string{"anthology"}},
		{"3 Books in 1", []string{"3 books"}},
		{"Bookshelf Set", nil},
	}
	for _, c := range cases {
		got := collectedWordingClasses(c.in)
		if len(got) != len(c.want) {
			t.Errorf("%q: %v, want %v", c.in, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%q: %v, want %v", c.in, got, c.want)
			}
		}
	}
}

// The scorer: an omnibus book is no longer scored x0.15 against its own
// omnibus, and every neighbour keeps the penalty.
func TestApplyNonBaseAdjustments_CollectedEdition(t *testing.T) {
	penalty := scoringKnobs().CompilationPenalty
	cand := metadata.BookMetadata{Title: "Knightfall Omnibus", Subtitle: "Books 1-3"}
	plain := metadata.BookMetadata{Title: "Knightfall Omnibus"}

	if got := ApplyNonBaseAdjustments(1.0, cand, 0, "Knightfall Omnibus, Books 1-3"); got < 0.99 {
		t.Errorf("the same collected edition was penalised: %.3f", got)
	}
	for name, bookTitle := range map[string]string{
		"no book in scope":      "",
		"a single volume":       "Knightfall",
		"a different range":     "Knightfall Omnibus, Books 1-5",
		"an unnumbered omnibus": "Knightfall Omnibus",
	} {
		if got := ApplyNonBaseAdjustments(1.0, cand, 0, bookTitle); got > penalty+0.001 {
			t.Errorf("%s: score %.3f, want the %.2f penalty kept", name, got, penalty)
		}
	}
	if got := ApplyNonBaseAdjustments(1.0, plain, 0, "Knightfall"); got > penalty+0.001 {
		t.Errorf("single volume vs compilation candidate: %.3f, want the %.2f penalty", got, penalty)
	}

	// The breakdown records the lifted step as x1.00 so a reviewer sees it was considered.
	_, steps := ApplyNonBaseAdjustmentsWithBreakdown(1.0, cand, 0, "Knightfall Omnibus, Books 1-3")
	found := false
	for _, s := range steps {
		if s.ID == "compilation" {
			found = true
			if s.Operand != 1 {
				t.Errorf("lifted compilation step operand %v, want 1", s.Operand)
			}
		}
	}
	if !found {
		t.Error("lifted compilation step not recorded in the breakdown")
	}
}

// Score and breakdown agree on the new parameter.
func TestScoreOneResult_CollectedEditionBreakdownAgrees(t *testing.T) {
	words := SignificantWords("Knightfall Omnibus")
	cand := metadata.BookMetadata{Title: "Knightfall Omnibus", Subtitle: "Books 1-3"}
	for _, bt := range []string{"", "Knightfall Omnibus, Books 1-3", "Knightfall"} {
		want := ScoreOneResult(cand, words, bt)
		got, _ := ScoreOneResultWithBreakdown(cand, words, bt)
		if got != want {
			t.Errorf("book %q: breakdown %.6f != score %.6f", bt, got, want)
		}
	}
}
