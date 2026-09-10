// file: internal/server/series_merge_trashed_gap_test.go
// version: 1.0.0
// guid: 9d6a5f7e-4b1c-4e3a-8f0d-2a7c9b6e5d41
// last-edited: 2026-09-10

package server

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// TestMergeSeriesGroupHelper_RefusesWhenTrashedRowsAreInvisible pins
// SERIES-NORMALIZE-TRASHED-GAP (TODO.md).
//
// mergeSeriesGroupHelper is the third series-merge path (after
// dedup.MergeSeries and executeSeriesPrune's phase 1, both fixed under #2908)
// and, before this change, had NO unfiltered reference guard. It is
// fail-CLOSED on everything it can see -- an unhydratable row or a failed
// UpdateBook returns an error before DeleteSeries fires -- so it cannot strand
// a row it was handed. What it could not see was a TRASHED row: both series
// getters (GetBooksBySeriesIDCore and GetBooksBySeriesIDAllVersions) skip
// soft-deleted books, so a series whose members are all trashed enumerates
// EMPTY and the delete used to fire unconditionally.
//
// PRE-FIX CAPTURE (this exact fixture, against
// mergeSeriesGroupHelper(store, keepID, []int{mergeID}) error -- the old,
// 3-arg/1-return signature with no refCounts parameter at all):
//
//	=== RUN   TestMergeSeriesGroupHelper_RefusesWhenTrashedRowsAreInvisible
//	    series_merge_trashed_gap_test.go:37: series 2 was deleted even though a
//	    trashed row (invisible to both series getters) still references it --
//	    that row is now stranded on a series ID that no longer resolves
//	--- FAIL: TestMergeSeriesGroupHelper_RefusesWhenTrashedRowsAreInvisible (0.00s)
//
// The fixture models exactly that: GetBooksBySeriesIDAllVersions returns no
// books for mergeID (as it does when every member is trashed), while the
// UNFILTERED reference count still shows one book pointing at it.
func TestMergeSeriesGroupHelper_RefusesWhenTrashedRowsAreInvisible(t *testing.T) {
	const (
		keepID  = 1
		mergeID = 2
	)

	store := &database.MockStore{}
	// Both series getters skip soft-deleted books, so a series whose only
	// remaining members are trashed enumerates EMPTY here -- the getter this
	// loop reads literally cannot see the row this test is about.
	store.GetBooksBySeriesIDAllVersionsFunc = func(id int) ([]database.BookCore, error) {
		return nil, nil
	}
	store.UpdateBookFunc = func(id string, b *database.Book) (*database.Book, error) {
		t.Fatalf("no book should be repointed: the getter returned none")
		return b, nil
	}

	var deleted []int
	store.DeleteSeriesFunc = func(id int) error {
		deleted = append(deleted, id)
		return nil
	}

	// The UNFILTERED count still sees one trashed book referencing mergeID --
	// exactly what GetBooksBySeriesIDAllVersions above cannot see.
	refCounts := map[int]int{mergeID: 1}

	merged, refused, err := mergeSeriesGroupHelper(store, keepID, []int{mergeID}, refCounts)
	if err != nil {
		t.Fatalf("mergeSeriesGroupHelper: unexpected hard error: %v", err)
	}
	if refused != 1 || merged != 0 {
		t.Fatalf("expected the delete to be REFUSED (merged=0, refused=1), got merged=%d refused=%d", merged, refused)
	}
	for _, id := range deleted {
		if id == mergeID {
			t.Fatalf("series %d was deleted even though the unfiltered reference count still shows %d "+
				"book(s) pointing at it (a trashed row neither series getter can see) -- that row is now "+
				"stranded on a series ID that no longer resolves", mergeID, refCounts[mergeID])
		}
	}
}
