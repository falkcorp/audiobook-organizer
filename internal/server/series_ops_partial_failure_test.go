// file: internal/server/series_ops_partial_failure_test.go
// version: 1.3.0
// guid: 5d8a2f37-c1e9-4b64-a0f3-7e29b6d41c85
// last-edited: 2026-10-30

package server

import (
	"context"
	"errors"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/dedup"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// These pin what the two series write ops REPORT when they leave series behind.
//
// The Authors & series lane drops a group from its list when the merge op
// completes, and keeps only the ids the failure text names when it does not. A
// run that left series behind and still returned nil would therefore be shown
// as a finished merge while the duplicates were still there -- the defect
// entities.author-merge had until the same change fixed it.

// seriesPartialStore builds a store of series with the given (id, name, author)
// and, per series, the books that enumerate (visible) against the UNFILTERED
// reference count. refCount > visible is a trashed row: the delete is refused.
type seriesPartialSeries struct {
	id       int
	name     string
	visible  []database.BookCore
	refCount int
}

func newSeriesPartialStore(series []seriesPartialSeries) (seriesRefCountingStore, *[]int) {
	mock := &database.MockStore{}
	byID := make(map[int]seriesPartialSeries, len(series))
	refs := make(map[int]int, len(series))
	for _, s := range series {
		byID[s.id] = s
		refs[s.id] = s.refCount
	}
	owner := map[string]int{}
	for _, s := range series {
		for _, b := range s.visible {
			owner[b.ID] = s.id
		}
	}
	mock.GetAllSeriesFunc = func() ([]database.Series, error) {
		out := make([]database.Series, 0, len(series))
		for _, s := range series {
			out = append(out, database.Series{ID: s.id, Name: s.name})
		}
		return out, nil
	}
	mock.GetSeriesByIDFunc = func(id int) (*database.Series, error) {
		s, ok := byID[id]
		if !ok {
			return nil, nil
		}
		return &database.Series{ID: s.id, Name: s.name}, nil
	}
	books := func(id int) ([]database.BookCore, error) { return byID[id].visible, nil }
	mock.GetBooksBySeriesIDCoreFunc = books
	mock.GetBooksBySeriesIDAllVersionsFunc = books
	mock.GetBookByIDFunc = func(id string) (*database.Book, error) {
		sid := owner[id]
		return &database.Book{ID: id, SeriesID: &sid}, nil
	}
	mock.UpdateBookFunc = func(_ string, b *database.Book) (*database.Book, error) { return b, nil }
	deleted := &[]int{}
	mock.DeleteSeriesFunc = func(id int) error {
		*deleted = append(*deleted, id)
		return nil
	}
	return seriesRefCountingStore{MockStore: mock, refCounts: refs}, deleted
}

func book(id string) database.BookCore { return database.BookCore{ID: id} }

// One merge id merges, the other is refused: the op fails, names exactly the
// refused id, and the merged one is really gone.
func TestSeriesMergeOp_PartialFailureNamesTheIDsLeftBehind(t *testing.T) {
	store, deleted := newSeriesPartialStore([]seriesPartialSeries{
		{id: 10, name: "Series Keep", refCount: 0},
		{id: 20, name: "Series Good", visible: []database.BookCore{book("BOOK_A")}, refCount: 1},
		{id: 30, name: "Series Held", refCount: 2}, // two trashed rows: delete refused
	})
	reg := seriesMergeOpTestReg(t)
	s := newSeriesMergeOpServer(t, store)
	require.NoError(t, s.RegisterSeriesMergeOp(reg))
	def, ok := reg.Def("dedup.series-merge")
	require.True(t, ok)

	err := def.Run(context.Background(), []byte(`{"keep_id":10,"merge_ids":[20,30]}`), &seriesMergeOpReporter{})

	require.Error(t, err)
	assert.Contains(t, err.Error(), SeriesMergeFailedIDsPrefix+"30]",
		"the left-behind id list is the client's contract")
	assert.NotContains(t, err.Error(), "20]", "20 merged and is gone; naming it would keep a ghost row")
	assert.Equal(t, []int{20}, *deleted)
}

// Every series merged but a later step failed: the op still fails, and the list
// is empty, which tells the client the whole group is gone.
func TestSeriesMergeOp_LaterFailureWithEverythingMergedHasAnEmptyList(t *testing.T) {
	store, deleted := newSeriesPartialStore([]seriesPartialSeries{
		{id: 10, name: "Series Keep"},
		{id: 20, name: "Series Good", visible: []database.BookCore{book("BOOK_A")}, refCount: 1},
	})
	// The kept series has authors on both sides, so the merge links them onto
	// the kept series' books; failing that write is an error after every delete.
	store.MockStore.GetSeriesByIDFunc = func(id int) (*database.Series, error) {
		author := id // distinct author per series
		return &database.Series{ID: id, Name: "Series " + strconv.Itoa(id), AuthorID: &author}, nil
	}
	store.MockStore.GetBooksBySeriesIDAllVersionsFunc = func(id int) ([]database.BookCore, error) {
		if id == 10 {
			return []database.BookCore{book("BOOK_KEEP")}, nil
		}
		return []database.BookCore{book("BOOK_A")}, nil
	}
	store.MockStore.SetBookAuthorsFunc = func(string, []database.BookAuthor) error {
		return assert.AnError
	}
	reg := seriesMergeOpTestReg(t)
	s := newSeriesMergeOpServer(t, store)
	require.NoError(t, s.RegisterSeriesMergeOp(reg))
	def, ok := reg.Def("dedup.series-merge")
	require.True(t, ok)

	err := def.Run(context.Background(), []byte(`{"keep_id":10,"merge_ids":[20]}`), &seriesMergeOpReporter{})

	require.Error(t, err)
	assert.Contains(t, err.Error(), SeriesMergeFailedIDsPrefix+"]")
	assert.Equal(t, []int{20}, *deleted)
}

// dedup.series-dedup used to return nil from an apply that kept a duplicate:
// the refused delete landed in result.Errors, the op logged "complete" and the
// client saw `completed`.
func newSeriesDedupFixture() (seriesRefCountingStore, *[]int) {
	return newSeriesPartialStore([]seriesPartialSeries{
		{id: 1, name: "Alpha", visible: []database.BookCore{book("BOOK_A")}, refCount: 1},
		{id: 2, name: "alpha", refCount: 2}, // two trashed rows: delete refused
		{id: 3, name: "Beta", visible: []database.BookCore{book("BOOK_B")}, refCount: 1},
		{id: 4, name: "beta", visible: []database.BookCore{book("BOOK_C")}, refCount: 1},
	})
}

// seriesDedupOpReporter adds the op id an APPLY requires (its undo-ledger rows
// are keyed by it; DedupSeries refuses to apply without one).
type seriesDedupOpReporter struct{ seriesMergeOpReporter }

func (r *seriesDedupOpReporter) OpID() string { return "op-series-dedup-test" }

func runSeriesDedupOp(t *testing.T, s *Server, params string) (*seriesDedupOpReporter, error) {
	t.Helper()
	rep := &seriesDedupOpReporter{}
	return rep, runSeriesDedupOpWith(t, s, params, rep)
}

func runSeriesDedupOpWith(t *testing.T, s *Server, params string, rep *seriesDedupOpReporter) error {
	t.Helper()
	reg := seriesMergeOpTestReg(t)
	require.NoError(t, s.RegisterSeriesDedupOp(reg))
	def, ok := reg.Def("dedup.series-dedup")
	require.True(t, ok)
	return def.Run(context.Background(), []byte(params), rep)
}

func TestSeriesDedupOp_ApplyThatKeepsASeriesFailsAndNamesIt(t *testing.T) {
	store, deleted := newSeriesDedupFixture()
	s := newSeriesMergeOpServer(t, store)
	s.dedupCache.Set("series-duplicates", map[string]any{"groups": []any{}})

	rep, err := runSeriesDedupOp(t, s, `{"dry_run":false}`)

	require.Error(t, err, "an apply that kept a duplicate series must not report success")
	// The digest must reach op_errors_v2, which the db reporter fills only from
	// error-level entries: at warn the failed run showed zero errors.
	require.Len(t, rep.errorLevelLogs(), 1)
	assert.Contains(t, rep.errorLevelLogs()[0], "refusing to delete series 2")
	assert.Contains(t, err.Error(), SeriesDedupFailedIDsPrefix+"2]")
	assert.NotContains(t, err.Error(), "4]", "the other group merged cleanly")
	assert.NotEmpty(t, *deleted, "the clean group was still merged: a partial failure does not undo the rest")
	_, cached := s.dedupCache.Get("series-duplicates")
	assert.False(t, cached, "series were deleted, so the cached duplicate list is stale even though the run failed")
}

func TestSeriesDedupOp_CleanApplySucceeds(t *testing.T) {
	store, _ := newSeriesPartialStore([]seriesPartialSeries{
		{id: 3, name: "Beta", visible: []database.BookCore{book("BOOK_B")}, refCount: 1},
		{id: 4, name: "beta", visible: []database.BookCore{book("BOOK_C")}, refCount: 1},
	})
	rep, err := runSeriesDedupOp(t, newSeriesMergeOpServer(t, store), `{"dry_run":false}`)
	require.NoError(t, err)
	assert.Empty(t, rep.errorLevelLogs())
}

// A preview deletes nothing, so nothing is "left behind": it keeps returning nil
// even when it predicts a refusal (the refusal text is in the result).
func TestSeriesDedupOp_PreviewDoesNotFail(t *testing.T) {
	store, deleted := newSeriesDedupFixture()
	rep, err := runSeriesDedupOp(t, newSeriesMergeOpServer(t, store), `{}`)
	require.NoError(t, err)
	assert.Empty(t, *deleted)
	// The preview predicts a refusal; that prediction is a warning, not an error
	// the operation's error table should hold.
	assert.Empty(t, rep.errorLevelLogs())
}

func TestSeriesDedupActivityMessage(t *testing.T) {
	merged := dedup.SeriesDedupResult{TotalMerged: 3, TotalBooksReassigned: 7}
	kept := dedup.SeriesDedupResult{TotalMerged: 3, TotalBooksReassigned: 7, LeftIDs: []int{9}, Errors: []string{"x", "y"}}

	assert.Equal(t, "Series deduplication completed: merged 3 series, reassigned 7 books",
		seriesDedupActivityMessage(merged, false))
	assert.Contains(t, seriesDedupActivityMessage(merged, true), "PREVIEW")
	incomplete := seriesDedupActivityMessage(kept, false)
	assert.Contains(t, incomplete, "INCOMPLETE")
	assert.Contains(t, incomplete, "kept 1 series, 2 error(s)")
	assert.NotContains(t, incomplete, "completed", "a failed apply must not write a 'completed' activity row")

	// An apply with errors but nothing kept (an undo-ledger row that could not be
	// written) is incomplete too: the same condition that fails the op.
	ledger := dedup.SeriesDedupResult{TotalMerged: 3, TotalBooksReassigned: 7, Errors: []string{"ledger"}}
	got := seriesDedupActivityMessage(ledger, false)
	assert.Contains(t, got, "INCOMPLETE")
	assert.Contains(t, got, "kept 0 series, 1 error(s)")
	// A preview that predicts errors is still a preview.
	assert.Contains(t, seriesDedupActivityMessage(ledger, true), "PREVIEW")
}

// DedupSeries records two failures that leave NO series behind: a reassigned
// book whose undo-ledger row could not be written, and a deleted series whose
// ledger row could not be written (an un-undoable delete). Neither adds to
// LeftIDs, so a check on LeftIDs alone reported both runs as completed. The op
// fails on any error from an apply, with an empty id list -- the series-merge
// contract.
func TestSeriesDedupOp_FailsWhenAnUndoLedgerRowCouldNotBeWritten(t *testing.T) {
	for _, changeType := range []string{"metadata_update", "series_delete"} {
		t.Run(changeType, func(t *testing.T) {
			store, deleted := newSeriesPartialStore([]seriesPartialSeries{
				{id: 3, name: "Beta", visible: []database.BookCore{book("BOOK_B")}, refCount: 1},
				{id: 4, name: "beta", visible: []database.BookCore{book("BOOK_C")}, refCount: 1},
			})
			store.MockStore.CreateOperationChangeFunc = func(c *database.OperationChange) error {
				if c.ChangeType == changeType {
					return errors.New("ledger down")
				}
				return nil
			}
			s := newSeriesMergeOpServer(t, store)
			s.dedupCache.Set("series-duplicates", map[string]any{"groups": []any{}})

			rep, err := runSeriesDedupOp(t, s, `{"dry_run":false}`)

			require.Error(t, err, "a run that cannot be undone must not report success")
			assert.Contains(t, err.Error(), SeriesDedupFailedIDsPrefix+"]", "nothing was kept, so the list is empty")
			assert.Contains(t, err.Error(), "undo-ledger")
			assert.Equal(t, []int{4}, *deleted, "the merge itself went ahead")
			assert.NotEmpty(t, rep.errorLevelLogs())
			_, cached := s.dedupCache.Get("series-duplicates")
			assert.False(t, cached)
		})
	}
}

// A cancel (or a lost stand-down lease) returns an error after the groups
// already processed were merged. The cached duplicate list must not keep
// offering them.
func TestSeriesDedupOp_CancelAfterSomeGroupsStillInvalidatesTheCache(t *testing.T) {
	store, deleted := newSeriesPartialStore([]seriesPartialSeries{
		{id: 1, name: "Alpha", visible: []database.BookCore{book("BOOK_A")}, refCount: 1},
		{id: 2, name: "alpha", visible: []database.BookCore{book("BOOK_A2")}, refCount: 1},
		{id: 3, name: "Beta", visible: []database.BookCore{book("BOOK_B")}, refCount: 1},
		{id: 4, name: "beta", visible: []database.BookCore{book("BOOK_C")}, refCount: 1},
	})
	s := newSeriesMergeOpServer(t, store)
	s.dedupCache.Set("series-duplicates", map[string]any{"groups": []any{}})
	rep := &seriesDedupOpReporter{}
	rep.cancelAfter = 1 // the first group goes, the second check is a cancel

	err := runSeriesDedupOpWith(t, s, `{"dry_run":false}`, rep)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "cancelled")
	assert.Len(t, *deleted, 1, "one group merged before the cancel")
	_, cached := s.dedupCache.Get("series-duplicates")
	assert.False(t, cached, "the merged group's series are gone, so the cached list is stale")
}

