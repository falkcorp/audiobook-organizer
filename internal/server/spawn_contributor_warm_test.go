// file: internal/server/spawn_contributor_warm_test.go
// version: 1.0.0
// guid: 4a91c7d3-6b02-4e58-9f14-2d7e830ab6c5
// last-edited: 2026-08-19

package server

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// The contributor-cache warm has one ordering requirement and one fallback, and
// until spawnContributorWarm was extracted neither could be tested at all:
// the logic lived inline in wireABSRoutes, which returns early unless
// ABSAPIEnabled is set and calls os.Exit(1) on a misconfigured ABS block. No
// test could enter the path, so `-race` reporting nothing about the original
// racy version was absence of coverage, not absence of a race.

// TestSpawnContributorWarm_WaitsForWarmupBeforeBuildingTheCache pins the
// ordering. The cache stores the authors of VISIBLE books; built against a
// half-published memdb it caches a view of a library that does not exist yet,
// and then serves it for the whole TTL. A slow-but-correct warm beats a fast
// wrong one, so WaitForWarmup must return BEFORE the warm function runs.
//
// Asserted as a recorded sequence rather than "both were called": a mock that
// only counts calls passes just as happily when the order is reversed, which is
// the single thing this test exists to catch.
func TestSpawnContributorWarm_WaitsForWarmupBeforeBuildingTheCache(t *testing.T) {
	waiter := newMockWarmupWaiter(t)

	var mu sync.Mutex
	var order []string
	done := make(chan struct{})

	waiter.EXPECT().WaitForWarmup().Run(func() {
		mu.Lock()
		order = append(order, "wait")
		mu.Unlock()
	}).Once()

	spawnContributorWarm(waiter, func(context.Context) {
		mu.Lock()
		order = append(order, "warm")
		mu.Unlock()
		close(done)
	})

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the warm function never ran")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(order) != 2 || order[0] != "wait" || order[1] != "warm" {
		t.Fatalf("got %v, want [wait warm] — the contributor cache was built "+
			"before the memdb warmup completed, so it caches the author set of a "+
			"partially-published library and serves it for the whole TTL", order)
	}
}

// TestSpawnContributorWarm_WarmsAnywayWhenTheStoreLacksTheCapability pins the
// fallback. A store that cannot report warmup is a supported state, and the
// cache must still be built — skipping the warm entirely would push the
// full-library scan (measured 6,104ms cold vs 105ms warm) onto whichever real
// request arrives first, normally the client's Authors tab, where it reads as a
// hang.
//
// database.MockStore is a plain struct with no WaitForWarmup, so
// resolveWarmupWaiter genuinely fails to resolve it. That is the point: this
// case must be produced by a store that really lacks the method, not by handing
// the resolver a nil.
func TestSpawnContributorWarm_WarmsAnywayWhenTheStoreLacksTheCapability(t *testing.T) {
	done := make(chan struct{})

	spawnContributorWarm(&database.MockStore{}, func(context.Context) { close(done) })

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the warm function never ran for a store without the warmup " +
			"capability — the contributor cache would never be built and the " +
			"first real request would pay a full-library scan")
	}
}

// TestSpawnContributorWarm_DoesNotReadTheServerStoreFromTheGoroutine is the
// regression guard for the race itself, and it only means anything under
// `-race`.
//
// Server.store is a plain unsynchronised field (server.go:331 returns it with no
// lock). Server.Start overwrites it with the Bleve indexedStore wrapper, while
// this goroutine is spawned earlier, at route-wiring time inside NewServer. So a
// goroutine body that reads s.Store() is an unsynchronised read racing that
// write. Whether the warm waited at all was decided by scheduling: the old bare
// store.(*database.PebbleStore) assertion succeeded against the bare store and
// failed against the wrapper.
//
// Passing the store as a PARAMETER is what removes the race — Go evaluates
// arguments in the caller — and this test fails if that is ever undone, e.g. by
// changing the signature to take *Server and reading the field inside.
//
// The overwrite happens BEFORE the receive on done, deliberately. Receiving
// first would establish a happens-before edge between the goroutine and the
// write, and the detector would then correctly report nothing no matter how the
// store was read.
func TestSpawnContributorWarm_DoesNotReadTheServerStoreFromTheGoroutine(t *testing.T) {
	s := &Server{store: &database.MockStore{}}

	done := make(chan struct{})
	spawnContributorWarm(s.Store(), func(context.Context) { close(done) })

	// Mirrors what Server.Start does to this field.
	s.store = &database.MockStore{}

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the warm function never ran")
	}
}
