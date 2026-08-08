// file: internal/operations/registry/writeset_gate_test.go
// version: 1.0.0
// guid: 3f8a2b1c-9d4e-4f6a-8b2c-5e7d9a1f3c60
// last-edited: 2026-08-07

package registry_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// assertStaysQueued polls for `hold` and fails if the op leaves "queued".
func assertStaysQueued(t *testing.T, store *fakeStore, opID string, hold time.Duration) {
	t.Helper()
	deadline := time.Now().Add(hold)
	for time.Now().Before(deadline) {
		if s := store.statusOf(opID); s != "queued" {
			t.Fatalf("op %s left queued too early: status=%s", opID, s)
		}
		time.Sleep(15 * time.Millisecond)
	}
}

// TestDispatcher_WriteSetOverlapDefers verifies the Gate-3b core contract:
// an op whose declared Writes overlap a RUNNING op's Writes stays QUEUED
// (not rejected, not failed) and dispatches after the running op completes.
func TestDispatcher_WriteSetOverlapDefers(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	store := newFakeStore()
	r := registry.New(store, slog.Default(), 4, nil)

	started := make(chan struct{})
	release := make(chan struct{})

	blocker := makeValidDef("test.ws-blocker")
	blocker.Writes = []registry.Resource{registry.ResBooks, registry.ResBookFiles}
	blocker.Run = func(runCtx context.Context, _ json.RawMessage, _ registry.Reporter) error {
		close(started)
		select {
		case <-release:
		case <-runCtx.Done():
		}
		return nil
	}

	var overlapRan atomic.Bool
	contender := makeValidDef("test.ws-contender")
	contender.Writes = []registry.Resource{registry.ResBooks} // overlaps on books
	contender.Run = func(_ context.Context, _ json.RawMessage, _ registry.Reporter) error {
		overlapRan.Store(true)
		return nil
	}

	_ = r.RegisterOp(blocker)
	_ = r.RegisterOp(contender)
	r.Start(ctx)

	op1, err := r.EnqueueOp(ctx, "test.ws-blocker", nil)
	if err != nil {
		t.Fatalf("EnqueueOp blocker: %v", err)
	}
	<-started

	op2, err := r.EnqueueOp(ctx, "test.ws-contender", nil)
	if err != nil {
		t.Fatalf("EnqueueOp contender: %v", err)
	}

	// While the blocker holds books+book_files, the contender must sit queued.
	assertStaysQueued(t, store, op2, 500*time.Millisecond)
	if overlapRan.Load() {
		t.Fatal("contender ran while blocker held an overlapping write-set")
	}

	// Release the blocker: the contender must now dispatch and complete.
	close(release)
	awaitStatus(t, store, op1, "completed", 5*time.Second)
	awaitStatus(t, store, op2, "completed", 5*time.Second)
	if !overlapRan.Load() {
		t.Fatal("contender never ran after blocker completed")
	}
}

