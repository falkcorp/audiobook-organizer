// file: core/tasks/taskstest/parity/cases_resume.go
// version: 1.0.0
// guid: 5ba76151-aef1-416e-8653-81c6225b4897
// last-edited: 2026-11-10

package parity

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/tasks"
)

const settle = 300 * time.Millisecond

// wantCalls asserts the probe's body ran n times (after a settle, so a
// double dispatch would show).
func wantCalls(t *testing.T, k *Kit, p *Probe, n int32) {
	t.Helper()
	k.Quiet(settle)
	if got := p.Calls.Load(); got != n {
		t.Fatalf("%s body ran %d times, want %d", p.Kind, got, n)
	}
}

func resumeCases() []Case {
	const f = "resume_test.go:"
	const fs = "resume_shutdown_roundtrip_test.go:"
	return []Case{
		{
			Name: "DropLeavesInterruptedDropped", Head: f + "TestResume_DropLeavesInterruptedDropped",
			Setup:    "a running row of a ResumeDrop kind left by a dead process",
			Action:   "boot an executor over the store",
			Expected: "the run is interrupted_dropped and its body never ran",
			Run: func(t *testing.T, k *Kit) {
				p := k.Probe("gotham.patrol")
				id := k.Plant(p.Kind, tasks.StateRunning)
				k.Boot(Defs(p.Def(Resume(tasks.ResumeDrop))))
				k.WaitState(id, tasks.StateInterruptedDropped)
				wantCalls(t, k, p, 0)
			},
		},
		{
			Name: "AskLeavesAwaitingDecision", Head: f + "TestResume_AskLeavesInterruptedAsk",
			Setup:    "a running row of a ResumeAsk kind",
			Action:   "boot an executor",
			Expected: "the run is awaiting_decision and its body did not run",
			Run: func(t *testing.T, k *Kit) {
				p := k.Probe("gotham.patrol")
				id := k.Plant(p.Kind, tasks.StateRunning)
				k.Boot(Defs(p.Def(Resume(tasks.ResumeAsk))))
				k.WaitState(id, tasks.StateAwaitingDecision)
				wantCalls(t, k, p, 0)
			},
		},
		{
			Name: "ContinueRedispatchesOnceWithNextAttempt", Head: f + "TestResume_RestartReDispatchesWithIncrementedResumeCount",
			Setup:    "a running row (attempt 1) of a ResumeContinue kind",
			Action:   "boot an executor",
			Expected: "the body runs exactly once, as a resumed attempt 2 of the same run, and succeeds",
			Run: func(t *testing.T, k *Kit) {
				p := k.Probe("gotham.patrol")
				id := k.Plant(p.Kind, tasks.StateRunning)
				k.Boot(Defs(p.Def()))
				r := k.WaitState(id, tasks.StateSucceeded)
				wantCalls(t, k, p, 1)
				if r.Attempt != 2 {
					t.Errorf("attempt = %d, want 2 (planted 1 + 1 resume)", r.Attempt)
				}
				if !<-p.Resumed {
					t.Error("rc.Resumed() = false on a resumed attempt")
				}
				if n := len(k.List([]tasks.Kind{p.Kind})); n != 1 {
					t.Errorf("%d runs of the kind, want the one planted", n)
				}
			},
		},
		{
			Name: "ResumeDoesNotInheritStaleLiveness", Head: f + "TestResume_RestartDoesNotInheritStaleLiveness",
			Setup:    "a running row started 3h ago, ProgressTimeout 1s, watchdog every 20ms",
			Action:   "boot; the resumed body works for 80ms before reporting progress",
			Expected: "the run succeeds: the watchdog's clock restarted with the new attempt",
			Run: func(t *testing.T, k *Kit) {
				p := k.Probe("gotham.patrol").Body(func(_ *Probe, rc *tasks.RC, _ Params) (string, error) {
					time.Sleep(80 * time.Millisecond)
					rc.Progress().Set(1, tasks.Known(1))
					return "done", nil
				})
				id := k.Plant(p.Kind, tasks.StateRunning, func(r *tasks.RunRecord) { r.Started = time.Now().Add(-3 * time.Hour) })
				k.Boot(Defs(p.Def(ProgressTimeout(time.Second))), Cfg(func(c *executorConfig) { c.WatchdogInterval = 20 * time.Millisecond }))
				k.WaitState(id, tasks.StateSucceeded)
			},
		},
		{
			Name: "FromZeroRerunsTheSameRun", Head: f + "TestResume_RequeueFreshRun",
			Setup:    "a running row (with a stored checkpoint) of a ResumeFromZero kind",
			Action:   "boot",
			Expected: "the body runs once from zero (nothing restored) as attempt 2 of the same run; no second row exists",
			Run: func(t *testing.T, k *Kit) {
				p := k.Probe("gotham.patrol").Body(func(_ *Probe, rc *tasks.RC, _ Params) (string, error) {
					var seen struct{ N int }
					if ok, err := rc.Restore(&seen); err != nil || ok {
						return "", fmt.Errorf("restored a voided checkpoint (%v, %v)", ok, err)
					}
					return "from zero", nil
				})
				id := k.Plant(p.Kind, tasks.StateRunning)
				k.PlantCheckpoint(id, "core.rc", `{"N":9}`)
				k.Boot(Defs(p.Def(Resume(tasks.ResumeFromZero))))
				r := k.WaitState(id, tasks.StateSucceeded)
				if r.Attempt != 2 {
					t.Errorf("attempt = %d, want 2", r.Attempt)
				}
				if n := len(k.List([]tasks.Kind{p.Kind})); n != 1 {
					t.Errorf("%d runs of the kind, want 1 (no replacement row)", n)
				}
			},
		},
		{
			Name: "ReconcileDropsThroughItsDefinition", Head: f + "TestResume_ReconcileScanRowsAreDroppedThroughTheirDef",
			Setup:    "a running row of a ResumeDrop kind",
			Action:   "boot",
			Expected: "interrupted_dropped (the current-ID half; the former-ID alias half is excluded)",
			Run: func(t *testing.T, k *Kit) {
				p := k.Probe("arkham.reconcile")
				id := k.Plant(p.Kind, tasks.StateRunning)
				k.Boot(Defs(p.Def(Resume(tasks.ResumeDrop))))
				k.WaitState(id, tasks.StateInterruptedDropped)
			},
		},
		{
			Name: "UnregisteredKindIsDroppedAsUnknown", Head: f + "TestResume_RowUnderTheTaskNameReconcileScanIsDroppedAsUnknown",
			Setup:    "a running row whose kind no definition registers",
			Action:   "boot",
			Expected: "interrupted_dropped",
			Run: func(t *testing.T, k *Kit) {
				other := k.Probe("gotham.patrol")
				id := k.Plant("arkham.reconcile_scan", tasks.StateRunning)
				k.Boot(Defs(other.Def()))
				k.WaitState(id, tasks.StateInterruptedDropped)
			},
		},
		{
			Name: "ParamsSurviveContinueAndFromZero", Head: f + "TestResume_PreservesParamsAcrossRestartAndRequeue",
			Setup:    "a running row carrying params {job_id, dry_run:true}, under each of two policies",
			Action:   "boot",
			Expected: "the body receives params that decode to the originals, dry_run still true",
			Run: func(t *testing.T, k *Kit) {
				for _, pol := range []struct {
					name string
					p    tasks.ResumePolicy
				}{{"continue", tasks.ResumeContinue}, {"from_zero", tasks.ResumeFromZero}} {
					k.Sub(pol.name, func(t *testing.T, k *Kit) {
						p := k.Probe("wayne.cleanup")
						want := Params{JobID: "cleanup-empty-folders", DryRun: true}
						id := k.Plant(p.Kind, tasks.StateRunning, WithParams(want))
						k.Boot(Defs(p.Def(Resume(pol.p))))
						k.WaitState(id, tasks.StateSucceeded)
						if got := <-p.Seen; got != want {
							t.Errorf("params = %+v, want %+v", got, want)
						}
					})
				}
			},
		},
		{
			Name: "ShutdownThenRebootResumesTheRun", Head: fs + "TestResume_RealShutdownIsResumedByTheSweep",
			Setup:    "a ResumeContinue run genuinely running; Shutdown cancels it",
			Action:   "inspect the row, then boot a second executor over the same store",
			Expected: "the row is interrupted (not canceled, not quiesced), is not unfinished-in-flight but a resume candidate, and the second boot reruns it to succeeded",
			Run: func(t *testing.T, k *Kit) {
				p := k.Probe("gotham.patrol").Blocks()
				k.Boot(Defs(p.Def()))
				id := k.Start(p.Kind, nil)
				k.WaitStarted(p)
				if err := k.X.Shutdown(context.Background()); err != nil {
					t.Fatalf("Shutdown: %v", err)
				}
				r := k.Get(id)
				if r.State != tasks.StateInterrupted {
					t.Fatalf("after Shutdown the run is %s (reason %q), want interrupted", r.State, r.Reason)
				}
				if got := <-p.Cause; !isShutdown(got) {
					t.Errorf("run saw cause %v, want the shutdown cause", got)
				}
				found := false
				for _, u := range k.Unfinished() {
					found = found || u.ID == id
				}
				if !found {
					t.Error("the interrupted run is not a resume candidate")
				}
				second := k.Probe("gotham.patrol")
				k.Boot(Defs(second.Def()))
				k.WaitState(id, tasks.StateSucceeded)
				wantCalls(t, k, second, 1)
			},
		},
		{
			Name: "ShutdownTimeoutLeavesTheRunResumable", Head: fs + "TestResume_ShutdownTimeoutLeavesQuiescedAndResumable",
			Setup:    "a ResumeContinue run whose body ignores cancellation",
			Action:   "Shutdown with a 200ms deadline; free the body; reboot",
			Expected: "Shutdown returns after the bound; the run is interrupted and a resume candidate; the next boot reruns it",
			Run: func(t *testing.T, k *Kit) {
				p := k.Probe("gotham.patrol").Stubborn()
				k.Boot(Defs(p.Def()), Cfg(func(c *executorConfig) { c.ShutdownDrain = 200 * time.Millisecond }))
				id := k.Start(p.Kind, nil)
				k.WaitStarted(p)
				ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
				defer cancel()
				done := make(chan error, 1)
				go func() { done <- k.X.Shutdown(ctx) }()
				select {
				case err := <-done:
					if err != nil && err != context.DeadlineExceeded {
						t.Errorf("Shutdown = %v, want nil or DeadlineExceeded", err)
					}
				case <-time.After(10 * time.Second):
					t.Fatal("Shutdown did not return within its bound")
				}
				if r := k.Get(id); r.State != tasks.StateInterrupted {
					t.Fatalf("state = %s (reason %q), want interrupted", r.State, r.Reason)
				}
				p.Free()
				k.Quiet(time.Second)
				second := k.Probe("gotham.patrol")
				k.Boot(Defs(second.Def()))
				k.WaitState(id, tasks.StateSucceeded)
			},
		},
		{
			Name: "QueuedAtShutdownIsResumedNotDropped", Head: fs + "TestResume_QueuedAtShutdownIsRestartedNotDropped",
			Setup:    "a first executor ran and shut down; a queued ResumeContinue row is then planted",
			Action:   "boot a second executor",
			Expected: "the planted run executes (not dropped) and succeeds; a ResumeDrop blocker kind registered beside it ends interrupted_dropped when it was running",
			Run: func(t *testing.T, k *Kit) {
				blocker := k.Probe("gotham.lockdown").Blocks()
				k.Boot(Defs(blocker.Def(Resume(tasks.ResumeDrop))))
				bid := k.Start(blocker.Kind, nil)
				k.WaitStarted(blocker)
				if err := k.X.Shutdown(context.Background()); err != nil {
					t.Fatalf("Shutdown: %v", err)
				}
				target := k.Probe("gotham.patrol")
				id := k.Plant(target.Kind, tasks.StateQueued)
				blocker2 := k.Probe("gotham.lockdown").Blocks()
				k.Boot(Defs(target.Def(), blocker2.Def(Resume(tasks.ResumeDrop))))
				k.WaitState(id, tasks.StateSucceeded)
				wantCalls(t, k, target, 1)
				k.WaitState(bid, tasks.StateInterruptedDropped)
			},
		},
	}
}
