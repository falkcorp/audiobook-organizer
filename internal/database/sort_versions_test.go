// file: internal/database/sort_versions_test.go
// version: 1.0.1
// guid: 4959dece-aa81-423a-bfff-63ecbf764a2b
// last-edited: 2026-09-02

package database

import (
	"sort"
	"testing"
)

// TestSortVersionsTreatsNilFlagAsPrimary pins the rule EffectiveIsPrimaryVersion
// exists to enforce: a nil IsPrimaryVersion counts as PRIMARY.
//
// The fixture carries all three states on purpose. A nil/true or a true/false
// fixture cannot observe this bug: the old comparator read the raw *bool, so it
// agreed with the helper on every explicit value and diverged ONLY on nil. A test
// without a nil-flagged row passes identically before and after the fix.
func TestSortVersionsTreatsNilFlagAsPrimary(t *testing.T) {
	books := []Book{
		{ID: "1", Title: "zeta-explicit-false", IsPrimaryVersion: new(false)},
		{ID: "2", Title: "alpha-nil-flag", IsPrimaryVersion: nil},
		{ID: "3", Title: "beta-explicit-true", IsPrimaryVersion: new(true)},
	}

	sortVersions(books)

	// The nil row and the explicit-true row are both primary, so both must land
	// ahead of the explicit-false row; between them, Title decides.
	want := []string{"alpha-nil-flag", "beta-explicit-true", "zeta-explicit-false"}
	for i, w := range want {
		if books[i].Title != w {
			t.Fatalf("position %d = %q, want %q (full order: %v); a nil flag is being sorted as NON-primary, disagreeing with EffectiveIsPrimaryVersion",
				i, books[i].Title, w, titlesOf(books))
		}
	}
}

// TestSortVersionsIsAValidStrictWeakOrdering guards the comparator's shape, not
// just its nil handling.
//
// The replaced implementation returned from two independent branches: "i is
// primary -> true" then "j is primary -> false". For two primaries that answers
// less(i,j)==true AND less(j,i)==true, violating asymmetry. sort.Slice is free
// to emit any permutation of an inconsistent comparator's input, so the tie order
// was unspecified rather than title-ordered.
func TestSortVersionsIsAValidStrictWeakOrdering(t *testing.T) {
	books := []Book{
		{ID: "1", Title: "delta", IsPrimaryVersion: new(true)},
		{ID: "2", Title: "alpha", IsPrimaryVersion: new(true)},
		{ID: "3", Title: "charlie", IsPrimaryVersion: nil},
		{ID: "4", Title: "bravo", IsPrimaryVersion: new(true)},
		{ID: "5", Title: "echo", IsPrimaryVersion: new(false)},
	}

	sortVersions(books)

	less := func(i, j int) bool {
		iP := EffectiveIsPrimaryVersion(books[i].IsPrimaryVersion)
		jP := EffectiveIsPrimaryVersion(books[j].IsPrimaryVersion)
		if iP != jP {
			return iP
		}
		return books[i].Title < books[j].Title
	}
	if !sort.SliceIsSorted(books, less) {
		t.Fatalf("result is not sorted under the intended ordering: %v", titlesOf(books))
	}

	// All four effective primaries must precede the one explicit non-primary,
	// and be title-ordered among themselves.
	want := []string{"alpha", "bravo", "charlie", "delta", "echo"}
	for i, w := range want {
		if books[i].Title != w {
			t.Fatalf("position %d = %q, want %q (full order: %v)", i, books[i].Title, w, titlesOf(books))
		}
	}
}

func titlesOf(books []Book) []string {
	out := make([]string, 0, len(books))
	for _, b := range books {
		out = append(out, b.Title)
	}
	return out
}
