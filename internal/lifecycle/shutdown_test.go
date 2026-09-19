// file: internal/lifecycle/shutdown_test.go
// version: 1.0.0
// guid: fe14168b-ac13-4a03-a923-b7a48419239e
// last-edited: 2026-09-19

package lifecycle

import (
	"context"
	"errors"
	"testing"
)

// Code that returns context.Cause(ctx) after a cancel (the scan stand-down
// checkpoint, the organizer) is checked by callers with
// errors.Is(err, context.Canceled). A shutdown cause must still satisfy that.
func TestShutdownCauseIsStillACancel(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(ErrShutdown)
	if !errors.Is(context.Cause(ctx), context.Canceled) {
		t.Fatal("shutdown cause must wrap context.Canceled")
	}
	if !IsShutdown(ctx) {
		t.Fatal("IsShutdown must recognize the shutdown cause")
	}
	plain, stop := context.WithCancel(context.Background())
	stop()
	if IsShutdown(plain) {
		t.Fatal("a plain cancel is not a shutdown")
	}
}
