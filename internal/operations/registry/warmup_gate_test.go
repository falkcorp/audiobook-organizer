// file: internal/operations/registry/warmup_gate_test.go
// version: 1.0.0
// guid: 3e8c1a74-5b92-4f06-a1d7-0c6b9e2f4a53
// last-edited: 2026-10-09

package registry_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// warmingStore is a fakeStore that reports a startup warmup still in progress
// until release is closed -- the same shape as *database.PebbleStore.
type warmingStore struct {
	*fakeStore
	release chan struct{}
}

func (w *warmingStore) WaitForWarmupCtx(ctx context.Context) error {
	select {
	case <-w.release:
		return nil
	default:
	}
	select {
	case <-w.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// An op that is dispatched while the store is still warming must not run its
// body until warmup finishes, and must run once it does.
func TestWorker_OpBodyWaitsForStartupWarmup(t *testing.T) {
	ctx := t.Context()

	store := &warmingStore{fakeStore: newFakeStore(), release: make(chan struct{})}
	r := registry.New(store, slog.Default(), 1, nil)

	var ran atomic.Bool
	def := makeValidDef("test.w-warmup")
	def.Run = func(_ context.Context, _ json.RawMessage, _ registry.Reporter) error {
		ran.Store(true)
		return nil
	}
	_ = r.RegisterOp(def)
	r.Start(ctx)

	opID, err := r.EnqueueOp(ctx, "test.w-warmup", nil)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	// The worker marks the row running before it gates on warmup.
	awaitStatus(t, store.fakeStore, opID, "running", 5*time.Second)
	time.Sleep(300 * time.Millisecond)
	if ran.Load() {
		t.Fatal("op body ran while the store was still warming")
	}

	close(store.release)
	awaitStatus(t, store.fakeStore, opID, "completed", 5*time.Second)
	if !ran.Load() {
		t.Fatal("op body never ran after warmup finished")
	}
}
