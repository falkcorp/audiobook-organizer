// file: internal/operations/registry/reliability_fixes_test.go
// version: 1.0.0
// guid: 8f1c2a3b-4d5e-6f70-8192-a3b4c5d6e7f8
// last-edited: 2026-07-17

package registry_test

// Regression tests for the 2026-07-17 registry reliability cluster:
//
//   R-2: watchdog detects an op that hangs BEFORE its first UpdateProgress
//        (marking an op running never stamps last_progress_at, so the old
//        !IsZero() guard made hang-from-birth ops undetectable).
//   C-3: a genuinely abandoned op gets a terminal status so its row leaves
//        the active index, and the ConcurrencyKey enqueue-dedupe no longer
//        returns the zombie's ID for every future enqueue of the def.
//   C-1: Cancel of an op sitting in the buffered nextRun channel (stub
//        handle, nil cancel func) is no longer a silent no-op — the op is
//        marked canceled and never runs.

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// TestWatchdog_StuckBeforeFirstProgress verifies that an op that hangs before
// ever calling UpdateProgress is still detected as stuck: the watchdog falls
// back to the row's StartedAt when both the in-memory atomic clock and the DB
// last_progress_at are unset (R-2).
func TestWatchdog_StuckBeforeFirstProgress(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	store := newFakeStore()
	r := registry.NewWithOptions(store, slog.Default(), 2, registry.Options{
		WatchdogInterval: 50 * time.Millisecond,
	})

	canceled := make(chan struct{})
	def := makeValidDef("test.wdog-stuck-from-birth")
	def.ResumePolicy = registry.ResumeDrop
	def.ProgressTimeout = 100 * time.Millisecond
	def.Run = func(runCtx context.Context, _ json.RawMessage, _ registry.Reporter) error {
		// Hang immediately — never call UpdateProgress. Before the fix this op
		// was invisible to the watchdog forever ("silent for hours" class).
		<-runCtx.Done()
		close(canceled)
		return runCtx.Err()
	}
	_ = r.RegisterOp(def)
	r.Start(ctx)

	opID, _ := r.EnqueueOp(ctx, "test.wdog-stuck-from-birth", nil)
	awaitStatus(t, store, opID, "running", 3*time.Second)

	// No backdating: StartedAt is stamped by the worker; after ProgressTimeout
	// (100ms) elapses the watchdog (50ms cycle) must cancel the run.
	select {
	case <-canceled:
	case <-time.After(3 * time.Second):
		t.Fatal("hang-from-birth op was not canceled within 3s")
	}
	awaitStatus(t, store, opID, "canceled", 3*time.Second)

	if n := len(store.strikesOfKind(opID, "stuck")); n == 0 {
		t.Error("expected at least 1 stuck strike, got 0")
	}
}

// TestAbandoned_TerminalStatusAllowsReenqueue verifies that a genuinely
// abandoned op (ctx-canceled, goroutine doesn't return within grace) gets a
// terminal status — so its row leaves the active index — and that a
// subsequent enqueue of the same ConcurrencyKey'd def creates a NEW op that
// runs to completion instead of returning the zombie's ID forever (C-3).
func TestAbandoned_TerminalStatusAllowsReenqueue(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	store := newFakeStore()
	r := registry.NewWithOptions(store, slog.Default(), 2, registry.Options{
		WatchdogInterval: 30 * time.Second, // keep the watchdog out of the way
		AbandonedCap:     10,
		AbandonGrace:     100 * time.Millisecond,
	})

	release := make(chan struct{})
	started := make(chan struct{})
	var calls atomic.Int32

	def := makeValidDef("test.abandoned-reenqueue")
	def.Plugin = "reenqueue-plugin"
	def.ConcurrencyKey = "reenqueue.key"
	def.ResumePolicy = registry.ResumeDrop
	def.Run = func(runCtx context.Context, _ json.RawMessage, _ registry.Reporter) error {
		if calls.Add(1) == 1 {
			close(started)
			<-release // ignore ctx — simulate a goroutine that won't die
			return nil
		}
		return nil
	}
	_ = r.RegisterOp(def)
	r.Start(ctx)
	defer close(release) // let the runaway goroutine drain at test end

	op1, _ := r.EnqueueOp(ctx, "test.abandoned-reenqueue", nil)
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("op1 did not start within 5s")
	}

	// Cancel op1; after AbandonGrace it is classified abandoned and must get a
	// terminal status (interrupted_dropped for ResumeDrop) so it leaves the
	// active index.
	_ = r.Cancel(op1)
	awaitStatus(t, store, op1, "interrupted_dropped", 5*time.Second)

	// Re-enqueue: the ConcurrencyKey dedupe must NOT return the zombie's ID.
	op2, err := r.EnqueueOp(ctx, "test.abandoned-reenqueue", nil)
	if err != nil {
		t.Fatalf("re-enqueue after abandonment failed: %v", err)
	}
	if op2 == op1 {
		t.Fatalf("re-enqueue returned the abandoned zombie's ID %s — op type silently disabled", op1)
	}
	// And the new op must actually run to completion.
	awaitStatus(t, store, op2, "completed", 5*time.Second)
}

