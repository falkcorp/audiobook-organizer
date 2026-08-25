// file: internal/database/book_sort_conformance_test.go
// version: 1.0.0
// guid: 8a4d2e07-6b39-4c15-9e8f-1d07b3a5c264
// last-edited: 2026-08-25
//
// The real conformance test between the two ways a library page gets ordered.
//
// memdb_sort_indexers_test.go already has a test called
// TestSortIndexOrderMatchesComparator whose comment says "the indexed walk and
// the materialise-and-sort path must produce the SAME order". It does not test
// that. It builds its expected order out of encodeSortableString and the
// index's own accessors, so it compares the index against itself and passes
// however the other path behaves. It also only ever asks for ascending.
//
// Under that green test the two paths disagreed in opposite directions for
// every nullable field: the indexes rank a missing value last ascending
// (0x01 vs 0x00), while the old sortFieldMap ran the value through derefInt /
// derefStr first, turning a nil year into the year 0 and a nil narrator into
// "" -- both of which sort FIRST. Which order a request got depended on
// whether its sort could be pushed down, i.e. on enabled_sort_indexes, not on
// anything the caller asked for.
//
// This test compares the indexed walk against database.SortBooks -- the actual
// other implementation -- in BOTH directions, over a fixture built so the
// disagreement is observable: books with nil values, with genuine zeros, and
// with an empty title.

package database

import (
	"testing"
	"time"
)

func TestIndexedWalkAndSortBooksAgree(t *testing.T) {
	enableAllSortIndexes(t)
	m, err := NewMemStore()
	if err != nil {
		t.Fatalf("NewMemStore: %v", err)
	}

	t1 := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	t2 := time.Date(2024, 6, 15, 12, 0, 0, 0, time.UTC)

	// The fixture is the test. A corpus where every book has every field
	// cannot observe a missing-value disagreement, which is how the existing
	// test stayed green.
	books := []Book{
		{ID: "b1", Title: "One", Narrator: ptrString_mem("Zoe"), Duration: ptrInt_mem(300),
			FileSize: ptrInt64_mem(9000), Bitrate: ptrInt_mem(128),
			AudiobookReleaseYear: ptrInt_mem(2001), CreatedAt: &t1, UpdatedAt: &t2},
		{ID: "b2", Title: "Two", Narrator: ptrString_mem("adam"), Duration: ptrInt_mem(100),
			FileSize: ptrInt64_mem(100), Bitrate: ptrInt_mem(320),
			AudiobookReleaseYear: ptrInt_mem(1999), CreatedAt: &t2, UpdatedAt: &t1},
		// Nothing set at all: every field missing.
		{ID: "b3", Title: "Three"},
		// Genuine zeros, which must sort WITH the numbers rather than with the
		// unknowns -- the distinction (int64, bool) accessors exist to keep and
		// that derefInt() destroyed. PrintYear-only exercises the year
		// fallback.
		{ID: "b4", Title: "Four", Narrator: ptrString_mem("Mabel"), Duration: ptrInt_mem(0),
			FileSize: ptrInt64_mem(0), Bitrate: ptrInt_mem(0),
			PrintYear: ptrInt_mem(1888)},
		// Empty narrator string vs nil narrator: both are "missing" and must
		// rank together, not one at each end.
		{ID: "b5", Title: "Five", Narrator: ptrString_mem(""), Duration: ptrInt_mem(50),
			AudiobookReleaseYear: ptrInt_mem(0)}, // 0 year == absent, not year zero
	}
	seedMemStore(t, m, books, nil, nil, nil)

	fields := []string{
		"title", "narrator", "year", "duration", "bitrate", "file_size",
		"created_at", "updated_at",
	}

	for _, field := range fields {
		for _, asc := range []bool{true, false} {
			dir := "asc"
			if !asc {
				dir = "desc"
			}
			t.Run(field+"/"+dir, func(t *testing.T) {
				got, err := m.GetBookSummaries(0, 0, BookSummaryFilter{
					SortBy: field, SortAscending: asc,
				})
				if err != nil {
					t.Fatalf("GetBookSummaries(%s): %v", field, err)
				}
				if len(got) != len(books) {
					t.Fatalf("indexed walk returned %d books, want %d -- a book "+
						"is missing from the %s index and would vanish from the page",
						len(got), len(books), field)
				}

				want := make([]Book, len(books))
				copy(want, books)
				SortBooks(want, field, asc)

				for i := range want {
					if got[i].ID != want[i].ID {
						t.Fatalf("%s %s: index and SortBooks disagree at position %d\n"+
							"  index walk: %s\n  SortBooks:  %s",
							field, dir, i, summaryIDLine(got), bookIDLine(want))
					}
				}
			})
		}
	}
}

func summaryIDLine(s []BookSummary) string {
	out := ""
	for i := range s {
		if i > 0 {
			out += " "
		}
		out += s[i].ID
	}
	return out
}

func bookIDLine(s []Book) string {
	out := ""
	for i := range s {
		if i > 0 {
			out += " "
		}
		out += s[i].ID
	}
	return out
}

// TestSortBooksRanksMissingLastAscending pins the rule itself, independently of
// any index, so a future change to the encoding cannot quietly redefine what
// "correct" means and take this suite's other assertions with it.
func TestSortBooksRanksMissingLastAscending(t *testing.T) {
	books := []Book{
		{ID: "none", Title: "n"},
		{ID: "zero", Title: "z", Duration: ptrInt_mem(0)},
		{ID: "big", Title: "b", Duration: ptrInt_mem(500)},
	}
	SortBooks(books, "duration", true)
	if got := bookIDLine(books); got != "zero big none" {
		t.Errorf("ascending duration = %q, want %q (a real 0 sorts with the "+
			"numbers; only an absent value goes last)", got, "zero big none")
	}

	SortBooks(books, "duration", false)
	if got := bookIDLine(books); got != "none big zero" {
		t.Errorf("descending duration = %q, want %q -- descending is served by "+
			"txn.GetReverse, a FULL reversal, so the missing bucket flips too",
			got, "none big zero")
	}
}

// TestSortBooksUnknownFieldIsANoOp documents the deliberate choice: an
// unrecognised field leaves the caller's order alone rather than inventing one.
func TestSortBooksUnknownFieldIsANoOp(t *testing.T) {
	books := []Book{{ID: "c"}, {ID: "a"}, {ID: "b"}}
	SortBooks(books, "no_such_field", true)
	if got := bookIDLine(books); got != "c a b" {
		t.Errorf("unknown field reordered the slice: got %q, want %q", got, "c a b")
	}
	if CanSortBooksBy("no_such_field") {
		t.Error("CanSortBooksBy accepted a field with no comparator")
	}
	for _, f := range []string{"title", "year", "author", "genre", "sample_rate"} {
		if !CanSortBooksBy(f) {
			t.Errorf("CanSortBooksBy(%q) = false, want true", f)
		}
	}
}
