// file: internal/operations/registry/warmup_gate_test.go
// version: 2.0.1
// guid: 3e8c1a74-5b92-4f06-a1d7-0c6b9e2f4a53
// last-edited: 2026-10-10

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
	"github.com/falkcorp/audiobook-organizer/internal/operations"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// warmingStore is a fakeStore that reports a startup warmup still in progress
// until release is closed -- the same WarmupStatus shape as *database.PebbleStore.
type warmingStore struct {
	*fakeStore
	release chan struct{}
}

func newWarmingStore() *warmingStore {
	return &warmingStore{fakeStore: newFakeStore(), release: make(chan struct{})}
}

func (w *warmingStore) WarmupStatus() (ready, done bool, ms int64) {
	select {
	case <-w.release:
		return true, true, 7
	default:
		return false, false, 0
	}
}

// warmupRecordingBus counts published events by name.
type warmupRecordingBus struct {
	mu     sync.Mutex
	counts map[string]int
}

func (b *warmupRecordingBus) Publish(_ context.Context, name string, _ any) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.counts == nil {
		b.counts = map[string]int{}
	}
	b.counts[name]++
	return nil
}

func (b *warmupRecordingBus) count(name string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.counts[name]
}

// A non-exempt op is held back while the store warms: it stays queued with no
// run handle (so it holds no worker slot and no watchdog/timeout clock), shows
// the wait message over its own progress numbers, and once warmup finishes it
// runs and its previous message is back.
func TestDispatch_OpHeldWhileWarmingThenRuns(t *testing.T) {
	ctx := t.Context()
	store := newWarmingStore()
	bus := &warmupRecordingBus{}
	r := registry.New(store, slog.Default(), 1, bus)

	proceed := make(chan struct{})
	var started atomic.Bool
	def := makeValidDef("test.w-hold")
	def.SummarizeQueued = func(json.RawMessage) (int, int, string) { return 1, 4, "prev summary" }
	def.Run = func(_ context.Context, _ json.RawMessage, _ registry.Reporter) error {
		started.Store(true)
		<-proceed
		return nil
	}
	_ = r.RegisterOp(def)
	r.Start(ctx)

	opID, err := r.EnqueueOp(ctx, "test.w-hold", nil)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	time.Sleep(500 * time.Millisecond)
	if started.Load() {
		t.Fatal("op body ran while the store was still warming")
	}
	if got := store.statusOf(opID); got != "queued" {
		t.Fatalf("status while held = %q, want queued", got)
	}
	if r.IsRunning(opID) {
		t.Fatal("a held op must have no run handle (it would hold a slot and start clocks)")
	}
	row, _ := store.GetOperationV2(opID)
	if row.ProgressMessage != operations.WarmupStatusMessage || row.ProgressCurrent != 1 || row.ProgressTotal != 4 {
		t.Fatalf("held row = %d/%d %q, want 1/4 %q", row.ProgressCurrent, row.ProgressTotal, row.ProgressMessage, operations.WarmupStatusMessage)
	}

	close(store.release)
	deadline := time.Now().Add(5 * time.Second)
	for !started.Load() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !started.Load() {
		t.Fatal("op body never ran after warmup finished")
	}
	row, _ = store.GetOperationV2(opID)
	if row.ProgressMessage != "prev summary" || row.ProgressCurrent != 1 || row.ProgressTotal != 4 {
		t.Fatalf("restored row = %d/%d %q, want 1/4 \"prev summary\"", row.ProgressCurrent, row.ProgressTotal, row.ProgressMessage)
	}
	if bus.count("op.updated") < 2 {
		t.Fatalf("op.updated published %d times; want the wait message and its restoration announced", bus.count("op.updated"))
	}
	close(proceed)
	awaitStatus(t, store.fakeStore, opID, "completed", 5*time.Second)
}

// The wait spends none of the op's own Timeout: Timeout 200 ms, warmup released
// at 600 ms; the body starts with a live context and most of a fresh timeout.
func TestDispatch_WarmupHoldDoesNotSpendOpTimeout(t *testing.T) {
	ctx := t.Context()
	store := newWarmingStore()
	r := registry.New(store, slog.Default(), 1, nil)

	var ctxDead atomic.Bool
	var remaining atomic.Int64
	def := makeValidDef("test.w-hold-timeout")
	def.Timeout = 200 * time.Millisecond
	def.Run = func(c context.Context, _ json.RawMessage, _ registry.Reporter) error {
		ctxDead.Store(c.Err() != nil)
		if dl, ok := c.Deadline(); ok {
			remaining.Store(int64(time.Until(dl)))
		}
		return nil
	}
	_ = r.RegisterOp(def)
	r.Start(ctx)

	opID, _ := r.EnqueueOp(ctx, "test.w-hold-timeout", nil)
	time.Sleep(600 * time.Millisecond)
	close(store.release)
	awaitStatus(t, store.fakeStore, opID, "completed", 5*time.Second)

	if ctxDead.Load() {
		t.Fatal("body started with a dead context")
	}
	if rem := time.Duration(remaining.Load()); rem < 100*time.Millisecond {
		t.Fatalf("body started with only %v of its 200ms timeout left; the hold spent the budget", rem)
	}
}

