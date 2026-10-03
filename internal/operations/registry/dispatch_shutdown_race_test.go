// file: internal/operations/registry/dispatch_shutdown_race_test.go
// version: 1.0.0
// guid: 2c67f935-8917-41f9-8b65-e03988c07b12
// last-edited: 2026-10-03

// White-box regression tests for OPS-V2-DISPATCH-RACE: a run must never START
// after Shutdown has begun.
//
// dispatchCycle checked r.shuttingDown once, at the top, then did a store list
// and a dispatch loop. Shutdown flips the flag and cancels the handles it
// gathers, but a claim made after the check is either missing from that gather
// or present only as the dispatcher's stub, whose cancel funcs are nil -- so
// cancelWithCause was a no-op and the worker ran the op to completion on a live
// context. Measured on CI run 32655184277: "registry: shutting down" ->
// "dispatched op" -> "run finished status=completed".
//
// Both tests reproduce the interleaving deterministically instead of racing
// Start against Shutdown. A choreographed Start/Shutdown test is not a valid
// regression test here: once Shutdown cancels the internal context the workers
// exit, so a late send just sits in the buffered channel and the test passes
// with or without the fix.
package registry

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync/atomic"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	databasemocks "github.com/falkcorp/audiobook-organizer/internal/database/mocks"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// TestDispatchCycle_ShutdownDuringListClaimsNothing pins the dispatcher half:
// Shutdown begins while the cycle is inside ListQueuedOperationsV2 (the flag is
// set by the store call itself, which is the widest point of the window). The
// cycle must not claim the row. GetOperationV2 has no expectation, so mockery
// fails the test if the claim block is reached -- the re-read happens only
// after a claim is published. The nextRun check alone would not be enough: the
// send is a select with a default, so a full or unbuffered channel would hide
// a claim.
func TestDispatchCycle_ShutdownDuringListClaimsNothing(t *testing.T) {
	store := databasemocks.NewMockOpsV2Store(t)
	store.EXPECT().UpsertOpDefinitionV2(mock.Anything).Return(nil).Maybe()

	r := New(store, slog.Default(), 1, nil)
	require.NoError(t, r.RegisterOp(plainDef("test.plain")))

	store.EXPECT().ListQueuedOperationsV2().
		RunAndReturn(func() ([]database.OperationV2Row, error) {
			// Shutdown's first act, landing after the cycle's top-of-cycle check.
			r.shuttingDown.Store(true)
			return []database.OperationV2Row{queuedRow("op-sd", "test.plain", staleParams)}, nil
		}).Once()

	r.dispatchCycle(context.Background())

	select {
	case qr := <-r.nextRun:
		t.Fatalf("dispatched op %s after Shutdown began", qr.opID)
	default:
	}
	requireClaimReleased(t, r, "op-sd", "test")
}

// TestExecuteRun_ShutdownBeforePickupNeverRuns pins the worker half, which is
// the actual guarantee: a claim that reached nextRun before or during Shutdown
// (the flag can flip between the dispatcher's last check and its channel send,
// and a claim may already be buffered when Shutdown starts) must be dropped at
// pickup, not run. The row must be left exactly as it is -- "queued", because
// it never ran -- so no store write is expected at all; mockery fails the test
// on the queued->running compare-and-set or any status write.
func TestExecuteRun_ShutdownBeforePickupNeverRuns(t *testing.T) {
	store := databasemocks.NewMockOpsV2Store(t)
	store.EXPECT().UpsertOpDefinitionV2(mock.Anything).Return(nil).Maybe()

	var ran atomic.Bool
	def := plainDef("test.keyed")
	def.ConcurrencyKey = "test-key"
	def.Run = func(_ context.Context, _ json.RawMessage, _ Reporter) error {
		ran.Store(true)
		return nil
	}

	r := New(store, slog.Default(), 1, nil)
	require.NoError(t, r.RegisterOp(def))

	// The dispatcher's claim, exactly as dispatchCycle publishes it: a stub
	// handle (nil cancel), a plugin slot and the concurrency key.
	r.mu.Lock()
	r.concurrencyKeys[def.ConcurrencyKey] = "op-buf"
	r.pluginRunning[def.Plugin]++
	r.running["op-buf"] = &runHandle{
		id: "op-buf", defID: def.ID, plugin: def.Plugin,
		concurrencyKey: def.ConcurrencyKey, resumePolicy: def.ResumePolicy,
	}
	r.mu.Unlock()

	// Shutdown begins. Its handle walk would find only the stub above and its
	// cancelWithCause would do nothing, which is why the pickup gate must hold.
	r.shuttingDown.Store(true)

	abandoned := r.executeRun(context.Background(), &queuedRun{
		opID: "op-buf", defID: def.ID, params: json.RawMessage(staleParams),
		priority: PriorityNormal, concurrKey: def.ConcurrencyKey,
		plugin: def.Plugin, resumePolicy: def.ResumePolicy,
	})

	require.False(t, abandoned)
	require.False(t, ran.Load(), "op ran after Shutdown began; its stub handle could not be canceled")
	requireClaimReleased(t, r, "op-buf", def.Plugin)
	r.mu.RLock()
	defer r.mu.RUnlock()
	require.NotContains(t, r.concurrencyKeys, def.ConcurrencyKey,
		"concurrency key leaked: the next start could never dispatch this def")
}
