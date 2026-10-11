// file: internal/operations/registry/cancel_requeue_race_test.go
// version: 1.1.0
// guid: 3f9e61b8-2c47-4d05-8a1b-e0d57c9a4b26
// last-edited: 2026-10-10

package registry_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// claimDuringCancelStore replays one interleaving of Registry.Cancel's
// persisted-row branch: Cancel holds retryMu and reads the row as "queued" (a
// re-queue just won), and BEFORE Cancel's next store call a worker claims the
// row and starts the run. The dispatcher and the worker do not take retryMu,
// so nothing prevents this in production; the hook makes it deterministic.
//
// On the first GetOperationV2 of target after arm() it (1) flips the row to
// queued like resumeQuiescedOp, (2) snapshots that queued row, (3) runs one
// dispatcher pass and waits for the run to start (so the worker's
// queued->running write has landed), then (4) returns the stale snapshot.
type claimDuringCancelStore struct {
	*fakeStore
	r      *registry.Registry
	target string
	armed  atomic.Bool
	// runStarted is closed by the def's Run.
	runStarted chan struct{}
	timeout    time.Duration
}

func (s *claimDuringCancelStore) GetOperationV2(id string) (*database.OperationV2Row, error) {
	if id != s.target || !s.armed.CompareAndSwap(true, false) {
		return s.fakeStore.GetOperationV2(id)
	}
	if err := s.fakeStore.ResetOperationV2ForResume(id); err != nil {
		return nil, err
	}
	snapshot, err := s.fakeStore.GetOperationV2(id)
	if err != nil {
		return nil, err
	}
	s.r.DispatchCycleForTest(context.Background())
	select {
	case <-s.runStarted:
	case <-time.After(s.timeout):
		// Surface as a read error: the test then fails on Cancel's result.
		return nil, context.DeadlineExceeded
	}
	return snapshot, nil
}

// TestCancel_RequeueClaimedByWorkerBeforeTheCancelWriteCancelsTheRun is the
// regression test for Cancel overwriting a live run's status. Cancel read a
// re-queued row as "queued" under retryMu and wrote "canceled"
// unconditionally; a worker that had claimed the row in between had its
// "running" status overwritten while its context kept running with no
// userCanceled flag, and the run's terminal write later overwrote "canceled".
// The user was told the op was canceled and it ran to completion.
//
// Mutation check: with an unconditional UpdateOperationV2Status in
// cancelByRow's queued case (instead of SetOperationV2StatusIfQueued, a re-read
// on a miss, and canceling the live handle of the "running" row it finds) the
// run's context is never canceled and the test fails on "the run's context was
// not canceled".
func TestCancel_RequeueClaimedByWorkerBeforeTheCancelWriteCancelsTheRun(t *testing.T) {
	base := newFakeStore()
	store := &claimDuringCancelStore{
		fakeStore:  base,
		target:     "op-requeued",
		runStarted: make(chan struct{}),
		timeout:    5 * time.Second,
	}
	r := registry.New(store, slog.Default(), 2, nil)
	store.r = r

	var runs atomic.Int32
	ctxCanceled := make(chan struct{})
	release := make(chan struct{})
	def := makeValidDef("test.cancel.requeue")
	def.Run = func(ctx context.Context, _ json.RawMessage, _ registry.Reporter) error {
		runs.Add(1)
		close(store.runStarted)
		select {
		case <-ctx.Done():
			close(ctxCanceled)
			return ctx.Err()
		case <-release:
			return nil
		}
	}
	if err := r.RegisterOp(def); err != nil {
		t.Fatal(err)
	}
	r.Start(t.Context())
	t.Cleanup(func() { close(release) })

	// A quiesced row nothing is running: Cancel's first compare-and-set (queued
	// only) misses it and the persisted-row branch takes retryMu.
	base.insertQueuedAtomic(database.OperationV2Row{
		ID: store.target, DefID: def.ID, Plugin: def.Plugin,
		Status: "interrupted_quiesced", QueuedAt: time.Now().UTC(),
	})
	store.armed.Store(true)

	if err := r.Cancel(store.target); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if got := runs.Load(); got != 1 {
		t.Fatalf("Run called %d times, want 1 (the interleaving did not happen)", got)
	}
	select {
	case <-ctxCanceled:
	case <-time.After(5 * time.Second):
		t.Fatalf("the run's context was not canceled; row status is %q (Cancel overwrote the live run's status)",
			base.statusOf(store.target))
	}
	awaitStatus(t, base, store.target, "canceled", 5*time.Second)
	// The terminal write of the canceled run must not have resurrected it.
	time.Sleep(50 * time.Millisecond)
	if got := base.statusOf(store.target); got != "canceled" {
		t.Fatalf("final status = %q, want canceled", got)
	}
}
