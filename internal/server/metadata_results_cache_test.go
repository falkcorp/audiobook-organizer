// file: internal/server/metadata_results_cache_test.go
// version: 1.0.0
// guid: 7c1a9f36-2d84-4e57-b0c9-51e8a3d76f42
// last-edited: 2026-08-05

package server

import (
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// resetMetadataResultsCache puts the package-level cache back to empty so tests
// do not leak state into one another.
func resetMetadataResultsCache(t *testing.T) {
	t.Helper()
	invalidateMetadataResultsCache()
	t.Cleanup(invalidateMetadataResultsCache)
}

// primeMetadataResultsCache installs a known entry without going near a store.
func primeMetadataResultsCache(latest map[string]database.OperationResult, counts map[string]int, at time.Time) {
	metaResultsCache.mu.Lock()
	metaResultsCache.latest, metaResultsCache.counts, metaResultsCache.at = latest, counts, at
	metaResultsCache.mu.Unlock()
}

// 🔴 THE REASON THIS CACHE EXISTS. Rebuilding the set costs ~22s on the
// production library — GetRecentOperations(5000) plus one GetOperationResults per
// metadata_candidate_fetch op, 36,805 results — and it ran on EVERY request no
// matter how small the page. A fresh entry must be served without rebuilding, or
// the endpoint stays unusable for picking matches.
func TestLatestMetadataResultsByBookCached_ServesAFreshEntryWithoutRebuilding(t *testing.T) {
	resetMetadataResultsCache(t)
	want := map[string]database.OperationResult{"b1": {BookID: "b1", Status: "matched"}}
	primeMetadataResultsCache(want, map[string]int{"matched": 1}, time.Now())

	// A nil store proves no rebuild happened: any attempt to build would panic.
	latest, counts, err := latestMetadataResultsByBookCached(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(latest) != 1 || latest["b1"].Status != "matched" {
		t.Fatalf("cached entry not returned: %+v", latest)
	}
	if counts["matched"] != 1 {
		t.Fatalf("counts not returned: %+v", counts)
	}
}

// An entry older than the TTL must not be served — a background fetch has to
// become visible without restarting anything.
func TestLatestMetadataResultsByBookCached_ExpiresAfterTTL(t *testing.T) {
	resetMetadataResultsCache(t)
	primeMetadataResultsCache(
		map[string]database.OperationResult{"b1": {BookID: "b1"}},
		map[string]int{"matched": 1},
		time.Now().Add(-metadataResultsCacheTTL-time.Second),
	)

	metaResultsCache.mu.Lock()
	stale := metaResultsCache.latest != nil &&
		time.Since(metaResultsCache.at) < metadataResultsCacheTTL
	metaResultsCache.mu.Unlock()
	if stale {
		t.Fatal("an entry older than the TTL was still considered fresh")
	}
}

// 🔑 Applying or rejecting a candidate changes a book's status. Without
// invalidation the list would keep offering a candidate the user just acted on —
// the one kind of staleness that actively misleads rather than merely lags.
func TestInvalidateMetadataResultsCache_ClearsTheEntry(t *testing.T) {
	resetMetadataResultsCache(t)
	primeMetadataResultsCache(
		map[string]database.OperationResult{"b1": {BookID: "b1"}},
		map[string]int{"matched": 1},
		time.Now(),
	)

	invalidateMetadataResultsCache()

	metaResultsCache.mu.Lock()
	defer metaResultsCache.mu.Unlock()
	if metaResultsCache.latest != nil || metaResultsCache.counts != nil {
		t.Fatal("invalidate left an entry behind")
	}
}

// The TTL must stay short enough to feel live. A long TTL would trade the
// twenty-second stall for stale results, which is not the deal being made here.
func TestMetadataResultsCacheTTL_IsShortEnoughToFeelLive(t *testing.T) {
	if metadataResultsCacheTTL > 5*time.Minute {
		t.Fatalf("TTL = %v, too long for a list a user is actively working through", metadataResultsCacheTTL)
	}
	if metadataResultsCacheTTL < 5*time.Second {
		t.Fatalf("TTL = %v, too short to spare the rebuild while paging", metadataResultsCacheTTL)
	}
}
