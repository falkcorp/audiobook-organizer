// file: internal/operations/registry/promote_realstore_test.go
// version: 1.1.1
// guid: b5c6d7e8-f9a0-1b2c-3d4e-5f6a7b8c9d0e
// last-edited: 2026-10-04

// promote_realstore_test.go contains the regression test for the critical
// "promoted ops never reach the dispatcher" bug. It uses a REAL PebbleStore
// (not fakeStore) so ListQueuedOperationsV2 is exercised against the actual
// opv2:q: queue-index key — the path that fakeStore's linear scan hides.

package registry_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	ulid "github.com/oklog/ulid/v2"
)

// openTestPebbleStore opens a temporary PebbleStore and returns it plus a
// cleanup function. Mirrors setupPebbleTestDB in pebble_store_test.go.
func openTestPebbleStore(t *testing.T) (*database.PebbleStore, func()) {
	t.Helper()
	// t.TempDir, not /tmp: the store's sidecar (<dir>.storage-format) is a
	// sibling of the directory and must be cleaned up with it.
	tmpdir := filepath.Join(t.TempDir(), "store.pebble")
	store, err := database.NewPebbleStore(tmpdir)
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	return store, func() {
		store.Close()
		os.RemoveAll(tmpdir)
	}
}

// pebbleSchedulerStore wraps *database.PebbleStore and adds the BookFiles
// method required by registry.SchedulerStore (which embeds registry.DepStore).
// BookFiles returns nil — AllFiles requirements are treated as unmet, which is
// the same conservative behaviour as OpsV2DepAdapter in production code.
type pebbleSchedulerStore struct {
	*database.PebbleStore
}

func (p *pebbleSchedulerStore) BookFiles(_ string) ([]string, error) {
	return nil, nil
}