func TestSeriesDedupOp_PreviewCancelLeavesTheCacheAlone(t *testing.T) {
	store, _ := newSeriesPartialStore([]seriesPartialSeries{
		{id: 1, name: "Alpha", visible: []database.BookCore{book("BOOK_A")}, refCount: 1},
		{id: 2, name: "alpha", visible: []database.BookCore{book("BOOK_A2")}, refCount: 1},
		{id: 3, name: "Beta", visible: []database.BookCore{book("BOOK_B")}, refCount: 1},
		{id: 4, name: "beta", visible: []database.BookCore{book("BOOK_C")}, refCount: 1},
	})
	s := newSeriesMergeOpServer(t, store)
	s.dedupCache.Set("series-duplicates", map[string]any{"groups": []any{}})
	rep := &seriesDedupOpReporter{}
	rep.cancelAfter = 1

	require.Error(t, runSeriesDedupOpWith(t, s, `{}`, rep))
	_, cached := s.dedupCache.Get("series-duplicates")
	assert.True(t, cached, "a preview wrote nothing, so there is nothing to invalidate")
}

func TestSeriesMergeOp_CancelAfterSomeSeriesStillInvalidatesTheCache(t *testing.T) {
	store, deleted := newSeriesPartialStore([]seriesPartialSeries{
		{id: 10, name: "Series Keep"},
		{id: 20, name: "Series Good", visible: []database.BookCore{book("BOOK_A")}, refCount: 1},
		{id: 30, name: "Series Next", visible: []database.BookCore{book("BOOK_B")}, refCount: 1},
	})
	s := newSeriesMergeOpServer(t, store)
	s.dedupCache.Set("series-duplicates", map[string]any{"groups": []any{}})
	reg := seriesMergeOpTestReg(t)
	require.NoError(t, s.RegisterSeriesMergeOp(reg))
	def, ok := reg.Def("dedup.series-merge")
	require.True(t, ok)
	rep := &seriesMergeOpReporter{cancelAfter: 1}

	err := def.Run(context.Background(), []byte(`{"keep_id":10,"merge_ids":[20,30]}`), rep)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "cancelled")
	assert.Equal(t, []int{20}, *deleted)
	_, cached := s.dedupCache.Get("series-duplicates")
	assert.False(t, cached)
}

