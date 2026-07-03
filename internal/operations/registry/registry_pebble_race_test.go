// file: internal/operations/registry/registry_pebble_race_test.go
// version: 1.0.0
// guid: 3f8a1c72-9d4e-4b06-8a53-1e7c2b9d0f45
// last-edited: 2026-07-03

// registry_pebble_race_test.go is the regression test for BUG-2 (TASK-19,
// consultancy roadmap): Registry.Shutdown did not durably wait for the per-op
// safeRun goroutine spawned in worker.go's executeRun.
//
// Failure mode (pre-fix): an op whose Run ignores ctx cancellation is
// classified as abandoned during shutdown. The shutdown ctx expires during the
// drain poll, Shutdown marks the op interrupted, cancels its internal context,
// and reaches the final goroutineWG.Wait(). The worker goroutines ARE enrolled
// in goroutineWG and exit quickly (after abandonGrace), so Wait() returns —
// but the safeRun goroutine was NOT enrolled, so nothing waited for it.
// Shutdown returned while plugin code was still writing to the store, and the
// caller (cmd/root.go's deferred closeStore) closed the store underneath it.
//
// Note on the assertion style: a naive "close the store and let the straggler
// write panic" repro (like shutdown_race_test.go uses for the dep-notify
// goroutines) does NOT work here, because the straggler write happens inside
// def.Run, which executes under safeRun's panic recovery — the "pebble:
// closed" panic is recovered, converted to an error, and silently drained by
// the abandoned-run monitor goroutine. So instead this test asserts the
// ordering invariant directly, against a REAL PebbleStore: by the time
// Shutdown returns, the abandoned run goroutine must have finished, and its
// store write must have succeeded against a still-open store. Pre-fix this
// fails deterministically (Shutdown returns ~abandonGrace after cancel, long
// before the 400ms write); post-fix (safeRun enrolled in goroutineWG)
// Shutdown's final Wait covers the goroutine and the write happens-before
// store.Close().
package registry_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

func TestShutdownWaitsForAbandonedRunGoroutine_RealStore(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	// Repeat the start/run/shutdown/close cycle to widen the window; scheduler
	// jitter under -race means a single iteration proves less.
	for iter := range 5 {
		store, cleanup := openTestPebbleStore(t)

		reg := registry.NewWithOptions(store, logger, 2, registry.Options{
			WatchdogInterval: 30 * time.Second,      // keep the watchdog out of the way
			AbandonedCap:     10,                    // don't block dispatch
			AbandonGrace:     20 * time.Millisecond, // classify-as-abandoned quickly
			SweepInterval:    time.Hour,             // keep SweepTick out of the way
		})

		started := make(chan struct{})
		var runFinished atomic.Bool
		var writeErr atomic.Value // error from the post-cancel store write

		def := makeValidDef("race.abandoned")
		def.ID = "race.abandoned"
		def.Plugin = "race-plugin"
		def.Run = func(_ context.Context, _ json.RawMessage, _ registry.Reporter) error {
			// Deliberately ignore ctx cancellation for longer than both the
			// shutdown ctx (5ms) and AbandonGrace (20ms) — mimics a slow or
			// misbehaving plugin — then write to the REAL store. If Shutdown
			// (and then store.Close) does not wait for this goroutine, this
			// write lands on a closed store.
			close(started)
			time.Sleep(400 * time.Millisecond)
			err := store.SetSetting("race_abandoned_marker", "true", "bool", false)
			if err != nil {
				writeErr.Store(err)
			}
			runFinished.Store(true)
			return err
		}
		if err := reg.RegisterOp(def); err != nil {
			t.Fatalf("iter %d: RegisterOp: %v", iter, err)
		}

		reg.Start(context.Background())

		if _, err := reg.EnqueueOp(context.Background(), "race.abandoned", nil); err != nil {
			t.Fatalf("iter %d: EnqueueOp: %v", iter, err)
		}
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatalf("iter %d: op did not start within 5s", iter)
		}

		// Shutdown with a ctx that expires almost immediately, forcing the
		// "ctx expired during drain poll" branch (the op is still running, the
		// drain poll gives up, the op is marked interrupted) and sending
		// Shutdown on to its final goroutineWG.Wait().
		shutCtx, shutCancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
		err := reg.Shutdown(shutCtx)
		shutCancel()
		if err == nil {
			t.Fatalf("iter %d: expected Shutdown to report ctx expiry (drain poll must time out for this repro); got nil", iter)
		}

		// BUG-2 core assertion: Shutdown must not return while the abandoned
		// run goroutine is still executing plugin code. The 400ms run fits
		// inside goroutineWG.Wait's 2s escape hatch, so post-fix Shutdown
		// blocks until the goroutine (and its store write) completes.
		if !runFinished.Load() {
			t.Fatalf("iter %d: Shutdown returned while the abandoned run goroutine was still executing (BUG-2: safeRun goroutine not enrolled in goroutineWG)", iter)
		}
		if we, ok := writeErr.Load().(error); ok && we != nil {
			t.Fatalf("iter %d: abandoned run goroutine's store write failed: %v", iter, we)
		}

		// Production sequence: cmd/root.go's deferred closeStore runs right
		// after shutdown. Post-fix this is safe because the write above
		// happened-before Shutdown returned.
		cleanup()
	}
}
