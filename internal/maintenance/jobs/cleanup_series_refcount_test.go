// file: internal/maintenance/jobs/cleanup_series_refcount_test.go
// version: 1.0.0
// guid: 2f871254-fb7f-475b-a668-ec240f1b0ef3
// last-edited: 2026-08-23

package jobs

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// TASK-044. cleanup-series decides what to delete from GetAllSeriesBookCounts
// and GetBooksBySeriesIDCore, both of which SKIP trashed and non-primary rows.
// That is right for a display count and wrong as an existence test: a series
// whose remaining books are all hidden reads as empty, gets deleted, and those
// books are left pointing at a series ID that no longer resolves.
//
// database/series_bookref.go records the production result of exactly this:
// 6,893 phantom series IDs held by 13,322 live books, accumulated a night at a
// time because this job runs unattended.
//
// Every fixture below therefore makes the two counts DISAGREE. A fixture where
// they agree passes with or without the guard.

// csFakeMerger implements the narrow seriesMerger slice csMergeSeriesGroup
// takes. Only the filtered getter is modelled -- the whole point is that it
// cannot see every referencing row.
type csFakeMerger struct {
	visible map[int][]database.BookCore
	deleted []int
	updated []string
}

func (f *csFakeMerger) GetBookByID(id string) (*database.Book, error) {
	return &database.Book{ID: id}, nil
}

func (f *csFakeMerger) UpdateBook(id string, b *database.Book) (*database.Book, error) {
	f.updated = append(f.updated, id)
	return b, nil
}

func (f *csFakeMerger) GetBooksBySeriesIDCore(seriesID int) ([]database.BookCore, error) {
	return f.visible[seriesID], nil
}

func (f *csFakeMerger) DeleteSeries(id int) error {
	f.deleted = append(f.deleted, id)
	return nil
}

func TestCsMergeSeriesGroup_KeepsSeriesRowWhenHiddenBooksStillReferenceIt(t *testing.T) {
	// Series 7 shows ONE book to the filtered getter but FOUR rows reference
	// it. The other three are trashed or non-primary: the reassignment below
	// cannot see them, so deleting the row would strand them.
	f := &csFakeMerger{visible: map[int][]database.BookCore{7: {{ID: "VISIBLE"}}}}

	if err := csMergeSeriesGroup(f, 1, []int{7}, map[int]int{7: 4}); err != nil {
		t.Fatalf("csMergeSeriesGroup returned an error, want a clean refusal: %v", err)
	}

	for _, id := range f.deleted {
		if id == 7 {
			t.Fatal("deleted series 7 while 3 rows the filtered getter cannot see still reference it — this is the phantom-series-ID bug")
		}
	}
	// The visible book IS still reassigned. Moving it is strictly an
	// improvement and is deliberately not rolled back; only the row removal is
	// refused, so the stranded rows keep a series ID that still resolves.
	if len(f.updated) != 1 || f.updated[0] != "VISIBLE" {
		t.Fatalf("visible book must still be reassigned, got updated=%v", f.updated)
	}
}

func TestCsMergeSeriesGroup_StillDeletesAGenuinelyUnreferencedSeries(t *testing.T) {
	// POSITIVE CONTROL. Without it, a guard that refuses EVERY delete passes
	// the test above while silently turning the job into a no-op.
	f := &csFakeMerger{visible: map[int][]database.BookCore{7: {{ID: "ONLYBOOK"}}}}

	if err := csMergeSeriesGroup(f, 1, []int{7}, map[int]int{7: 1}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	found := false
	for _, id := range f.deleted {
		if id == 7 {
			found = true
		}
	}
	if !found {
		t.Fatal("series 7 was referenced by exactly the one book that got reassigned, so the row is genuinely unreferenced and must be deleted")
	}
}

func TestCsMergeSeriesGroup_AbsentFromRefCountsMeansUnreferenced(t *testing.T) {
	// series_bookref.go's contract: "A series absent from the map is
	// referenced by NOTHING and is safe to delete." Pin that reading, because
	// the alternative -- treating a missing key as unknown and refusing --
	// would make the job stop deleting the orphans it exists to remove.
	f := &csFakeMerger{visible: map[int][]database.BookCore{7: nil}}

	if err := csMergeSeriesGroup(f, 1, []int{7}, map[int]int{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(f.deleted) != 1 || f.deleted[0] != 7 {
		t.Fatalf("a series absent from the ref-count map is unreferenced and must be deleted, got deleted=%v", f.deleted)
	}
}