// ---- the lane's merges carry the protections Merge All used to have --------------

func lockedBookStore() seriesRefCountingStore {
	store := newSeriesMergeOpStore([]database.BookCore{{ID: "BOOK_LOCKED"}}, 1)
	store.MockStore.GetMetadataFieldStatesFunc = func(bookID string) ([]database.MetadataFieldState, error) {
		return []database.MetadataFieldState{{BookID: bookID, Field: database.FieldKeySeriesName, OverrideLocked: true}}, nil
	}
	return store
}

func runSeriesMergeOp(t *testing.T, s *Server, params string, rep opsregistry.Reporter) error {
	t.Helper()
	reg := seriesMergeOpTestReg(t)
	require.NoError(t, s.RegisterSeriesMergeOp(reg))
	def, ok := reg.Def("dedup.series-merge")
	require.True(t, ok)
	return def.Run(context.Background(), []byte(params), rep)
}

func TestSeriesMergeOp_HonoursASeriesNameLockByDefault(t *testing.T) {
	store := lockedBookStore()
	err := runSeriesMergeOp(t, newSeriesMergeOpServer(t, store), `{"keep_id":10,"merge_ids":[20]}`, &seriesMergeOpReporter{})

	require.Error(t, err, "the lane's request carries no honor_locks field and must still be protected")
	assert.Contains(t, err.Error(), SeriesMergeFailedIDsPrefix+"20]", "the series is reported as left, by id")
	assert.Contains(t, err.Error(), "user-locked", "with the reason")
}

