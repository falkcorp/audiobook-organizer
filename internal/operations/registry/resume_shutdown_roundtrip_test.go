// file: internal/operations/registry/resume_shutdown_roundtrip_test.go
// version: 1.3.0
// guid: 0b5c4a19-7e2d-4f83-9a61-3c8d5e7f012a
// last-edited: 2026-08-24

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

// newRoundtripRegistry builds a registry with the watchdog parked, so these
// tests observe only the shutdown/resume paths.
func newRoundtripRegistry(store *fakeStore, workers int) *registry.Registry {
	return registry.NewWithOptions(store, slog.Default(), workers, registry.Options{
		WatchdogInterval: 30 * time.Second,
	})
}

// TestResume_RealShutdownIsResumedByTheSweep is the test the existing resume
// suite cannot be: it never writes a status into the store by hand.
//
// Every case in resume_test.go begins at insertRunningOp (resume_test.go:22),
// which writes Status:"running" directly and then calls Start. That fixture
// encodes the very assumption the suite exists to check -- that an interrupted
// run is left in the store as "running". It is not.
//
// THIS IS THE END-TO-END REGRESSION TEST FOR OPS-V2-RESUME-BLIND. It was written
// with assertions (2) and (3) inverted, documenting the gap while it stood; both
// have now been flipped to require the fix. The two halves:
//
//   - RECORDED CORRECTLY (PR1, #2793). A graceful Shutdown cancels the run.
//     executeRun used to classify that as an ordinary ctxCanceled and write
//     "canceled", losing the fact that the server -- not a user -- ended the run.
//     finalStatusForCanceledRun distinguishes the two and writes
//     interruptedStatus(resumePolicy), so a ResumeRestart op shuts down as
//     "interrupted_quiesced".
//
//   - RESUMED (PR2, this branch). resumeAfterStartup now takes its candidates
//     from ListResumableOperationsV2, which scans the opv2:op: keyspace and
//     includes interrupted_quiesced, instead of ListActiveOperationsV2, whose
//     opv2:act: index UpdateOperationV2Status had already dropped the row from
//     (pebble_store_ops_v2.go). The correctly-labelled row is now seen and its
//     declared ResumePolicy is finally consulted.
//
// Assertion (2) still requires the ACTIVE set to be empty. That is deliberate,
// not a leftover: ListActiveOperationsV2 means "in flight" and four other callers
// depend on it, so the fix deliberately did NOT widen it. The two sets diverging
// here is the fix working, not a bug.
func TestResume_RealShutdownIsResumedByTheSweep(t *testing.T) {
	store := newFakeStore()
	r1 := newRoundtripRegistry(store, 2)

	started := make(chan struct{})
	def := makeValidDef("test.shutdown-roundtrip")
	def.ResumePolicy = registry.ResumeRestart
	def.ProgressTimeout = 30 * time.Minute
	def.Run = func(runCtx context.Context, _ json.RawMessage, _ registry.Reporter) error {
		close(started)
		<-runCtx.Done()
		return runCtx.Err()
	}
	if err := r1.RegisterOp(def); err != nil {
		t.Fatalf("register: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r1.Start(ctx)

	opID, err := r1.EnqueueOp(ctx, "test.shutdown-roundtrip", nil)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	// LOAD-BEARING. The op must be genuinely RUNNING before the shutdown. A row
	// still "queued" keeps its active-set key and IS visible to the sweep, so
	// without this wait the test could pass for the opposite reason.
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("op never started; fixture invalid")
	}

	if err := r1.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}

	// (1) A graceful shutdown does not leave the row "running" -- which is the
	//     only state insertRunningOp ever produces -- and it no longer flattens
	//     the run to a plain "canceled" either. The def declares ResumeRestart,
	//     so interruptedStatus maps it to interrupted_quiesced. A "canceled"
	//     here means finalStatusForCanceledRun stopped distinguishing a server
	//     shutdown from a user cancel, which is the whole point of this branch.
	if got := store.statusOf(opID); got != "interrupted_quiesced" {
		t.Errorf("after a graceful Shutdown status=%q, want %q "+
			"(if this changed, re-check whether the resume suite's fixture is now realistic)",
			got, "interrupted_quiesced")
	}

	// (2) The row has left the ACTIVE set -- correctly, and permanently.
	//     interrupted_quiesced is neither running nor queued, and
	//     ListActiveOperationsV2 must keep meaning "in flight" for the scheduler's
	//     in-flight guard, the AI same-mode guard and the enqueue dedupe.
	active, err := store.ListActiveOperationsV2()
	if err != nil {
		t.Fatalf("ListActiveOperationsV2: %v", err)
	}
	if len(active) != 0 {
		t.Errorf("ListActiveOperationsV2 returned %d rows after shutdown, want 0 "+
			"(a quiesced row must not read as in-flight)", len(active))
	}

	//     ...but the RESUMABLE set must still hold it. This is the assertion that
	//     separates the fix from the defect: before PR2 both sets were the same
	//     query and the row was simply gone.
	resumable, err := store.ListResumableOperationsV2()
	if err != nil {
		t.Fatalf("ListResumableOperationsV2: %v", err)
	}
	if len(resumable) != 1 || resumable[0].ID != opID {
		t.Fatalf("ListResumableOperationsV2 = %+v, want exactly the quiesced op %s; "+
			"the sweep cannot resume a row it cannot see", resumable, opID)
	}

	// (3) A fresh registry over the same store therefore DOES resume it, because
	//     the def declares ResumeRestart.
	var reran atomic.Bool
	def2 := def
	def2.Run = func(context.Context, json.RawMessage, registry.Reporter) error {
		reran.Store(true)
		return nil
	}
	r2 := newRoundtripRegistry(store, 2)
	if err := r2.RegisterOp(def2); err != nil {
		t.Fatalf("r2 register: %v", err)
	}
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	r2.Start(ctx2)
	defer func() { _ = r2.Shutdown(context.Background()) }()
	time.Sleep(300 * time.Millisecond)

	// THE LOAD-BEARING ASSERTION. ResumeRestart says "resume me"; before this
	// branch it silently did not happen. A failure here means the sweep is blind
	// to interrupted_quiesced rows again -- check that resumeAfterStartup still
	// reads ListResumableOperationsV2 and not the active index.
	if !reran.Load() {
		t.Fatal("a ResumeRestart op did NOT resume after a graceful shutdown: " +
			"OPS-V2-RESUME-BLIND has regressed. resumeAfterStartup must take its " +
			"candidates from ListResumableOperationsV2 (which includes " +
			"interrupted_quiesced), not ListActiveOperationsV2.")
	}
}

