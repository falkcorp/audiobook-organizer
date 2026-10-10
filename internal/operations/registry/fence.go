// file: internal/operations/registry/fence.go
// version: 1.1.0
// guid: 284e13c1-70dc-4bab-909c-687f3fe04646
// last-edited: 2026-10-10

package registry

import (
	"context"
	"errors"
	"sync/atomic"
)

// ErrFenced is returned by every write path of a run whose fence was revoked
// (user cancel, timeout, watchdog kill, quiesce, shutdown, abandonment, lost
// stand-down lease). The write it guards was not made.
var ErrFenced = errors.New("registry: run is fenced (canceled, abandoned or lease lost); write refused")

// fenceState is shared by every copy of one Fence.
type fenceState struct {
	epoch   atomic.Uint64
	revoked atomic.Bool
	// done is the run context's Done channel (nil when unbound). Check
	// refuses once it is closed, whether or not revoke has run yet.
	done <-chan struct{}
}

// Fence is a revocable write permit for one run attempt. The registry creates
// one per attempt (executeRun), puts it on the run's context, and revokes it
// BEFORE it cancels that context on every stop path, so a goroutine that
// checks the fence after it sees ctx.Done() already sees it revoked. A
// canceled context only asks a run to stop; the fence is what makes a run
// that does not stop (a zombie) unable to write through a fenced writer.
//
// Check also refuses once the run context it is bound to is done. That closes
// the stop paths no registry code initiates -- the def Timeout firing inside
// context.WithTimeout, or the parent context ending -- where the revoke can
// only follow the cancel: the run can no longer see ctx.Done() and still pass
// Check. Revoked() reports only the explicit revoke.
//
// The zero Fence (no registry, tests, direct callers) is never revoked.
//
// This file deliberately imports nothing registry-private: 05-PR5 moves it to
// internal/operations/opswriter as a rename.
type Fence struct{ st *fenceState }

// newFence returns a valid (unrevoked) fence for the given attempt epoch,
// bound to done: the run context's Done channel, after which Check refuses.
// A nil done binds nothing (only revoke fences it).
func newFence(epoch uint64, done <-chan struct{}) Fence {
	st := &fenceState{done: done}
	st.epoch.Store(epoch)
	return Fence{st: st}
}

// Epoch returns the attempt epoch the fence was created for; 0 for the zero
// Fence.
func (f Fence) Epoch() uint64 {
	if f.st == nil {
		return 0
	}
	return f.st.epoch.Load()
}

// Revoked reports whether the permit has been explicitly revoked. It does not
// look at the bound context (Check does). Always false for the zero Fence.
func (f Fence) Revoked() bool {
	return f.st != nil && f.st.revoked.Load()
}

// Check returns nil while the permit is valid, and ErrFenced once it is
// revoked or its bound run context is done.
func (f Fence) Check() error {
	if f.st == nil {
		return nil
	}
	if f.st.revoked.Load() {
		return ErrFenced
	}
	select {
	case <-f.st.done: // a nil channel never fires: unbound fences skip this
		return ErrFenced
	default:
		return nil
	}
}

// revoke withdraws the permit. Idempotent, and a no-op on the zero Fence
// (dispatcher stub handles carry one). Unexported: only the registry revokes.
func (f Fence) revoke() {
	if f.st != nil {
		f.st.revoked.Store(true)
	}
}

type fenceKey struct{}

// WithFence returns ctx carrying f.
func WithFence(ctx context.Context, f Fence) context.Context {
	return context.WithValue(ctx, fenceKey{}, f)
}

// FenceFromContext returns the run's fence, or the zero Fence when absent.
func FenceFromContext(ctx context.Context) Fence {
	if ctx == nil {
		return Fence{}
	}
	f, _ := ctx.Value(fenceKey{}).(Fence)
	return f
}
