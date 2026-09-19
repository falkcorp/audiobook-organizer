// file: internal/operations/registry/shutdown_cause_test.go
// version: 1.0.0
// guid: 9e110f01-b4d6-431d-9e4d-9f0bbec837e5
// last-edited: 2026-09-19

package registry_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// TestShutdownCancelsRunsWithShutdownCause: a running op must be able to tell
// "the server is restarting" from "an operator canceled me". A ResumeRestart op
// that holds paid external work (ai.author-scan's OpenAI batches) must leave it
// running on shutdown and cancel it only on a real cancel. ctx.Err() is
// context.Canceled either way; context.Cause carries the difference.
func TestShutdownCancelsRunsWithShutdownCause(t *testing.T) {
	store := newFakeStore()
	r := registry.New(store, slog.Default(), 1, nil)

	started := make(chan struct{})
	cause := make(chan error, 1)
	def := makeValidDef("test.shutdown-cause")
	def.Run = func(runCtx context.Context, _ json.RawMessage, _ registry.Reporter) error {
		close(started)
		<-runCtx.Done()
		cause <- context.Cause(runCtx)
		return runCtx.Err()
	}
	if err := r.RegisterOp(def); err != nil {
		t.Fatal(err)
	}
	r.Start(t.Context())
	if _, err := r.EnqueueOp(t.Context(), "test.shutdown-cause", nil); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("op never started")
	}

	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = r.Shutdown(sctx)

	select {
	case got := <-cause:
		if !errors.Is(got, registry.ErrShutdown) {
			t.Fatalf("run context cause = %v, want registry.ErrShutdown", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run never observed cancellation")
	}
}
