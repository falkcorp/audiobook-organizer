// file: internal/operations/registry/live_tracker.go
// version: 1.0.0
// guid: 5f8a3d21-9c47-4e06-b1d2-8e6f0a4c7b39
// last-edited: 2026-07-03

// live_tracker.go tracks every Registry that has been Start()ed and not yet
// Shutdown() so that a store owner can drain leaked registries BEFORE closing
// the store.
//
// WHY: internal/testutil's SetupIntegration cleanup closes the PebbleStore,
// but each test is individually responsible for deferring
// opRegistry.Shutdown. Any test that forgets (or fails before registering the
// defer) leaks a running registry whose background goroutines — the
// DepsScheduler sweep ticker, the dispatcher's 100ms cycle, dbReporter
// progress/log flushes — keep touching the store for the remainder of the
// package run and panic "pebble: closed" minutes after that store closed
// (PEBBLE-CLOSED-SWEEPTICK-RESIDUAL family). The tracker gives the store
// owner an ordered-teardown seam: ShutdownAllForStore(ctx, store) before
// store.Close().
//
// The map is keyed per Registry and matched per store, so concurrent tests
// (each with its own store) only ever drain their own registries. In
// production there is exactly one process-lifetime registry, so the tracker
// is a single map entry and ShutdownAllForStore is never called.

package registry

import (
	"context"
	"sync"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

var (
	liveRegistriesMu sync.Mutex
	liveRegistries   = map[*Registry]struct{}{}
)

// trackLiveRegistry records r as running. Called from Registry.Start.
func trackLiveRegistry(r *Registry) {
	liveRegistriesMu.Lock()
	liveRegistries[r] = struct{}{}
	liveRegistriesMu.Unlock()
}

// untrackLiveRegistry removes r. Called (deferred) from Registry.Shutdown so
// an already-drained registry is never re-drained by ShutdownAllForStore.
func untrackLiveRegistry(r *Registry) {
	liveRegistriesMu.Lock()
	delete(liveRegistries, r)
	liveRegistriesMu.Unlock()
}

// ShutdownAllForStore drains every live (Started, not yet Shutdown) Registry
// whose backing store is exactly `store` (interface identity — the same
// concrete pointer), and returns how many it drained. Store owners MUST call
// this before closing the store when they cannot prove every registry using
// it was shut down — most importantly internal/testutil's SetupIntegration
// cleanup. Registry.Shutdown is idempotent, so racing a test's own deferred
// Shutdown is safe; the tracker simply finds nothing left to drain.
func ShutdownAllForStore(ctx context.Context, store database.OpsV2Store) int {
	liveRegistriesMu.Lock()
	var matched []*Registry
	for r := range liveRegistries {
		if r.store == store {
			matched = append(matched, r)
		}
	}
	liveRegistriesMu.Unlock()

	for _, r := range matched {
		r.logger.Warn("registry: draining leaked registry before store close (Shutdown was never called — fix the owning test)")
		_ = r.Shutdown(ctx)
	}
	return len(matched)
}