func TestSeriesMergeOp_HonorLocksFalseRestoresTheOldBehaviour(t *testing.T) {
	store := lockedBookStore()
	require.NoError(t, runSeriesMergeOp(t, newSeriesMergeOpServer(t, store),
		`{"keep_id":10,"merge_ids":[20],"honor_locks":false}`, &seriesMergeOpReporter{}))
}

func TestSeriesMergeOp_HonorLocksTrueMatchesTheDefault(t *testing.T) {
	err := runSeriesMergeOp(t, newSeriesMergeOpServer(t, lockedBookStore()),
		`{"keep_id":10,"merge_ids":[20],"honor_locks":true}`, &seriesMergeOpReporter{})
	require.Error(t, err)
}

// noOpIDReporter has no run id, as a reporter that does not know its operation.
type noOpIDReporter struct{ seriesMergeOpReporter }

func (r *noOpIDReporter) OpID() string { return "" }

// The scan stand-down gate is keyed by the op id; the op passes the server as the
// controller, so a run with no id is refused rather than written ungated.
func TestSeriesMergeOp_RefusesToApplyWithoutAnOperationID(t *testing.T) {
	store, deleted := newSeriesPartialStore([]seriesPartialSeries{
		{id: 10, name: "Series Keep"},
		{id: 20, name: "Series Good", visible: []database.BookCore{book("BOOK_A")}, refCount: 1},
	})
	err := runSeriesMergeOp(t, newSeriesMergeOpServer(t, store), `{"keep_id":10,"merge_ids":[20]}`, &noOpIDReporter{})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "without an operation id")
	assert.Empty(t, *deleted)
}

