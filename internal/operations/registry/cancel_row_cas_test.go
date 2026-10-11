// file: internal/operations/registry/cancel_row_cas_test.go
// version: 1.4.0
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

	// onCancelCAS runs on the first UpdateOperationV2StatusIf(target,
	// "queued", "canceled", ...) after armCancelCAS is set, wrapped around the real write:
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

	// onGateWrite, if set, runs once just before the first write of
	// interrupted_quiesced to the target, on the writing goroutine (instead
	// of the gateMode hold).
	onGateWrite     func()
	onGateWriteOnce sync.Once

	// failCancelCAS / failRead make the next UpdateOperationV2StatusIf(target,
	// "queued", "canceled", ...) / GetOperationV2(target) fail with
	// errStoreFlake.
	failCancelCAS atomic.Bool
	failRead      atomic.Bool
	// failCancelIf makes the next UpdateOperationV2StatusIf(target, from,
	// "canceled", ...) with from other than "queued" fail with errStoreFlake.
	failCancelIf atomic.Bool

	// onFinalWrite, if set, runs once just before the first
	// UpdateOperationV2StatusIf(target, "running", finalWriteStatus, ...): the
	// worker's final write of a run's outcome.
	onFinalWrite     func()
	finalWriteStatus string
	onFinalWriteOnce sync.Once
}

var errStoreFlake = errors.New("store flake (injected)")

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
	if id == s.target && s.failRead.CompareAndSwap(true, false) {
		return nil, errStoreFlake
	}
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

