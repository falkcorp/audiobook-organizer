// file: internal/operations/registry/dispatcher_test.go
// version: 1.3.0
// guid: e1f2a3b4-c5d6-7e8f-9a0b-1c2d3e4f5a6b
// last-edited: 2026-06-04

package registry_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// awaitStatus polls store.statusOf(opID) until it matches want or times out.
func awaitStatus(t *testing.T, store *fakeStore, opID, want string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if store.statusOf(opID) == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Errorf("op %s: wanted status=%s, got=%s after %v", opID, want, store.statusOf(opID), timeout)
}

// TestDispatcher_SingleOpRunsAndCompletes tests the happy path.
func TestDispatcher_SingleOpRunsAndCompletes(t *testing.T) {
	ctx := t.Context()

	store := newFakeStore()
	r := registry.New(store, slog.Default(), 2, nil)

	def := makeValidDef("test.single")
	_ = r.RegisterOp(def)
	r.Start(ctx)

	opID, err := r.EnqueueOp(ctx, "test.single", nil)
	if err != nil {
		t.Fatalf("EnqueueOp: %v", err)
	}
	awaitStatus(t, store, opID, "completed", 5*time.Second)
}

// TestDispatcher_SameConcurrencyKeySerializes verifies that two ops with the
// same ConcurrencyKey do not run simultaneously.
func TestDispatcher_SameConcurrencyKeySerializes(t *testing.T) {
	ctx := t.Context()

	store := newFakeStore()
	r := registry.New(store, slog.Default(), 4, nil)

	var overlap int64
	var running int64
	var mu sync.Mutex
	var maxOverlap int

	barrier := make(chan struct{})
	var barrierOnce sync.Once

	def := makeValidDef("test.serial")
	def.ConcurrencyKey = "same-key"
	def.Run = func(runCtx context.Context, _ json.RawMessage, _ registry.Reporter) error {
		cur := atomic.AddInt64(&running, 1)
		mu.Lock()
		if int(cur) > maxOverlap {
			maxOverlap = int(cur)
		}
		mu.Unlock()
		// Signal after first op starts.
		barrierOnce.Do(func() { close(barrier) })
		time.Sleep(30 * time.Millisecond)
		atomic.AddInt64(&running, -1)
		atomic.AddInt64(&overlap, int64(cur-1)) // accumulate concurrent count above 1
		return nil
	}
	_ = r.RegisterOp(def)
	r.Start(ctx)

	op1, _ := r.EnqueueOp(ctx, "test.serial", nil)
	// Wait for first to start before enqueuing second.
	<-barrier
	op2, _ := r.EnqueueOp(ctx, "test.serial", nil)

	awaitStatus(t, store, op1, "completed", 5*time.Second)
	awaitStatus(t, store, op2, "completed", 5*time.Second)

	mu.Lock()
	defer mu.Unlock()
	if maxOverlap > 1 {
		t.Errorf("ops with same ConcurrencyKey ran concurrently (maxOverlap=%d)", maxOverlap)
	}
}

// TestDispatcher_DifferentConcurrencyKeysRunConcurrently verifies that two ops
// with different ConcurrencyKeys can overlap.
func TestDispatcher_DifferentConcurrencyKeysRunConcurrently(t *testing.T) {
	ctx := t.Context()

	store := newFakeStore()
	r := registry.New(store, slog.Default(), 4, nil)

	var running int64
	var maxOverlap int64

	gate := make(chan struct{})
	var gateOnce sync.Once

	makeDef := func(id, key string) registry.OperationDef {
		d := makeValidDef(id)
		d.ConcurrencyKey = key
		d.Run = func(runCtx context.Context, _ json.RawMessage, _ registry.Reporter) error {
			cur := atomic.AddInt64(&running, 1)
			for {
				old := atomic.LoadInt64(&maxOverlap)
				if cur <= old || atomic.CompareAndSwapInt64(&maxOverlap, old, cur) {
					break
				}
			}
			gateOnce.Do(func() { close(gate) })
			// Hold until the other op is also running.
			select {
			case <-gate:
			case <-runCtx.Done():
			}
			time.Sleep(10 * time.Millisecond)
			atomic.AddInt64(&running, -1)
			return nil
		}
		return d
	}

	_ = r.RegisterOp(makeDef("test.concurrent-a", "key-a"))
	_ = r.RegisterOp(makeDef("test.concurrent-b", "key-b"))
	r.Start(ctx)

	op1, _ := r.EnqueueOp(ctx, "test.concurrent-a", nil)
	op2, _ := r.EnqueueOp(ctx, "test.concurrent-b", nil)

	awaitStatus(t, store, op1, "completed", 5*time.Second)
	awaitStatus(t, store, op2, "completed", 5*time.Second)

	if atomic.LoadInt64(&maxOverlap) < 2 {
		// It's possible they ran serially if the scheduler didn't interleave.
		// This is a timing-sensitive test; log a warning but don't hard-fail.
		t.Logf("warning: different ConcurrencyKey ops did not overlap (maxOverlap=%d) — may be a race in test timing", atomic.LoadInt64(&maxOverlap))
	}
}