// dedup.series-dedup read only dry_run until 2026-10-30, so a caller using the
// spelling every neighbouring op accepts ({"dryRun":false}) got a preview and a
// completed-looking run that deleted nothing. Both spellings now reach
// opmode.ResolveDryRun.
func TestSeriesDedupOp_DryRunSpellings(t *testing.T) {
	t.Run("dryRun=false applies", func(t *testing.T) {
		store, deleted := newSeriesDedupFixture()
		_, _ = runSeriesDedupOp(t, newSeriesMergeOpServer(t, store), `{"dryRun":false}`)
		assert.NotEmpty(t, *deleted, "dryRun=false must apply; the camelCase spelling is being dropped")
	})
	for _, body := range []string{`{"dryRun":true}`, `{"dry_run":true}`, `{}`} {
		t.Run("preview "+body, func(t *testing.T) {
			store, deleted := newSeriesDedupFixture()
			_, err := runSeriesDedupOp(t, newSeriesMergeOpServer(t, store), body)
			require.NoError(t, err)
			assert.Empty(t, *deleted)
		})
	}
	for _, body := range []string{`{"dry_run":false,"dryRun":true}`, `{"dry_run":true,"dryRun":false}`} {
		t.Run("conflict "+body, func(t *testing.T) {
			store, deleted := newSeriesDedupFixture()
			_, err := runSeriesDedupOp(t, newSeriesMergeOpServer(t, store), body)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "disagree")
			assert.Contains(t, err.Error(), "dedup.series-dedup")
			assert.Empty(t, *deleted, "a refused body must not delete anything")
		})
	}
}