// The watchdog/uncheckpointed baseline starts when the op is picked up, not
// when it was queued: with an injected clock advanced 250 s during the hold,
// started_at is the post-hold time, so the wait cannot be counted as idle.
func TestDispatch_StartedAtIsAfterTheHold(t *testing.T) {
	ctx := t.Context()
	store := newWarmingStore()
	var clockMu sync.Mutex
	clock := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	now := func() time.Time { clockMu.Lock(); defer clockMu.Unlock(); return clock }
	r := registry.NewWithOptions(store, slog.Default(), 1, registry.Options{LivenessNow: now})

	proceed := make(chan struct{})
	def := makeValidDef("test.w-hold-clock")
	def.Run = func(_ context.Context, _ json.RawMessage, _ registry.Reporter) error { <-proceed; return nil }
	_ = r.RegisterOp(def)
	r.Start(ctx)

	opID, _ := r.EnqueueOp(ctx, "test.w-hold-clock", nil)
	time.Sleep(300 * time.Millisecond)
	clockMu.Lock()
	clock = clock.Add(250 * time.Second)
	want := clock
	clockMu.Unlock()
	close(store.release)
	awaitStatus(t, store.fakeStore, opID, "running", 5*time.Second)

	row, _ := store.GetOperationV2(opID)
	if row.StartedAt == nil || !row.StartedAt.Equal(want) {
		t.Fatalf("started_at = %v, want the post-hold clock %v", row.StartedAt, want)
	}
	close(proceed)
	awaitStatus(t, store.fakeStore, opID, "completed", 5*time.Second)
}

// A warmup that never finishes releases held work after the bound (injected
// clock; no real 300 s wait).
func TestDispatch_HoldEndsAtTheBound(t *testing.T) {
	ctx := t.Context()
	store := newWarmingStore() // never released
	var clockMu sync.Mutex
	clock := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	now := func() time.Time { clockMu.Lock(); defer clockMu.Unlock(); return clock }
	r := registry.NewWithOptions(store, slog.Default(), 1, registry.Options{LivenessNow: now})
	_ = r.RegisterOp(makeValidDef("test.w-bound"))
	r.Start(ctx)

	opID, _ := r.EnqueueOp(ctx, "test.w-bound", nil)
	time.Sleep(400 * time.Millisecond)
	if got := store.statusOf(opID); got != "queued" {
		t.Fatalf("status before the bound = %q, want queued", got)
	}
	clockMu.Lock()
	clock = clock.Add(operations.WarmupWaitTimeout + time.Second)
	clockMu.Unlock()
	awaitStatus(t, store.fakeStore, opID, "completed", 5*time.Second)
}

// A cancel while held ends the op as canceled without ever running the body,
// publishes op.terminal exactly once, and a later release does not revive it.
func TestDispatch_CancelWhileHeldNeverRunsBody(t *testing.T) {
	ctx := t.Context()
	store := newWarmingStore()
	bus := &warmupRecordingBus{}
	r := registry.New(store, slog.Default(), 1, bus)

	var ran atomic.Bool
	def := makeValidDef("test.w-hold-cancel")
	def.Run = func(_ context.Context, _ json.RawMessage, _ registry.Reporter) error { ran.Store(true); return nil }
	_ = r.RegisterOp(def)
	r.Start(ctx)

	opID, _ := r.EnqueueOp(ctx, "test.w-hold-cancel", nil)
	time.Sleep(300 * time.Millisecond)
	if err := r.Cancel(opID); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	close(store.release)
	time.Sleep(400 * time.Millisecond)
	if ran.Load() {
		t.Fatal("body ran after a cancel during the hold")
	}
	if got := store.statusOf(opID); got != "canceled" {
		t.Fatalf("final status = %q, want canceled", got)
	}
	if n := bus.count("op.terminal"); n != 1 {
		t.Fatalf("op.terminal published %d times, want exactly 1", n)
	}
}

