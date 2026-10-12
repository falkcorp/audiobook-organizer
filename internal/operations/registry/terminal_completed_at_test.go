// file: internal/operations/registry/terminal_completed_at_test.go
// version: 1.3.0
// guid: 65ef6402-0192-4d0d-ac5a-0314f463137c
// last-edited: 2026-10-11

package registry_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// These tests drive each terminal transition the registry performs, end to end,
// and assert the row it leaves behind carries a completed_at. CompletedAt is the
// canonical liveness signal: ListOperationsV2Since and the timeline handler
// both treat `completed_at == nil` as in-flight, so a terminal row without one
// sits in Active Operations indefinitely. A canceled
// maintenance.transcribe-book-intros op did exactly that from 2026-06-26 to
// 2026-09-07.
//
// The fakeStore's UpdateOperationV2Status deliberately does NOT stamp on its own
// (the real PebbleStore does, since 2026-09-12). That makes these tests pin the
// CALL SITES: each registry path must pass its own completedAt, so the store's
// stamp stays a backstop rather than the only thing holding the invariant.

// requireCompletedAtT12 fails the test if opID's row has no completed_at.
// Task-unique name per the parallel-test-helper-collision rule.
func requireCompletedAtT12(t *testing.T, store *fakeStore, opID, wantStatus string) {
	t.Helper()
	row, err := store.GetOperationV2(opID)
	if err != nil {
		t.Fatalf("GetOperationV2(%s): %v", opID, err)
	}
	if row.Status != wantStatus {
		t.Fatalf("op %s: status %q, want %q", opID, row.Status, wantStatus)
	}
	if row.CompletedAt == nil {
		t.Fatalf("op %s reached terminal status %q with completed_at nil", opID, wantStatus)
	}
}

func TestTerminalCompletedAt_Completed(t *testing.T) {
	ctx := t.Context()
	store := newFakeStore()
	bus := &t06TermBus{}
	r := registry.New(store, slog.Default(), 1, bus)

	def := makeValidDef("test.t12-completed")
	def.Run = func(_ context.Context, _ json.RawMessage, _ registry.Reporter) error { return nil }
	_ = r.RegisterOp(def)
	r.Start(ctx)

	opID, _ := r.EnqueueOp(ctx, "test.t12-completed", nil)
	bus.waitTerminal(t, opID, 5*time.Second)
	requireCompletedAtT12(t, store, opID, "completed")
}

func TestTerminalCompletedAt_Failed(t *testing.T) {
	ctx := t.Context()
	store := newFakeStore()
	bus := &t06TermBus{}
	r := registry.New(store, slog.Default(), 1, bus)

	def := makeValidDef("test.t12-failed")
	def.Run = func(_ context.Context, _ json.RawMessage, _ registry.Reporter) error {
		return errors.New("boom")
	}
	_ = r.RegisterOp(def)
	r.Start(ctx)

	opID, _ := r.EnqueueOp(ctx, "test.t12-failed", nil)
	bus.waitTerminal(t, opID, 5*time.Second)
	requireCompletedAtT12(t, store, opID, "failed")
}

func TestTerminalCompletedAt_CanceledWhileRunning(t *testing.T) {
	ctx := t.Context()
	store := newFakeStore()
	bus := &t06TermBus{}
	r := registry.New(store, slog.Default(), 1, bus)

	started := make(chan struct{})
	def := makeValidDef("test.t12-canceled-running")
	def.Run = func(runCtx context.Context, _ json.RawMessage, _ registry.Reporter) error {
		close(started)
		<-runCtx.Done()
		return runCtx.Err()
	}
	_ = r.RegisterOp(def)
	r.Start(ctx)

	opID, _ := r.EnqueueOp(ctx, "test.t12-canceled-running", nil)
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("op did not start")
	}
	_ = r.Cancel(opID)
	bus.waitTerminal(t, opID, 5*time.Second)
	requireCompletedAtT12(t, store, opID, "canceled")
}

// TestTerminalCompletedAt_CanceledWhileQueued is the path behind the 73-day
// zombie: an op canceled before any worker picked it up goes through
// a compare-and-set from "queued" that passes no completedAt (it was
// SetOperationV2StatusIfQueued, which took none), so the stamp has to come
// from the store. The fakeStore mirrors PebbleStore there.
func TestTerminalCompletedAt_CanceledWhileQueued(t *testing.T) {
	store := newFakeStore()
	bus := &t06TermBus{}
	r := registry.New(store, slog.Default(), 1, bus)

	_ = r.RegisterOp(makeValidDef("test.t12-canceled-queued"))
	// Not started: the op stays queued and is never dispatched.
	opID, err := r.EnqueueOp(context.Background(), "test.t12-canceled-queued", nil)
	if err != nil {
		t.Fatalf("EnqueueOp: %v", err)
	}
	if err := r.Cancel(opID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	bus.waitTerminal(t, opID, 2*time.Second)
	requireCompletedAtT12(t, store, opID, "canceled")
}

// TestTerminalCompletedAt_Abandoned covers the abandonment path: a run that
// ignores cancellation still gets completed_at stamped. A deliberate Cancel is
// written "canceled" however slowly the Run unwinds. Since 05-PR4 that write
// (and op.terminal) happens when the goroutine returns, not at abandonment.
func TestTerminalCompletedAt_Abandoned(t *testing.T) {
	ctx := t.Context()
	store := newFakeStore()
	bus := &t06TermBus{}
	r := registry.NewWithOptions(store, slog.Default(), 2, registry.Options{
		WatchdogInterval: 30 * time.Second,
		AbandonedCap:     10,
		AbandonGrace:     100 * time.Millisecond,
		Bus:              bus,
	})

	release := make(chan struct{})
	started := make(chan struct{})
	def := makeValidDef("test.t12-dropped")
	def.Run = func(_ context.Context, _ json.RawMessage, _ registry.Reporter) error {
		close(started)
		<-release // ignore ctx: forces the abandonment path
		return nil
	}
	_ = r.RegisterOp(def)
	r.Start(ctx)

	opID, _ := r.EnqueueOp(ctx, "test.t12-dropped", nil)
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("op did not start")
	}
	_ = r.Cancel(opID)
	deadline := time.Now().Add(5 * time.Second)
	for r.ZombieCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if r.ZombieCount() != 1 {
		close(release)
		t.Fatal("the run never became a zombie")
	}
	if got := store.statusOf(opID); got != "running" {
		close(release)
		t.Fatalf("status while the goroutine lives = %q, want running", got)
	}
	close(release)
	bus.waitTerminal(t, opID, 5*time.Second)
	requireCompletedAtT12(t, store, opID, "canceled")
}