// holdGateWrite wraps the worker's pickup-gate write (the only writer of
// interrupted_quiesced to the target in these tests).
func (s *interleaveStore) holdGateWrite(id, status string, write func()) {
	if id == s.target && status == "interrupted_quiesced" && s.onGateWrite != nil {
		s.onGateWriteOnce.Do(s.onGateWrite)
		write()
		return
	}
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
	// Cancel's compare-and-set from "queued" (the lock-free attempt and
	// cancelByRow's queued case).
	if id == s.target && fromStatus == "queued" && status == "canceled" {
		if s.failCancelCAS.CompareAndSwap(true, false) {
			return false, errStoreFlake
		}
		if s.armCancelCAS.CompareAndSwap(true, false) {
			if s.beforeCAS != nil {
				s.beforeCAS()
			}
			ok, err := s.fakeStore.UpdateOperationV2StatusIf(id, fromStatus, status, startedAt, completedAt, errMsg)
			if s.afterCAS != nil {
				s.afterCAS()
			}
			return ok, err
		}
	}
	if id == s.target && fromStatus != "queued" && status == "canceled" && s.failCancelIf.CompareAndSwap(true, false) {
		return false, errStoreFlake
	}
	if id == s.target && fromStatus == "running" && s.onFinalWrite != nil && status == s.finalWriteStatus {
		s.onFinalWriteOnce.Do(s.onFinalWrite)
	}
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

// TestCancel_BetweenTheRunsFinalStatusAndItsWriteKeepsTheRowCanceled covers
// the window between a run deciding its final status and writing it. A scan
// quiesced by a stand-down returns, its worker releases the handle and decides
// interrupted_quiesced; a cancel landing then finds no handle, reads "running"
// and writes "canceled". The worker's terminal write was unconditional, so it
// replaced "canceled" with interrupted_quiesced and the stand-down release
// re-queued the scan the user had canceled.
func TestCancel_BetweenTheRunsFinalStatusAndItsWriteKeepsTheRowCanceled(t *testing.T) {
	h := newCancelHarness(t, registry.ScanStandDownDefIDForTest, registry.ResumeRestart)
	s := h.store
	var cancelErr error
	canceled := make(chan struct{})
	s.onGateWrite = func() {
		cancelErr = h.r.Cancel(s.target)
		close(canceled)
	}
	h.insert("queued")
	h.r.DispatchCycleForTest(t.Context())
	recv(t, h.runStarted, "the scan to start")

	release, err := h.r.AcquireScanStandDown(t.Context(), "holder-test", "test")
	if err != nil {
		t.Fatalf("AcquireScanStandDown: %v", err)
	}
	recv(t, canceled, "the cancel inside the worker's terminal write")
	if cancelErr != nil {
		t.Fatalf("Cancel: %v", cancelErr)
	}
	h.awaitReleased(t)
	awaitStatus(t, s.fakeStore, s.target, "canceled", 5*time.Second)
	release()
	h.r.DispatchCycleForTest(t.Context())
	time.Sleep(300 * time.Millisecond)
	if got := h.runs.Load(); got != 1 {
		t.Fatalf("Run called %d times, want 1: the canceled scan was resumed (status %q)", got, s.statusOf(s.target))
	}
	if got := s.statusOf(s.target); got != "canceled" {
		t.Fatalf("final status = %q, want canceled", got)
	}
}

// TestCancel_StoppedLiveRunSucceedsDespiteAStoreError: once Cancel has stopped
// a live run (revoked its fence, set userCanceled, canceled its context), the
// cancel has happened. A transient store error on the row follow-up must not
// turn that into a failed cancel or a 404.
func TestCancel_StoppedLiveRunSucceedsDespiteAStoreError(t *testing.T) {
	for _, tc := range []struct {
		name string
		arm  func(s *interleaveStore)
	}{
		{name: "compare-and-set fails", arm: func(s *interleaveStore) { s.failCancelCAS.Store(true) }},
		{name: "row read fails", arm: func(s *interleaveStore) { s.failRead.Store(true) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newCancelHarness(t, "test.cancel.store-error", registry.ResumeDrop)
			s := h.store
			h.insert("queued")
			h.r.DispatchCycleForTest(t.Context())
			recv(t, h.runStarted, "the run to start")
			awaitStatus(t, s.fakeStore, s.target, "running", 5*time.Second)

			tc.arm(s)
			if err := h.r.Cancel(s.target); err != nil {
				t.Fatalf("Cancel = %v, want nil: the live run was already stopped", err)
			}
			recv(t, h.ctxDone, "the run's context to be canceled")
			awaitStatus(t, s.fakeStore, s.target, "canceled", 5*time.Second)
		})
	}
}

// TestCancel_StubDroppedByTheDispatcherPublishesOpTerminal: Cancel flags a
// dispatcher stub and cancels its queued row, and leaves op.terminal to the
// worker that drops the stub. If the dispatcher's post-claim re-read sees the
// canceled row first, it releases the claim and no worker ever sees the run,
// so nobody published: the UI bell showed the op as running.
func TestCancel_StubDroppedByTheDispatcherPublishesOpTerminal(t *testing.T) {
	store := newInterleaveStore("op-stub-drop")
	bus := &recordingBus{}
	// Not started: the dispatcher pass below is the only one.
	r := registry.New(store, slog.Default(), 1, nil)
	r.SetBus(bus)
	def := makeValidDef("test.cancel.stub-drop")
	if err := r.RegisterOp(def); err != nil {
		t.Fatal(err)
	}
	store.insertQueuedAtomic(database.OperationV2Row{
		ID: store.target, DefID: def.ID, Plugin: def.Plugin,
		Status: "queued", QueuedAt: time.Now().UTC(),
	})
	// The dispatcher's re-read is the first read of the row: cancel there,
	// after the stub is published and before the re-read returns.
	var cancelErr error
	store.onRead = func() (*database.OperationV2Row, error) {
		cancelErr = r.Cancel(store.target)
		return store.fakeStore.GetOperationV2(store.target)
	}
	store.armRead.Store(true)

	r.DispatchCycleForTest(t.Context())
	if cancelErr != nil {
		t.Fatalf("Cancel: %v", cancelErr)
	}
	if r.IsRunning(store.target) {
		t.Fatal("the dispatcher kept its claim on a canceled row")
	}
	if got := store.statusOf(store.target); got != "canceled" {
		t.Fatalf("status = %q, want canceled", got)
	}
	bus.mu.Lock()
	defer bus.mu.Unlock()
	n := 0
	for _, ev := range bus.events {
		if p, ok := ev.payload.(map[string]any); ok && ev.name == "op.terminal" && p["op_id"] == store.target {
			if p["status"] != "canceled" {
				t.Fatalf("op.terminal status = %v, want canceled", p["status"])
			}
			n++
		}
	}
	if n != 1 {
		t.Fatalf("op.terminal published %d times for the dropped stub, want 1", n)
	}
}

// TestCancel_PickupGateHonorsAUserCancelItsOwnWriteRaced pins the worker's
// userCanceled re-check after an interrupted_* write. The pickup gate has
// written interrupted_quiesced with its full handle registered; Cancel takes
// that handle for a live run (sets userCanceled) and its row follow-up fails,
// so Cancel does not write the row (and reports the error, since no run had
// started). The worker must still record "canceled"
// from the flag, and the stand-down release must not resume the scan.
//
// Mutation check: drop the settleInterrupted call in the pickup
// gate and the row stays interrupted_quiesced and the scan runs.
func TestCancel_PickupGateHonorsAUserCancelItsOwnWriteRaced(t *testing.T) {
	h := newCancelHarness(t, registry.ScanStandDownDefIDForTest, registry.ResumeRestart)
	s := h.store
	s.gateMode = gateWriteAfter
	s.raiseGate.Store(true)
	h.insert("queued")
	h.r.DispatchCycleForTest(t.Context())
	recv(t, s.gateArrived, "the pickup gate's write")

	s.failRead.Store(true)
	err := h.r.Cancel(s.target)
	// Checked while the worker is still held after its write, so only Cancel
	// could have written "canceled" by now.
	got := s.statusOf(s.target)
	s.proceed()
	// The handle had not claimed its row, so no run was stopped and the store
	// error is Cancel's to report (cancelStoreError). The flag it set still
	// reaches the worker.
	if !errors.Is(err, errStoreFlake) {
		t.Fatalf("Cancel = %v, want the injected store error", err)
	}
	if got == "canceled" {
		t.Fatal("Cancel wrote the row itself; the store flake did not happen, so the worker's re-check is untested")
	}
	h.assertStaysCanceledAfterGateRelease(t)
}

// TestCancel_InTheReleaseGapOfACompletedRunKeepsCompleted is review round 2's
// B1. A run returns nil (its work is done), its worker releases the handle and
// decides "completed"; a cancel landing before the final write finds no
// handle, reads "running" and writes "canceled". The worker used to keep that
// "canceled": completed work was recorded, published and counted as canceled,
// and the deps scheduler failed every dependent waiting on it. A run that
// finished keeps its real outcome.
func TestCancel_InTheReleaseGapOfACompletedRunKeepsCompleted(t *testing.T) {
	h := newCancelHarness(t, "test.cancel.release-gap", registry.ResumeDrop)
	s := h.store
	bus := &recordingBus{}
	h.r.SetBus(bus)
	var cancelErr error
	canceled := make(chan struct{})
	s.finalWriteStatus = "completed"
	s.onFinalWrite = func() {
		cancelErr = h.r.Cancel(s.target)
		close(canceled)
	}
	h.insert("queued")
	h.r.DispatchCycleForTest(t.Context())
	recv(t, h.runStarted, "the run to start")
	h.release <- struct{}{}
	recv(t, canceled, "the cancel inside the worker's final write")
	if cancelErr != nil {
		t.Fatalf("Cancel = %v, want nil (a cancel racing completion is not an error)", cancelErr)
	}
	h.awaitReleased(t)
	awaitStatus(t, s.fakeStore, s.target, "completed", 5*time.Second)
	if got := s.statusOf(s.target); got != "completed" {
		t.Fatalf("final status = %q, want completed: the run's work completed", got)
	}
	// Review round 3 S2: a nil message means "leave unchanged", so the row
	// kept the cancel's message under a completed status.
	if row, _ := s.fakeStore.GetOperationV2(s.target); row.ErrorMessage != nil {
		t.Fatalf("completed row keeps error_message %q from the cancel", *row.ErrorMessage)
	}
	// The last op.terminal the UI sees must be the real outcome.
	deadline := time.Now().Add(5 * time.Second)
	for {
		bus.mu.Lock()
		last := ""
		for _, ev := range bus.events {
			if p, ok := ev.payload.(map[string]any); ok && ev.name == "op.terminal" && p["op_id"] == s.target {
				last, _ = p["status"].(string)
			}
		}
		bus.mu.Unlock()
		if last == "completed" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("last op.terminal = %q, want completed", last)
		}
		time.Sleep(time.Millisecond)
	}
}

