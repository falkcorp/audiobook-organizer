// file: internal/operations/registry/dispatch_stale_params_test.go
// version: 1.0.0
// guid: 5e18d3b7-0c92-44a6-8f31-b7d260e94a18
// last-edited: 2026-08-29

// White-box regression tests for the dispatcher's stale-params TOCTOU.
//
// dispatchCycle snapshots every queued row via ListQueuedOperationsV2() WITHOUT
// holding r.mu, then later claims a row under the lock. For a def that declares
// MergeQueuedParams, a merge can land in that gap:
//
//	T0  dispatchCycle reads row X, Params={book_ids:[A]}
//	T1  EnqueueOp merges B -> persists {book_ids:[A,B]} and returns X's op id,
//	    so the caller believes B is queued
//	T2  dispatchCycle claims X (it was unclaimed at T1, so the merge was right
//	    to take it)
//	T3  the queuedRun is built from the T0 snapshot -> B is silently dropped
//
// The run then applies A only while reporting success, and the DB row shows
// [A,B] forever. These tests pin the fix: the dispatcher re-reads the row after
// claiming it, and fails closed when it cannot.
//
// The race is reproduced deterministically rather than by racing goroutines:
// the store returns stale params from ListQueuedOperationsV2 and merged params
// from GetOperationV2, which is precisely the state the interleaving above
// leaves behind at T2.
package registry

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	databasemocks "github.com/falkcorp/audiobook-organizer/internal/database/mocks"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

const (
	staleParams  = `{"book_ids":["A"]}`
	mergedParams = `{"book_ids":["A","B"]}`
)

// mergeableDef is a def that CAN absorb a queued request, so the dispatcher
// must treat its params as mutable-while-queued.
func mergeableDef(id string) OperationDef {
	return OperationDef{
		ID:              id,
		Plugin:          "test",
		DisplayName:     "Mergeable Op",
		Description:     "declares MergeQueuedParams",
		Run:             func(_ context.Context, _ json.RawMessage, _ Reporter) error { return nil },
		DefaultPriority: PriorityNormal,
		Cancellable:     true,
		ResumePolicy:    ResumeDrop,
		Liveness:        LivenessManual,
		MergeQueuedParams: func(existing, _ json.RawMessage) (json.RawMessage, bool, error) {
			return existing, true, nil
		},
	}
}

// plainDef declares no MergeQueuedParams, so its params cannot change while
// queued and the dispatcher must NOT pay for an extra read.
func plainDef(id string) OperationDef {
	d := mergeableDef(id)
	d.DisplayName = "Plain Op"
	d.Description = "no MergeQueuedParams"
	d.MergeQueuedParams = nil
	return d
}

func queuedRow(id, defID, params string) database.OperationV2Row {
	return database.OperationV2Row{
		ID: id, DefID: defID, Status: "queued", Params: params,
		Priority: int(PriorityNormal),
	}
}

// TestDispatchCycle_UsesParamsMergedAfterSnapshot is the core regression test.
// Before the fix the dispatcher shipped the snapshot and book B was dropped.
func TestDispatchCycle_UsesParamsMergedAfterSnapshot(t *testing.T) {
	store := databasemocks.NewMockOpsV2Store(t)
	// RegisterOp persists the definition; not what these tests are about.
	store.EXPECT().UpsertOpDefinitionV2(mock.Anything).Return(nil).Maybe()
	store.EXPECT().ListQueuedOperationsV2().
		Return([]database.OperationV2Row{queuedRow("op-1", "test.mergeable", staleParams)}, nil).Once()
	store.EXPECT().GetOperationV2("op-1").
		Return(ptrRow(queuedRow("op-1", "test.mergeable", mergedParams)), nil).Once()

	r := New(store, slog.Default(), 1, nil)
	require.NoError(t, r.RegisterOp(mergeableDef("test.mergeable")))

	r.dispatchCycle(context.Background())

	select {
	case qr := <-r.nextRun:
		require.JSONEq(t, mergedParams, string(qr.params),
			"dispatcher shipped the pre-merge snapshot; the merged-in book was silently dropped")
	default:
		t.Fatal("nothing was dispatched")
	}
}

