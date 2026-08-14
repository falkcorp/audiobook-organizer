// file: internal/server/duplicates_series_cache_test.go
// version: 1.0.0
// guid: 7c1d94b6-2ea8-4f30-9c57-1b0e6a8d3f42
// last-edited: 2026-08-14

package server

import (
	"context"
	"testing"
	"time"

	audiobookspkg "github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/cache"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// seriesPruneNoopProgress discards every reporting call — executeSeriesPrune is
// exercised for its cache side effect here, not its progress output.
type seriesPruneNoopProgress struct{}

func (seriesPruneNoopProgress) UpdateProgress(_, _ int, _ string) error { return nil }
func (seriesPruneNoopProgress) Log(_, _ string, _ *string) error        { return nil }
func (seriesPruneNoopProgress) IsCanceled() bool                        { return false }

// seriesRefCountingStore is a MockStore that also answers the unfiltered
// reference count. executeSeriesPrune refuses to delete anything unless
// database.AsSeriesBookRefStore(store) succeeds, so a bare MockStore would fail
// the guard before reaching the code under test.
type seriesRefCountingStore struct {
	*database.MockStore
	refCounts map[int]int
}

func (s seriesRefCountingStore) GetAllSeriesBookRefCounts() (map[int]int, error) {
	return s.refCounts, nil
}

// newSeriesPruneServer builds the minimum Server needed by executeSeriesPrune,
// with a primed series cache so the test can observe whether it was dropped.
func newSeriesPruneServer(t *testing.T) *Server {
	t.Helper()
	s := &Server{
		seriesCache: cache.NewWithLimit[*audiobookspkg.SeriesWithCountsResponse]("series-test", time.Hour, 1),
	}
	s.seriesCache.Set("all", &audiobookspkg.SeriesWithCountsResponse{Count: 99})
	return s
}

func seriesCacheIsPrimed(s *Server) bool {
	_, ok := s.seriesCache.Get("all")
	return ok
}

// TestExecuteSeriesPrune_InvalidatesSeriesCacheWhenRowsRemoved locks the fix for
// the 2026-08-14 production observation: a prune reported "17 duplicates merged,
// 326 orphans deleted" while /api/v1/series kept serving the pre-prune list,
// which is indistinguishable from a prune that did nothing. The cache carries a
// 24-hour TTL, so "it expires eventually" is not a defence.
func TestExecuteSeriesPrune_InvalidatesSeriesCacheWhenRowsRemoved(t *testing.T) {
	s := newSeriesPruneServer(t)

	// Two distinctly-named series (so Phase 1 merges nothing) that NOTHING
	// references, so Phase 2 deletes both.
	deleted := []int{}
	mock := &database.MockStore{}
	mock.GetAllSeriesFunc = func() ([]database.Series, error) {
		return []database.Series{{ID: 1, Name: "Discworld"}, {ID: 2, Name: "Safehold"}}, nil
	}
	mock.GetBooksBySeriesIDCoreFunc = func(int) ([]database.BookCore, error) { return nil, nil }
	mock.DeleteSeriesFunc = func(id int) error {
		deleted = append(deleted, id)
		return nil
	}
	store := seriesRefCountingStore{MockStore: mock, refCounts: map[int]int{}}

	if !seriesCacheIsPrimed(s) {
		t.Fatal("precondition: series cache should be primed before the prune")
	}
	if err := s.executeSeriesPrune(context.Background(), store, seriesPruneNoopProgress{}, ""); err != nil {
		t.Fatalf("executeSeriesPrune: %v", err)
	}

	if len(deleted) != 2 {
		t.Fatalf("expected both orphan series deleted, got %v", deleted)
	}
	if seriesCacheIsPrimed(s) {
		t.Error("series cache still primed after a prune that deleted 2 rows — " +
			"/api/v1/series would serve the pre-prune list for up to 24h")
	}
}

// TestExecuteSeriesPrune_KeepsSeriesCacheWhenNothingRemoved is the other half of
// the guard. A run that cleaned nothing changed nothing, and dropping a warm
// cache costs a full recount for no reason — the same rule the author
// conjunction repair follows. Without this case, "invalidate unconditionally"
// would pass the test above.
func TestExecuteSeriesPrune_KeepsSeriesCacheWhenNothingRemoved(t *testing.T) {
	s := newSeriesPruneServer(t)

	mock := &database.MockStore{}
	mock.GetAllSeriesFunc = func() ([]database.Series, error) {
		return []database.Series{{ID: 1, Name: "Discworld"}, {ID: 2, Name: "Safehold"}}, nil
	}
	mock.GetBooksBySeriesIDCoreFunc = func(int) ([]database.BookCore, error) { return nil, nil }
	mock.DeleteSeriesFunc = func(id int) error {
		t.Errorf("DeleteSeries(%d) called, but every series is referenced", id)
		return nil
	}
	// Both series are referenced, so neither is an orphan and nothing merges.
	store := seriesRefCountingStore{MockStore: mock, refCounts: map[int]int{1: 3, 2: 5}}

	if err := s.executeSeriesPrune(context.Background(), store, seriesPruneNoopProgress{}, ""); err != nil {
		t.Fatalf("executeSeriesPrune: %v", err)
	}

	if !seriesCacheIsPrimed(s) {
		t.Error("series cache dropped by a prune that removed nothing — " +
			"a no-op run must not force a full recount")
	}
}
