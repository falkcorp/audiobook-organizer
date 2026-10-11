// file: internal/operations/registry/cancel_row_cas_test.go
// version: 1.0.0
// guid: 90197191-b0f8-4979-823d-4e5265a1c1c4
// last-edited: 2026-10-10

package registry_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// These tests replay interleavings of Registry.Cancel against the writers it
// does not exclude: the dispatcher, a worker (its queued->running claim and
// the scan stand-down pickup gate) and the deps scheduler's promotion. None of
// them takes retryMu, so the interleavings happen in production; the store
// hooks below make each one deterministic.

// gateWriteMode says where interleaveStore holds the worker's pickup-gate
// write of "interrupted_quiesced".
type gateWriteMode int

const (
	gateWriteFree   gateWriteMode = iota
	gateWriteBefore               // hold the worker before the write lands
	gateWriteAfter                // hold the worker after the write landed
)

// interleaveStore wraps fakeStore with one-shot hooks for the target row.
type interleaveStore struct {
	*fakeStore
	target string

	// onRead runs on the first GetOperationV2(target) after armRead is set,
	// and its result is returned instead of the store's.
	armRead atomic.Bool
	onRead  func() (*database.OperationV2Row, error)

	// onCancelCAS runs on the first SetOperationV2StatusIfQueued(target,
	// "canceled") after armCancelCAS is set, wrapped around the real write:
	// before(), the write, then after().
	armCancelCAS atomic.Bool
	beforeCAS    func()
	afterCAS     func()

	// raiseGate acquires the scan stand-down on the next GetOperationV2(target)
	// after it is set. The dispatcher's post-claim re-read is that call, so
	// the gate goes up between the claim and the worker's pickup: the window
	// the pickup gate exists for, which Gate 3.5 cannot reach.
	raiseGate   atomic.Bool
	r           *registry.Registry
	releaseGate func()
	gateRaised  chan struct{}

	// The worker's pickup-gate write of interrupted_quiesced is held per
	// gateMode: gateArrived closes when it is reached, gateProceed releases it.
	gateMode    gateWriteMode
	gateOnce    sync.Once
	gateArrived chan struct{}
	gateProceed chan struct{}
	proceedOnce sync.Once
}

// proceed releases a held pickup-gate write. Idempotent.
func (s *interleaveStore) proceed() { s.proceedOnce.Do(func() { close(s.gateProceed) }) }

func newInterleaveStore(target string) *interleaveStore {
	return &interleaveStore{
		fakeStore:   newFakeStore(),
		target:      target,
		gateRaised:  make(chan struct{}),
		gateArrived: make(chan struct{}),
		gateProceed: make(chan struct{}),
	}
}

func (s *interleaveStore) GetOperationV2(id string) (*database.OperationV2Row, error) {
	if id == s.target && s.armRead.CompareAndSwap(true, false) {
		return s.onRead()
	}
	if id == s.target && s.raiseGate.CompareAndSwap(true, false) {
		rel, err := s.r.AcquireScanStandDown(context.Background(), "holder-test", "test")
		if err != nil {
			panic("AcquireScanStandDown: " + err.Error())
		}
		s.releaseGate = rel
		close(s.gateRaised)
	}
	return s.fakeStore.GetOperationV2(id)
}

func (s *interleaveStore) SetOperationV2StatusIfQueued(id, newStatus string) (bool, error) {
	if id == s.target && newStatus == "canceled" && s.armCancelCAS.CompareAndSwap(true, false) {
		if s.beforeCAS != nil {
			s.beforeCAS()
		}
		ok, err := s.fakeStore.SetOperationV2StatusIfQueued(id, newStatus)
		if s.afterCAS != nil {
			s.afterCAS()
		}
		return ok, err
	}
	return s.fakeStore.SetOperationV2StatusIfQueued(id, newStatus)
}

// holdGateWrite wraps the worker's pickup-gate write (the only writer of
// interrupted_quiesced to the target in these tests).
func (s *interleaveStore) holdGateWrite(id, status string, write func()) {
	if id != s.target || status != "interrupted_quiesced" || s.gateMode == gateWriteFree {
		write()
		return
	}
	first := false
	s.gateOnce.Do(func() { first = true })
	if !first {
		write()
		return
	}
	if s.gateMode == gateWriteBefore {
		close(s.gateArrived)
		<-s.gateProceed
		write()
		return
	}
	write()
	close(s.gateArrived)
	<-s.gateProceed
}

