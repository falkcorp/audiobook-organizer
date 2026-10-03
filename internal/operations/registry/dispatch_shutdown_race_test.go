// file: internal/operations/registry/dispatch_shutdown_race_test.go
// version: 1.1.0
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
	"time"

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

// TestExecuteRun_ShutdownBeforeInfiniteRestartCheckWritesNothing pins the early
// exit: a ResumeRestart run picked up after Shutdown began must not reach the
// infinite-restart check, which reads the row and can force-drop it
// (interrupted_dropped). No GetOperationV2 expectation, so mockery fails the
// test if the check runs.
func TestExecuteRun_ShutdownBeforeInfiniteRestartCheckWritesNothing(t *testing.T) {
	store := databasemocks.NewMockOpsV2Store(t)
	store.EXPECT().UpsertOpDefinitionV2(mock.Anything).Return(nil).Maybe()

	def := plainDef("test.restart")
	def.ResumePolicy = ResumeRestart
	r := New(store, slog.Default(), 1, nil)
	require.NoError(t, r.RegisterOp(def))
	plantStub(r, "op-rs", def)
	r.shuttingDown.Store(true)

	require.False(t, r.executeRun(context.Background(), queuedRunFor("op-rs", def)))
	requireClaimReleased(t, r, "op-rs", def.Plugin)
}

// TestExecuteRun_ShutdownDuringPickupNeverRuns pins the LOCKED gate, the one
// that is the guarantee: the flag flips after the early unlocked check has
// passed (here, inside the infinite-restart check's row read). The run must
// still not start: no queued->running compare-and-set, no Run call.
func TestExecuteRun_ShutdownDuringPickupNeverRuns(t *testing.T) {
	store := databasemocks.NewMockOpsV2Store(t)
	store.EXPECT().UpsertOpDefinitionV2(mock.Anything).Return(nil).Maybe()

	var ran atomic.Bool
	def := plainDef("test.restart")
	def.ResumePolicy = ResumeRestart
	def.Run = func(_ context.Context, _ json.RawMessage, _ Reporter) error {
		ran.Store(true)
		return nil
	}
	r := New(store, slog.Default(), 1, nil)
	require.NoError(t, r.RegisterOp(def))
	plantStub(r, "op-mid", def)

	row := queuedRow("op-mid", def.ID, staleParams)
	store.EXPECT().GetOperationV2("op-mid").
		RunAndReturn(func(string) (*database.OperationV2Row, error) {
			r.shuttingDown.Store(true) // Shutdown begins mid-pickup
			return &row, nil
		}).Once()

	require.False(t, r.executeRun(context.Background(), queuedRunFor("op-mid", def)))
	require.False(t, ran.Load(), "op started after Shutdown began")
	requireClaimReleased(t, r, "op-mid", def.Plugin)
}

// TestShutdown_ReleasesBufferedClaimWithoutStatus: a claim sitting in nextRun
// with no worker to pick it up (here the registry was never started; in
// production the only worker exited after abandoning its run). Shutdown must
// give the claim back with its row still queued, and must not wait out its
// whole context on it. It used to do both: the drain poll waited for the stub
// until ctx expired, then the timeout path wrote interrupted_dropped onto a
// ResumeDrop op that never ran. No UpdateOperationV2Status expectation, so
// any status write fails the test.
func TestShutdown_ReleasesBufferedClaimWithoutStatus(t *testing.T) {
	store := databasemocks.NewMockOpsV2Store(t)
	store.EXPECT().UpsertOpDefinitionV2(mock.Anything).Return(nil).Maybe()

	def := plainDef("test.plain") // ResumeDrop
	r := New(store, slog.Default(), 1, nil)
	require.NoError(t, r.RegisterOp(def))
	plantStub(r, "op-buf", def)
	r.nextRun <- queuedRunFor("op-buf", def)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	start := time.Now()
	require.NoError(t, r.Shutdown(ctx), "Shutdown hit its timeout on a never-run claim")
	require.Less(t, time.Since(start), 5*time.Second)
	requireClaimReleased(t, r, "op-buf", def.Plugin)
	require.Empty(t, r.nextRun)
}

// TestShutdown_TimeoutReleasesStubsAndInterruptsRuns: at the shutdown timeout
// a stub whose claim never reached nextRun (still between claim and send) is
// released with no status write, while a real running handle is still marked
// interrupted as before.
func TestShutdown_TimeoutReleasesStubsAndInterruptsRuns(t *testing.T) {
	store := databasemocks.NewMockOpsV2Store(t)
	store.EXPECT().UpsertOpDefinitionV2(mock.Anything).Return(nil).Maybe()
	store.EXPECT().UpdateOperationV2Status("op-run", "interrupted_dropped", mock.Anything, mock.Anything, mock.Anything).
		Return(nil).Once()

	def := plainDef("test.plain") // ResumeDrop
	r := New(store, slog.Default(), 2, nil)
	require.NoError(t, r.RegisterOp(def))
	plantStub(r, "op-stub", def)
	r.mu.Lock()
	r.pluginRunning[def.Plugin]++
	r.running["op-run"] = &runHandle{
		id: "op-run", defID: def.ID, plugin: def.Plugin, resumePolicy: def.ResumePolicy,
		cancel: func() {}, // a running op that ignores cancellation
	}
	r.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	require.ErrorIs(t, r.Shutdown(ctx), context.DeadlineExceeded)

	r.mu.RLock()
	defer r.mu.RUnlock()
	require.NotContains(t, r.running, "op-stub", "the never-run claim must be released, not interrupted")
	require.Contains(t, r.running, "op-run", "a real run stays registered until its goroutine exits")
}

// plantStub publishes a claim exactly as dispatchCycle does: a stub handle
// (nil cancel), a plugin slot, and the concurrency key if the def has one.
func plantStub(r *Registry, opID string, def OperationDef) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if def.ConcurrencyKey != "" {
		r.concurrencyKeys[def.ConcurrencyKey] = opID
	}
	r.pluginRunning[def.Plugin]++
	r.running[opID] = &runHandle{
		id: opID, defID: def.ID, plugin: def.Plugin,
		concurrencyKey: def.ConcurrencyKey, resumePolicy: def.ResumePolicy,
	}
}

func queuedRunFor(opID string, def OperationDef) *queuedRun {
	return &queuedRun{
		opID: opID, defID: def.ID, params: json.RawMessage(staleParams),
		priority: PriorityNormal, concurrKey: def.ConcurrencyKey,
		plugin: def.Plugin, resumePolicy: def.ResumePolicy,
	}
}
