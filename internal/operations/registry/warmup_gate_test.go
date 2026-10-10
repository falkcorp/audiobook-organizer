// file: internal/operations/registry/warmup_gate_test.go
// version: 1.0.1
// guid: 3e8c1a74-5b92-4f06-a1d7-0c6b9e2f4a53
// last-edited: 2026-10-10

package registry_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// warmingStore is a fakeStore that reports a startup warmup still in progress
// until release is closed -- the same shape as *database.PebbleStore.
type warmingStore struct {
	*fakeStore
	release chan struct{}
}

func (w *warmingStore) WaitForWarmupCtx(ctx context.Context) error {
	select {
	case <-w.release:
		return nil
	default:
	}
	select {
	case <-w.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// An op that is dispatched while the store is still warming must not run its
// body until warmup finishes, and must run once it does. While it waits the row
// is still "queued" (the watchdog skips such rows, so the wait is not counted
// against the op).
func TestWorker_OpBodyWaitsForStartupWarmup(t *testing.T) {
	ctx := t.Context()

	store := &warmingStore{fakeStore: newFakeStore(), release: make(chan struct{})}
	r := registry.New(store, slog.Default(), 1, nil)

	var ran atomic.Bool
	def := makeValidDef("test.w-warmup")
	def.Run = func(_ context.Context, _ json.RawMessage, _ registry.Reporter) error {
		ran.Store(true)
		return nil
	}
	_ = r.RegisterOp(def)
	r.Start(ctx)

	opID, err := r.EnqueueOp(ctx, "test.w-warmup", nil)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	time.Sleep(400 * time.Millisecond)
	if ran.Load() {
		t.Fatal("op body ran while the store was still warming")
	}
	if got := store.statusOf(opID); got != "queued" {
		t.Fatalf("status while waiting = %q, want queued", got)
	}

	close(store.release)
	awaitStatus(t, store.fakeStore, opID, "completed", 5*time.Second)
	if !ran.Load() {
		t.Fatal("op body never ran after warmup finished")
	}
}

// The wait must not spend the op's own Timeout. Timeout 200 ms, warmup released
// at 600 ms: the op completes, and the body sees a live context with most of a
// fresh timeout left.
func TestWorker_WarmupWaitDoesNotSpendOpTimeout(t *testing.T) {
	ctx := t.Context()

	store := &warmingStore{fakeStore: newFakeStore(), release: make(chan struct{})}
	r := registry.New(store, slog.Default(), 1, nil)

	var ran atomic.Bool
	var ctxDead atomic.Bool
	var remaining atomic.Int64
	def := makeValidDef("test.w-warmup-timeout")
	def.Timeout = 200 * time.Millisecond
	def.Run = func(c context.Context, _ json.RawMessage, _ registry.Reporter) error {
		ran.Store(true)
		ctxDead.Store(c.Err() != nil)
		if dl, ok := c.Deadline(); ok {
			remaining.Store(int64(time.Until(dl)))
		}
		return nil
	}
	_ = r.RegisterOp(def)
	r.Start(ctx)

	opID, _ := r.EnqueueOp(ctx, "test.w-warmup-timeout", nil)
	time.Sleep(600 * time.Millisecond)
	close(store.release)
	awaitStatus(t, store.fakeStore, opID, "completed", 5*time.Second)

	if !ran.Load() {
		t.Fatal("body never ran")
	}
	if ctxDead.Load() {
		t.Fatal("body started with a dead context")
	}
	if rem := time.Duration(remaining.Load()); rem < 100*time.Millisecond {
		t.Fatalf("body started with only %v of its 200ms timeout left; the wait spent the budget", rem)
	}
}

// A user cancel during the wait ends the op as canceled and never runs the
// body or reports it as started.
func TestWorker_CancelDuringWarmupWaitNeverRunsBody(t *testing.T) {
	ctx := t.Context()

	store := &warmingStore{fakeStore: newFakeStore(), release: make(chan struct{})}
	r := registry.New(store, slog.Default(), 1, nil)

	var ran atomic.Bool
	def := makeValidDef("test.w-warmup-cancel")
	def.Run = func(_ context.Context, _ json.RawMessage, _ registry.Reporter) error {
		ran.Store(true)
		return nil
	}
	_ = r.RegisterOp(def)
	r.Start(ctx)

	opID, _ := r.EnqueueOp(ctx, "test.w-warmup-cancel", nil)
	time.Sleep(300 * time.Millisecond)
	if err := r.Cancel(opID); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	awaitStatus(t, store.fakeStore, opID, "canceled", 5*time.Second)
	close(store.release) // a late release must not resurrect the run
	time.Sleep(200 * time.Millisecond)
	if ran.Load() {
		t.Fatal("body ran after a cancel during the warmup wait")
	}
	if got := store.statusOf(opID); got != "canceled" {
		t.Fatalf("final status = %q, want canceled", got)
	}
}

// NoWarmupWait defs (interactive ones) do not wait at all.
func TestWorker_NoWarmupWaitDefRunsWhileWarming(t *testing.T) {
	ctx := t.Context()

	store := &warmingStore{fakeStore: newFakeStore(), release: make(chan struct{})}
	r := registry.New(store, slog.Default(), 1, nil)

	def := makeValidDef("test.w-warmup-exempt")
	def.NoWarmupWait = true
	_ = r.RegisterOp(def)
	r.Start(ctx)

	opID, _ := r.EnqueueOp(ctx, "test.w-warmup-exempt", nil)
	awaitStatus(t, store.fakeStore, opID, "completed", 5*time.Second)
}
