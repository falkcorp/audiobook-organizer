// file: internal/operations/registry/terminal_events_test.go
// version: 1.0.0
// guid: 7e3d9c1a-2b46-4f80-9c5e-1a7f2b3c4d5e
// last-edited: 2026-07-18

package registry_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// t06TermBus is a mutex-protected recording Bus for the R-1 op.terminal tests.
// Terminal events are published from worker goroutines, so a plain slice append
// (like the existing recordingBus) would race under -race. Task-unique name per
// the parallel-test-helper-collision rule.
type t06TermBus struct {
	mu     sync.Mutex
	events []t06TermEvent
}

type t06TermEvent struct {
	name    string
	payload map[string]any
}

func (b *t06TermBus) Publish(_ context.Context, name string, payload any) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	m, _ := payload.(map[string]any)
	b.events = append(b.events, t06TermEvent{name: name, payload: m})
	return nil
}

// waitTerminal polls for an op.terminal event for opID and returns its status,
// or fails the test after timeout. The event is published immediately AFTER the
// terminal DB write in the worker, so a store-status check can win the race by
// a hair — polling the bus closes that window.
func (b *t06TermBus) waitTerminal(t *testing.T, opID string, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		b.mu.Lock()
		for _, ev := range b.events {
			if ev.name == "op.terminal" && ev.payload["op_id"] == opID {
				status, _ := ev.payload["status"].(string)
				b.mu.Unlock()
				return status
			}
		}
		b.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no op.terminal event for op %s within %v", opID, timeout)
	return ""
}

// TestTerminalEvent_Completed asserts op.terminal fires with status=completed.
func TestTerminalEvent_Completed(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	store := newFakeStore()
	bus := &t06TermBus{}
	r := registry.New(store, slog.Default(), 1, bus)

	def := makeValidDef("test.term-completed")
	def.Run = func(_ context.Context, _ json.RawMessage, _ registry.Reporter) error { return nil }
	_ = r.RegisterOp(def)
	r.Start(ctx)

	opID, _ := r.EnqueueOp(ctx, "test.term-completed", nil)
	awaitStatus(t, store, opID, "completed", 5*time.Second)
	if got := bus.waitTerminal(t, opID, 5*time.Second); got != "completed" {
		t.Errorf("op.terminal status: got %q want completed", got)
	}
}

// TestTerminalEvent_Failed asserts op.terminal fires with status=failed.
func TestTerminalEvent_Failed(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	store := newFakeStore()
	bus := &t06TermBus{}
	r := registry.New(store, slog.Default(), 1, bus)

	def := makeValidDef("test.term-failed")
	def.Run = func(_ context.Context, _ json.RawMessage, _ registry.Reporter) error {
		return errors.New("boom")
	}
	_ = r.RegisterOp(def)
	r.Start(ctx)

	opID, _ := r.EnqueueOp(ctx, "test.term-failed", nil)
	awaitStatus(t, store, opID, "failed", 5*time.Second)
	if got := bus.waitTerminal(t, opID, 5*time.Second); got != "failed" {
		t.Errorf("op.terminal status: got %q want failed", got)
	}
}

// TestTerminalEvent_CanceledRunning asserts op.terminal fires with
// status=canceled when a running op's context is canceled (in-process path).
func TestTerminalEvent_CanceledRunning(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	store := newFakeStore()
	bus := &t06TermBus{}
	r := registry.New(store, slog.Default(), 1, bus)

	started := make(chan struct{})
	def := makeValidDef("test.term-canceled")
	def.Run = func(runCtx context.Context, _ json.RawMessage, _ registry.Reporter) error {
		close(started)
		<-runCtx.Done() // honor cancellation promptly (not abandoned)
		return runCtx.Err()
	}
	_ = r.RegisterOp(def)
	r.Start(ctx)

	opID, _ := r.EnqueueOp(ctx, "test.term-canceled", nil)
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("op did not start")
	}
	_ = r.Cancel(opID)

	awaitStatus(t, store, opID, "canceled", 5*time.Second)
	if got := bus.waitTerminal(t, opID, 5*time.Second); got != "canceled" {
		t.Errorf("op.terminal status: got %q want canceled", got)
	}
}

// TestTerminalEvent_Abandoned asserts op.terminal fires with an interrupted_*
// status when a run ignores cancellation and is classified as abandoned.
func TestTerminalEvent_Abandoned(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

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
	def := makeValidDef("test.term-abandoned") // ResumeDrop → interrupted_dropped
	def.Run = func(_ context.Context, _ json.RawMessage, _ registry.Reporter) error {
		close(started)
		<-release // ignore ctx: forces the abandonment path
		return nil
	}
	_ = r.RegisterOp(def)
	r.Start(ctx)

	opID, _ := r.EnqueueOp(ctx, "test.term-abandoned", nil)
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("op did not start")
	}
	_ = r.Cancel(opID)

	awaitStatus(t, store, opID, "interrupted_dropped", 5*time.Second)
	if got := bus.waitTerminal(t, opID, 5*time.Second); got != "interrupted_dropped" {
		t.Errorf("op.terminal status: got %q want interrupted_dropped", got)
	}
	close(release)
}

// TestTerminalEvent_ForceDropped asserts op.terminal fires with
// status=interrupted_dropped on the infinite-restart force-drop path. The
// resume_count is set BEFORE Start so it can never race the worker pickup.
func TestTerminalEvent_ForceDropped(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	store := newFakeStore()
	bus := &t06TermBus{}
	r := registry.New(store, slog.Default(), 1, bus)

	def := makeValidDef("test.term-forcedrop")
	def.ResumePolicy = registry.ResumeRestart // required for checkInfiniteRestart
	def.Run = func(_ context.Context, _ json.RawMessage, _ registry.Reporter) error { return nil }
	_ = r.RegisterOp(def)

	// Enqueue before Start so the dispatcher can't pick it up until resume_count
	// is staged (guards the enqueue-vs-setup race).
	opID, _ := r.EnqueueOp(ctx, "test.term-forcedrop", nil)
	store.setResumeCount(opID, 3) // >= 3 with high_water=0 → force drop
	r.Start(ctx)

	awaitStatus(t, store, opID, "interrupted_dropped", 5*time.Second)
	if got := bus.waitTerminal(t, opID, 5*time.Second); got != "interrupted_dropped" {
		t.Errorf("op.terminal status: got %q want interrupted_dropped", got)
	}
}

// TestTerminalEvent_CanceledQueued asserts op.terminal fires with
// status=canceled when a purely-queued op (never picked up) is canceled — the
// path with no worker to publish the event on the op's behalf.
func TestTerminalEvent_CanceledQueued(t *testing.T) {
	store := newFakeStore()
	bus := &t06TermBus{}
	r := registry.New(store, slog.Default(), 1, bus)

	def := makeValidDef("test.term-queued-cancel")
	_ = r.RegisterOp(def)

	// Do NOT Start — the op stays queued, never dispatched.
	opID, err := r.EnqueueOp(context.Background(), "test.term-queued-cancel", nil)
	if err != nil {
		t.Fatalf("EnqueueOp: %v", err)
	}
	if err := r.Cancel(opID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if got := bus.waitTerminal(t, opID, 2*time.Second); got != "canceled" {
		t.Errorf("op.terminal status: got %q want canceled", got)
	}
}
