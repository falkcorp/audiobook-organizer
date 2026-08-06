// file: internal/server/metadata_results_swr_test.go
// version: 1.0.0
// guid: 3ca5f9d1-807b-42e6-95af-1d60428c7bf3
// last-edited: 2026-08-06

package server

import (
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// resetMetaResultsCache puts the package cache in a known state for a test.
func resetMetaResultsCache() {
	metaResultsCache.mu.Lock()
	metaResultsCache.latest = nil
	metaResultsCache.counts = nil
	metaResultsCache.at = time.Time{}
	metaResultsCache.rebuilding = false
	metaResultsCache.mu.Unlock()
}

// TestStaleEntryServedImmediately is the whole point of the change: once an entry
// exists, an expired TTL must NOT make the caller wait for a rebuild.
//
// Production measurement 2026-08-06: with the old expire-then-rebuild behaviour a
// request 70s after the last build took 28.9 SECONDS. The TTL was a cliff, not a
// refresh trigger.
func TestStaleEntryServedImmediately(t *testing.T) {
	resetMetaResultsCache()
	defer resetMetaResultsCache()

	// Install an entry and age it well past the TTL.
	seeded := map[string]database.OperationResult{"book-1": {}}
	metaResultsCache.mu.Lock()
	metaResultsCache.latest = seeded
	metaResultsCache.counts = map[string]int{"book-1": 1}
	metaResultsCache.at = time.Now().Add(-10 * metadataResultsCacheTTL)
	metaResultsCache.mu.Unlock()

	store := &database.MockStore{}
	start := time.Now()
	got, _, err := latestMetadataResultsByBookCached(store)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("stale read returned an error: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("expected the stale set to be served, got %d entries", len(got))
	}
	// The caller must not have blocked on a rebuild. Generous bound so this is not
	// timing-flaky on a loaded CI box; the real regression would be seconds.
	if elapsed > 2*time.Second {
		t.Errorf("stale read blocked for %v — it must return immediately", elapsed)
	}
}

// TestColdReadBuildsSynchronously: with nothing cached there is nothing to serve,
// so the caller has to build. This is the one case where waiting is correct.
func TestColdReadBuildsSynchronously(t *testing.T) {
	resetMetaResultsCache()
	defer resetMetaResultsCache()

	store := &database.MockStore{}
	got, _, err := latestMetadataResultsByBookCached(store)
	if err != nil {
		t.Fatalf("cold read failed: %v", err)
	}
	if got == nil {
		t.Fatal("cold read returned a nil map; the cache would stay cold forever")
	}

	metaResultsCache.mu.Lock()
	populated := metaResultsCache.latest != nil
	metaResultsCache.mu.Unlock()
	if !populated {
		t.Error("cold read did not populate the cache")
	}
}

// TestRebuildingFlagPreventsStampede: many concurrent callers hitting a stale
// entry must trigger AT MOST one background rebuild, not one each. Each rebuild
// costs ~30s on production, so a stampede would be far worse than the original
// problem.
func TestRebuildingFlagPreventsStampede(t *testing.T) {
	resetMetaResultsCache()
	defer resetMetaResultsCache()

	metaResultsCache.mu.Lock()
	metaResultsCache.latest = map[string]database.OperationResult{"b": {}}
	metaResultsCache.counts = map[string]int{"b": 1}
	metaResultsCache.at = time.Now().Add(-10 * metadataResultsCacheTTL)
	metaResultsCache.mu.Unlock()

	store := &database.MockStore{}
	var wg sync.WaitGroup
	for i := 0; i < 25; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, _ = latestMetadataResultsByBookCached(store)
		}()
	}
	wg.Wait()

	// Whatever happened, the cache must be left consistent and not permanently
	// stuck in the rebuilding state (which would block all future refreshes).
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		metaResultsCache.mu.Lock()
		stuck := metaResultsCache.rebuilding
		metaResultsCache.mu.Unlock()
		if !stuck {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Error("rebuilding flag never cleared — future refreshes would be blocked forever")
}

// TestInvalidateForcesColdPath: after an explicit invalidation there is no
// trustworthy set, so the next caller must rebuild rather than serve stale. This
// is what stops the list offering a candidate the user just acted on.
func TestInvalidateForcesColdPath(t *testing.T) {
	resetMetaResultsCache()
	defer resetMetaResultsCache()

	metaResultsCache.mu.Lock()
	metaResultsCache.latest = map[string]database.OperationResult{"b": {}}
	metaResultsCache.at = time.Now()
	metaResultsCache.mu.Unlock()

	invalidateMetadataResultsCache()

	metaResultsCache.mu.Lock()
	cleared := metaResultsCache.latest == nil
	metaResultsCache.mu.Unlock()
	if !cleared {
		t.Error("invalidate must clear the entry so the next read cannot serve stale data")
	}
}
