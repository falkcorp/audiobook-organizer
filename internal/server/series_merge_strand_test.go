// file: internal/server/series_merge_strand_test.go
// version: 1.1.0
// guid: 7b1c4e29-3a86-4d51-9f70-2c8ad6be4415
// last-edited: 2026-08-30

package server

import (
	"context"
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

// TestExecuteSeriesPrune_MergeRepointsNonPrimaryVersions covers the OTHER
// stranding path in this file: phase 1 of the prune, which folds same-named
// series together.
//
// This one is worth its own test rather than trusting the mergeSeriesGroupHelper
// case above, because it is a SEPARATE loop with its own getter call -- the two
// were fixed together but nothing structurally ties them, so a later edit can
// regress one while the other stays green.
//
// Phase 1 now has its own unfiltered reference guard (SERIES-DELETE-UNGUARDED,
// #2908) in addition to the phase-2 orphan guard, so the refCounts below must
// agree with what the getter returns or the delete is refused and this test
// observes nothing. That is exactly why the fixture supplies mergeID: 2 — both
// rows are reassigned, so nothing is left holding the row.
func TestExecuteSeriesPrune_MergeRepointsNonPrimaryVersions(t *testing.T) {
	const (
		keepID     = 1
		mergeID    = 2
		primary    = "prune-primary"
		nonPrimary = "prune-alternate-rip"
	)

	s := newSeriesPruneServer(t)

	mock := &database.MockStore{}
	// Same normalized name + author, so phase 1 groups them and merges.
	mock.GetAllSeriesFunc = func() ([]database.Series, error) {
		return []database.Series{
			{ID: keepID, Name: "Discworld"},
			{ID: mergeID, Name: "  discworld "},
		}, nil
	}

	// Equal Core counts, so the canonical is the lower ID and mergeID is the one
	// folded away. The getters disagree only for the series being merged away.
	mock.GetBooksBySeriesIDCoreFunc = func(id int) ([]database.BookCore, error) {
		switch id {
		case keepID:
			return []database.BookCore{{ID: "keeper-book"}}, nil
		case mergeID:
			return []database.BookCore{{ID: primary}}, nil
		}
		return nil, nil
	}
	mock.GetBooksBySeriesIDAllVersionsFunc = func(id int) ([]database.BookCore, error) {
		switch id {
		case keepID:
			return []database.BookCore{{ID: "keeper-book"}}, nil
		case mergeID:
			return []database.BookCore{{ID: primary}, {ID: nonPrimary}}, nil
		}
		return nil, nil
	}

	mock.GetBookByIDFunc = func(id string) (*database.Book, error) {
		sid := mergeID
		return &database.Book{ID: id, SeriesID: &sid}, nil
	}

	repointed := map[string]int{}
	mock.UpdateBookFunc = func(id string, b *database.Book) (*database.Book, error) {
		if b.SeriesID != nil {
			repointed[id] = *b.SeriesID
		}
		return b, nil
	}

	deleted := []int{}
	mock.DeleteSeriesFunc = func(id int) error {
		deleted = append(deleted, id)
		return nil
	}

	// Both series are referenced, so phase 2 deletes nothing and every delete
	// observed below came from the phase-1 merge.
	store := seriesRefCountingStore{MockStore: mock, refCounts: map[int]int{keepID: 2, mergeID: 2}}

	if err := s.executeSeriesPrune(context.Background(), store, seriesPruneNoopProgress{}, ""); err != nil {
		t.Fatalf("executeSeriesPrune: %v", err)
	}

	if len(deleted) != 1 || deleted[0] != mergeID {
		t.Fatalf("expected the merged-away series %d deleted exactly once, got %v -- "+
			"if empty, phase 1 did not merge and the assertions below prove nothing",
			mergeID, deleted)
	}

	if got, ok := repointed[primary]; !ok || got != keepID {
		t.Errorf("primary %s: repointed to %d (present=%v), want %d", primary, got, ok, keepID)
	}

	if got, ok := repointed[nonPrimary]; !ok || got != keepID {
		t.Errorf("non-primary %s was NOT repointed to %d (present=%v, got %d) -- "+
			"series %d was deleted unconditionally by phase 1, so this row now "+
			"references a series that does not exist", nonPrimary, keepID, ok, got, mergeID)
	}
}