// TestCancel_OpQueuedInWorkerChannelNeverRuns verifies that canceling an op
// that has been claimed by the dispatcher but is still sitting in the buffered
// nextRun channel (workers saturated) marks it canceled and prevents its Run
// from ever being invoked (C-1 — previously a silent no-op: the stub handle
// has a nil cancel func and the queued-path DB cancel was never attempted).
func TestCancel_OpQueuedInWorkerChannelNeverRuns(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	store := newFakeStore()
	// Single worker so a long-running op saturates the pool.
	r := registry.NewWithOptions(store, slog.Default(), 1, registry.Options{
		WatchdogInterval: 30 * time.Second,
	})

	blockerStarted := make(chan struct{})
	releaseBlocker := make(chan struct{})
	blocker := makeValidDef("test.cancel-chan-blocker")
	blocker.Run = func(runCtx context.Context, _ json.RawMessage, _ registry.Reporter) error {
		close(blockerStarted)
		select {
		case <-releaseBlocker:
		case <-runCtx.Done():
		}
		return nil
	}
	_ = r.RegisterOp(blocker)

	var targetRan atomic.Bool
	target := makeValidDef("test.cancel-chan-target")
	target.Run = func(_ context.Context, _ json.RawMessage, _ registry.Reporter) error {
		targetRan.Store(true)
		return nil
	}
	_ = r.RegisterOp(target)
	r.Start(ctx)

	// Saturate the sole worker.
	blockerID, _ := r.EnqueueOp(ctx, "test.cancel-chan-blocker", nil)
	select {
	case <-blockerStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("blocker op did not start within 5s")
	}

	// Enqueue the target; the dispatcher (100ms ticker) claims it and parks it
	// in the nextRun channel behind the busy worker. Give it a few cycles.
	targetID, _ := r.EnqueueOp(ctx, "test.cancel-chan-target", nil)
	time.Sleep(400 * time.Millisecond)

	// Cancel while it sits in the channel.
	if err := r.Cancel(targetID); err != nil {
		t.Fatalf("Cancel returned error: %v", err)
	}
	awaitStatus(t, store, targetID, "canceled", 3*time.Second)

	// Free the worker; it will pick the target from the channel and must drop
	// it without invoking Run.
	close(releaseBlocker)
	awaitStatus(t, store, blockerID, "completed", 5*time.Second)

	// Give the worker time to (incorrectly) start the target if the fix were
	// absent, then assert it never ran and stayed canceled.
	time.Sleep(500 * time.Millisecond)
	if targetRan.Load() {
		t.Error("canceled-in-channel op's Run was invoked; cancel must prevent the run")
	}
	if got := store.statusOf(targetID); got != "canceled" {
		t.Errorf("expected target status to remain canceled, got %q", got)
	}
}
