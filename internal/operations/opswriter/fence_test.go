// file: internal/operations/opswriter/fence_test.go
// version: 1.0.0
// guid: 065bbf9a-83d2-4952-a1a0-1d4145b8868a
// last-edited: 2026-10-13

package opswriter

import (
	"context"
	"errors"
	"testing"
)

// A fence bound to the run context refuses as soon as that context is done,
// before (and without) any revoke: the timeout and parent-cancel paths, where
// the registry's revoke can only follow the cancel. Revoked() keeps meaning
// "explicitly revoked". (Moved from internal/operations/registry with the
// fence.)
func TestFence_CheckRefusesOnceTheBoundContextIsDone(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	f, revoke := NewFence(1, ctx.Done())
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
	revoke()
	if !f.Revoked() || !errors.Is(f.Check(), ErrFenced) {
		t.Fatalf("after revoke: revoked=%v check=%v", f.Revoked(), f.Check())
	}

	// An unbound fence is fenced only by revoke, and every copy sees it.
	u, revokeU := NewFence(2, nil)
	copyOfU := u
	if u.Check() != nil || u.Epoch() != 2 {
		t.Fatalf("unbound fence Check() = %v epoch %d", u.Check(), u.Epoch())
	}
	revokeU()
	revokeU() // idempotent
	if !errors.Is(copyOfU.Check(), ErrFenced) {
		t.Fatalf("a copy of a revoked fence Check() = %v, want ErrFenced", copyOfU.Check())
	}

	// The zero Fence is never revoked, and a context without one yields it.
	if (Fence{}).Check() != nil || FenceFromContext(context.Background()).Revoked() {
		t.Fatal("the zero Fence must be a valid permit")
	}
	if got := FenceFromContext(WithFence(context.Background(), u)); !got.Revoked() {
		t.Fatal("WithFence/FenceFromContext lost the fence")
	}
}
