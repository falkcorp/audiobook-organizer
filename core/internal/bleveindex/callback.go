// file: core/internal/bleveindex/callback.go
// version: 1.0.0
// guid: 3e1d7c10-8a3b-4a8e-9a53-6d0c2f5b7a11
// last-edited: 2026-11-11

package bleveindex

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/blevesearch/bleve/v2/index/scorch"
)

// scorchRegistryMu guards scorch.RegistryAsyncErrorCallbacks, a plain map in
// bleve that every scorch index reads when it opens. It is the one
// package-level variable of this package: the map belongs to bleve and is
// process-global, so two indexes opening or closing at once (two engines in
// one process, as the tests have) would otherwise race on it.
var scorchRegistryMu sync.Mutex //archtest:allow-var guards scorch's process-global async-error callback registry; the single globals entry of core/internal/bleveindex (TestNoGlobals)

// asyncErrorLogEvery rate-limits the Error line: a failing merge plan retries
// in a tight loop and would otherwise log hundreds of identical lines.
const asyncErrorLogEvery = time.Minute

// asyncCallback is one index's scorch async-error callback state.
//
// WHY the callback exists: scorch's merger and persister run in their own
// goroutines and report failures ONLY through config["asyncErrorCallbackName"].
// With none registered every failure is counted in a stat and otherwise
// discarded. HEAD's production index ran 92 consecutive failed merge plans in
// 13 minutes with zero log lines; the persister, which waits for the merger
// once the directory holds >= 1000 files, paused forever, and every safe write
// blocked on it. The whole write path wedged and nothing said why
// (internal/search/bleve_index.go, 2026-09-25).
type asyncCallback struct {
	once    sync.Once
	name    string
	errors  atomic.Int64
	lastLog atomic.Int64 // UnixNano of the last Error line
	log     func(err error, path string, total int64)
}

// register adds the callback to scorch's registry under a name no other index
// of the process holds, exactly once per index (a second call is a no-op), and
// returns the runtime config every open and create of the index must pass:
// bleve.OpenUsing with no config would drop the callback on the second boot.
func (c *asyncCallback) register(module, index string) map[string]any {
	c.once.Do(func() {
		// %p of the callback state is unique among live indexes, and the name
		// is deleted in unregister, so a reused address is a free name.
		c.name = fmt.Sprintf("mediacore-index/%s/%s/%p", module, index, c)
		scorchRegistryMu.Lock()
		defer scorchRegistryMu.Unlock()
		scorch.RegistryAsyncErrorCallbacks[c.name] = c.onError
	})
	return map[string]any{"asyncErrorCallbackName": c.name}
}

// unregister removes the registry entry; the index is closing.
func (c *asyncCallback) unregister() {
	if c.name == "" {
		return
	}
	scorchRegistryMu.Lock()
	defer scorchRegistryMu.Unlock()
	delete(scorch.RegistryAsyncErrorCallbacks, c.name)
}

func (c *asyncCallback) onError(err error, path string) {
	n := c.errors.Add(1)
	now := time.Now().UnixNano()
	last := c.lastLog.Load()
	if n > 1 && now-last < int64(asyncErrorLogEvery) {
		return
	}
	if !c.lastLog.CompareAndSwap(last, now) {
		return
	}
	if c.log != nil {
		c.log(err, path, n)
	}
}