// Shutdown while held leaves the row queued for the next start and never runs it.
func TestDispatch_ShutdownWhileHeldLeavesRowQueued(t *testing.T) {
	ctx := t.Context()
	store := newWarmingStore()
	r := registry.New(store, slog.Default(), 1, nil)

	var ran atomic.Bool
	def := makeValidDef("test.w-hold-shutdown")
	def.Run = func(_ context.Context, _ json.RawMessage, _ registry.Reporter) error { ran.Store(true); return nil }
	_ = r.RegisterOp(def)
	r.Start(ctx)

	opID, _ := r.EnqueueOp(ctx, "test.w-hold-shutdown", nil)
	time.Sleep(300 * time.Millisecond)
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := r.Shutdown(sctx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	close(store.release)
	time.Sleep(300 * time.Millisecond)
	if ran.Load() {
		t.Fatal("body ran after shutdown")
	}
	if got := store.statusOf(opID); got != "queued" {
		t.Fatalf("status after shutdown = %q, want queued (resumable on next start)", got)
	}
}

// Exempt (user-triggered, seconds-long) defs run while the store warms, and a
// held op does not occupy the only worker: with 1 worker, a non-exempt op
// enqueued first is held and the exempt op still completes.
func TestDispatch_ExemptDefRunsWhileWarmingAndHeldOpTakesNoSlot(t *testing.T) {
	ctx := t.Context()
	store := newWarmingStore()
	r := registry.New(store, slog.Default(), 1, nil)

	heldDef := makeValidDef("test.w-held")
	exempt := makeValidDef("test.w-exempt")
	exempt.NoWarmupWait = true
	_ = r.RegisterOp(heldDef)
	_ = r.RegisterOp(exempt)
	r.Start(ctx)

	heldID, _ := r.EnqueueOp(ctx, "test.w-held", nil)
	time.Sleep(200 * time.Millisecond)
	exID, _ := r.EnqueueOp(ctx, "test.w-exempt", nil)
	awaitStatus(t, store.fakeStore, exID, "completed", 5*time.Second)
	if got := store.statusOf(heldID); got != "queued" {
		t.Fatalf("held op status = %q, want queued", got)
	}
	close(store.release)
	awaitStatus(t, store.fakeStore, heldID, "completed", 5*time.Second)
}

// cancelAfterSnapshotStore returns the queued snapshot the dispatcher asked for
// and THEN cancels those rows, reproducing a cancel that lands between a
// dispatch cycle's snapshot and its announce.
type cancelAfterSnapshotStore struct {
	*warmingStore
	once sync.Once
}

func (c *cancelAfterSnapshotStore) ListQueuedOperationsV2() ([]database.OperationV2Row, error) {
	rows, err := c.warmingStore.ListQueuedOperationsV2()
	if len(rows) > 0 {
		c.once.Do(func() {
			now := time.Now().UTC()
			for _, r := range rows {
				_ = c.warmingStore.UpdateOperationV2Status(r.ID, "canceled", nil, &now, nil)
			}
		})
	}
	return rows, err
}

// A cancel between the cycle's queued snapshot and the announce must never
// leave a canceled row saying "waiting for startup warmup", nor publish
// op.updated for it.
func TestDispatch_CancelAfterSnapshotNeverLeavesWaitMessage(t *testing.T) {
	ctx := t.Context()
	store := &cancelAfterSnapshotStore{warmingStore: newWarmingStore()}
	bus := &warmupRecordingBus{}
	r := registry.New(store, slog.Default(), 1, bus)
	_ = r.RegisterOp(makeValidDef("test.w-snap-cancel"))
	r.Start(ctx)

	opID, _ := r.EnqueueOp(ctx, "test.w-snap-cancel", nil)
	time.Sleep(500 * time.Millisecond)
	row, _ := store.GetOperationV2(opID)
	if row.Status != "canceled" {
		t.Fatalf("status = %q, want canceled", row.Status)
	}
	if row.ProgressMessage == operations.WarmupStatusMessage {
		t.Fatal("a canceled row was left saying \"waiting for startup warmup\"")
	}
	if n := bus.count("op.updated"); n != 0 {
		t.Fatalf("op.updated published %d times for a row that was never written", n)
	}
}

// Holding an op touches neither high_water_progress nor last_progress_at (the
// watchdog and checkInfiniteRestart read them).
func TestDispatch_HoldLeavesWatermarkAndLivenessUntouched(t *testing.T) {
	ctx := t.Context()
	store := newWarmingStore()
	r := registry.New(store, slog.Default(), 1, nil)
	def := makeValidDef("test.w-hold-hwm")
	def.SummarizeQueued = func(json.RawMessage) (int, int, string) { return 3, 4, "summary" }
	_ = r.RegisterOp(def)
	r.Start(ctx)

	opID, _ := r.EnqueueOp(ctx, "test.w-hold-hwm", nil)
	time.Sleep(500 * time.Millisecond)
	row, _ := store.GetOperationV2(opID)
	if row.ProgressMessage != operations.WarmupStatusMessage {
		t.Fatalf("op was not announced as held (message %q)", row.ProgressMessage)
	}
	if row.HighWaterProgress != 0 {
		t.Fatalf("high_water_progress = %d after a hold, want 0", row.HighWaterProgress)
	}
	if row.LastProgressAt != nil {
		t.Fatalf("last_progress_at = %v after a hold, want nil", row.LastProgressAt)
	}
	close(store.release)
	awaitStatus(t, store.fakeStore, opID, "completed", 5*time.Second)
}
