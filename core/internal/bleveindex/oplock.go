// file: core/internal/bleveindex/oplock.go
// version: 1.0.0
// guid: 32f83330-6d24-4eda-8f8f-c4d41b5f8a31
// last-edited: 2026-11-11

package bleveindex

import "context"

// opLock is an index's writer lock: a one-slot semaphore, so that unlike a
// sync.Mutex a waiter can give up when its context ends (CopyTo and the rebuild
// task wait for a running reconcile pass or rebuild), while the reconciler keeps
// its non-blocking try.
type opLock struct{ slot chan struct{} }

func newOpLock() opLock { return opLock{slot: make(chan struct{}, 1)} }

// Lock waits for the lock or for ctx, whichever comes first. An already ended
// context never takes the lock, even if it is free.
func (l opLock) Lock(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return context.Cause(ctx)
	}
	select {
	case l.slot <- struct{}{}:
		return nil
	case <-ctx.Done():
		return context.Cause(ctx)
	}
}

// TryLock takes the lock if it is free.
func (l opLock) TryLock() bool {
	select {
	case l.slot <- struct{}{}:
		return true
	default:
		return false
	}
}

// Unlock releases the lock. It must be held.
func (l opLock) Unlock() { <-l.slot }
