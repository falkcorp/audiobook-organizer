// file: internal/operations/registry/resume_shutdown_roundtrip_test.go
// version: 1.0.0
// guid: 0b5c4a19-7e2d-4f83-9a61-3c8d5e7f012a
// last-edited: 2026-08-23

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

// TestResume_RealShutdownLeavesNothingForTheSweep is the test the existing
// resume suite cannot be: it never writes a status into the store by hand.
//
// Every case in resume_test.go begins at insertRunningOp (resume_test.go:22),
// which writes Status:"running" directly and then calls Start. That fixture
// encodes the very assumption the suite exists to check -- that an interrupted
// run is left in the store as "running". It is not. A graceful Shutdown cancels
// the run, executeRun classifies it ctxCanceled and writes "canceled"
// (worker.go:412), and UpdateOperationV2Status deletes the opv2:act: key for any
// status that is not running|queued (pebble_store_ops_v2.go:277). The next
// startup's resumeAfterStartup reads ListActiveOperationsV2, so it sees nothing
// at all and no ResumePolicy is ever consulted.
//
// The final assertion is INVERTED on purpose: it asserts the op does NOT come
// back, which is the current behaviour, so this test runs and passes today
// rather than skipping. See OPS-V2-RESUME-BLIND in
// todo.d/20260823-v2-resume-sweep-is-blind-to-interrupted-rows.md.
func TestResume_RealShutdownLeavesNothingForTheSweep(t *testing.T) {
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
	//     only state insertRunningOp ever produces.
	if got := store.statusOf(opID); got != "canceled" {
		t.Errorf("after a graceful Shutdown status=%q, want %q "+
			"(if this changed, re-check whether the resume suite's fixture is now realistic)",
			got, "canceled")
	}

	// (2) ...so the row has left the set resumeAfterStartup reads.
	active, err := store.ListActiveOperationsV2()
	if err != nil {
		t.Fatalf("ListActiveOperationsV2: %v", err)
	}
	if len(active) != 0 {
		t.Errorf("ListActiveOperationsV2 returned %d rows after shutdown, want 0", len(active))
	}

	// (3) A fresh registry over the same store therefore resumes nothing, even
	//     though the def declares ResumeRestart.
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

	// INVERTED ON PURPOSE -- this documents the gap rather than the guarantee.
	// ResumeRestart says "resume me" and it does not happen. When
	// OPS-V2-RESUME-BLIND is fixed, FLIP this to require reran==true and delete
	// this comment: the test then becomes the regression test for the fix.
	if reran.Load() {
		t.Fatalf("the op resumed after a graceful shutdown -- OPS-V2-RESUME-BLIND " +
			"(todo.d/20260823-v2-resume-sweep-is-blind-to-interrupted-rows.md) appears " +
			"to be fixed; invert this assertion")
	}
}

// TestResume_ShutdownTimeoutLeavesQuiescedAndStillInvisible covers the other
// shutdown branch -- registry.go:1075, where the drain deadline expires and the
// row is written interrupted_quiesced by interruptedStatus. No test touches that
// branch today. It is likewise not running|queued, so it too leaves the active
// index and the sweep still sees nothing.
func TestResume_ShutdownTimeoutLeavesQuiescedAndStillInvisible(t *testing.T) {
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
			"if this now holds the row, OPS-V2-RESUME-BLIND may be fixed", len(active))
	}

	close(release)
	select {
	case <-shutdownReturned:
	case <-time.After(5 * time.Second):
		t.Fatal("Shutdown never returned")
	}
}