// TestCancel_StoreErrorWithNoStoppedRunIsReturned pins the other half of
// cancelStoreError: when this call stopped no running run, a store error is
// the cancel's error. Swallowing it answered 200 for a row that stays
// resumable, and the stand-down release or the next boot resumed the scan the
// user was told was canceled.
func TestCancel_StoreErrorWithNoStoppedRunIsReturned(t *testing.T) {
	t.Run("no handle, quiesced row, the cancel write fails", func(t *testing.T) {
		h := newCancelHarness(t, "test.cancel.store-error-nohandle", registry.ResumeRestart)
		s := h.store
		h.insert("interrupted_quiesced")
		s.failCancelIf.Store(true)
		err := h.r.Cancel(s.target)
		if !errors.Is(err, errStoreFlake) {
			t.Fatalf("Cancel = %v, want an error wrapping the store failure", err)
		}
		if got := s.statusOf(s.target); got != "interrupted_quiesced" {
			t.Fatalf("status = %q, want interrupted_quiesced (unchanged)", got)
		}
	})
	t.Run("handle registered at the pickup gate, run never started", func(t *testing.T) {
		// A full handle is registered before the claim, so finding one does
		// not mean a run was stopped (review N4): its error is returned.
		h := newCancelHarness(t, registry.ScanStandDownDefIDForTest, registry.ResumeRestart)
		s := h.store
		s.gateMode = gateWriteBefore
		s.raiseGate.Store(true)
		h.insert("queued")
		h.r.DispatchCycleForTest(t.Context())
		recv(t, s.gateArrived, "the worker to reach the pickup gate")
		s.failCancelCAS.Store(true)
		err := h.r.Cancel(s.target)
		s.proceed()
		if !errors.Is(err, errStoreFlake) {
			t.Fatalf("Cancel = %v, want an error wrapping the store failure (no run was stopped)", err)
		}
	})
}

