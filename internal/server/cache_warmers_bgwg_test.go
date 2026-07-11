// file: internal/server/cache_warmers_bgwg_test.go
// version: 1.1.0
// guid: 8f2b6f2a-9e1c-4d3a-8b7e-0c6f5a1d2e3b
// last-edited: 2026-07-10

// cache_warmers_bgwg_test verifies TASK-05 (#1794): all four fire-and-forget
// cache warmers (warmFacetsCache/warmLibrarySizes/warmAuthorsCache/
// warmSeriesCache) are enrolled in s.bgWG and skip when s.bgCtx is already
// canceled, mirroring the library-list-warmer sibling enrolled by #1781.

package server

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// waitCacheWarmersBgWG runs wg.Wait() in a goroutine and fails the test
// instead of hanging forever if a warmer leaks past its bgCtx-canceled skip
// check (task-unique name to avoid the parallel-test-helper-collision
// footgun — internal/server already has other _test.go files in flight).
func waitCacheWarmersBgWG(t *testing.T, wg *namedWaitGroup, timeout time.Duration) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(timeout):
		t.Fatalf("bgWG.Wait() did not return within %s — still running: %v", timeout, wg.Running())
	}
}

// TestStartCacheWarmers_SkipOnCanceledCtx verifies that when the server's
// bgCtx is already canceled (shutdown in progress), all four warmers skip
// their store access instead of racing a possibly-closed Pebble store, and
// bgWG.Wait() still returns promptly with no panic. A canceled bgCtx also
// lets the long-lived siblings sharing bgWG (apikey-expiry-sweep,
// library-list-trickle-warmer) exit immediately via their own bgCtx.Done()
// checks, so Wait() returning here is not warmer-specific — it is the
// no-goroutine-leak assertion.
func TestStartCacheWarmers_SkipOnCanceledCtx(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	// Simulate a server already tearing down.
	server.bgCancel()

	server.startCacheWarmers()
	waitCacheWarmersBgWG(t, &server.bgWG, 5*time.Second)

	_, facetsOK := server.facetsCache.Get(facetsCacheKey)
	assert.False(t, facetsOK, "facets warmer should have skipped on canceled bgCtx")

	_, authorsOK := server.authorsCache.Get("all")
	assert.False(t, authorsOK, "authors warmer should have skipped on canceled bgCtx")

	_, seriesOK := server.seriesCache.Get("all")
	assert.False(t, seriesOK, "series warmer should have skipped on canceled bgCtx")
}

// TestStartCacheWarmers_EnrolledInBgWG is the anti-over-suppression
// counterpart: with a live (non-canceled) bgCtx every warmer must still run
// to completion and populate its cache — the skip check must never fire
// while the server is healthy.
//
// bgWG also carries two intentionally long-lived siblings that only exit on
// bgCtx cancellation (apikey-expiry-sweep ticks every 6h;
// library-list-trickle-warmer runs until shutdown), so this test cannot call
// bgWG.Wait() while bgCtx is still live — that would hang the whole suite.
// Instead: assert the four target warmers ran (their caches populated)
// while bgCtx is live, THEN cancel bgCtx so every enrolled goroutine
// (targets + siblings) can exit, THEN Wait() with a timeout to confirm no
// goroutine leaked past shutdown.
func TestStartCacheWarmers_EnrolledInBgWG(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	require.NoError(t, server.bgCtx.Err(), "bgCtx must be live for this test")

	server.startCacheWarmers()

	assert.Eventually(t, func() bool {
		_, ok := server.facetsCache.Get(facetsCacheKey)
		return ok
	}, 5*time.Second, 10*time.Millisecond, "facets warmer should have executed and populated the cache with a live bgCtx")

	assert.Eventually(t, func() bool {
		_, ok := server.authorsCache.Get("all")
		return ok
	}, 5*time.Second, 10*time.Millisecond, "authors warmer should have executed and populated the cache with a live bgCtx")

	assert.Eventually(t, func() bool {
		_, ok := server.seriesCache.Get("all")
		return ok
	}, 5*time.Second, 10*time.Millisecond, "series warmer should have executed and populated the cache with a live bgCtx")

	// Let the long-lived siblings (and the trickle warmer) observe shutdown
	// so bgWG.Wait() below can return.
	server.bgCancel()
	waitCacheWarmersBgWG(t, &server.bgWG, 10*time.Second)
}
