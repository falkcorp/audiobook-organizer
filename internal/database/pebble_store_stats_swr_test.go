// file: internal/database/pebble_store_stats_swr_test.go
// version: 1.0.0
// guid: 8ddc1437-dc75-40db-b1b8-b1c641ead8e8
// last-edited: 2026-09-07
//
// Stale-while-revalidate tests for the dashboard stats cache. Every test here is
// written to FAIL against the pre-2026-09-07 implementation, where
// InvalidateLibraryStats hard-deleted stats:library and readCachedLibraryStats
// expired at a 10-minute TTL equal to the recompute min-interval.

package database

import (
	"testing"
	"time"
)

// sentinelTotalBooks is a value computeLibraryStats can never produce on an
// empty store, so "did we serve the cache or recompute?" is answerable from the
// returned struct alone.
const sentinelTotalBooks = 424242

func statsTestStore(t *testing.T) *PebbleStore {
	t.Helper()
	s, err := NewPebbleStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// seedCache writes a cached LibraryStats whose ComputedAt is `age` in the past.
func seedCache(t *testing.T, s *PebbleStore, age time.Duration) {
	t.Helper()
	s.writeCachedLibraryStats(&LibraryStats{
		TotalBooks: sentinelTotalBooks,
		ComputedAt: time.Now().Add(-age),
	})
}

// TestGetDashboardStats_ServesStaleAfterInvalidate is the whole point of the
// change. A mutation marks the cache dirty; the very next dashboard load must
// still get the cached numbers instantly rather than blocking on an 87-second
// full scan.
//
// Against the old code InvalidateLibraryStats deleted the key, so
// readCachedLibraryStats returned nil, GetDashboardStats skipped its
// stale-while-revalidate branch entirely and took the blocking cold path —
// returning freshly computed zeroes instead of the sentinel.
func TestGetDashboardStats_ServesStaleAfterInvalidate(t *testing.T) {
	s := statsTestStore(t)
	seedCache(t, s, time.Minute) // fresh by any threshold

	s.InvalidateLibraryStats()

	got, err := s.GetDashboardStats()
	if err != nil {
		t.Fatalf("GetDashboardStats: %v", err)
	}
	if got.TotalBooks != sentinelTotalBooks {
		t.Fatalf("TotalBooks = %d, want the cached %d — invalidation must not destroy the value the dashboard serves",
			got.TotalBooks, sentinelTotalBooks)
	}
}

// TestInvalidateLibraryStats_KeepsTheCachedValue states the same invariant one
// layer down, so a future change to GetDashboardStats cannot quietly reintroduce
// the delete without a second test failing.
func TestInvalidateLibraryStats_KeepsTheCachedValue(t *testing.T) {
	s := statsTestStore(t)
	seedCache(t, s, time.Minute)

	s.InvalidateLibraryStats()

	cached := s.readCachedLibraryStats()
	if cached == nil {
		t.Fatal("readCachedLibraryStats returned nil after invalidation; the value must survive so it can be served while a refresh runs")
	}
	if !s.libraryStatsDirty.Load() {
		t.Fatal("libraryStatsDirty not set; invalidation must still schedule a refresh")
	}
}

// TestGetDashboardStats_ServesValueOlderThanTheOldTTL covers the second half of
// the bug: readCachedLibraryStats used to return nil past 10 minutes, which is
// the SAME number as the recompute min-interval. A value old enough to trigger a
// refresh was therefore always already old enough to be discarded, so the
// stale-while-revalidate branch could only ever run in a one-second window.
func TestGetDashboardStats_ServesValueOlderThanTheOldTTL(t *testing.T) {
	s := statsTestStore(t)
	seedCache(t, s, 30*time.Minute) // well past the old 10-minute TTL

	got, err := s.GetDashboardStats()
	if err != nil {
		t.Fatalf("GetDashboardStats: %v", err)
	}
	if got.TotalBooks != sentinelTotalBooks {
		t.Fatalf("TotalBooks = %d, want the cached %d — an old value must still be served immediately, not discarded",
			got.TotalBooks, sentinelTotalBooks)
	}
}

// TestRefreshThresholdIsFiveMinutes pins the interval the owner asked for. It is
// deliberately a separate assertion from the behaviour tests: those would still
// pass at any threshold, so nothing else would notice this drifting back to 600.
func TestRefreshThresholdIsFiveMinutes(t *testing.T) {
	if got := defaultLibraryCountsMinIntervalSeconds; got != 300 {
		t.Fatalf("defaultLibraryCountsMinIntervalSeconds = %d, want 300 (5 minutes)", got)
	}
}

// TestStartLibraryStatsRecompute_RefusesAfterClose covers the shutdown guard.
// computeLibraryStats iterates p.db and Pebble panics on use-after-close, so a
// recompute must not begin once Close has run. Passing means the mutex was
// released rather than handed to a goroutine.
func TestStartLibraryStatsRecompute_RefusesAfterClose(t *testing.T) {
	s, err := NewPebbleStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	seedCache(t, s, time.Hour)
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	s.startLibraryStatsRecompute("test", 3600)

	// If the call had spawned a goroutine it would hold the mutex while
	// scanning a closed database; TryLock succeeding proves it did not.
	if !s.libraryCountsRecomputeMu.TryLock() {
		t.Fatal("startLibraryStatsRecompute spawned a recompute after Close; it must refuse once the database is closing")
	}
	s.libraryCountsRecomputeMu.Unlock()
}

// TestClose_JoinsInFlightRecompute proves Close waits. The recompute holds
// libraryCountsRecomputeMu for its whole life, so Close taking that mutex is the
// join — this asserts Close cannot return while it is held.
func TestClose_JoinsInFlightRecompute(t *testing.T) {
	s, err := NewPebbleStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}

	// Stand in for an in-flight recompute by holding the mutex the way one does.
	s.libraryCountsRecomputeMu.Lock()
	released := make(chan struct{})
	go func() {
		time.Sleep(150 * time.Millisecond)
		close(released)
		s.libraryCountsRecomputeMu.Unlock()
	}()

	start := time.Now()
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case <-released:
	default:
		t.Fatal("Close returned while a recompute still held libraryCountsRecomputeMu; it must join before closing the database")
	}
	if elapsed := time.Since(start); elapsed < 100*time.Millisecond {
		t.Fatalf("Close returned in %v, too fast to have waited for the in-flight recompute", elapsed)
	}
}
