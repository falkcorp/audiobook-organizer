// file: internal/operations/registry/fence_internal_test.go
// version: 1.0.0
// guid: c8fe3c17-3eeb-4b19-b528-3b405e193472
// last-edited: 2026-10-10

package registry

import (
	"context"
	"errors"
	"testing"
)

// TestCancelPaths_RevokeBeforeCancel pins the core fence invariant without
// timing: every handle cancel method withdraws the write permit BEFORE it
// cancels the run's context, so a run that checks the fence right after it
// sees ctx.Done() is already refused. The cancel closures record the fence
// state at the moment they are called, synchronously, so the assertion does
// not race executeRun's own revoke in its select (which is what made the
// end-to-end TestFence_RevokedOnEveryStopPath catch a missing revoke only by
// chance).
//
// Every code-initiated stop path goes through one of these methods:
//   - cancelIfActive: Registry.Cancel, the watchdog kill, the stand-down
//     quiesce (AcquireScanStandDown);
//   - cancelWithCause: Shutdown (ErrShutdown).
//
// Mutation check: delete h.fence.revoke() from cancelIfActive and the
// cancelIfActive case fails; delete it from cancelWithCause and the
// "with cause func" case fails (the nil-cause case falls back to
// cancelIfActive, which revokes on its own, and is listed so that fallback
// stays covered too).
func TestCancelPaths_RevokeBeforeCancel(t *testing.T) {
	cases := []struct {
		name string
		// wire installs recording cancel closures on h; record is called
		// from inside the cancel with the fence state it observed.
		wire func(h *runHandle, record func())
		stop func(h *runHandle)
	}{
		{
			name: "cancelIfActive",
			wire: func(h *runHandle, record func()) { h.cancel = record },
			stop: func(h *runHandle) { h.cancelIfActive() },
		},
		{
			name: "cancelWithCause with cause func",
			wire: func(h *runHandle, record func()) {
				h.cancel = func() { t.Error("cancelWithCause used cancel although cancelCause is set") }
				h.cancelCause = func(error) { record() }
			},
			stop: func(h *runHandle) { h.cancelWithCause(ErrShutdown) },
		},
		{
			name: "cancelWithCause without cause func",
			wire: func(h *runHandle, record func()) { h.cancel = record },
			stop: func(h *runHandle) { h.cancelWithCause(ErrShutdown) },
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := &runHandle{fence: newFence(1, nil)}
			calls := 0
			revokedAtCancel := false
			tc.wire(h, func() {
				calls++
				revokedAtCancel = h.fence.Revoked()
			})
			tc.stop(h)
			if calls != 1 {
				t.Fatalf("cancel called %d times, want 1", calls)
			}
			if !revokedAtCancel {
				t.Fatal("the context was canceled before the fence was revoked")
			}
			if !errors.Is(h.fence.Check(), ErrFenced) {
				t.Fatalf("Check() after stop = %v, want ErrFenced", h.fence.Check())
			}
		})
	}
}

// A stub handle (no cancel funcs, zero Fence) must not panic and stays a
// no-op on both cancel methods.
func TestCancelPaths_StubHandleIsNoop(t *testing.T) {
	h := &runHandle{}
	h.cancelIfActive()
	h.cancelWithCause(ErrShutdown)
	if h.fence.Revoked() || h.fence.Check() != nil {
		t.Fatalf("zero fence on a stub: revoked=%v check=%v", h.fence.Revoked(), h.fence.Check())
	}
}

// A fence bound to the run context refuses as soon as that context is done,
// before (and without) any revoke: the timeout and parent-cancel paths, where
// the registry's revoke can only follow the cancel. Revoked() keeps meaning
// "explicitly revoked".
func TestFence_CheckRefusesOnceTheBoundContextIsDone(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	f := newFence(1, ctx.Done())
	if err := f.Check(); err != nil {
		t.Fatalf("Check() on a live context = %v, want nil", err)
	}
	cancel()
	if !errors.Is(f.Check(), ErrFenced) {
		t.Fatalf("Check() after the bound context ended = %v, want ErrFenced", f.Check())
	}
	if f.Revoked() {
		t.Fatal("Revoked() reports a context end; it must report only the explicit revoke")
	}
	f.revoke()
	if !f.Revoked() || !errors.Is(f.Check(), ErrFenced) {
		t.Fatalf("after revoke: revoked=%v check=%v", f.Revoked(), f.Check())
	}

	// An unbound fence is fenced only by revoke.
	u := newFence(1, nil)
	if u.Check() != nil {
		t.Fatalf("unbound fence Check() = %v, want nil", u.Check())
	}
	u.revoke()
	if !errors.Is(u.Check(), ErrFenced) {
		t.Fatalf("unbound fence after revoke Check() = %v, want ErrFenced", u.Check())
	}
}