// TestResume_ShutdownTimeoutLeavesQuiescedAndResumable covers the other shutdown
// branch -- registry.go:1075, where the drain deadline expires and the row is
// written interrupted_quiesced by interruptedStatus. It is likewise not
// running|queued, so it too leaves the active index; the point of this test is
// that leaving the active index no longer means leaving the RESUMABLE set.
func TestResume_ShutdownTimeoutLeavesQuiescedAndResumable(t *testing.T) {
	store := newFakeStore()
	r := registry.NewWithOptions(store, slog.Default(), 2, registry.Options{
		WatchdogInterval: 30 * time.Second,
		AbandonGrace:     50 * time.Millisecond,
		AbandonedCap:     10,
	})

	started := make(chan struct{})
	release := make(chan struct{})
	def := makeValidDef("test.shutdown-timeout")
	def.ResumePolicy = registry.ResumeRestart
	def.ProgressTimeout = 30 * time.Minute
	def.Run = func(_ context.Context, _ json.RawMessage, _ registry.Reporter) error {
		close(started)
		<-release // deliberately ignores cancellation
		return nil
	}
	if err := r.RegisterOp(def); err != nil {
		t.Fatalf("register: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.Start(ctx)

	opID, err := r.EnqueueOp(ctx, "test.shutdown-timeout", nil)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("op never started; fixture invalid")
	}

	shutCtx, shutCancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer shutCancel()
	shutdownReturned := make(chan struct{})
	go func() {
		_ = r.Shutdown(shutCtx)
		close(shutdownReturned)
	}()
	// The drain deadline must expire while the op is still in r.running.
	time.Sleep(400 * time.Millisecond)

	if got := store.statusOf(opID); got != "interrupted_quiesced" {
		t.Errorf("after a timed-out Shutdown status=%q, want %q", got, "interrupted_quiesced")
	}
	active, err := store.ListActiveOperationsV2()
	if err != nil {
		t.Fatalf("ListActiveOperationsV2: %v", err)
	}
	if len(active) != 0 {
		t.Errorf("interrupted_quiesced row still in the active set (%d rows); "+
			"it must not read as in-flight", len(active))
	}
	resumable, err := store.ListResumableOperationsV2()
	if err != nil {
		t.Fatalf("ListResumableOperationsV2: %v", err)
	}
	if len(resumable) != 1 || resumable[0].ID != opID {
		t.Fatalf("ListResumableOperationsV2 = %+v, want the timed-out op %s; "+
			"a drain-deadline quiesce must be resumable too, not just a clean cancel",
			resumable, opID)
	}

	close(release)
	select {
	case <-shutdownReturned:
	case <-time.After(5 * time.Second):
		t.Fatal("Shutdown never returned")
	}
}

// TestResume_QueuedAtShutdownIsRestartedNotDropped is the POSITIVE half: it pins
// that a QUEUED op survives a restart and is re-run under ResumeRestart.
//
// Shutdown walks r.running only. An op that was enqueued but never dispatched to
// a worker is still status="queued", still carries its opv2:act: key, and IS
// therefore visible to the next startup's resumeAfterStartup. That is the one
// path on which a maintenance job's declared ResumePolicy is consulted after a
// clean restart -- and it is the common shape for a scheduled job enqueued just
// before a deploy.
//
// Under ResumeDrop this row is written interrupted_dropped and thrown away.
// Under ResumeRestart it is re-queued with resume_count incremented and runs.
//
// WHAT THIS TEST DOES AND DOES NOT PIN (measured 2026-08-23). An earlier version
// of this comment claimed the test "FAILS at b3bf412f6 and PASSES at HEAD" -- it
// does not, and never did. Measured both ways: with watchdog.go reverted to the
// merge-base (49819eb44, the only product file in this package the PR touches),
// BOTH this fixture and the original enqueue-based one still pass. The test
// declares its own def with ResumePolicy=ResumeRestart, so the six maintenance
// job files this PR re-policies cannot reach it either.
//
// It is kept because nothing else covers the queued-at-shutdown resume path at
// all -- resume_test.go starts every case at insertRunningOp. But the PR's own
// two fixes are pinned elsewhere, and those DO fail against the merge-base:
// TestMaintenancePolicy_RestartWithoutCheckpointing for the watchdog gate, and
// TestUpdateOpProgressV2_AdvancesHighWaterProgress (internal/database) for the
// high-water mark. Do not cite this test as evidence for either.
//
// FIXTURE NOTE (2026-08-23): the queued row is PLANTED after r1.Shutdown returns
// rather than produced by enqueuing against the live r1. The obvious fixture --
// occupy the single worker with a blocker, enqueue the target behind it, then
// shut down -- is irreducibly racy. Shutdown cancels the blocker, the blocker's
// `<-runCtx.Done()` returns at once, slot 0 frees, and a dispatchCycle that had
// already passed its shuttingDown check (dispatcher.go:36 reads the flag once,
// then does a store list plus a dispatch loop -- check-then-act) picks the target
// up and runs it to completion AFTER shutdown has been entered. That is exactly
// how this test failed on CI at 6da3e9dcb:
//
//	target status="completed" after shutdown, want "queued"
//
// No blocker tuning closes that window: anything holding the slot through
// shutdown lands on the timeout path, which
// TestResume_ShutdownTimeoutLeavesQuiescedAndStillInvisible already covers.
//
// Planting is sound because Shutdown calls r.cancelFn and then JOINS
// goroutineWG before returning (registry.go, "all goroutines exited"), so r1's
// dispatcher is dead and nothing can touch the row between the plant and
// r2.Start. A real shutdown is still exercised here by the blocker, and
// end-to-end by TestResume_RealShutdownLeavesNothingForTheSweep.
//
// The dispatcher's check-then-act window is a real defect in its own right --
// it starts brand-new runs after Shutdown begins, each then recorded interrupted
// -- but it is not what this PR set out to fix. Filed as OPS-V2-DISPATCH-RACE.
func TestResume_QueuedAtShutdownIsRestartedNotDropped(t *testing.T) {
	store := newFakeStore()
	r1 := newRoundtripRegistry(store, 1)

	blockerStarted := make(chan struct{})
	blocker := makeValidDef("test.queued-blocker")
	blocker.ResumePolicy = registry.ResumeDrop
	blocker.ProgressTimeout = 30 * time.Minute
	blocker.Run = func(runCtx context.Context, _ json.RawMessage, _ registry.Reporter) error {
		close(blockerStarted)
		<-runCtx.Done()
		return runCtx.Err()
	}
	if err := r1.RegisterOp(blocker); err != nil {
		t.Fatalf("register blocker: %v", err)
	}

	// Not registered on r1: r1 only has to perform a real shutdown. The def is
	// built here because r2 needs it below.
	target := makeValidDef("test.queued-target")
	target.ResumePolicy = registry.ResumeRestart
	target.ProgressTimeout = 30 * time.Minute
	target.Run = func(context.Context, json.RawMessage, registry.Reporter) error { return nil }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r1.Start(ctx)

	if _, err := r1.EnqueueOp(ctx, "test.queued-blocker", nil); err != nil {
		t.Fatalf("enqueue blocker: %v", err)
	}
	select {
	case <-blockerStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("blocker never started; fixture invalid")
	}

	if err := r1.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}

	// r1 is fully stopped (dispatcher joined), so nothing can race this plant.
	targetID := insertQueuedOp(store, "test.queued-target", target.Plugin, 1)

	// LOAD-BEARING: the row must be QUEUED going into r2. If it were anything
	// else this test would be measuring a different resume path than the one
	// the maintenance jobs' declared ResumePolicy is consulted on.
	if got := store.statusOf(targetID); got != "queued" {
		t.Fatalf("target status=%q before restart, want %q; fixture invalid", got, "queued")
	}

	// Second registry over the same store: the sweep must see it and restart it.
	ran := make(chan struct{}, 1)
	target2 := target
	target2.Run = func(context.Context, json.RawMessage, registry.Reporter) error {
		select {
		case ran <- struct{}{}:
		default:
		}
		return nil
	}
	r2 := newRoundtripRegistry(store, 1)
	if err := r2.RegisterOp(target2); err != nil {
		t.Fatalf("r2 register target: %v", err)
	}
	// The blocker's def must exist too, or the sweep drops its row as an unknown
	// def -- which is correct, but would make this test depend on registration
	// order rather than on the target's policy.
	blocker2 := blocker
	blocker2.Run = func(context.Context, json.RawMessage, registry.Reporter) error { return nil }
	if err := r2.RegisterOp(blocker2); err != nil {
		t.Fatalf("r2 register blocker: %v", err)
	}

	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	r2.Start(ctx2)
	defer func() { _ = r2.Shutdown(context.Background()) }()

	select {
	case <-ran:
	case <-time.After(5 * time.Second):
		t.Fatal("the queued ResumeRestart op did not run after restart; " +
			"under ResumeDrop it would have been marked interrupted_dropped instead")
	}

	row, err := store.GetOperationV2(targetID)
	if err != nil || row == nil {
		t.Fatalf("target row not found after resume: %v", err)
	}
	if row.ResumeCount != 1 {
		t.Errorf("resume_count=%d after one restart, want 1", row.ResumeCount)
	}
}

