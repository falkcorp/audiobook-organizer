// file: internal/operations/registry/abandoned_test.go
// version: 1.0.1
// guid: 5e6f7a8b-9c0d-1234-ef01-234567890abc
// last-edited: 2026-09-02

package registry_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// TestAbandoned_CountIncrementOnCtxCancel verifies that when a ctx-canceled op
// does not return within abandonGrace, the abandoned count for the plugin
// increments, and decrements when the goroutine eventually returns.
func TestAbandoned_CountIncrementOnCtxCancel(t *testing.T) {
	ctx := t.Context()

	store := newFakeStore()
	// AbandonedCap=10 so we don't block dispatch during the test.
	r := registry.NewWithOptions(store, slog.Default(), 2, registry.Options{
		WatchdogInterval: 30 * time.Second, // don't let watchdog interfere
		AbandonedCap:     10,
	})

	release := make(chan struct{})
	started := make(chan struct{})

	def := makeValidDef("test.abandoned-count")
	def.Plugin = "abandoned-plugin"
	def.Run = func(runCtx context.Context, _ json.RawMessage, _ registry.Reporter) error {
		close(started)
		// Ignore ctx — simulate a goroutine that doesn't respect cancellation.
		<-release
		return nil
	}
	_ = r.RegisterOp(def)
	r.Start(ctx)

	opID, _ := r.EnqueueOp(ctx, "test.abandoned-count", nil)

	// Wait for run to start.
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("op did not start within 5s")
	}

	// Cancel the op via the registry — triggers the stuck-op path in executeRun.
	_ = r.Cancel(opID)

	// Wait for the abandoned counter to become > 0 (happens after abandonGrace).
	// abandonGrace is 5s in production; for tests we accept up to 10s total.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if r.AbandonedCount("abandoned-plugin") > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if r.AbandonedCount("abandoned-plugin") == 0 {
		t.Error("expected abandoned count > 0 after ctx cancel + grace period")
	}

	// Release the blocked goroutine — count should drop back to 0.
	close(release)

	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if r.AbandonedCount("abandoned-plugin") == 0 {
			return // success
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Error("expected abandoned count to return to 0 after goroutine unblocked")
}

// TestAbandoned_BlocksDispatchAtCap verifies that when abandonedCount >= cap,
// the dispatcher refuses new dispatches for that plugin.
func TestAbandoned_BlocksDispatchAtCap(t *testing.T) {
	ctx := t.Context()

	store := newFakeStore()
	// Cap of 1 so a single abandoned op blocks the plugin immediately.
	r := registry.NewWithOptions(store, slog.Default(), 4, registry.Options{
		WatchdogInterval: 30 * time.Second,
		AbandonedCap:     1,
	})

	release := make(chan struct{})
	started := make(chan struct{})
	var startedOnce bool

	def := makeValidDef("test.abandoned-block")
	def.Plugin = "block-plugin"
	def.Run = func(runCtx context.Context, _ json.RawMessage, _ registry.Reporter) error {
		if !startedOnce {
			startedOnce = true
			close(started)
			<-release // block until released
		}
		return nil
	}
	_ = r.RegisterOp(def)
	r.Start(ctx)

	op1, _ := r.EnqueueOp(ctx, "test.abandoned-block", nil)
	<-started
	// Cancel op1 — after abandonGrace it becomes abandoned, count=1=cap.
	_ = r.Cancel(op1)

	// Wait for abandoned count to hit cap.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if r.AbandonedCount("block-plugin") >= 1 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if r.AbandonedCount("block-plugin") == 0 {
		t.Fatal("abandoned count did not reach cap within 10s")
	}

	// Enqueue a second op — it should stay queued because plugin is blocked.
	op2, _ := r.EnqueueOp(ctx, "test.abandoned-block", nil)
	time.Sleep(300 * time.Millisecond) // give dispatcher a few ticks
	if store.statusOf(op2) != "queued" {
		t.Errorf("expected op2 to remain queued while plugin is blocked; got %s", store.statusOf(op2))
	}

	// Release op1 — abandoned count drops, op2 can run.
	close(release)
	awaitStatus(t, store, op2, "completed", 5*time.Second)
}

// TestAbandoned_CountDecrementsWhenGoroutineReturns verifies the decrement path
// works correctly in a simpler scenario without full registry overhead.
func TestAbandoned_CountDecrementsWhenGoroutineReturns(t *testing.T) {
	ctx := t.Context()

	store := newFakeStore()
	r := registry.NewWithOptions(store, slog.Default(), 2, registry.Options{
		AbandonedCap: 10,
	})

	release := make(chan struct{})
	started := make(chan struct{})

	def := makeValidDef("test.abandoned-decr")
	def.Plugin = "decr-plugin"
	def.Run = func(runCtx context.Context, _ json.RawMessage, _ registry.Reporter) error {
		close(started)
		<-release
		return nil
	}
	_ = r.RegisterOp(def)
	r.Start(ctx)

	opID, _ := r.EnqueueOp(ctx, "test.abandoned-decr", nil)
	<-started
	_ = r.Cancel(opID)

	// Wait for abandon classification.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if r.AbandonedCount("decr-plugin") > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Release and verify decrement.
	close(release)
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if r.AbandonedCount("decr-plugin") == 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("abandoned count did not decrement to 0; got %d", r.AbandonedCount("decr-plugin"))
}
