// file: internal/server/series_merge_strand_test.go
// version: 1.0.0
// guid: 7b1c4e29-3a86-4d51-9f70-2c8ad6be4415
// last-edited: 2026-08-24

package server

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// TestMergeSeriesGroupHelper_RepointsNonPrimaryVersions pins the stranding fix.
//
// mergeSeriesGroupHelper repoints every book it is handed to keepID and then
// calls DeleteSeries(fromID) UNCONDITIONALLY -- there is no ref-count guard on
// this path. So whatever the getter hides is not merely skipped, it is left
// holding a series row that no longer exists.
//
// The fixture models the only thing that matters: the Core listing getter and
// the AllVersions getter DISAGREE. Core returns the primary alone; AllVersions
// also returns the alternate rip. Reading Core here is the bug.
//
// Note this cannot be asserted by counting UpdateBook calls alone -- a call
// count of 1 is what the buggy version produces AND what a fixture with a
// single book produces. The assertion has to name the non-primary row.
func TestMergeSeriesGroupHelper_RepointsNonPrimaryVersions(t *testing.T) {
	const (
		keepID   = 1
		mergeID  = 2
		primary  = "book-primary"
		nonPrim  = "book-nonprimary"
		otherSID = mergeID
	)

	store := &database.MockStore{}

	store.GetBooksBySeriesIDCoreFunc = func(id int) ([]database.BookCore, error) {
		if id == mergeID {
			return []database.BookCore{{ID: primary}}, nil
		}
		return nil, nil
	}
	store.GetBooksBySeriesIDAllVersionsFunc = func(id int) ([]database.BookCore, error) {
		if id == mergeID {
			return []database.BookCore{{ID: primary}, {ID: nonPrim}}, nil
		}
		return nil, nil
	}

	store.GetBookByIDFunc = func(id string) (*database.Book, error) {
		sid := otherSID
		return &database.Book{ID: id, SeriesID: &sid}, nil
	}

	repointed := map[string]int{}
	store.UpdateBookFunc = func(id string, b *database.Book) (*database.Book, error) {
		if b.SeriesID != nil {
			repointed[id] = *b.SeriesID
		}
		return b, nil
	}

	deleted := []int{}
	store.DeleteSeriesFunc = func(id int) error {
		deleted = append(deleted, id)
		return nil
	}

	if err := mergeSeriesGroupHelper(store, keepID, []int{mergeID}); err != nil {
		t.Fatalf("mergeSeriesGroupHelper: %v", err)
	}

	// The series IS deleted -- this path has no guard and does not refuse. That
	// is precisely why every row must be repointed first.
	if len(deleted) != 1 || deleted[0] != mergeID {
		t.Fatalf("expected series %d deleted exactly once, got %v", mergeID, deleted)
	}

	if got, ok := repointed[primary]; !ok || got != keepID {
		t.Errorf("primary %s: repointed to %d (present=%v), want %d", primary, got, ok, keepID)
	}

	if got, ok := repointed[nonPrim]; !ok || got != keepID {
		t.Errorf("non-primary %s was NOT repointed to %d (present=%v, got %d) -- "+
			"series %d was then deleted, so this row now references a series that "+
			"does not exist. The merge must read the complete-set getter, not the "+
			"listing getter.", nonPrim, keepID, ok, got, mergeID)
	}
}