// TestPromoteToQueued_RealStore is the critical regression test:
//
// Setup:
//   - Register op-type "A" (no requirements) and op-type "B" (requires A).
//   - Enqueue an A op and complete it (record completion in PebbleStore).
//   - Enqueue op B — because A hasn't completed at enqueue time we skip
//     a "satisfied" check and instead park B directly as waiting_deps so
//     we can test the promotion path in isolation.
//
// Assertion:
//   - After DepsScheduler.OnOpCompleted fires, B's row status becomes "queued"
//     AND ListQueuedOperationsV2() actually returns B (proves the opv2:q:
//     queue-index key was written, which UpdateOperationV2Status("queued")
//     does NOT do).
//
// Without PromoteToQueued (old promote() using UpdateOperationV2Status):
//
//	B's row status is "queued" but ListQueuedOperationsV2() returns empty →
//	the dispatcher never sees B → test FAILS.
//
// With PromoteToQueued:
//
//	Both the row and the queue-index key are written atomically → test PASSES.
func TestPromoteToQueued_RealStore(t *testing.T) {
	store, cleanup := openTestPebbleStore(t)
	defer cleanup()

	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	// Wrap PebbleStore with BookFiles so it satisfies registry.SchedulerStore.
	schedStore := &pebbleSchedulerStore{PebbleStore: store}

	// Build a registry against the real store.
	reg := registry.NewWithOptions(store, logger, 2, registry.Options{
		// Short sweep interval; not actually needed for this test since we
		// call OnOpCompleted directly, but wired for correctness.
		SweepInterval: 100 * time.Millisecond,
	})

	// Register op A (no deps). Note: dots are allowed, colons are not.
	defA := makeValidDef("opA")
	defA.ID = "opA"
	if err := reg.RegisterOp(defA); err != nil {
		t.Fatalf("RegisterOp A: %v", err)
	}

	// Register op B (requires A completed).
	defB := makeValidDef("opB")
	defB.ID = "opB"
	defB.Requires = []registry.Requirement{
		{Kind: registry.ReqOpCompleted, OpType: "opA"},
	}
	if err := reg.RegisterOp(defB); err != nil {
		t.Fatalf("RegisterOp B: %v", err)
	}

	// Wire the DepsScheduler. We do NOT call reg.Start() here because we are
	// testing store-level queue-index correctness, not dispatch. Starting the
	// registry would let workers race-claim the newly promoted op before our
	// ListQueuedOperationsV2 assertion can observe it.
	sched := registry.NewDepsScheduler(reg, schedStore)
	reg.SetDepsScheduler(sched)

	// --- Park op B directly as waiting_deps in the real store ---
	// We insert a waiting_deps row manually rather than via EnqueueOp to
	// ensure B starts parked regardless of whether A has a completion record
	// at enqueue time (avoids a timing race with the subject / dep_rev check).
	subject := database.OpSubject{Type: "book", ID: "book-42"}
	reqsJSON, _ := json.Marshal([]registry.Requirement{
		{Kind: registry.ReqOpCompleted, OpType: "opA"},
	})
	bID := ulid.Make().String()
	now := time.Now().UTC()
	waitingRow := database.OperationV2Row{
		ID:           bID,
		DefID:        "opB",
		Plugin:       "test",
		Status:       "waiting_deps",
		Priority:     int(registry.PriorityNormal),
		QueuedAt:     now,
		SubjectType:  subject.Type,
		SubjectID:    subject.ID,
		Requirements: string(reqsJSON),
		TraceID:      ulid.Make().String(),
		SpanID:       ulid.Make().String(),
		Params:       `{"book_id":"book-42"}`,
	}
	if err := store.InsertOperationV2(waitingRow); err != nil {
		t.Fatalf("InsertOperationV2 (waiting B): %v", err)
	}

	// Verify B starts as waiting_deps (sanity check).
	rowBefore, err := store.GetOperationV2(bID)
	if err != nil || rowBefore == nil {
		t.Fatalf("GetOperationV2 before promotion: %v", err)
	}
	if rowBefore.Status != "waiting_deps" {
		t.Fatalf("expected waiting_deps before promotion, got %q", rowBefore.Status)
	}

	// Verify B is NOT in ListQueuedOperationsV2 yet.
	queuedBefore, err := store.ListQueuedOperationsV2()
	if err != nil {
		t.Fatalf("ListQueuedOperationsV2 before promotion: %v", err)
	}
	for _, op := range queuedBefore {
		if op.ID == bID {
			t.Fatal("B must NOT be in the queue before promotion")
		}
	}

	// Simulate A completing: call OnOpCompleted directly.
	// This records the completion and triggers re-evaluation + PromoteToQueued.
	subjectReg := registry.Subject{Type: subject.Type, ID: subject.ID}
	if err := sched.OnOpCompleted(ctx, subjectReg, "opA"); err != nil {
		t.Fatalf("OnOpCompleted: %v", err)
	}

	// Allow a brief moment for any async work to settle (OnOpCompleted is
	// synchronous in this call path, but be defensive).
	time.Sleep(20 * time.Millisecond)

	// --- Core assertion: ListQueuedOperationsV2 must now return B ---
	queuedAfter, err := store.ListQueuedOperationsV2()
	if err != nil {
		t.Fatalf("ListQueuedOperationsV2 after promotion: %v", err)
	}
	found := false
	for _, op := range queuedAfter {
		if op.ID == bID {
			found = true
			break
		}
	}
	if !found {
		// Also check the row status for a clearer failure message.
		rowAfter, _ := store.GetOperationV2(bID)
		status := "<nil>"
		if rowAfter != nil {
			status = rowAfter.Status
		}
		t.Fatalf("B (id=%s) must be discoverable via ListQueuedOperationsV2 after promotion (row status=%q); "+
			"the opv2:q: queue-index key was not written — PromoteToQueued fix is not working", bID, status)
	}

	// Sanity: row status must also be queued.
	rowAfter, err := store.GetOperationV2(bID)
	if err != nil || rowAfter == nil {
		t.Fatalf("GetOperationV2 after promotion: %v", err)
	}
	if rowAfter.Status != "queued" {
		t.Fatalf("B row status must be %q after promotion, got %q", "queued", rowAfter.Status)
	}
}
