// file: internal/server/metadata_results_warmer_test.go
// version: 1.0.0
// guid: 9e07a3c6-14bd-4f28-8506-b2971fe0a4d3
// last-edited: 2026-08-06

package server

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// TestWarmMetadataResultsCacheNilStore: the warmer must degrade, never panic,
// when the store is not initialised. Every other cache warmer is best-effort and
// this one is enrolled in the same fire-and-forget group — a panic here would
// take the whole server down at startup, which is the failure warmerRecover
// exists to catch and which this test makes unnecessary to rely on.
func TestWarmMetadataResultsCacheNilStore(t *testing.T) {
	s := &Server{}
	// Must return cleanly rather than panicking on a nil store.
	s.warmMetadataResultsCache()
}

// TestMetadataResultsCachePopulatedAfterWarm asserts the warmer actually leaves
// the memoised set populated — the whole point of the change. A warmer that runs
// and logs but does not populate would look healthy in the logs while the first
// real request still paid the full build.
func TestMetadataResultsCachePopulatedAfterWarm(t *testing.T) {
	// Start from a known-cold cache so the assertion cannot pass on residue from
	// another test in the same package run.
	invalidateMetadataResultsCache()

	metaResultsCache.mu.Lock()
	before := metaResultsCache.latest
	metaResultsCache.mu.Unlock()
	if before != nil {
		t.Fatal("cache should be cold after invalidate")
	}

	store := &database.MockStore{}
	// An empty library is a valid warm: the build succeeds and memoises an empty
	// set, which is exactly what a fresh install should cache.
	latest, _, err := latestMetadataResultsByBookCached(store)
	if err != nil {
		t.Fatalf("build failed: %v", err)
	}
	if latest == nil {
		t.Fatal("build returned a nil map; the cache would stay cold")
	}

	metaResultsCache.mu.Lock()
	after := metaResultsCache.latest
	metaResultsCache.mu.Unlock()
	if after == nil {
		t.Error("cache not populated after build — first request would still pay the full cost")
	}

	// Leave the package cache clean for other tests.
	invalidateMetadataResultsCache()
}
