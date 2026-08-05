// file: internal/server/metadata_results_cache.go
// version: 1.0.0
// guid: 65b9ee5d-b3df-43ed-95a3-b393bb0532a7
// last-edited: 2026-08-05

package server

import (
	"log/slog"
	"sync"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// metadataResultsCacheTTL bounds how long the joined metadata-result set is
// reused. Short enough that a fetch running in the background shows up promptly,
// long enough that paging through the list costs one build rather than one per
// page.
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
// `?limit=5000` did. That made choosing metadata matches impractical: every
// interaction cost twenty seconds.
//
// Caching the BUILD rather than the page mirrors what the ABS contributor cache
// does for the same reason — the expensive part is assembling the set, and every
// page is a free slice of it.
type metadataResultsCache struct {
	mu     sync.Mutex
	latest map[string]database.OperationResult
	counts map[string]int
	at     time.Time
}

var metaResultsCache metadataResultsCache

// latestMetadataResultsByBookCached returns the memoised result set, rebuilding
// when the entry is older than metadataResultsCacheTTL.
//
// The rebuild happens OUTSIDE the lock. Holding it across a multi-second build
// would serialise every concurrent request behind one rebuild — which is exactly
// the access pattern a UI paging through results produces. Two callers racing a
// cold cache may both build; that costs a duplicated build once, and is strictly
// better than making every caller wait on a mutex through it.
func latestMetadataResultsByBookCached(store database.Store) (map[string]database.OperationResult, map[string]int, error) {
	now := time.Now()

	metaResultsCache.mu.Lock()
	if metaResultsCache.latest != nil && now.Sub(metaResultsCache.at) < metadataResultsCacheTTL {
		l, c := metaResultsCache.latest, metaResultsCache.counts
		metaResultsCache.mu.Unlock()
		return l, c, nil
	}
	metaResultsCache.mu.Unlock()

	started := time.Now()
	latest, counts, err := latestMetadataResultsByBook(store)
	if err != nil {
		return nil, nil, err
	}
	slog.Info("metadata-results cache rebuilt",
		"books", len(latest), "duration_ms", time.Since(started).Milliseconds())

	metaResultsCache.mu.Lock()
	metaResultsCache.latest, metaResultsCache.counts, metaResultsCache.at = latest, counts, now
	metaResultsCache.mu.Unlock()
	return latest, counts, nil
}

// invalidateMetadataResultsCache drops the memoised set so the next read sees
// fresh state.
//
// Called from the apply / reject / unreject paths: those change a book's status,
// and a stale list would keep offering a candidate the user just acted on — the
// one kind of staleness that is actively confusing rather than merely old.
func invalidateMetadataResultsCache() {
	metaResultsCache.mu.Lock()
	metaResultsCache.latest, metaResultsCache.counts = nil, nil
	metaResultsCache.mu.Unlock()
}
