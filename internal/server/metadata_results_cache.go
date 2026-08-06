// file: internal/server/metadata_results_cache.go
// version: 2.0.0
// guid: 65b9ee5d-b3df-43ed-95a3-b393bb0532a7
// last-edited: 2026-08-06

package server

import (
	"log/slog"
	"sync"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// metadataResultsCacheTTL is how long a built set is served before a refresh is
// TRIGGERED. It is not an expiry cliff: past it the cached set is still served
// immediately while a rebuild runs in the background.
const metadataResultsCacheTTL = 60 * time.Second

// metadataResultsCache memoises latestMetadataResultsByBook.
//
// 🔴 WHY THIS EXISTS. GET /api/v1/library/metadata-results took **21.9 seconds**
// on this library — for three rows out of 36,805 — because the underlying build
// re-ran on every request regardless of the page asked for:
//
//	GetRecentOperations(5000), then a SEPARATE GetOperationResults(op.ID) per
//	metadata_candidate_fetch operation, folded into a latest-per-book map.
//
// The page slice was applied afterwards, so `?limit=3` paid exactly what
// `?limit=5000` did.
//
// 🔴 WHY IT IS STALE-WHILE-REVALIDATE (measured on production 2026-08-06).
// v1 expired the entry after the TTL and made the next caller rebuild
// SYNCHRONOUSLY. With a build costing ~30s and a TTL of 60s that is close to
// useless — measured 39.4s on the first request after a restart, and **28.9s on
// a request 70s later with no restart involved**. Anyone not clicking at least
// once a minute paid the full build every time. Pre-warming at boot (#2152)
// fixed exactly one occurrence; the cliff returned sixty seconds later.
//
// Serving stale is safe because the TTL was never the correctness mechanism:
// invalidateMetadataResultsCache() is called explicitly from the apply / reject /
// unreject paths, which are the only events that make an entry misleading.
// Freshness comes from that invalidation; the TTL only decides when to refresh in
// the background. A cache whose staleness bound is enforced by making a user wait
// thirty seconds is not protecting them from anything.
type metadataResultsCache struct {
	mu     sync.Mutex
	latest map[string]database.OperationResult
	counts map[string]int
	at     time.Time
	// rebuilding guards against a stampede: once a background rebuild is in
	// flight, later callers keep serving the stale set rather than each starting
	// their own ~30s build.
	rebuilding bool

	// gen is bumped by every invalidation. A build captures it before starting
	// and refuses to install its result if it changed while the build was running.
	//
	// 🔴 WITHOUT THIS, INVALIDATION IS DEFEATED BY ITS OWN SLOWNESS. The build
	// takes ~30s and runs outside the lock. If a user applies or rejects a
	// candidate during that window, invalidateMetadataResultsCache() clears the
	// entry — and then the in-flight build finishes and writes back the snapshot
	// it read BEFORE the invalidation. The cache ends up holding exactly the
	// stale set the invalidation existed to purge, so the list keeps offering a
	// candidate the user just acted on: the one kind of staleness this cache is
	// explicitly not allowed to have.
	gen uint64
}

var metaResultsCache metadataResultsCache

// latestMetadataResultsByBookCached returns the memoised result set.
//
//   - FRESH — return it.
//   - STALE but present — return it IMMEDIATELY and kick off ONE background
//     rebuild. No caller ever blocks on a refresh.
//   - ABSENT (truly cold: first call after boot before the warmer finishes, or
//     right after an explicit invalidation) — build synchronously, because there
//     is nothing to serve.
//
// The synchronous build stays outside the lock. Holding it across a multi-second
// build would serialise every concurrent request behind one rebuild, which is
// exactly the access pattern a UI paging through results produces.
func latestMetadataResultsByBookCached(store database.Store) (map[string]database.OperationResult, map[string]int, error) {
	now := time.Now()

	metaResultsCache.mu.Lock()
	haveEntry := metaResultsCache.latest != nil
	fresh := haveEntry && now.Sub(metaResultsCache.at) < metadataResultsCacheTTL
	l, c := metaResultsCache.latest, metaResultsCache.counts
	startRefresh := haveEntry && !fresh && !metaResultsCache.rebuilding
	if startRefresh {
		metaResultsCache.rebuilding = true
	}
	metaResultsCache.mu.Unlock()

	if haveEntry {
		if startRefresh {
			go refreshMetadataResultsCache(store)
		}
		// Fresh or stale, the caller gets an answer now rather than in 30 seconds.
		return l, c, nil
	}

	// Truly cold: nothing to serve, so this caller has to build it.
	return buildAndStoreMetadataResults(store, "cold")
}

// refreshMetadataResultsCache rebuilds in the background and clears the in-flight
// flag. Errors are logged and swallowed: the previous entry keeps being served,
// which is strictly better than failing a request that already had a usable
// answer.
func refreshMetadataResultsCache(store database.Store) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("metadata-results background refresh panicked", "panic", r)
		}
		metaResultsCache.mu.Lock()
		metaResultsCache.rebuilding = false
		metaResultsCache.mu.Unlock()
	}()
	if _, _, err := buildAndStoreMetadataResults(store, "background"); err != nil {
		slog.Info("metadata-results background refresh failed — still serving the previous set", "err", err)
	}
}

// buildAndStoreMetadataResults runs the expensive build and installs the result.
// reason is logged so a cold build (a user waited) is distinguishable from a
// background refresh (nobody waited) when reading production logs.
func buildAndStoreMetadataResults(store database.Store, reason string) (map[string]database.OperationResult, map[string]int, error) {
	// Capture the generation BEFORE reading. Anything that invalidates while this
	// build runs bumps it, and we must then discard our now-stale result.
	metaResultsCache.mu.Lock()
	startGen := metaResultsCache.gen
	metaResultsCache.mu.Unlock()

	started := time.Now()
	latest, counts, err := latestMetadataResultsByBook(store)
	if err != nil {
		return nil, nil, err
	}
	slog.Info("metadata-results cache rebuilt",
		"reason", reason, "books", len(latest), "duration_ms", time.Since(started).Milliseconds())

	metaResultsCache.mu.Lock()
	superseded := metaResultsCache.gen != startGen
	if !superseded {
		metaResultsCache.latest, metaResultsCache.counts, metaResultsCache.at = latest, counts, time.Now()
	}
	metaResultsCache.mu.Unlock()

	if superseded {
		// An apply/reject landed mid-build. Installing now would resurrect the
		// pre-invalidation snapshot, so drop it and leave the cache cold — the
		// next read rebuilds from current state.
		slog.Info("metadata-results build superseded by an invalidation — discarding result",
			"reason", reason)
	}
	// Return the freshly built set to THIS caller regardless: it is the newest
	// data this goroutine has, and a cold-path caller must not be handed nil.
	return latest, counts, nil
}

// invalidateMetadataResultsCache drops the memoised set so the next read sees
// fresh state.
//
// This — not the TTL — is what keeps the list correct. It is called from the
// apply / reject / unreject paths: those change a book's status, and a stale list
// would keep offering a candidate the user just acted on, which is the one kind
// of staleness that is actively confusing rather than merely old.
//
// It deliberately forces the NEXT caller onto the synchronous cold path: after an
// explicit invalidation there is no trustworthy set left to serve.
func invalidateMetadataResultsCache() {
	metaResultsCache.mu.Lock()
	metaResultsCache.latest, metaResultsCache.counts = nil, nil
	// Bump the generation so any build already in flight discards its result
	// instead of writing back the snapshot it read before this call.
	metaResultsCache.gen++
	metaResultsCache.mu.Unlock()
}
