// file: internal/server/metadata_results_warmer.go
// version: 1.0.0
// guid: 2f6c81b4-7d09-4e35-a1c8-540be9723fda
// last-edited: 2026-08-06

package server

import (
	"log/slog"
	"time"
)

// warmMetadataResultsCache pre-builds the metadata-results set at startup so the
// first person to open the match UI does not pay for it.
//
// 🔴 WHY. The build is memoised (metadata_results_cache.go, 60s TTL) but nothing
// populates it at boot, so the cache is cold after every restart and the first
// request eats the full build — measured at ~34 seconds on this library. Since
// choosing metadata matches is the primary way books get matched at all, that
// delay lands squarely on the workflow it most needs to be out of the way of.
//
// Memoising the BUILD rather than the page was the earlier half of this fix
// (PR #2142); warming it is the other half. A cache that is only ever populated
// by the request that pays for it moves the cost, it does not remove it.
//
// Best-effort by design: a failure here logs and leaves the cache cold, which is
// exactly the behaviour before this warmer existed. It never blocks startup and
// never propagates an error, matching warmFacetsCache / warmAuthorsCache.
//
// The 60s TTL is deliberately NOT extended to cover the gap between boot and
// first use. A longer TTL would make every stale read staler; the right fix for
// "cold at boot" is to warm at boot.
func (s *Server) warmMetadataResultsCache() {
	store := s.Store()
	if store == nil {
		slog.Info("metadata-results warmer skipped — store not initialised")
		return
	}

	started := time.Now()
	slog.Info("metadata-results pre-warming cache")

	latest, _, err := latestMetadataResultsByBookCached(store)
	if err != nil {
		// Degrade to a cold cache rather than failing startup — the next request
		// simply rebuilds, which is the pre-warmer behaviour.
		slog.Info("metadata-results warm-up failed — continuing with a cold cache", "err", err)
		return
	}

	slog.Info("metadata-results cache warm",
		"books", len(latest), "duration_ms", time.Since(started).Milliseconds())
}