func (s *interleaveStore) UpdateOperationV2Status(id, status string, startedAt, completedAt *time.Time, errMsg *string) error {
	var err error
	s.holdGateWrite(id, status, func() {
		err = s.fakeStore.UpdateOperationV2Status(id, status, startedAt, completedAt, errMsg)
	})
	return err
}

func (s *interleaveStore) UpdateOperationV2StatusIf(id, fromStatus, status string, startedAt, completedAt *time.Time, errMsg *string) (bool, error) {
	var (
		ok  bool
		err error
	)
	s.holdGateWrite(id, status, func() {
		ok, err = s.fakeStore.UpdateOperationV2StatusIf(id, fromStatus, status, startedAt, completedAt, errMsg)
	})
	return ok, err
}

// cancelHarness is a started registry over an interleaveStore with one
// registered def whose Run counts calls and blocks until its context ends or
// the test releases it.
type cancelHarness struct {
	r          *registry.Registry
	store      *interleaveStore
	def        registry.OperationDef
	runs       atomic.Int32
	runStarted chan struct{}
	startOnce  sync.Once
	ctxDone    chan struct{}
	release    chan struct{}
}

func newCancelHarness(t *testing.T, defID string, policy registry.ResumePolicy) *cancelHarness {
	t.Helper()
	h := &cancelHarness{
		store:      newInterleaveStore("op-target"),
		runStarted: make(chan struct{}),
		ctxDone:    make(chan struct{}),
		release:    make(chan struct{}),
	}
	h.r = registry.New(h.store, slog.Default(), 2, nil)
	h.store.r = h.r
	h.def = makeValidDef(defID)
	h.def.ResumePolicy = policy
	h.def.Run = func(ctx context.Context, _ json.RawMessage, _ registry.Reporter) error {
		h.runs.Add(1)
		h.startOnce.Do(func() { close(h.runStarted) })
		select {
		case <-ctx.Done():
			close(h.ctxDone)
			return ctx.Err()
		case <-h.release:
			return nil
		}
	}
	if err := h.r.RegisterOp(h.def); err != nil {
		t.Fatal(err)
	}
	h.r.Start(t.Context())
	t.Cleanup(func() {
		h.store.proceed()
		close(h.release)
		if h.store.releaseGate != nil {
			h.store.releaseGate()
		}
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = h.r.Shutdown(sctx)
	})
	return h
}

func (h *cancelHarness) insert(status string) {
	h.store.insertQueuedAtomic(database.OperationV2Row{
		ID: h.store.target, DefID: h.def.ID, Plugin: h.def.Plugin,
		Status: status, QueuedAt: time.Now().UTC(),
	})
}

// awaitReleased waits until the target's handle is gone from r.running.
func (h *cancelHarness) awaitReleased(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for h.r.IsRunning(h.store.target) {
		if time.Now().After(deadline) {
			t.Fatalf("op %s still holds a handle after 5s", h.store.target)
		}
		time.Sleep(time.Millisecond)
	}
}

// assertStaysCanceledAfterGateRelease releases the stand-down (which re-queues
// every scan the gate dropped) and asserts the target is not resumed: Run is
// never called and the row stays canceled.
func (h *cancelHarness) assertStaysCanceledAfterGateRelease(t *testing.T) {
	t.Helper()
	h.awaitReleased(t)
	if h.store.releaseGate == nil {
		t.Fatal("the scan gate was never raised (the interleaving did not happen)")
	}
	h.store.releaseGate()
	h.store.releaseGate = nil
	h.r.DispatchCycleForTest(t.Context())
	select {
	case <-h.runStarted:
		t.Fatalf("Run was called after the stand-down released: the canceled scan was resumed (status %q)",
			h.store.statusOf(h.store.target))
	case <-time.After(300 * time.Millisecond):
	}
	if got := h.store.statusOf(h.store.target); got != "canceled" {
		t.Fatalf("final status = %q, want canceled", got)
	}
}

