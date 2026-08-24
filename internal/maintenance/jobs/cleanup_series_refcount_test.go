// file: internal/maintenance/jobs/cleanup_series_refcount_test.go
// version: 1.3.0
// guid: 2f871254-fb7f-475b-a668-ec240f1b0ef3
// last-edited: 2026-08-24

package jobs

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// TASK-044. cleanup-series decides what to delete from GetAllSeriesBookCounts
// and a series membership getter. Both SKIP trashed rows, and the Core getter it
// originally used also skipped non-primary ones. That is right for a display
// count and wrong as an existence test: a series whose remaining books are all
// hidden reads as empty, gets deleted, and those books are left pointing at a
// series ID that no longer resolves.
//
// The membership read is now GetBooksBySeriesIDAllVersions, so a non-primary
// version is no longer invisible. The guard is unchanged and still required:
// trashed rows and rows the memdb counts but Pebble cannot hydrate are still
// counted by refCounts and still absent from the getter.
//
// database/series_bookref.go records the production result of exactly this:
// 6,893 phantom series IDs held by 13,322 live books, accumulated a night at a
// time because this job runs unattended.
//
// Every fixture below therefore makes the two counts DISAGREE. A fixture where
// they agree passes with or without the guard.

// csFakeMerger implements the narrow seriesMerger slice csMergeSeriesGroup
// takes. Only the membership getter is modelled -- the whole point is that it
// cannot see every referencing row.
type csFakeMerger struct {
	visible      map[int][]database.BookCore
	unhydratable map[string]bool
	deleted      []int
	updated      []string
}

// unhydratable models the memdb/Pebble split: GetAllSeriesBookRefCounts reads
// the memdb when warm, GetBookByID reads Pebble, so a row can be listed and
// counted while hydrating to (nil, nil) on every single run.
func (f *csFakeMerger) GetBookByID(id string) (*database.Book, error) {
	if f.unhydratable[id] {
		return nil, nil
	}
	return &database.Book{ID: id}, nil
}

func (f *csFakeMerger) UpdateBook(id string, b *database.Book) (*database.Book, error) {
	f.updated = append(f.updated, id)
	return b, nil
}

// GetBooksBySeriesIDAllVersions, not ...Core: csMergeSeriesGroup was switched to
// the complete-set getter so a non-primary version stops being a reason a row is
// invisible here. What can still hide is a TRASHED row (both series getters skip
// soft-deleted books) or an unhydratable one, and `visible` models exactly that
// residue -- the fixtures below still make the getter and the ref count disagree.
func (f *csFakeMerger) GetBooksBySeriesIDAllVersions(seriesID int) ([]database.BookCore, error) {
	return f.visible[seriesID], nil
}

func (f *csFakeMerger) DeleteSeries(id int) error {
	f.deleted = append(f.deleted, id)
	return nil
}

func TestCsMergeSeriesGroup_KeepsSeriesRowWhenHiddenBooksStillReferenceIt(t *testing.T) {
	// Series 7 shows ONE book to the membership getter but FOUR rows reference
	// it. The other three are trashed or unhydratable: the reassignment below
	// cannot see them, so deleting the row would strand them.
	f := &csFakeMerger{visible: map[int][]database.BookCore{7: {{ID: "VISIBLE"}}}}

	merged, refused, err := csMergeSeriesGroup(f, 1, []int{7}, map[int]int{7: 4})
	if err != nil {
		t.Fatalf("csMergeSeriesGroup returned an error, want a clean refusal: %v", err)
	}
	if merged != 0 || refused != 1 {
		t.Fatalf("a refusal must be reported to the caller as (merged=0, refused=1), got (%d, %d) — "+
			"the caller counts merged>0 as an applied group, so a mis-report makes the refusal invisible",
			merged, refused)
	}

	for _, id := range f.deleted {
		if id == 7 {
			t.Fatal("deleted series 7 while 3 rows the getter cannot see still reference it — this is the phantom-series-ID bug")
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

	merged, refused, err := csMergeSeriesGroup(f, 1, []int{7}, map[int]int{7: 1})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if merged != 1 || refused != 0 {
		t.Fatalf("a real merge must report (merged=1, refused=0), got (%d, %d)", merged, refused)
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

	merged, refused, err := csMergeSeriesGroup(f, 1, []int{7}, map[int]int{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if merged != 1 || refused != 0 {
		t.Fatalf("an absent key means unreferenced, so this must report (merged=1, refused=0), got (%d, %d)", merged, refused)
	}
	if len(f.deleted) != 1 || f.deleted[0] != 7 {
		t.Fatalf("a series absent from the ref-count map is unreferenced and must be deleted, got deleted=%v", f.deleted)
	}
}

func TestCsMergeSeriesGroup_RefusesWhenARowCannotBeHydrated(t *testing.T) {
	// The membership getter lists two books for series 7 and refCounts agrees at
	// 2, so the counts do NOT disagree here -- this fixture isolates the other
	// way the guard can be defeated. GHOST hydrates to (nil, nil), which is
	// what a memdb row that Pebble no longer holds looks like from here.
	//
	// If the nil skip were allowed to count toward moved, moved would reach 2,
	// stranded would be 0, and series 7 would be deleted on a row we never
	// confirmed we had moved. That is the same fail-open shape as the filtered
	// count itself.
	f := &csFakeMerger{
		visible:      map[int][]database.BookCore{7: {{ID: "REAL"}, {ID: "GHOST"}}},
		unhydratable: map[string]bool{"GHOST": true},
	}

	merged, refused, err := csMergeSeriesGroup(f, 1, []int{7}, map[int]int{7: 2})
	if err != nil {
		t.Fatalf("an unhydratable row must be a clean refusal, not an error: %v", err)
	}
	if merged != 0 || refused != 1 {
		t.Fatalf("want (merged=0, refused=1), got (%d, %d)", merged, refused)
	}
	for _, id := range f.deleted {
		if id == 7 {
			t.Fatal("deleted series 7 after failing to hydrate one of the two rows that reference it")
		}
	}
	// The hydratable row is still moved -- refusing the DELETE must not also
	// abandon the reassignment work that succeeded.
	if len(f.updated) != 1 || f.updated[0] != "REAL" {
		t.Fatalf("the hydratable book must still be reassigned, got updated=%v", f.updated)
	}
}