// TestDispatchCycle_SkipsRereadWhenDefCannotMerge pins the narrowing: a def
// without MergeQueuedParams has immutable queued params, so GetOperationV2 must
// not be called at all. mockery fails the test if an unexpected call happens.
func TestDispatchCycle_SkipsRereadWhenDefCannotMerge(t *testing.T) {
	store := databasemocks.NewMockOpsV2Store(t)
	// RegisterOp persists the definition; not what these tests are about.
	store.EXPECT().UpsertOpDefinitionV2(mock.Anything).Return(nil).Maybe()
	store.EXPECT().ListQueuedOperationsV2().
		Return([]database.OperationV2Row{queuedRow("op-2", "test.plain", staleParams)}, nil).Once()
	// Deliberately no GetOperationV2 expectation.

	r := New(store, slog.Default(), 1, nil)
	require.NoError(t, r.RegisterOp(plainDef("test.plain")))

	r.dispatchCycle(context.Background())

	select {
	case qr := <-r.nextRun:
		require.JSONEq(t, staleParams, string(qr.params))
	default:
		t.Fatal("nothing was dispatched")
	}
}

// TestDispatchCycle_FailsClosedWhenRereadErrors verifies we do NOT run with
// params we could not confirm -- running the snapshot on a read error is the
// exact silent drop this guard exists to prevent. The claim must be released so
// a later cycle retries, otherwise the op is stranded forever behind Gate 0.
func TestDispatchCycle_FailsClosedWhenRereadErrors(t *testing.T) {
	store := databasemocks.NewMockOpsV2Store(t)
	// RegisterOp persists the definition; not what these tests are about.
	store.EXPECT().UpsertOpDefinitionV2(mock.Anything).Return(nil).Maybe()
	store.EXPECT().ListQueuedOperationsV2().
		Return([]database.OperationV2Row{queuedRow("op-3", "test.mergeable", staleParams)}, nil).Once()
	store.EXPECT().GetOperationV2("op-3").Return(nil, errors.New("boom")).Once()

	r := New(store, slog.Default(), 1, nil)
	require.NoError(t, r.RegisterOp(mergeableDef("test.mergeable")))

	r.dispatchCycle(context.Background())

	select {
	case qr := <-r.nextRun:
		t.Fatalf("dispatched despite an unverifiable params read: %s", qr.params)
	default:
	}
	requireClaimReleased(t, r, "op-3", "test")
}

// TestDispatchCycle_FailsClosedWhenRowVanished covers cancel/delete landing
// between the snapshot and the claim.
func TestDispatchCycle_FailsClosedWhenRowVanished(t *testing.T) {
	store := databasemocks.NewMockOpsV2Store(t)
	// RegisterOp persists the definition; not what these tests are about.
	store.EXPECT().UpsertOpDefinitionV2(mock.Anything).Return(nil).Maybe()
	store.EXPECT().ListQueuedOperationsV2().
		Return([]database.OperationV2Row{queuedRow("op-4", "test.mergeable", staleParams)}, nil).Once()
	store.EXPECT().GetOperationV2("op-4").Return(nil, nil).Once()

	r := New(store, slog.Default(), 1, nil)
	require.NoError(t, r.RegisterOp(mergeableDef("test.mergeable")))

	r.dispatchCycle(context.Background())

	select {
	case qr := <-r.nextRun:
		t.Fatalf("dispatched a vanished op: %s", qr.params)
	default:
	}
	requireClaimReleased(t, r, "op-4", "test")
}

// requireClaimReleased asserts releaseClaim undid every piece of the claim.
// Leaving the stub handle in r.running is the load-bearing failure: Gate 0
// consults it, so a leaked handle makes the op permanently un-dispatchable.
func requireClaimReleased(t *testing.T, r *Registry, opID, plugin string) {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	require.NotContains(t, r.running, opID,
		"claim not released: the stub handle strands the op behind Gate 0 forever")
	require.Zero(t, r.pluginRunning[plugin], "plugin concurrency accounting leaked")
}

func ptrRow(row database.OperationV2Row) *database.OperationV2Row { return &row }
