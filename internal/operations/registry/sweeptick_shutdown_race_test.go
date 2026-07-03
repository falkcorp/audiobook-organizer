// file: internal/operations/registry/sweeptick_shutdown_race_test.go
// version: 1.0.0
// guid: 3c9f1a72-8d54-4be1-9f20-6a7c1d4e5b83
// last-edited: 2026-07-03

// sweeptick_shutdown_race_test.go is the regression test for
// PEBBLE-CLOSED-SWEEPTICK-RESIDUAL — the residual ticker leg of the shutdown
// race that shutdown_race_test.go (the dep-notify leg) did not cover.
//
// The DepsScheduler sweep ticker goroutine (registry.go, wired in Start) calls
// DepsScheduler.SweepTick → store.ListWaitingDepsOps, which opens a Pebble
// iterator. That goroutine is enrolled in goroutineWG, but Registry.Shutdown()
// waits on goroutineWG with a hardcoded 2s escape. A SweepTick that is still
// running (mid store-iteration) when that 2s elapses is abandoned: Shutdown
// returns, the caller closes the store, and the in-flight iterator then panics
// with "pebble: closed".
//
// This test forces exactly that: a store wrapper whose ListWaitingDepsOps
// blocks (once armed) for longer than Shutdown's 2s escape before touching the
// real store. Pre-fix the store is closed underneath the sleeping sweep → the
// subsequent NewIter panics. Post-fix Shutdown joins the sweep goroutine
// explicitly (bounded by the caller ctx, not the 2s escape), so the sweep
// finishes against the still-open store.
package registry_test

import (
	"context"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// slowSweepStore wraps pebbleSchedulerStore and, once armed, makes
// ListWaitingDepsOps (the call the sweep ticker makes) block for `delay` before
// delegating to the real store. Arming is deferred until AFTER reg.Start()
// returns so the wrapper does NOT trip on the ListWaitingDepsOps that
// DepsScheduler.rebuildIndex() performs synchronously during Start — only a
// real ticker-driven sweep should block. `delay` MUST exceed Shutdown's 2s
// goroutineWG escape so that, pre-fix, the store is closed while a sweep is
// still parked inside this method.
type slowSweepStore struct {
	*pebbleSchedulerStore
	armed     atomic.Bool
	entered   chan struct{} // closed once, when the first armed sweep enters
	enterOnce sync.Once
	delay     time.Duration
}

func (s *slowSweepStore) ListWaitingDepsOps() ([]database.OperationV2Row, error) {
	if s.armed.Load() {
		s.enterOnce.Do(func() { close(s.entered) })
		time.Sleep(s.delay)
	}
	return s.pebbleSchedulerStore.ListWaitingDepsOps()
}

func TestShutdownDrainsSweepTicker_RealStore(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	// Repeat to widen the window; the fix makes every iteration deterministic.
	for iter := range 5 {
		store, cleanup := openTestPebbleStore(t)

		// delay (2500ms) is deliberately > Shutdown's 2s goroutineWG escape so
		// that, without the fix, the store is closed while a sweep is still
		// blocked here and the subsequent real ListWaitingDepsOps hits a closed
		// DB. Keep this coupling if either constant changes.
		slow := &slowSweepStore{
			pebbleSchedulerStore: &pebbleSchedulerStore{PebbleStore: store},
			entered:              make(chan struct{}),
			delay:                2500 * time.Millisecond,
		}

		reg := registry.NewWithOptions(store, logger, 2, registry.Options{
			// Fire ticks almost immediately so a sweep is in flight well before
			// we call Shutdown.
			SweepInterval: 10 * time.Millisecond,
		})
		sched := registry.NewDepsScheduler(reg, slow)
		reg.SetDepsScheduler(sched)

		def := makeValidDef("sweep.op")
		def.ID = "sweep.op"
		if err := reg.RegisterOp(def); err != nil {
			t.Fatalf("iter %d: RegisterOp: %v", iter, err)
		}

		reg.Start(context.Background())
		// Arm only now — rebuildIndex() already ran its (fast) ListWaitingDepsOps
		// during Start. From here, the next ticker sweep blocks in the wrapper.
		slow.armed.Store(true)

		// Wait until an armed sweep has entered ListWaitingDepsOps (and is now
		// parked in the >2s sleep) before shutting down, so the sweep is
		// guaranteed in-flight at Close time.
		select {
		case <-slow.entered:
		case <-time.After(5 * time.Second):
			t.Fatalf("iter %d: sweep never entered ListWaitingDepsOps", iter)
		}

		// Shutdown must NOT return until the in-flight sweep has finished
		// touching the store. The ctx is generous (> delay) so the explicit
		// join can complete; a correct Shutdown never falls back to the 2s
		// escape for the sweep goroutine.
		shutCtx, shutCancel := context.WithTimeout(context.Background(), 15*time.Second)
		if err := reg.Shutdown(shutCtx); err != nil {
			t.Fatalf("iter %d: Shutdown: %v", iter, err)
		}
		shutCancel()

		// Pre-fix, an abandoned sweep's iterator panics here with
		// "pebble: closed" and crashes the test binary.
		cleanup()
	}
}