// opTerminalStatuses lists the statuses of every op.terminal published for id,
// in order.
func opTerminalStatuses(bus *recordingBus, id string) []string {
	bus.mu.Lock()
	defer bus.mu.Unlock()
	var out []string
	for _, ev := range bus.events {
		if p, ok := ev.payload.(map[string]any); ok && ev.name == "op.terminal" && p["op_id"] == id {
			st, _ := p["status"].(string)
			out = append(out, st)
		}
	}
	return out
}

// TestShutdown_StampedCanceledRunPublishesOpTerminal is review round 3's S1.
// The user cancels a slow run (Cancel sets userCanceled and leaves the row to
// the run); Shutdown times out while the run is still unwinding and stamps the
// row "canceled"; the run then returns inside its grace. The worker found
// "canceled", kept it, and published nothing on the assumption that Cancel
// had -- but the stamp does not publish either, so the op's legacy twin and
// the UI bell never heard the op had ended.
func TestShutdown_StampedCanceledRunPublishesOpTerminal(t *testing.T) {
	store := newFakeStore()
	bus := &recordingBus{}
	r := registry.NewWithOptions(store, slog.Default(), 2, registry.Options{
		AbandonGrace: 10 * time.Second, // the run returns inside it
		Bus:          bus,
	})
	started := make(chan struct{})
	release := make(chan struct{})
	def := makeValidDef("test.cancel.shutdown-stamp")
	def.ResumePolicy = registry.ResumeRestart
	def.Run = func(ctx context.Context, _ json.RawMessage, _ registry.Reporter) error {
		close(started)
		<-release // ignores ctx: still unwinding when Shutdown times out
		return ctx.Err()
	}
	if err := r.RegisterOp(def); err != nil {
		t.Fatal(err)
	}
	r.Start(t.Context())
	id, err := r.EnqueueOp(t.Context(), def.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	recv(t, started, "the run to start")
	awaitStatus(t, store, id, "running", 5*time.Second)
	if err := r.Cancel(id); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if got := store.statusOf(id); got != "running" {
		t.Fatalf("status after Cancel = %q, want running (Cancel leaves a live run's row to the run)", got)
	}

	// Release the run once Shutdown's stamp has landed.
	go func() {
		deadline := time.Now().Add(10 * time.Second)
		for store.statusOf(id) != "canceled" && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		close(release)
	}()
	sctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_ = r.Shutdown(sctx)

	if got := store.statusOf(id); got != "canceled" {
		t.Fatalf("final status = %q, want canceled", got)
	}
	got := opTerminalStatuses(bus, id)
	if len(got) == 0 {
		t.Fatal("no op.terminal published for a run that ended canceled")
	}
	for _, st := range got {
		if st != "canceled" {
			t.Fatalf("op.terminal statuses = %v, want only canceled", got)
		}
	}
}

// TestCancel_WorkerDoesNotPublishAStaleInterruptedStatus pins
// settleInterrupted's re-read (review round 2 N3, round 3 N1). The pickup gate
// writes interrupted_quiesced and is held; Cancel moves that row to "canceled"
// and publishes it; the worker then must not publish its now-stale
// interrupted_quiesced over Cancel's event.
//
// Mutation check: make the re-read in settleInterrupted never match and the
// worker publishes interrupted_quiesced after "canceled".
func TestCancel_WorkerDoesNotPublishAStaleInterruptedStatus(t *testing.T) {
	h := newCancelHarness(t, registry.ScanStandDownDefIDForTest, registry.ResumeRestart)
	s := h.store
	bus := &recordingBus{}
	h.r.SetBus(bus)
	s.gateMode = gateWriteAfter
	s.raiseGate.Store(true)
	h.insert("queued")
	h.r.DispatchCycleForTest(t.Context())
	recv(t, s.gateArrived, "the pickup gate's write")

	if err := h.r.Cancel(s.target); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if got := s.statusOf(s.target); got != "canceled" {
		t.Fatalf("status after Cancel = %q, want canceled", got)
	}
	s.proceed()
	h.assertStaysCanceledAfterGateRelease(t)
	got := opTerminalStatuses(bus, s.target)
	if len(got) == 0 {
		t.Fatal("no op.terminal published")
	}
	for _, st := range got {
		if st == "interrupted_quiesced" {
			t.Fatalf("op.terminal statuses = %v: the worker published a stale interrupted_quiesced over the cancel", got)
		}
	}
}
