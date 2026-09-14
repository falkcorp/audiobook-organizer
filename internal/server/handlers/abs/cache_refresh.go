// file: internal/server/handlers/abs/cache_refresh.go
// version: 1.0.0
// guid: 7c2e4a91-5b3d-4f08-9e6a-2d1b8c7f4e30
// last-edited: 2026-09-13

package abs

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

// absCacheStaleMax bounds how old an expired build may be and still be served
// while a rebuild runs behind it. Past this age a request waits for the rebuild
// and receives its error, so a store that keeps failing surfaces as a failure
// rather than as a library view that silently stops changing.
const absCacheStaleMax = 30 * time.Minute

var cacheLog = logger.New("abs-cache")

// cacheRefresher runs at most one background refresh of one cache at a time.
//
// The singleflight group behind each rebuild already stops concurrent builds;
// this stops concurrent GOROUTINES — without it every request that finds the
// cache expired parks one goroutine on the group for the whole rebuild.
type cacheRefresher struct {
	busy atomic.Bool
	wg   sync.WaitGroup
}

// refresh starts build in the background unless a refresh is already running.
// A failed build is logged: the caller has already been served the previous
// build and never sees the error.
func (r *cacheRefresher) refresh(name string, build func() error) {
	if !r.busy.CompareAndSwap(false, true) {
		return
	}
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		defer r.busy.Store(false)
		started := time.Now()
		if err := build(); err != nil {
			cacheLog.Warn("abs: background refresh of the %s cache failed after %dms; still serving the previous build: %v",
				name, time.Since(started).Milliseconds(), err)
		}
	}()
}

// wait blocks until no refresh is running. Tests use it so a background build
// never outlives the fixture it reads.
func (r *cacheRefresher) wait() { r.wg.Wait() }

// servableStale reports whether a build stamped at builtAt may still be served
// while a rebuild runs.
func (h *Handler) servableStale(builtAt time.Time) bool {
	return h.now().Sub(builtAt) < absCacheStaleMax
}