// TestCancel_LostQueuedCASToThePickupGateCancelsTheQuiescedRow is review S1.
// Cancel reads a re-queued scan as "queued" under retryMu; before its
// compare-and-set, the dispatcher claims the row, a maintenance op raises the
// scan gate, and the worker's pickup gate writes interrupted_quiesced with its
// full handle still registered. Cancel used to read "a full handle is
// registered" as "a worker claimed the row and is running it", cancel a
// context nobody waits on, and return success; the stand-down release then
// re-queued the scan and it ran.
func TestCancel_LostQueuedCASToThePickupGateCancelsTheQuiescedRow(t *testing.T) {
	h := newCancelHarness(t, registry.ScanStandDownDefIDForTest, registry.ResumeRestart)
	s := h.store
	s.gateMode = gateWriteAfter
	s.onRead = func() (*database.OperationV2Row, error) {
		if err := s.fakeStore.ResetOperationV2ForResume(s.target); err != nil {
			return nil, err
		}
		snapshot, err := s.fakeStore.GetOperationV2(s.target)
		if err != nil {
			return nil, err
		}
		s.raiseGate.Store(true)
		h.r.DispatchCycleForTest(context.Background())
		select {
		case <-s.gateArrived:
		case <-time.After(5 * time.Second):
			return nil, context.DeadlineExceeded
		}
		return snapshot, nil
	}
	// A quiesced row nothing runs: Cancel's lock-free attempt misses it and
	// the row loop takes retryMu and reads it (the hook).
	h.insert("interrupted_quiesced")
	s.armRead.Store(true)

	err := h.r.Cancel(s.target)
	s.proceed()
	if err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if got := s.statusOf(s.target); got != "canceled" {
		t.Fatalf("status right after Cancel = %q, want canceled (Cancel left the quiesced row live)", got)
	}
	h.assertStaysCanceledAfterGateRelease(t)
}

// TestCancel_WonQueuedCASIsNotOverwrittenByThePickupGate is the half of S1
// already on main. Cancel finds no handle, the dispatcher then claims the row
// (a stub with no cancel flag), Cancel's compare-and-set from queued wins, and
// only then does the worker's pickup gate fire. Its write was unconditional:
// it turned "canceled" back into interrupted_quiesced and scheduled a resume.
func TestCancel_WonQueuedCASIsNotOverwrittenByThePickupGate(t *testing.T) {
	h := newCancelHarness(t, registry.ScanStandDownDefIDForTest, registry.ResumeRestart)
	s := h.store
	s.gateMode = gateWriteBefore
	s.beforeCAS = func() {
		// The row is made queued only here, inside Cancel, so the background
		// dispatcher cannot claim and run it before Cancel has looked for a
		// handle (and found none).
		if err := s.fakeStore.ResetOperationV2ForResume(s.target); err != nil {
			panic(err)
		}
		s.raiseGate.Store(true)
		h.r.DispatchCycleForTest(context.Background())
		select {
		case <-s.gateArrived:
		case <-time.After(5 * time.Second):
		}
	}
	s.afterCAS = s.proceed
	h.insert("interrupted_quiesced")
	s.armCancelCAS.Store(true)

	if err := h.r.Cancel(s.target); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	select {
	case <-s.gateArrived:
	default:
		t.Fatal("the worker never reached the pickup gate (the interleaving did not happen)")
	}
	h.assertStaysCanceledAfterGateRelease(t)
}

// TestCancel_LiveHandleAtThePickupGateStillCancelsTheRow covers the same flaw
// at the top of Cancel. The worker registers its full handle BEFORE the
// pickup gate's status write, so Cancel's first lookup can find a full handle
// for a scan that is about to be quiesced rather than run. Cancel canceled
// the handle and returned; the gate then wrote interrupted_quiesced and the
// stand-down release resumed the scan.
func TestCancel_LiveHandleAtThePickupGateStillCancelsTheRow(t *testing.T) {
	h := newCancelHarness(t, registry.ScanStandDownDefIDForTest, registry.ResumeRestart)
	s := h.store
	s.gateMode = gateWriteBefore
	s.raiseGate.Store(true)
	h.insert("queued")
	h.r.DispatchCycleForTest(t.Context())
	recv(t, s.gateArrived, "the worker to reach the pickup gate")
	if !h.r.IsRunning(s.target) {
		t.Fatal("no handle registered at the pickup gate")
	}

	err := h.r.Cancel(s.target)
	s.proceed()
	if err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	h.assertStaysCanceledAfterGateRelease(t)
}