// TestResume_QueuedAtShutdownIsRestartedNotDropped is the POSITIVE half, and the
// only test in this file that the PR's policy change actually makes pass.
//
// Shutdown walks r.running only. An op that was enqueued but never dispatched to
// a worker is still status="queued", still carries its opv2:act: key, and IS
// therefore visible to the next startup's resumeAfterStartup. That is the one
// path on which a maintenance job's declared ResumePolicy is consulted after a
// clean restart -- and it is the common shape for a scheduled job enqueued just
// before a deploy.
//
// Under the old ResumeDrop this row was written interrupted_dropped and thrown
// away. Under ResumeRestart it is re-queued with resume_count incremented and
// runs. This test therefore FAILS at b3bf412f6 and PASSES at HEAD.
func TestResume_QueuedAtShutdownIsRestartedNotDropped(t *testing.T) {
	store := newFakeStore()
	// One worker, so the second op cannot be picked up while the first blocks.
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

	target := makeValidDef("test.queued-target")
	target.ResumePolicy = registry.ResumeRestart
	target.ProgressTimeout = 30 * time.Minute
	target.Run = func(context.Context, json.RawMessage, registry.Reporter) error { return nil }
	if err := r1.RegisterOp(target); err != nil {
		t.Fatalf("register target: %v", err)
	}

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

	targetID, err := r1.EnqueueOp(ctx, "test.queued-target", nil)
	if err != nil {
		t.Fatalf("enqueue target: %v", err)
	}
	// LOAD-BEARING: the target must still be QUEUED at shutdown. If the single
	// worker ever picked it up, this test would be measuring the running path.
	if got := store.statusOf(targetID); got != "queued" {
		t.Fatalf("target status=%q before shutdown, want %q; fixture invalid", got, "queued")
	}

	if err := r1.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}

	// The queued row survives the shutdown untouched and stays in the active set.
	if got := store.statusOf(targetID); got != "queued" {
		t.Fatalf("target status=%q after shutdown, want %q", got, "queued")
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

// TestMaintenancePolicy_RestartNeverCheckpoints is the missing coverage for the
// consequence side of this PR, and it is deliberately expressed against the
// registry rather than the job list so it cannot drift.
//
// maintenance.ProgressReporter is {SetTotal, Increment, Log} -- it has no
// Checkpoint method, so no maintenance job can call one. LastCheckpointAt and
// HighWaterProgress are written ONLY by UpdateOpCheckpointV2, whose only caller
// is dbReporter.Checkpoint. A maintenance op on ResumeRestart therefore sits at
// HighWaterProgress==0 with LastCheckpointAt==nil forever, which is exactly the
// state checkInfiniteRestart (worker.go:525) force-drops at ResumeCount>=3 and
// the watchdog (watchdog.go:154) strikes as uncheckpointed every 5 minutes.
//
// This models a maintenance job by reporting progress WITHOUT checkpointing --
// the only thing a maintenance job can do.
func TestMaintenancePolicy_RestartNeverCheckpoints(t *testing.T) {
	t.Run("uncheckpointed strike fires for a progress-only ResumeRestart op", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		store := newFakeStore()
		r := registry.NewWithOptions(store, slog.Default(), 2, registry.Options{
			WatchdogInterval: 50 * time.Millisecond,
		})

		started := make(chan struct{})
		def := makeValidDef("test.maint-progress-only")
		def.ResumePolicy = registry.ResumeRestart
		def.ProgressTimeout = 30 * time.Minute
		def.Run = func(runCtx context.Context, _ json.RawMessage, rep registry.Reporter) error {
			// Exactly what maintenance.ProgressAdapter.Increment does. Note it
			// does NOT and CANNOT call rep.Checkpoint.
			_ = rep.UpdateProgress(1, 100, "")
			close(started)
			<-runCtx.Done()
			return runCtx.Err()
		}
		if err := r.RegisterOp(def); err != nil {
			t.Fatalf("register: %v", err)
		}
		r.Start(ctx)

		opID, err := r.EnqueueOp(ctx, "test.maint-progress-only", nil)
		if err != nil {
			t.Fatalf("enqueue: %v", err)
		}
		<-started

		// A maintenance job with a 4h timeout trivially exceeds the 5m floor.
		stale := time.Now().UTC().Add(-6 * time.Minute)
		store.setStartedAt(opID, &stale)
		_ = store.UpdateOperationV2Status(opID, "running", nil, nil, nil)

		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			if len(store.strikesOfKind(opID, "uncheckpointed")) > 0 {
				// Reporting progress did not clear it: only Checkpoint can, and
				// maintenance jobs have no way to call it.
				row, _ := store.GetOperationV2(opID)
				if row != nil && row.HighWaterProgress != 0 {
					t.Fatalf("high_water_progress=%d without a Checkpoint call; "+
						"if progress now advances the HWM, checkInfiniteRestart no longer "+
						"force-drops maintenance jobs and this test should be revisited",
						row.HighWaterProgress)
				}
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("no uncheckpointed strike within 2s; the six jobs this PR moved to " +
			"ResumeRestart were expected to start accruing these")
	})
}