// TestMaintenancePolicy_RestartWithoutCheckpointing pins the two fixes that make
// ResumeRestart correct for an op whose resume anchor is its OPERATION ID rather
// than a checkpoint blob.
//
// maintenance.ProgressReporter is {SetTotal, Increment, Log} -- no Checkpoint
// method exists for a job to call. Before 2026-08-23 that meant two things fired
// against every such op forever: the watchdog's `uncheckpointed` strike (gated on
// ResumePolicy, so unavoidable) and checkInfiniteRestart's force-drop at
// resume_count>=3 (gated on HighWaterProgress, which only Checkpoint could move).
//
// Both are now gated on what the def actually DECLARED, so the checks stay sharp
// for defs that promised a cadence and stay silent for defs that never could.
//
// The high_water_progress half is pinned at the store level, where the change
// lives, by TestUpdateOpProgressV2_AdvancesHighWaterProgress in internal/database
// -- asserting it through the reporter here would race its async flush loop.
func TestMaintenancePolicy_RestartWithoutCheckpointing(t *testing.T) {
	// runProgressOnlyOp starts a ResumeRestart op that reports progress and never
	// checkpoints -- the only thing a maintenance job can do -- backdates its
	// StartedAt past the 5m floor, and returns the store and op id.
	runProgressOnlyOp := func(t *testing.T, defID string, minCheckpoint time.Duration) (*fakeStore, string) {
		t.Helper()
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)

		store := newFakeStore()
		r := registry.NewWithOptions(store, slog.Default(), 2, registry.Options{
			WatchdogInterval: 50 * time.Millisecond,
		})

		started := make(chan struct{})
		def := makeValidDef(defID)
		def.ResumePolicy = registry.ResumeRestart
		def.ProgressTimeout = 30 * time.Minute
		def.MinCheckpointInterval = minCheckpoint
		def.Run = func(runCtx context.Context, _ json.RawMessage, rep registry.Reporter) error {
			// Exactly what maintenance.ProgressAdapter.Increment does. It does
			// not and cannot call rep.Checkpoint.
			_ = rep.UpdateProgress(7, 100, "")
			close(started)
			<-runCtx.Done()
			return runCtx.Err()
		}
		if err := r.RegisterOp(def); err != nil {
			t.Fatalf("register: %v", err)
		}
		r.Start(ctx)

		opID, err := r.EnqueueOp(ctx, defID, nil)
		if err != nil {
			t.Fatalf("enqueue: %v", err)
		}
		<-started

		stale := time.Now().UTC().Add(-6 * time.Minute)
		store.setStartedAt(opID, &stale)
		_ = store.UpdateOperationV2Status(opID, "running", nil, nil, nil)
		return store, opID
	}

	t.Run("no uncheckpointed strike when the def declares no checkpoint cadence", func(t *testing.T) {
		store, opID := runProgressOnlyOp(t, "test.no-cadence-declared", 0)

		// MinCheckpointInterval==0 means "this def never promised to checkpoint".
		// Striking it is a check against a contract it never entered.
		time.Sleep(1 * time.Second)
		if got := store.strikesOfKind(opID, "uncheckpointed"); len(got) > 0 {
			t.Fatalf("got %d uncheckpointed strikes for a def declaring no "+
				"MinCheckpointInterval; the strike is gated on the declaration, so a "+
				"maintenance job (whose reporter has no Checkpoint method) must never "+
				"accrue one", len(got))
		}
	})

	t.Run("strike STILL fires when the def declared a cadence and missed it", func(t *testing.T) {
		// The sharpening half. Silencing the strike for everything would have been
		// the easy fix and the wrong one: a def that promised a cadence and never
		// checkpoints is exactly the defect the strike exists to report.
		store, opID := runProgressOnlyOp(t, "test.cadence-declared-and-missed", 30*time.Second)

		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if len(store.strikesOfKind(opID, "uncheckpointed")) > 0 {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("no uncheckpointed strike for a def that declared " +
			"MinCheckpointInterval=30s and never checkpointed; the gate has been " +
			"loosened too far and the check is now dead for every def")
	})
}