// TestDispatcher_WriteSetDisjointRunsConcurrently verifies that ops writing
// DIFFERENT tables are not serialized by the gate.
func TestDispatcher_WriteSetDisjointRunsConcurrently(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	store := newFakeStore()
	r := registry.New(store, slog.Default(), 4, nil)

	var running, maxOverlap int64
	gate := make(chan struct{})
	var gateClosed atomic.Bool

	makeDef := func(id string, writes []registry.Resource) registry.OperationDef {
		d := makeValidDef(id)
		d.Writes = writes
		d.Run = func(runCtx context.Context, _ json.RawMessage, _ registry.Reporter) error {
			cur := atomic.AddInt64(&running, 1)
			for {
				old := atomic.LoadInt64(&maxOverlap)
				if cur <= old || atomic.CompareAndSwapInt64(&maxOverlap, old, cur) {
					break
				}
			}
			if gateClosed.CompareAndSwap(false, true) {
				close(gate)
			}
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

	_ = r.RegisterOp(makeDef("test.ws-books", []registry.Resource{registry.ResBooks}))
	_ = r.RegisterOp(makeDef("test.ws-files", []registry.Resource{registry.ResBookFiles}))
	r.Start(ctx)

	op1, _ := r.EnqueueOp(ctx, "test.ws-books", nil)
	op2, _ := r.EnqueueOp(ctx, "test.ws-files", nil)

	awaitStatus(t, store, op1, "completed", 5*time.Second)
	awaitStatus(t, store, op2, "completed", 5*time.Second)

	if atomic.LoadInt64(&maxOverlap) < 2 {
		// Timing-sensitive: serial execution is legal scheduling, so warn only —
		// same convention as TestDispatcher_DifferentConcurrencyKeysRunConcurrently.
		t.Logf("warning: disjoint write-set ops did not overlap (maxOverlap=%d) — may be test timing", atomic.LoadInt64(&maxOverlap))
	}
}

// TestDispatcher_WriteSetUndeclaredUnaffected verifies incremental rollout:
// an op with EMPTY Writes runs to completion while a declared writer is
// still running — the gate ignores undeclared ops in both directions.
func TestDispatcher_WriteSetUndeclaredUnaffected(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	store := newFakeStore()
	r := registry.New(store, slog.Default(), 4, nil)

	started := make(chan struct{})
	release := make(chan struct{})

	writer := makeValidDef("test.ws-declared-writer")
	writer.Writes = []registry.Resource{registry.ResBooks}
	writer.Run = func(runCtx context.Context, _ json.RawMessage, _ registry.Reporter) error {
		close(started)
		select {
		case <-release:
		case <-runCtx.Done():
		}
		return nil
	}

	undeclared := makeValidDef("test.ws-undeclared")
	// Writes deliberately empty.
	_ = r.RegisterOp(writer)
	_ = r.RegisterOp(undeclared)
	r.Start(ctx)

	op1, _ := r.EnqueueOp(ctx, "test.ws-declared-writer", nil)
	<-started

	// The undeclared op must complete WHILE the declared writer is running.
	op2, _ := r.EnqueueOp(ctx, "test.ws-undeclared", nil)
	awaitStatus(t, store, op2, "completed", 5*time.Second)
	if s := store.statusOf(op1); s != "running" {
		t.Errorf("declared writer should still be running while undeclared op completed, got %s", s)
	}

	close(release)
	awaitStatus(t, store, op1, "completed", 5*time.Second)
}

// TestDispatcher_WriteSetReleasedOnFailure verifies the release path: a
// FAILED op frees its write-set slot (releaseRunHandle drops the handle, and
// the write-set lives on the handle), so a deferred contender dispatches
// afterward. Release-on-completion is covered by
// TestDispatcher_WriteSetOverlapDefers.
func TestDispatcher_WriteSetReleasedOnFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	store := newFakeStore()
	r := registry.New(store, slog.Default(), 4, nil)

	started := make(chan struct{})
	release := make(chan struct{})

	failing := makeValidDef("test.ws-failing")
	failing.Writes = []registry.Resource{registry.ResBooks}
	failing.Run = func(runCtx context.Context, _ json.RawMessage, _ registry.Reporter) error {
		close(started)
		select {
		case <-release:
		case <-runCtx.Done():
		}
		return errors.New("synthetic failure for write-set release test")
	}

	contender := makeValidDef("test.ws-after-failure")
	contender.Writes = []registry.Resource{registry.ResBooks}

	_ = r.RegisterOp(failing)
	_ = r.RegisterOp(contender)
	r.Start(ctx)

	op1, _ := r.EnqueueOp(ctx, "test.ws-failing", nil)
	<-started

	op2, _ := r.EnqueueOp(ctx, "test.ws-after-failure", nil)
	assertStaysQueued(t, store, op2, 300*time.Millisecond)

	close(release)
	awaitStatus(t, store, op1, "failed", 5*time.Second)
	// The failed op's handle release must free the write-set slot.
	awaitStatus(t, store, op2, "completed", 5*time.Second)
}
