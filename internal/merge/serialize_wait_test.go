// file: internal/merge/serialize_wait_test.go
// version: 1.0.0
// guid: 5d0c7e2a-9b41-4f6e-8a37-c1e4b2d90f53
// last-edited: 2026-10-22

package merge

import (
	"errors"
	"testing"
	"time"
)

// An interactive action in front of a running merge or combine must answer
// ErrMergeBusy within its bound, not block for the combine's whole run.
func TestWaitMergeIdle_BusyLockAnswersErrMergeBusyWithinTheBound(t *testing.T) {
	LockMergeRMW()
	released := false
	defer func() {
		if !released {
			UnlockMergeRMW()
		}
	}()

	start := time.Now()
	err := WaitMergeIdle(60 * time.Millisecond)
	elapsed := time.Since(start)
	if !errors.Is(err, ErrMergeBusy) {
		t.Fatalf("WaitMergeIdle with the lock held = %v, want ErrMergeBusy", err)
	}
	if elapsed < 50*time.Millisecond || elapsed > 2*time.Second {
		t.Fatalf("WaitMergeIdle waited %v, want about the 60ms bound", elapsed)
	}

	UnlockMergeRMW()
	released = true
	if err := WaitMergeIdle(time.Second); err != nil {
		t.Fatalf("WaitMergeIdle on a free lock = %v, want nil", err)
	}
}

// A merge that ends inside the wait lets the gate through, and the gate
// itself must not leave the lock held (the action takes it next).
func TestWaitMergeIdle_WaitsOutAShortMergeAndReleasesTheLock(t *testing.T) {
	LockMergeRMW()
	go func() {
		time.Sleep(40 * time.Millisecond)
		UnlockMergeRMW()
	}()
	if err := WaitMergeIdle(5 * time.Second); err != nil {
		t.Fatalf("WaitMergeIdle = %v, want nil once the merge ended", err)
	}
	if !mergeSerializeMu.mu.TryLock() {
		t.Fatal("WaitMergeIdle left the merge lock held")
	}
	mergeSerializeMu.mu.Unlock()
}
