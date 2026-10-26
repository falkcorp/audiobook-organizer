// file: internal/merge/serialize_wait_test.go
// version: 1.0.0
// guid: 5d0c7e2a-9b41-4f6e-8a37-c1e4b2d90f53
// last-edited: 2026-10-26

package merge

import (
	"errors"
	"testing"
	"time"
)

// An interactive action in front of a running merge or combine must answer
// ErrMergeBusy within its bound, not block for the combine's whole run.
func TestTryLockMergeRMWWithin_BusyLockAnswersErrMergeBusyWithinTheBound(t *testing.T) {
	LockMergeRMW()
	released := false
	defer func() {
		if !released {
			UnlockMergeRMW()
		}
	}()

	start := time.Now()
	unlock, err := TryLockMergeRMWWithin(60 * time.Millisecond)
	elapsed := time.Since(start)
	if !errors.Is(err, ErrMergeBusy) || unlock != nil {
		t.Fatalf("TryLockMergeRMWWithin with the lock held = (%v, %v), want (nil, ErrMergeBusy)", unlock != nil, err)
	}
	if elapsed < 50*time.Millisecond || elapsed > 2*time.Second {
		t.Fatalf("TryLockMergeRMWWithin waited %v, want about the 60ms bound", elapsed)
	}

	UnlockMergeRMW()
	released = true
	unlock, err = TryLockMergeRMWWithin(time.Second)
	if err != nil {
		t.Fatalf("TryLockMergeRMWWithin on a free lock = %v, want nil", err)
	}
	unlock()
}

// The lock is KEPT, not just checked: a combine that starts right after the
// wait must find it held (that gap is what made a check-only gate still hang
// the request), and the returned unlock must release it.
func TestTryLockMergeRMWWithin_KeepsTheLockUntilUnlocked(t *testing.T) {
	unlock, err := TryLockMergeRMWWithin(time.Second)
	if err != nil {
		t.Fatalf("TryLockMergeRMWWithin = %v", err)
	}
	if mergeSerializeMu.mu.TryLock() {
		mergeSerializeMu.mu.Unlock()
		t.Fatal("the lock was free right after TryLockMergeRMWWithin returned: it only checked")
	}
	unlock()
	if !mergeSerializeMu.mu.TryLock() {
		t.Fatal("the returned unlock did not release the merge lock")
	}
	mergeSerializeMu.mu.Unlock()
}

// A merge that ends inside the wait lets the caller in.
func TestTryLockMergeRMWWithin_WaitsOutAShortMerge(t *testing.T) {
	LockMergeRMW()
	go func() {
		time.Sleep(40 * time.Millisecond)
		UnlockMergeRMW()
	}()
	unlock, err := TryLockMergeRMWWithin(5 * time.Second)
	if err != nil {
		t.Fatalf("TryLockMergeRMWWithin = %v, want nil once the merge ended", err)
	}
	unlock()
}

// The hold is counted (MergeLockAcquisitions), once per call: the actions that
// take it may move file rows, and a counter consumer must see them. A failed
// wait is not a hold.
func TestTryLockMergeRMWWithin_CountsOneHoldPerAcquisitionAndNoneForABusyAnswer(t *testing.T) {
	before := MergeLockAcquisitions()
	unlock, err := TryLockMergeRMWWithin(time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if got := MergeLockAcquisitions() - before; got != 1 {
		t.Fatalf("acquisitions after one TryLockMergeRMWWithin = %d, want 1", got)
	}
	if _, err := TryLockMergeRMWWithin(20 * time.Millisecond); !errors.Is(err, ErrMergeBusy) {
		t.Fatalf("second TryLockMergeRMWWithin = %v, want ErrMergeBusy", err)
	}
	if got := MergeLockAcquisitions() - before; got != 1 {
		t.Fatalf("a busy answer counted a hold: acquisitions = %d, want still 1", got)
	}
	unlock()
}
