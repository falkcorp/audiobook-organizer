// file: internal/operations/registry/context_decorator_test.go
// version: 1.0.1
// guid: 3d9f9e02-6b41-4a6b-9a2a-0e6a1b7f2c5d
// last-edited: 2026-07-11

package registry_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// sdkGuardDecoratorProbeKey is a task-unique context key type (TASK-03,
// SDKGUARD-VIOLATION #1795) used only by the tests in this file to detect
// whether Registry.SetRunContextDecorator's decorator ran before the op's
// Run func was invoked. Named distinctly from any other test-helper key in
// this package to avoid the parallel-test-helper-collision failure mode.
type sdkGuardDecoratorProbeKey struct{}

// TestRunContextDecorator_DecoratesRunContext verifies that a decorator
// wired via SetRunContextDecorator runs before the dispatched op's Run func
// is invoked, and that its return value (a modified context) is the one the
// op actually observes. This is the behavior registry_wire.go depends on to
// preserve SLOG op-ID correlation (logger.WithOperation) now that the
// registry package no longer imports the internal logging package directly.
func TestRunContextDecorator_DecoratesRunContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	store := newFakeStore()
	r := registry.NewWithOptions(store, slog.Default(), 2, registry.Options{})

	var gotOpIDInDecorator string
	var gotValueInRun any
	seenInRun := make(chan struct{})

	r.SetRunContextDecorator(func(ctx context.Context, opID string) context.Context {
		gotOpIDInDecorator = opID
		return context.WithValue(ctx, sdkGuardDecoratorProbeKey{}, "decorated-"+opID)
	})

	def := makeValidDef("test.rundecorator-set")
	def.Run = func(runCtx context.Context, _ json.RawMessage, _ registry.Reporter) error {
		gotValueInRun = runCtx.Value(sdkGuardDecoratorProbeKey{})
		close(seenInRun)
		return nil
	}
	if err := r.RegisterOp(def); err != nil {
		t.Fatalf("RegisterOp: %v", err)
	}
	r.Start(ctx)

	opID, err := r.EnqueueOp(ctx, "test.rundecorator-set", nil)
	if err != nil {
		t.Fatalf("EnqueueOp: %v", err)
	}

	select {
	case <-seenInRun:
	case <-time.After(5 * time.Second):
		t.Fatal("op's Run func was not invoked within 5s")
	}
	awaitStatus(t, store, opID, "completed", 5*time.Second)

	if gotOpIDInDecorator != opID {
		t.Errorf("decorator saw opID=%q, want %q", gotOpIDInDecorator, opID)
	}
	want := "decorated-" + opID
	if gotValueInRun != want {
		t.Errorf("Run observed ctx value %v, want %q — decorator's returned context was not the one passed to Run", gotValueInRun, want)
	}
}

// TestRunContextDecorator_NilDecoratorRunsUndecorated verifies that a
// Registry with no decorator wired (the default — SetRunContextDecorator was
// never called) dispatches runs normally: no panic, and the run context
// carries no decoration.
func TestRunContextDecorator_NilDecoratorRunsUndecorated(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	store := newFakeStore()
	r := registry.NewWithOptions(store, slog.Default(), 2, registry.Options{})

	var gotValueInRun any
	var runErr error
	seenInRun := make(chan struct{})

	def := makeValidDef("test.rundecorator-nil")
	def.Run = func(runCtx context.Context, _ json.RawMessage, _ registry.Reporter) (err error) {
		defer func() {
			if p := recover(); p != nil {
				err = context.DeadlineExceeded // sentinel: any panic fails the test below
				runErr = err
			}
		}()
		gotValueInRun = runCtx.Value(sdkGuardDecoratorProbeKey{})
		close(seenInRun)
		return nil
	}
	if err := r.RegisterOp(def); err != nil {
		t.Fatalf("RegisterOp: %v", err)
	}
	r.Start(ctx)

	opID, err := r.EnqueueOp(ctx, "test.rundecorator-nil", nil)
	if err != nil {
		t.Fatalf("EnqueueOp: %v", err)
	}

	select {
	case <-seenInRun:
	case <-time.After(5 * time.Second):
		t.Fatal("op's Run func was not invoked within 5s (nil decorator should not block dispatch)")
	}
	awaitStatus(t, store, opID, "completed", 5*time.Second)

	if runErr != nil {
		t.Fatalf("Run func panicked with nil decorator wired: %v", runErr)
	}
	if gotValueInRun != nil {
		t.Errorf("Run observed ctx value %v, want nil (no decorator was wired)", gotValueInRun)
	}
}
