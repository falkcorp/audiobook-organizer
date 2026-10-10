// file: internal/operations/registry/fence.go
// version: 1.0.0
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
}

// Fence is a revocable write permit for one run attempt. The registry creates
// one per attempt (executeRun), puts it on the run's context, and revokes it
// BEFORE it cancels that context on every stop path, so a goroutine that
// checks the fence after it sees ctx.Done() already sees it revoked. A
// canceled context only asks a run to stop; the fence is what makes a run
// that does not stop (a zombie) unable to write through a fenced writer.
//
// The zero Fence (no registry, tests, direct callers) is never revoked.
//
// This file deliberately imports nothing registry-private: 05-PR5 moves it to
// internal/operations/opswriter as a rename.
type Fence struct{ st *fenceState }

// newFence returns a valid (unrevoked) fence for the given attempt epoch.
func newFence(epoch uint64) Fence {
	st := &fenceState{}
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

// Revoked reports whether the permit has been revoked. Always false for the
// zero Fence.
func (f Fence) Revoked() bool {
	return f.st != nil && f.st.revoked.Load()
}

// Check returns nil while the permit is valid and ErrFenced after revoke.
func (f Fence) Check() error {
	if f.Revoked() {
		return ErrFenced
	}
	return nil
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
