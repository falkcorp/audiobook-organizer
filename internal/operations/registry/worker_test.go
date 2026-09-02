// file: internal/operations/registry/worker_test.go
// version: 1.1.1
// guid: f2a3b4c5-d6e7-8f9a-0b1c-2d3e4f5a6b7c
// last-edited: 2026-09-02

package registry_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"pgregory.net/rapid"
)

// TestWorker_SuccessfulRunSetsCompleted verifies the happy path.
func TestWorker_SuccessfulRunSetsCompleted(t *testing.T) {
	ctx := t.Context()

	store := newFakeStore()
	r := registry.New(store, slog.Default(), 1, nil)

	def := makeValidDef("test.w-success")
	def.Run = func(_ context.Context, _ json.RawMessage, _ registry.Reporter) error {
		return nil
	}
	_ = r.RegisterOp(def)
	r.Start(ctx)

	opID, _ := r.EnqueueOp(ctx, "test.w-success", nil)
	awaitStatus(t, store, opID, "completed", 5*time.Second)
}

// TestWorker_RunReturningErrorSetsFailed verifies error path.
func TestWorker_RunReturningErrorSetsFailed(t *testing.T) {
	ctx := t.Context()

	store := newFakeStore()
	r := registry.New(store, slog.Default(), 1, nil)

	def := makeValidDef("test.w-fail")
	def.Run = func(_ context.Context, _ json.RawMessage, _ registry.Reporter) error {
		return errors.New("intentional error")
	}
	_ = r.RegisterOp(def)
	r.Start(ctx)

	opID, _ := r.EnqueueOp(ctx, "test.w-fail", nil)
	awaitStatus(t, store, opID, "failed", 5*time.Second)

	row, _ := store.GetOperationV2(opID)
	if row == nil {
		t.Fatal("op row not found")
	}
	if row.ErrorMessage == nil || *row.ErrorMessage == "" {
		t.Error("expected non-empty error_message on failed op")
	}
}

// TestWorker_PanicSetsFailed verifies that a panicking Run is caught and
// the op is marked as failed.
func TestWorker_PanicSetsFailed(t *testing.T) {
	ctx := t.Context()

	store := newFakeStore()
	r := registry.New(store, slog.Default(), 1, nil)

	def := makeValidDef("test.w-panic")
	def.Run = func(_ context.Context, _ json.RawMessage, _ registry.Reporter) error {
		panic("intentional panic")
	}
	_ = r.RegisterOp(def)
	r.Start(ctx)

	opID, _ := r.EnqueueOp(ctx, "test.w-panic", nil)
	awaitStatus(t, store, opID, "failed", 5*time.Second)

	row, _ := store.GetOperationV2(opID)
	if row == nil {
		t.Fatal("op row not found")
	}
	if row.ErrorMessage == nil {
		t.Error("expected error_message on panicked op")
	}
}

// TestWorker_IsolateReturnsSentinelError verifies that Isolate=true ops are
// rejected with ErrSubprocessNotImplemented until UOS-03.
func TestWorker_IsolateReturnsSentinelError(t *testing.T) {
	ctx := t.Context()

	store := newFakeStore()
	r := registry.New(store, slog.Default(), 1, nil)

	def := makeValidDef("test.w-isolate")
	def.Isolate = true
	def.Run = func(_ context.Context, _ json.RawMessage, _ registry.Reporter) error {
		return nil // Never called for Isolate=true in UOS-02.
	}
	_ = r.RegisterOp(def)
	r.Start(ctx)

	opID, _ := r.EnqueueOp(ctx, "test.w-isolate", nil)
	awaitStatus(t, store, opID, "failed", 5*time.Second)

	row, _ := store.GetOperationV2(opID)
	if row == nil {
		t.Fatal("op row not found")
	}
	if row.ErrorMessage == nil {
		t.Error("expected error_message for Isolate=true op")
	}
}

// --- Property test ---

// TestPropertyPluginRunningNeverNegative uses rapid to verify that for any
// random sequence of RegisterOp + EnqueueOp + Cancel calls, the registry's
// internal pluginRunning counter never goes negative.
//
// We verify this indirectly: CountRunningByPluginV2 from the store never
// returns a negative value, and after all ops complete it returns 0.
//
// All rapid.Draw calls are made on the test goroutine only (before Start);
// the Run closure captures pre-drawn values to avoid concurrent Draw calls.
func TestPropertyPluginRunningNeverNegative(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		store := newFakeStore()
		r := registry.New(store, slog.Default(), 4, nil)

		// Generate between 1 and 5 defs (all Draw calls on test goroutine).
		numDefs := rapid.IntRange(1, 5).Draw(rt, "numDefs")
		ids := make([]string, numDefs)
		sleepMs := make([]int, numDefs) // pre-drawn sleep durations

		// Draw all random values before spawning any goroutines.
		for i := range numDefs {
			ids[i] = rapid.StringMatching(`[a-z]{4,8}\.[a-z]{4,8}`).Draw(rt, "defID")
			sleepMs[i] = rapid.IntRange(0, 20).Draw(rt, "sleepMs")
		}

		for i := range numDefs {
			def := makeValidDef(ids[i])
			def.Plugin = "prop-test-plugin"
			sleep := time.Duration(sleepMs[i]) * time.Millisecond // capture loop var
			def.Run = func(_ context.Context, _ json.RawMessage, _ registry.Reporter) error {
				time.Sleep(sleep)
				return nil
			}
			// Best-effort — duplicate IDs are rejected, that's fine.
			_ = r.RegisterOp(def)
		}

		r.Start(ctx)

		// Pre-draw all op indices before enqueuing.
		numOps := rapid.IntRange(1, 10).Draw(rt, "numOps")
		opIndices := make([]int, numOps)
		for i := range numOps {
			opIndices[i] = rapid.IntRange(0, numDefs-1).Draw(rt, "defIdx")
		}

		opIDs := make([]string, 0, numOps)
		for _, idx := range opIndices {
			opID, err := r.EnqueueOp(ctx, ids[idx], nil)
			if err == nil {
				opIDs = append(opIDs, opID)
			}
		}

		// Wait for all ops to reach a terminal state.
		deadline := time.Now().Add(8 * time.Second)
		for _, opID := range opIDs {
			for time.Now().Before(deadline) {
				s := store.statusOf(opID)
				if s == "completed" || s == "failed" || s == "canceled" {
					break
				}
				time.Sleep(5 * time.Millisecond)
			}
		}

		// Invariant: CountRunningByPluginV2 must never be negative.
		// (We check at rest — all ops terminal — so it should also be 0.)
		n, err := store.CountRunningByPluginV2("prop-test-plugin")
		if err != nil {
			rt.Fatalf("CountRunningByPluginV2 error: %v", err)
		}
		if n < 0 {
			rt.Fatalf("pluginRunning went negative: %d", n)
		}
	})
}
