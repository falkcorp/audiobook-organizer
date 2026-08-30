// file: internal/dedup/lifecycle_test.go
// version: 1.0.1
// guid: cf16e52e-6506-4cf5-bd34-b279263e0d58
// last-edited: 2026-08-30

// Tests for Engine lifecycle: PostInit/Stop shutdown join, double-Stop
// safety, and bounded-timeout behaviour. All tests run with -race.

package dedup

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestStop_JoinsHydrationGoroutine verifies that Stop() blocks until
// the hydration goroutine fully exits even when the goroutine is
// mid-flight when Stop is called.
//
// Strategy: manually wire bgWg.Add + a slow goroutine (mirrors what
// PostInit does when it launches HydrateChromem), then call Stop() and
// assert it returns only after the goroutine has exited.
func TestStop_JoinsHydrationGoroutine(t *testing.T) {
	t.Parallel()

	engine := &Engine{}
	// Initialise the engine's bg-context (normally done in PostInit).
	engine.bgMu.Lock()
	engine.bgCtx, engine.bgCancel = context.WithCancel(context.Background())
	engine.bgMu.Unlock()

	var goroutineExited atomic.Bool
	started := make(chan struct{})

	// Simulate what PostInit does: Add(1) under bgMu, then launch the goroutine.
	engine.bgMu.Lock()
	engine.bgWg.Add(1)
	engine.bgMu.Unlock()

	go func() {
		defer engine.bgWg.Done()
		close(started)
		// Block until the context is canceled (simulates a slow Pebble read).
		engine.bgMu.RLock()
		ctx := engine.bgCtx
		engine.bgMu.RUnlock()
		if ctx != nil {
			<-ctx.Done()
		}
		// Tiny yield so the race detector can observe ordering.
		time.Sleep(5 * time.Millisecond)
		goroutineExited.Store(true)
	}()

	// Wait for the goroutine to be running before we call Stop.
	<-started

	stopDone := make(chan struct{})
	go func() {
		defer close(stopDone)
		_ = engine.Stop(context.Background())
	}()

	select {
	case <-stopDone:
		// Stop returned — now goroutineExited must be true.
		if !goroutineExited.Load() {
			t.Error("Stop() returned before the hydration goroutine exited")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Stop() did not return within 5 s — possible deadlock")
	}
}

// TestStop_DoubleStop verifies that calling Stop() twice does not panic
// or deadlock.
func TestStop_DoubleStop(t *testing.T) {
	t.Parallel()

	engine := &Engine{}
	engine.bgMu.Lock()
	engine.bgCtx, engine.bgCancel = context.WithCancel(context.Background())
	engine.bgMu.Unlock()

	// First Stop should succeed.
	if err := engine.Stop(context.Background()); err != nil {
		t.Fatalf("first Stop: %v", err)
	}
	// Second Stop should be a no-op and not panic or deadlock.
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = engine.Stop(context.Background())
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("second Stop() did not return within 2 s — possible deadlock")
	}
}

// TestStop_NilEngine verifies that Stop on a nil *Engine is a safe no-op.
func TestStop_NilEngine(t *testing.T) {
	t.Parallel()

	var engine *Engine
	if err := engine.Stop(context.Background()); err != nil {
		t.Fatalf("nil engine Stop: %v", err)
	}
}

// TestStop_BoundedTimeout_WarnPath verifies that Stop() does not hang
// indefinitely when a goroutine ignores the canceled context.  We set
// engine.stopTimeout_ to a small value (per-engine field — no package-level
// race), start a goroutine that never exits, and verify Stop returns quickly
// with a WARN rather than hanging.
//
// The goroutine is leaked intentionally — it holds a channel read that
// unblocks in t.Cleanup so the runtime can collect it after the test.
func TestStop_BoundedTimeout_WarnPath(t *testing.T) {
	t.Parallel()

	const shortTimeout = 50 * time.Millisecond

	engine := &Engine{
		stopTimeout_: shortTimeout,
	}
	engine.bgMu.Lock()
	engine.bgCtx, engine.bgCancel = context.WithCancel(context.Background())
	engine.bgMu.Unlock()

	leaked := make(chan struct{}) // closed in Cleanup so goroutine can exit
	t.Cleanup(func() { close(leaked) })

	// A goroutine that ignores bgCtx cancellation and only exits when leaked.
	var wg sync.WaitGroup
	wg.Add(1)
	engine.bgWg.Go(func() {
		wg.Done() // signal that the goroutine is running
		<-leaked
	})
	wg.Wait() // ensure goroutine is started before Stop

	start := time.Now()
	_ = engine.Stop(context.Background())
	elapsed := time.Since(start)

	// Stop must return within a generous multiple of the short timeout.
	// It should not wait the full default 5s or hang.
	if elapsed > 2*time.Second {
		t.Fatalf("Stop() took %v — expected to return in ~%v (bounded timeout)", elapsed, shortTimeout)
	}
}