// TestCancel_WaitingDepsPromotedAndClaimedBeforeTheCancelWriteCancelsTheRun is
// review S2. Cancel reads a waiting_deps row under retryMu; before its write,
// the deps scheduler promotes the row to queued (PromoteToQueued does not take
// retryMu), the dispatcher claims it and the worker starts the run. Cancel's
// unconditional "canceled" then overwrote a live run's status while its
// context kept running with no userCanceled flag.
func TestCancel_WaitingDepsPromotedAndClaimedBeforeTheCancelWriteCancelsTheRun(t *testing.T) {
	h := newCancelHarness(t, "test.cancel.waiting-deps", registry.ResumeDrop)
	s := h.store
	s.onRead = func() (*database.OperationV2Row, error) {
		snapshot, err := s.fakeStore.GetOperationV2(s.target)
		if err != nil {
			return nil, err
		}
		if err := s.fakeStore.PromoteToQueued(s.target); err != nil {
			return nil, err
		}
		h.r.DispatchCycleForTest(context.Background())
		select {
		case <-h.runStarted:
		case <-time.After(5 * time.Second):
			return nil, context.DeadlineExceeded
		}
		return snapshot, nil
	}
	h.insert("waiting_deps")
	s.armRead.Store(true)

	if err := h.r.Cancel(s.target); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if got := h.runs.Load(); got != 1 {
		t.Fatalf("Run called %d times, want 1 (the interleaving did not happen)", got)
	}
	select {
	case <-h.ctxDone:
	case <-time.After(5 * time.Second):
		t.Fatalf("the run's context was not canceled; row status is %q (Cancel overwrote the live run's status)",
			s.statusOf(s.target))
	}
	awaitStatus(t, s.fakeStore, s.target, "canceled", 5*time.Second)
}

// TestCancel_RunFinishedBeforeTheReReadIsNotFound covers the no-handle re-read
// (review N1). Cancel reads a re-queued row as "queued"; before its
// compare-and-set the run is claimed, runs to completion and releases its
// handle. The compare-and-set misses, the re-read finds a completed row, and
// Cancel answers ErrOpNotFound without overwriting it.
func TestCancel_RunFinishedBeforeTheReReadIsNotFound(t *testing.T) {
	h := newCancelHarness(t, "test.cancel.finished", registry.ResumeDrop)
	s := h.store
	s.onRead = func() (*database.OperationV2Row, error) {
		if err := s.fakeStore.ResetOperationV2ForResume(s.target); err != nil {
			return nil, err
		}
		snapshot, err := s.fakeStore.GetOperationV2(s.target)
		if err != nil {
			return nil, err
		}
		h.r.DispatchCycleForTest(context.Background())
		select {
		case <-h.runStarted:
		case <-time.After(5 * time.Second):
			return nil, context.DeadlineExceeded
		}
		h.release <- struct{}{}
		deadline := time.Now().Add(5 * time.Second)
		for (h.r.IsRunning(s.target) || s.statusOf(s.target) != "completed") && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		return snapshot, nil
	}
	h.insert("interrupted_quiesced")
	s.armRead.Store(true)

	err := h.r.Cancel(s.target)
	if !errors.Is(err, registry.ErrOpNotFound) {
		t.Fatalf("Cancel = %v, want ErrOpNotFound (the run had already finished)", err)
	}
	if got := s.statusOf(s.target); got != "completed" {
		t.Fatalf("status = %q, want completed (Cancel overwrote a finished row)", got)
	}
}

// TestCancel_StubAlongsideANonQueuedRow covers a dispatcher stub (claimed,
// waiting in nextRun, no context yet) whose row is no longer queued (review
// N1). The worker's restart-strike check writes interrupted_dropped BEFORE it
// looks at the cancel flag, so a flagged stub does not mean the row will end
// canceled. Cancel used to answer success for it; now it answers by the row.
func TestCancel_StubAlongsideANonQueuedRow(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  string
		wantErr error
	}{
		// Dropped by the restart-strike check: nothing ran, but the row is
		// not canceled, so the cancel did not take.
		{name: "dropped row", status: "interrupted_dropped", wantErr: registry.ErrOpNotFound},
		// A concurrent cancel already won: a repeat cancel is not an error.
		{name: "already canceled", status: "canceled", wantErr: nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeStore()
			// Not started: no worker drains nextRun, so the dispatcher's stub
			// stays in place for the whole test.
			r := registry.New(store, slog.Default(), 1, nil)
			def := makeValidDef("test.cancel.stub")
			if err := r.RegisterOp(def); err != nil {
				t.Fatal(err)
			}
			store.insertQueuedAtomic(database.OperationV2Row{
				ID: "op-stub", DefID: def.ID, Plugin: def.Plugin,
				Status: "queued", QueuedAt: time.Now().UTC(),
			})
			r.DispatchCycleForTest(t.Context())
			if !r.IsRunning("op-stub") {
				t.Fatal("the dispatcher did not claim the row")
			}
			now := time.Now().UTC()
			if err := store.UpdateOperationV2Status("op-stub", tc.status, nil, &now, nil); err != nil {
				t.Fatal(err)
			}

			err := r.Cancel("op-stub")
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Cancel = %v, want %v", err, tc.wantErr)
			}
			if got := store.statusOf("op-stub"); got != tc.status {
				t.Fatalf("status = %q, want %q (Cancel must not overwrite a terminal row)", got, tc.status)
			}
		})
	}
}
