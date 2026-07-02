// file: internal/operations/registry/shutdown_race_test.go
// version: 1.0.0
// guid: 7d2e9a41-6c3b-4f80-9a1e-2b5c8d0f3e64
// last-edited: 2026-07-02

// shutdown_race_test.go is the regression test for PEBBLE-CLOSED-SHUTDOWN-RACE.
//
// When an op completes, worker.go calls Registry.notifyDepCompletion for each
// subject, which spawns a fire-and-forget goroutine that calls
// DepsScheduler.OnOpCompleted -> store.GetDepRev(...). Historically that
// goroutine used context.Background() and was NOT enrolled in goroutineWG, so
// Registry.Shutdown() (which cancels the internal context and waits on
// goroutineWG) returned WITHOUT draining it. A caller that closed the store
// immediately after Shutdown() could then race the lingering goroutine, whose
// GetDepRev read panics with "pebble: closed" and crashes the whole test binary.
//
// This test reproduces that race against a REAL PebbleStore: it runs many ops
// (each with a book subject so a notify goroutine is spawned), calls Shutdown(),
// then closes the store — repeatedly. A leaked goroutine touching the closed
// store panics; a properly drained one does not.
package registry_test

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	ulid "github.com/oklog/ulid/v2"
)

func TestShutdownDrainsDepNotifyGoroutines_RealStore(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	// Repeat the start/run/shutdown/close cycle to widen the race window. Under
	// -race the scheduler delay between op completion and the notify goroutine's
	// GetDepRev read is large enough that a single iteration often misses; the
	// loop makes reproduction reliable.
	for iter := range 8 {
		store, cleanup := openTestPebbleStore(t)
		schedStore := &pebbleSchedulerStore{PebbleStore: store}

		reg := registry.NewWithOptions(store, logger, 4, registry.Options{
			SweepInterval: time.Hour, // isolate the notify path from SweepTick
		})
		sched := registry.NewDepsScheduler(reg, schedStore)
		reg.SetDepsScheduler(sched)

		def := makeValidDef("race.op")
		def.ID = "race.op"
		if err := reg.RegisterOp(def); err != nil {
			t.Fatalf("iter %d: RegisterOp: %v", iter, err)
		}

		reg.Start(context.Background())

		// Enqueue several ops, each with a distinct book subject so the worker
		// derives a subject and calls notifyDepCompletion on completion.
		const n = 12
		ids := make([]string, 0, n)
		for range n {
			params := map[string]any{"book_id": "book-" + ulid.Make().String()}
			id, err := reg.EnqueueOp(context.Background(), "race.op", params)
			if err != nil {
				t.Fatalf("iter %d: EnqueueOp: %v", iter, err)
			}
			ids = append(ids, id)
		}

		// Wait for all ops to finish executing (status "completed"). The notify
		// goroutine is spawned around this point and is NOT waited on by us.
		for _, id := range ids {
			deadline := time.Now().Add(10 * time.Second)
			for {
				row, err := store.GetOperationV2(id)
				if err == nil && row != nil && row.Status == "completed" {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("iter %d: op %s never reached completed", iter, id)
				}
				time.Sleep(2 * time.Millisecond)
			}
		}

		// Shutdown SHOULD drain every goroutine that can still touch the store,
		// including the dep-notify goroutines. If it does not, the close below
		// races them.
		shutCtx, shutCancel := context.WithTimeout(context.Background(), 10*time.Second)
		if err := reg.Shutdown(shutCtx); err != nil {
			t.Fatalf("iter %d: Shutdown: %v", iter, err)
		}
		shutCancel()

		// If a dep-notify goroutine survived Shutdown, its GetDepRev read here
		// panics with "pebble: closed" and crashes the test binary.
		cleanup()
	}
}
