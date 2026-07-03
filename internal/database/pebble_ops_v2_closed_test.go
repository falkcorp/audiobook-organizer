// file: internal/database/pebble_ops_v2_closed_test.go
// version: 2.0.0
// guid: 9e4b2c17-5a6d-4f38-8c01-7d2e9f4a6b53
// last-edited: 2026-07-03

// pebble_ops_v2_closed_test.go covers the PEBBLE-CLOSED-SWEEPTICK-RESIDUAL
// defense-in-depth guard: every opv2 read/write on a closed PebbleStore must
// return an error instead of propagating pebble's ErrClosed PANIC out of
// Get/Set/NewIter/Commit and crashing the process. The op registry drives
// these accesses from background goroutines (deps sweep ticker, dispatcher
// cycle, dbReporter progress/log flushes); a registry torn down without
// Shutdown (as internal/server test leaks can do) otherwise kills the whole
// test binary minutes later. Observed legs: ListWaitingDepsOps (sweep),
// ListQueuedOperationsV2 (dispatcher), UpdateOpProgressV2 (dbReporter
// flushProgressLazy).
package database

import (
	"errors"
	"os"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"
	ulid "github.com/oklog/ulid/v2"
)

// openClosedOpsStore opens a fresh PebbleStore and immediately closes it,
// returning the closed store for post-Close access tests.
func openClosedOpsStore(t *testing.T) *PebbleStore {
	t.Helper()
	tmpdir := "/tmp/test_pebble_" + ulid.Make().String()
	store, err := NewPebbleStore(tmpdir)
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(tmpdir) })
	store.WaitForWarmup()
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return store
}

// requireClosedErr asserts the call surfaced pebble.ErrClosed as an error.
func requireClosedErr(t *testing.T, op string, err error) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: expected error on closed store, got nil", op)
	}
	if !errors.Is(err, pebble.ErrClosed) {
		t.Fatalf("%s: expected error wrapping pebble.ErrClosed, got: %v", op, err)
	}
}

func TestListWaitingDepsOps_ClosedStoreReturnsError(t *testing.T) {
	store := openClosedOpsStore(t)

	// Pre-guard this panicked "pebble: closed" (pebble panics ErrClosed from
	// NewIter rather than returning it); post-guard it must be an error.
	rows, err := store.ListWaitingDepsOps()
	requireClosedErr(t, "ListWaitingDepsOps", err)
	if rows != nil {
		t.Fatalf("expected nil rows on closed store, got %v", rows)
	}
}

// TestOpsV2ClosedStore_AllPathsReturnErrors sweeps every opv2 access the op
// registry's background goroutines can make after a leaked teardown. Each call
// pre-guard PANICS the whole binary; post-guard each must return an error
// wrapping pebble.ErrClosed.
func TestOpsV2ClosedStore_AllPathsReturnErrors(t *testing.T) {
	store := openClosedOpsStore(t)
	now := time.Now().UTC()

	// The observed third leg: dbReporter.flushProgressLazy → UpdateOpProgressV2
	// → pebbleGetJSON → p.db.Get on the closed store.
	requireClosedErr(t, "UpdateOpProgressV2", store.UpdateOpProgressV2("op-1", 1, 10, "msg"))

	_, err := store.ListQueuedOperationsV2()
	requireClosedErr(t, "ListQueuedOperationsV2", err)

	requireClosedErr(t, "InsertOperationV2",
		store.InsertOperationV2(OperationV2Row{ID: "op-1", Status: "queued", QueuedAt: now}))
	requireClosedErr(t, "UpdateOperationV2Status",
		store.UpdateOperationV2Status("op-1", "running", &now, nil, nil))
	_, err = store.SetOperationV2StatusIfQueued("op-1", "running")
	requireClosedErr(t, "SetOperationV2StatusIfQueued", err)
	_, err = store.ListActiveOperationsV2()
	requireClosedErr(t, "ListActiveOperationsV2", err)
	_, err = store.GetOperationV2("op-1")
	requireClosedErr(t, "GetOperationV2", err)
	requireClosedErr(t, "IncrementResumeCountV2", store.IncrementResumeCountV2("op-1"))
	requireClosedErr(t, "UpdateOpPhaseV2", store.UpdateOpPhaseV2("op-1", nil))
	requireClosedErr(t, "UpdateOpCheckpointV2", store.UpdateOpCheckpointV2("op-1", 5))
	requireClosedErr(t, "UpsertOpStateV2", store.UpsertOpStateV2(OpStateV2Row{OperationID: "op-1"}))
	_, err = store.GetOpStateV2("op-1")
	requireClosedErr(t, "GetOpStateV2", err)
	requireClosedErr(t, "DeleteOpStateV2", store.DeleteOpStateV2("op-1"))
	requireClosedErr(t, "UpdateOperationV2Params", store.UpdateOperationV2Params("op-1", []byte("{}")))
	requireClosedErr(t, "AppendOpLogsV2",
		store.AppendOpLogsV2([]OpLogV2Row{{OperationID: "op-1", CreatedAt: now}}))
	requireClosedErr(t, "InsertOpErrorV2",
		store.InsertOpErrorV2(OpErrorV2Row{OperationID: "op-1", OccurredAt: now}))
	requireClosedErr(t, "InsertOpStrikeV2",
		store.InsertOpStrikeV2(OpStrikeV2Row{DefID: "d", OperationID: "op-1", OccurredAt: now}))
	_, err = store.ListOperationsV2Since(now.Add(-time.Hour), 10)
	requireClosedErr(t, "ListOperationsV2Since", err)
	_, err = store.GetOpLogsV2("op-1", 10)
	requireClosedErr(t, "GetOpLogsV2", err)
	requireClosedErr(t, "UpsertOpDefinitionV2", store.UpsertOpDefinitionV2(OpDefinitionV2Row{ID: "d"}))
	requireClosedErr(t, "DeleteOrphanOpDefsV2", store.DeleteOrphanOpDefsV2(nil))

	sub := OpSubject{Type: "book", ID: "b1"}
	_, err = store.GetDepRev(sub)
	requireClosedErr(t, "GetDepRev", err)
	_, err = store.BumpDepRev(sub)
	requireClosedErr(t, "BumpDepRev", err)
	requireClosedErr(t, "RecordOpCompletion", store.RecordOpCompletion(sub, "scan", "", 1))
	_, _, err = store.GetOpCompletion(sub, "scan")
	requireClosedErr(t, "GetOpCompletion", err)
	_, err = store.ListFileCompletions(sub, "scan")
	requireClosedErr(t, "ListFileCompletions", err)
	requireClosedErr(t, "PromoteToQueued", store.PromoteToQueued("op-1"))
	requireClosedErr(t, "AddToBatchBucket", store.AddToBatchBucket("scan", sub))
	_, err = store.ListBatchBucket("scan")
	requireClosedErr(t, "ListBatchBucket", err)
	requireClosedErr(t, "ClearBatchBucket", store.ClearBatchBucket("scan", []OpSubject{sub}))
}