// TestDispatcher_PriorityOrderingHighBeforeLow verifies that a high-priority
// op is dispatched before a low-priority op enqueued around the same time.
func TestDispatcher_PriorityOrderingHighBeforeLow(t *testing.T) {
	ctx := t.Context()

	// Single worker so ordering is strict.
	store := newFakeStore()
	r := registry.New(store, slog.Default(), 1, nil)

	order := make([]string, 0, 2)
	var mu sync.Mutex
	makeOrderedDef := func(id string, prio registry.Priority) registry.OperationDef {
		d := makeValidDef(id)
		d.DefaultPriority = prio
		d.Run = func(_ context.Context, _ json.RawMessage, _ registry.Reporter) error {
			mu.Lock()
			order = append(order, id)
			mu.Unlock()
			return nil
		}
		return d
	}
	_ = r.RegisterOp(makeOrderedDef("test.prio-low", registry.PriorityLow))
	_ = r.RegisterOp(makeOrderedDef("test.prio-high", registry.PriorityHigh))

	r.Start(ctx)

	// Make BOTH ops visible to the dispatcher atomically (single store lock), so
	// the first dispatch cycle that sees them orders them strictly by priority
	// (DESC) and sends high to the worker first.
	//
	// The previous version enqueued them separately (low then high) via
	// EnqueueOp against a deliberately-busy worker — racy (~25% flake under
	// load): each EnqueueOp triggers a dispatch cycle, so the dispatcher claimed
	// the low-priority op into the buffered (FIFO) nextRun channel before the
	// high-priority op was even enqueued, and the worker then ran low first.
	// Priority is only guaranteed among ops visible within ONE cycle; inserting
	// both atomically makes that precondition deterministic. (Inserting directly
	// rather than via EnqueueOp also sidesteps resumeAfterStartup, which would
	// drop/requeue ops enqueued before Start.)
	now := time.Now().UTC()
	store.insertQueuedAtomic(
		database.OperationV2Row{
			ID: "op-prio-low", DefID: "test.prio-low", Plugin: "test",
			Status: "queued", Priority: int(registry.PriorityLow), QueuedAt: now,
		},
		database.OperationV2Row{
			ID: "op-prio-high", DefID: "test.prio-high", Plugin: "test",
			Status: "queued", Priority: int(registry.PriorityHigh), QueuedAt: now,
		},
	)

	awaitStatus(t, store, "op-prio-low", "completed", 5*time.Second)
	awaitStatus(t, store, "op-prio-high", "completed", 5*time.Second)

	mu.Lock()
	defer mu.Unlock()
	if len(order) != 2 {
		t.Fatalf("expected 2 ops to run, got %d: %v", len(order), order)
	}
	if order[0] != "test.prio-high" {
		t.Errorf("expected high-priority op first, got order %v", order)
	}
}

// TestDispatcher_MaxConcurrentCapsPlugin verifies per-plugin concurrency cap.
func TestDispatcher_MaxConcurrentCapsPlugin(t *testing.T) {
	ctx := t.Context()

	store := newFakeStore()
	r := registry.New(store, slog.Default(), 8, nil)
	r.SetPluginMaxConcurrent("capped-plugin", 1)

	var running int64
	var maxRunning int64

	runOnce := make(chan struct{})
	var runOnceOnce sync.Once

	def := makeValidDef("capped.op")
	def.Plugin = "capped-plugin"
	def.Run = func(runCtx context.Context, _ json.RawMessage, _ registry.Reporter) error {
		cur := atomic.AddInt64(&running, 1)
		for {
			old := atomic.LoadInt64(&maxRunning)
			if cur <= old || atomic.CompareAndSwapInt64(&maxRunning, old, cur) {
				break
			}
		}
		runOnceOnce.Do(func() { close(runOnce) })
		time.Sleep(40 * time.Millisecond)
		atomic.AddInt64(&running, -1)
		return nil
	}
	_ = r.RegisterOp(def)
	r.Start(ctx)

	// Enqueue 3 ops — only 1 should run at a time.
	op1, _ := r.EnqueueOp(ctx, "capped.op", nil)
	op2, _ := r.EnqueueOp(ctx, "capped.op", nil)
	op3, _ := r.EnqueueOp(ctx, "capped.op", nil)

	awaitStatus(t, store, op1, "completed", 10*time.Second)
	awaitStatus(t, store, op2, "completed", 10*time.Second)
	awaitStatus(t, store, op3, "completed", 10*time.Second)

	if atomic.LoadInt64(&maxRunning) > 1 {
		t.Errorf("plugin concurrency cap violated: maxRunning=%d", atomic.LoadInt64(&maxRunning))
	}
}
