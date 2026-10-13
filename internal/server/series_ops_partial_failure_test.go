// file: internal/server/series_ops_partial_failure_test.go
// version: 1.1.0
// guid: 5d8a2f37-c1e9-4b64-a0f3-7e29b6d41c85
// last-edited: 2026-10-13

package server

import (
	"context"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/dedup"
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
	reg := seriesMergeOpTestReg(t)
	require.NoError(t, s.RegisterSeriesDedupOp(reg))
	def, ok := reg.Def("dedup.series-dedup")
	require.True(t, ok)
	rep := &seriesDedupOpReporter{}
	return rep, def.Run(context.Background(), []byte(params), rep)
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
	assert.Contains(t, incomplete, "kept 1 series with 2 error(s)")
	assert.NotContains(t, incomplete, "completed", "a failed apply must not write a 'completed' activity row")
}
