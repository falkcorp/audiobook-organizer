// file: internal/server/series_prune_phase1_refcount_test.go
// version: 1.0.0
// guid: 3f8c1d64-7a52-4be0-9c31-64d0f2a8ab17
// last-edited: 2026-08-30

package server

import (
	"context"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// SERIES-DELETE-UNGUARDED (#2908): the PHASE 1 reference guard.
//
// Phase 1's repointFailed gate answers "did every row I was HANDED get
// repointed?". The delete needs the other question answered — "is anything
// still pointing at this series?" — and until this guard existed nothing asked
// it before phase 1's DeleteSeries. Phase 2's ref counter is read AFTER phase 1
// has already finished, so it could not protect it.
//
// GetBooksBySeriesIDAllVersions returns non-primary versions but SKIPS TRASHED
// rows, so a series whose books are all trashed enumerates EMPTY, repoints
// nothing, passes repointFailed == 0 and was deleted — stranding every trashed
// row. That is the surviving half of the shape that produced 6,893 phantom
// series IDs held by 13,322 live books on production 2026-08-14.
//
// The fixtures below seed that FILTERED/UNFILTERED asymmetry directly. A
// fixture where the enumeration and the count agree passes with or without the
// guard, so it would prove nothing.

// newPhase1PruneFixture builds two same-name series that phase 1 groups and
// merges. Series 2 is the merged-away one; `visible` is what the series getters
// return for it, refCounts is what the UNFILTERED counter says.
//
// Series 1 always has one visible book so it wins the canonical vote.
func newPhase1PruneFixture(
	t *testing.T,
	visible []database.BookCore,
	refCounts map[int]int,
) (seriesRefCountingStore, *[]int, map[string]int) {
	t.Helper()
	const (
		keepID  = 1
		mergeID = 2
	)
	mock := &database.MockStore{}
	mock.GetAllSeriesFunc = func() ([]database.Series, error) {
		return []database.Series{
			{ID: keepID, Name: "Discworld"},
			{ID: mergeID, Name: "  discworld "},
		}, nil
	}
	// Both getters set EXPLICITLY. MockStore.GetBooksBySeriesIDAllVersions
	// falls back to GetBooksBySeriesIDCoreFunc when its own stub is nil, so
	// leaving it unset would silently model a store phase 1 does not read.
	mock.GetBooksBySeriesIDCoreFunc = func(id int) ([]database.BookCore, error) {
		switch id {
		case keepID:
			return []database.BookCore{{ID: "keeper-book"}}, nil
		case mergeID:
			return visible, nil
		}
		return nil, nil
	}
	mock.GetBooksBySeriesIDAllVersionsFunc = func(id int) ([]database.BookCore, error) {
		switch id {
		case keepID:
			return []database.BookCore{{ID: "keeper-book"}}, nil
		case mergeID:
			return visible, nil
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
	deleted := &[]int{}
	mock.DeleteSeriesFunc = func(id int) error {
		*deleted = append(*deleted, id)
		return nil
	}
	return seriesRefCountingStore{MockStore: mock, refCounts: refCounts}, deleted, repointed
}

// TestExecuteSeriesPrune_Phase1RefusesDeleteWhenEveryReferencingBookIsTrashed
// is the headline case from #2908.
//
// Series 2 enumerates EMPTY because both of its books are in the trash, and
// both series getters skip soft-deleted rows. Nothing to repoint, no failure to
// record, and the row was deleted anyway. This is NOT an empty fixture: two
// books genuinely hold series 2, they are simply invisible to the getter.
func TestExecuteSeriesPrune_Phase1RefusesDeleteWhenEveryReferencingBookIsTrashed(t *testing.T) {
	s := newSeriesPruneServer(t)
	// keepID is referenced too, so phase 2 deletes nothing and every delete
	// observed below would have come from phase 1.
	store, deleted, repointed := newPhase1PruneFixture(t, nil, map[int]int{1: 1, 2: 2})

	err := s.executeSeriesPrune(context.Background(), store, seriesPruneNoopProgress{}, "")

	if len(*deleted) != 0 {
		t.Fatalf("series %v was deleted; the two trashed rows holding series 2 are now stranded "+
			"on a series ID that no longer resolves", *deleted)
	}
	if err == nil {
		t.Fatal("the refusal must be surfaced to the caller — a silent skip trades a data-loss " +
			"bug for a silent no-op, and the operator reads this error")
	}
	if !strings.Contains(err.Error(), "still reference it") {
		t.Errorf("error does not name the refusal: %v", err)
	}
	if len(repointed) != 0 {
		t.Errorf("nothing was visible to repoint, got %v", repointed)
	}
}

// TestExecuteSeriesPrune_Phase1StillDeletesWhenNothingHiddenReferencesIt is the
// POSITIVE CONTROL. Without it a guard that refuses EVERY delete passes the
// test above while silently turning phase 1 into a no-op.
func TestExecuteSeriesPrune_Phase1StillDeletesWhenNothingHiddenReferencesIt(t *testing.T) {
	s := newSeriesPruneServer(t)
	store, deleted, repointed := newPhase1PruneFixture(t,
		[]database.BookCore{{ID: "visible-book"}}, map[int]int{1: 1, 2: 1})

	if err := s.executeSeriesPrune(context.Background(), store, seriesPruneNoopProgress{}, ""); err != nil {
		t.Fatalf("executeSeriesPrune: %v", err)
	}

	if len(*deleted) != 1 || (*deleted)[0] != 2 {
		t.Fatalf("the one reference was reassigned, so series 2 is genuinely unreferenced and "+
			"must still be deleted; got %v", *deleted)
	}
	if repointed["visible-book"] != 1 {
		t.Errorf("visible-book must be repointed to series 1, got %d", repointed["visible-book"])
	}
}

// TestExecuteSeriesPrune_RefusesToMergeWithoutTheUnfilteredCount pins the
// fail-closed claim for PHASE 1 specifically.
//
// The equivalent guard existed only in front of phase 2, so a store that could
// not answer the unfiltered question still ran the whole merge phase — deleting
// rows — before aborting. Nothing may be deleted now.
func TestExecuteSeriesPrune_RefusesToMergeWithoutTheUnfilteredCount(t *testing.T) {
	s := newSeriesPruneServer(t)
	mock := &database.MockStore{}
	mock.GetAllSeriesFunc = func() ([]database.Series, error) {
		return []database.Series{
			{ID: 1, Name: "Discworld"},
			{ID: 2, Name: "discworld"},
		}, nil
	}
	mock.GetBooksBySeriesIDCoreFunc = func(int) ([]database.BookCore, error) { return nil, nil }
	mock.DeleteSeriesFunc = func(int) error {
		t.Fatal("must not delete anything when the unfiltered count is unavailable")
		return nil
	}

	err := s.executeSeriesPrune(context.Background(), noRefCountPruneStore{mock},
		seriesPruneNoopProgress{}, "")
	if err == nil {
		t.Fatal("a store that cannot count unfiltered references must abort the prune")
	}
	if !strings.Contains(err.Error(), "unfiltered reference counts") {
		t.Errorf("error does not name the fail-closed reason: %v", err)
	}
}

// noRefCountPruneStore satisfies seriesPruneStore but deliberately does NOT
// satisfy database.SeriesBookRefStore. Listing the methods explicitly (rather
// than embedding *MockStore) is what drops the promoted
// GetAllSeriesBookRefCounts, which is the only way to model a store that cannot
// answer the unfiltered question.
type noRefCountPruneStore struct{ m *database.MockStore }

func (s noRefCountPruneStore) GetAllSeries() ([]database.Series, error) { return s.m.GetAllSeries() }
func (s noRefCountPruneStore) GetBooksBySeriesIDCore(id int) ([]database.BookCore, error) {
	return s.m.GetBooksBySeriesIDCore(id)
}
func (s noRefCountPruneStore) GetBooksBySeriesIDAllVersions(id int) ([]database.BookCore, error) {
	return s.m.GetBooksBySeriesIDAllVersions(id)
}
func (s noRefCountPruneStore) DeleteSeries(id int) error { return s.m.DeleteSeries(id) }
func (s noRefCountPruneStore) GetBookByID(id string) (*database.Book, error) {
	return s.m.GetBookByID(id)
}
func (s noRefCountPruneStore) UpdateBook(id string, b *database.Book) (*database.Book, error) {
	return s.m.UpdateBook(id, b)
}
func (s noRefCountPruneStore) CreateOperationChange(c *database.OperationChange) error {
	return s.m.CreateOperationChange(c)
}
