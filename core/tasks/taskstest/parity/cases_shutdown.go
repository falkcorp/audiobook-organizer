// file: core/tasks/taskstest/parity/cases_shutdown.go
// version: 1.0.0
// guid: 6cad6633-01f2-443b-a782-255b8bf3e4e5
// last-edited: 2026-11-10

package parity

import (
	"context"
	"errors"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/tasks"
)

// shutdownDrain sets the executor's drain bound.
func shutdownDrain(d time.Duration) Opt {
	return Cfg(func(c *executorConfig) { c.ShutdownDrain = d })
}

// shutdownWithin runs Shutdown(ctx) and fails if it has not returned after
// limit (fake time). It returns how long it took and Shutdown's error.
func (k *Kit) shutdownWithin(ctx context.Context, limit time.Duration) (time.Duration, error) {
	k.T.Helper()
	start := time.Now()
	done := make(chan error, 1)
	go func() { done <- k.X.Shutdown(ctx) }()
	select {
	case err := <-done:
		return time.Since(start), err
	case <-time.After(limit):
		k.T.Fatalf("Shutdown had not returned after %s", limit)
		return 0, nil
	}
}

// okShutdownErr accepts the two results a bounded Shutdown may give.
func okShutdownErr(t *testing.T, err error) {
	t.Helper()
	if err != nil && !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Shutdown = %v, want nil or DeadlineExceeded", err)
	}
}

func shutdownCases() []Case {
	const fd = "shutdown_drain_test.go:"
	const fc = "shutdown_cause_test.go:"
	const fr = "shutdown_race_test.go:"
	const ft = "shutdown_terminal_status_test.go:"
	const fx = "dispatch_shutdown_race_test.go:"
	const fs = "sweeptick_shutdown_race_test.go:"
	return []Case{
		{
			Name: "ShutdownIsBoundedForAnUncooperativeRun", Head: fd + "TestShutdown_WaitsForOpGoroutineToExit",
			Setup:    "a run whose body ignores its context; ShutdownDrain 300ms; an unbounded caller context",
			Action:   "Shutdown(Background)",
			Expected: "Shutdown returns after the drain bound although the body goroutine still lives; the run is stamped interrupted with its fence revoked; once freed the goroutine exits",
			Run: func(t *testing.T, k *Kit) {
				p := k.Probe("gotham.patrol").Stubborn()
				k.Boot(Defs(p.Def()), shutdownDrain(300*time.Millisecond))
				id := k.Start(p.Kind, nil)
				k.WaitStarted(p)
				before := k.Get(id)
				took, err := k.shutdownWithin(context.Background(), 10*time.Second)
				if err != nil {
					t.Errorf("Shutdown = %v, want nil", err)
				}
				if took > 5*time.Second {
					t.Errorf("Shutdown took %s, want about the 300ms drain", took)
				}
				if p.Exited.Load() {
					t.Error("the body had exited before it was released")
				}
				r := k.Get(id)
				if r.State != tasks.StateInterrupted || r.FenceEpoch != before.FenceEpoch+1 {
					t.Errorf("run is %s fence %d, want interrupted fence %d", r.State, r.FenceEpoch, before.FenceEpoch+1)
				}
				p.Free()
				k.Quiet(settle)
				if !p.Exited.Load() {
					t.Error("the body never exited after release")
				}
			},
		},
		{
			Name: "ShutdownAndCancelHaveDistinctCauses", Head: fc + "TestShutdownCancelsRunsWithShutdownCause",
			Setup:    "a cooperative run that reports context.Cause of its run context",
			Action:   "once Shutdown, once Cancel",
			Expected: "the cause is the shutdown sentinel for Shutdown and the cancel sentinel for Cancel, never each other's",
			Run: func(t *testing.T, k *Kit) {
				k.Sub("shutdown", func(t *testing.T, k *Kit) {
					p := k.Probe("gotham.patrol").Blocks()
					k.Boot(Defs(p.Def()))
					k.Start(p.Kind, nil)
					k.WaitStarted(p)
					if err := k.X.Shutdown(context.Background()); err != nil {
						t.Fatalf("Shutdown: %v", err)
					}
					got := <-p.Cause
					if !errors.Is(got, tasks.ErrShutdown) || errors.Is(got, tasks.ErrCanceled) {
						t.Errorf("cause %v, want the shutdown sentinel only", got)
					}
				})
				k.Sub("cancel", func(t *testing.T, k *Kit) {
					p := k.Probe("gotham.patrol").Blocks()
					k.Boot(Defs(p.Def()))
					id := k.Start(p.Kind, nil)
					k.WaitStarted(p)
					k.mustCancel(id)
					got := <-p.Cause
					if !errors.Is(got, tasks.ErrCanceled) || errors.Is(got, tasks.ErrShutdown) {
						t.Errorf("cause %v, want the cancel sentinel only", got)
					}
				})
			},
		},
		{
			Name: "ShutdownThenCloseNeverTouchesTheStore", Head: fr + "TestShutdownDrainsDepNotifyGoroutines_RealStore",
			Setup:    "four rounds of 12 quick runs, each over a fresh store",
			Action:   "wait for all to succeed, Shutdown, close the store at once, then let time pass",
			Expected: "every Shutdown returns nil and nothing touches the closed store afterwards (no panic)",
			Run: func(t *testing.T, k *Kit) {
				for round := range 4 {
					k.Sub("round", func(t *testing.T, k *Kit) {
						p := k.Probe("gotham.patrol")
						k.Boot(Defs(p.Def())) // the 12 run one after another: same kind
						var ids []tasks.RunID
						for i := range 12 {
							ids = append(ids, k.Start(p.Kind, Params{Case: "round", Idx: round*100 + i}))
						}
						for _, id := range ids {
							k.WaitState(id, tasks.StateSucceeded)
						}
						if err := k.X.Shutdown(context.Background()); err != nil {
							t.Fatalf("Shutdown: %v", err)
						}
						k.CloseStore()
						k.Quiet(time.Second)
					})
				}
			},
		},
		{
			Name: "ShutdownRecordsAnInterruptedRunResumable", Head: ft + "TestShutdown_InterruptedRunIsRecordedResumable",
			Setup:    "a ResumeContinue run that reports progress then blocks on its context",
			Action:   "Shutdown",
			Expected: "the run is interrupted, not canceled",
			Run: func(t *testing.T, k *Kit) {
				p := k.Probe("gotham.patrol").Body(func(p *Probe, rc *tasks.RC, _ Params) (string, error) {
					rc.Progress().Set(1, tasks.Known(4))
					send(p.Started, rc.RunID())
					<-rc.Context().Done()
					return "", context.Cause(rc.Context())
				})
				k.Boot(Defs(p.Def(ProgressTimeout(30 * time.Minute))))
				id := k.Start(p.Kind, nil)
				k.WaitStarted(p)
				if err := k.X.Shutdown(context.Background()); err != nil {
					t.Fatalf("Shutdown: %v", err)
				}
				if r := k.Get(id); r.State != tasks.StateInterrupted {
					t.Errorf("state = %s (reason %q), want interrupted", r.State, r.Reason)
				}
			},
		},
		{
			Name: "UserCanceledRunStaysCanceledThroughShutdown", Head: ft + "TestShutdown_UserCanceledRunStaysCanceled",
			Setup:    "a cooperative running run",
			Action:   "Cancel, then Shutdown",
			Expected: "the run is canceled, not interrupted; nothing is left to resume",
			Run: func(t *testing.T, k *Kit) {
				p := k.Probe("gotham.patrol").Blocks()
				k.Boot(Defs(p.Def()))
				id := k.Start(p.Kind, nil)
				k.WaitStarted(p)
				k.mustCancel(id)
				if err := k.X.Shutdown(context.Background()); err != nil {
					t.Fatalf("Shutdown: %v", err)
				}
				if r := k.Get(id); r.State != tasks.StateCanceled {
					t.Errorf("state = %s, want canceled", r.State)
				}
				if u := k.Unfinished(); len(u) != 0 {
					t.Errorf("%d unfinished runs remain for the next boot", len(u))
				}
			},
		},
		{
			Name: "DropPolicyRunIsDroppedAtTheNextBoot", Head: ft + "TestShutdown_DropPolicyIsRecordedDropped",
			Setup:    "a ResumeDrop run blocking on its context",
			Action:   "Shutdown, then boot again over the store",
			Expected: "Shutdown stamps it interrupted; the next boot's resume sweep makes it interrupted_dropped and its body does not run again",
			Run: func(t *testing.T, k *Kit) {
				p := k.Probe("gotham.patrol").Blocks()
				k.Boot(Defs(p.Def(Resume(tasks.ResumeDrop))))
				id := k.Start(p.Kind, nil)
				k.WaitStarted(p)
				if err := k.X.Shutdown(context.Background()); err != nil {
					t.Fatalf("Shutdown: %v", err)
				}
				if r := k.Get(id); r.State != tasks.StateInterrupted {
					t.Fatalf("after Shutdown state = %s, want interrupted", r.State)
				}
				second := k.Probe("gotham.patrol")
				k.Boot(Defs(second.Def(Resume(tasks.ResumeDrop))))
				k.WaitState(id, tasks.StateInterruptedDropped)
				wantCalls(t, k, second, 0)
			},
		},
		{
			Name: "ShutdownDuringTheQueueListingClaimsNothing", Head: fx + "TestDispatchCycle_ShutdownDuringListClaimsNothing",
			Setup:    "a queued run; Shutdown begins from inside the dispatcher's queue listing",
			Action:   "boot",
			Expected: "the run is not started and stays queued; after a reboot it runs once",
			Run: func(t *testing.T, k *Kit) {
				p := k.Probe("gotham.patrol")
				id := k.Plant(p.Kind, tasks.StateQueued)
				var done Box[<-chan error]
				k.Hooks().OnQueued(func() { done.Set(k.GoShutdown(context.Background())) })
				k.Boot(Defs(p.Def()))
				k.Quiet(settle)
				ch, started := done.Get()
				if !started {
					t.Fatal("the dispatcher never listed the queue")
				}
				okShutdownErr(t, <-ch)
				if r := k.Get(id); r.State != tasks.StateQueued || r.Attempt != 0 {
					t.Errorf("run is %s attempt %d, want queued attempt 0", r.State, r.Attempt)
				}
				wantCalls(t, k, p, 0)
				second := k.Probe("gotham.patrol")
				k.Boot(Defs(second.Def()))
				k.WaitState(id, tasks.StateSucceeded)
				wantCalls(t, k, second, 1)
			},
		},
		{
			Name: "ShutdownBeforeTheClaimNeverRuns", Head: fx + "TestExecuteRun_ShutdownBeforePickupNeverRuns",
			Setup:    "a queued run on a shared concurrency key; Shutdown begins at the instant before the claim is written",
			Action:   "boot",
			Expected: "the body never runs, the run is queued again with its attempt count restored, and the concurrency key is not leaked (a sibling runs after a reboot)",
			Run: func(t *testing.T, k *Kit) {
				p, sib := k.Probe("gotham.patrol"), k.Probe("gotham.stakeout")
				id := k.Plant(p.Kind, tasks.StateQueued)
				var done Box[<-chan error]
				k.Hooks().BeforeTransition(claimOf(id), func(TransitionEvent) { done.Set(k.GoShutdown(context.Background())) })
				k.Boot(Defs(p.Def(Key("gotham.watch")), sib.Def(Key("gotham.watch"))))
				k.Quiet(settle)
				ch, started := done.Get()
				if !started {
					t.Fatal("the run was never claimed")
				}
				okShutdownErr(t, <-ch)
				if r := k.Get(id); r.State != tasks.StateQueued || r.Attempt != 0 {
					t.Errorf("run is %s attempt %d, want queued attempt 0", r.State, r.Attempt)
				}
				wantCalls(t, k, p, 0)
				sid := k.Plant(sib.Kind, tasks.StateQueued)
				k.Boot(Defs(p.Def(Key("gotham.watch")), sib.Def(Key("gotham.watch"))))
				k.WaitState(id, tasks.StateSucceeded)
				k.WaitState(sid, tasks.StateSucceeded)
			},
		},
		{
			Name: "ShutdownDuringTheClaimNeverRuns", Head: fx + "TestExecuteRun_ShutdownDuringPickupNeverRuns",
			Setup:    "a queued run; Shutdown begins right after the claim is staged, before the run starts",
			Action:   "boot",
			Expected: "the body never runs; the run is queued with its attempt restored",
			Run: func(t *testing.T, k *Kit) {
				p := k.Probe("gotham.patrol")
				id := k.Plant(p.Kind, tasks.StateQueued)
				var done Box[<-chan error]
				k.Hooks().AfterTransition(claimOf(id), func(TransitionEvent) { done.Set(k.GoShutdown(context.Background())) })
				k.Boot(Defs(p.Def()))
				k.Quiet(settle)
				ch, started := done.Get()
				if !started {
					t.Fatal("the run was never claimed")
				}
				okShutdownErr(t, <-ch)
				if r := k.Get(id); r.State != tasks.StateQueued || r.Attempt != 0 {
					t.Errorf("run is %s attempt %d, want queued attempt 0", r.State, r.Attempt)
				}
				wantCalls(t, k, p, 0)
			},
		},
		{
			Name: "ShutdownLeavesAnUnstartedRunQueued", Head: fx + "TestShutdown_ReleasesBufferedClaimWithoutStatus",
			Setup:    "a queued ResumeDrop run that the dispatcher holds back (the executor is paused)",
			Action:   "Shutdown with a 10s bound",
			Expected: "Shutdown returns nil promptly; the run stays queued (neither interrupted nor dropped); its body never ran",
			Run: func(t *testing.T, k *Kit) {
				p := k.Probe("gotham.patrol")
				k.Boot(Defs(p.Def(Resume(tasks.ResumeDrop))))
				if err := k.X.PauseAs("owner review", tasks.Actor{UserID: "alfred", AuthMethod: "session"}); err != nil {
					t.Fatalf("PauseAs: %v", err)
				}
				id := k.Plant(p.Kind, tasks.StateQueued) // after the pause, so no pass can have started it
				k.Quiet(settle)
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				took, err := k.shutdownWithin(ctx, 20*time.Second)
				if err != nil || took > 5*time.Second {
					t.Errorf("Shutdown = %v after %s, want nil promptly", err, took)
				}
				if r := k.Get(id); r.State != tasks.StateQueued {
					t.Errorf("state = %s, want queued", r.State)
				}
				wantCalls(t, k, p, 0)
			},
		},
		{
			Name: "ShutdownTimeoutStampsTheLiveRunAndSparesTheQueuedOne", Head: fx + "TestShutdown_TimeoutReleasesStubsAndInterruptsRuns",
			Setup:    "a ResumeDrop run whose body ignores cancellation, and a second queued run held back behind it on the same concurrency key",
			Action:   "Shutdown with a 200ms bound, free the body, boot again",
			Expected: "Shutdown returns after the bound; the live run is stamped interrupted, the never-started run is still queued untouched; the next boot drops the first and runs the second",
			Run: func(t *testing.T, k *Kit) {
				live, held := k.Probe("gotham.patrol").Stubborn(), k.Probe("gotham.stakeout")
				defs := func(l, h *Probe) []tasks.Definition {
					return Defs(l.Def(Resume(tasks.ResumeDrop), Key("gotham.watch")), h.Def(Key("gotham.watch")))
				}
				k.Boot(defs(live, held), shutdownDrain(200*time.Millisecond))
				lid := k.Start(live.Kind, nil)
				k.WaitStarted(live)
				hid := k.Start(held.Kind, nil)
				k.Quiet(settle)
				if got := k.Get(hid).State; got != tasks.StateQueued {
					t.Fatalf("held run is %s, want queued behind the key", got)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
				defer cancel()
				_, err := k.shutdownWithin(ctx, 10*time.Second)
				okShutdownErr(t, err)
				if got := k.Get(lid).State; got != tasks.StateInterrupted {
					t.Errorf("live run is %s, want interrupted", got)
				}
				if got := k.Get(hid); got.State != tasks.StateQueued || got.Attempt != 0 {
					t.Errorf("held run is %s attempt %d, want queued attempt 0", got.State, got.Attempt)
				}
				live.Free()
				k.Quiet(settle)
				l2, h2 := k.Probe("gotham.patrol"), k.Probe("gotham.stakeout")
				k.Boot(defs(l2, h2))
				k.WaitState(lid, tasks.StateInterruptedDropped)
				k.WaitState(hid, tasks.StateSucceeded)
			},
		},
		{
			Name: "ShutdownTimeoutStampsACanceledRunCanceled", Head: fx + "TestShutdown_TimeoutStampsAUserCanceledRunCanceled",
			Setup:    "a ResumeContinue run that ignores cancellation, already canceled by the user (stopping)",
			Action:   "Shutdown with a 200ms bound",
			Expected: "Shutdown returns after the bound; the run is stamped canceled, not interrupted, and stays so after the body exits; it is not a resume candidate",
			Run: func(t *testing.T, k *Kit) {
				p := k.Probe("gotham.patrol").Stubborn()
				k.Boot(Defs(p.Def()), shutdownDrain(200*time.Millisecond))
				id := k.Start(p.Kind, nil)
				k.WaitStarted(p)
				k.mustCancel(id)
				ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
				defer cancel()
				_, err := k.shutdownWithin(ctx, 10*time.Second)
				okShutdownErr(t, err)
				if got := k.Get(id).State; got != tasks.StateCanceled {
					t.Fatalf("state = %s, want canceled", got)
				}
				p.Free()
				k.staysTerminal(id, tasks.StateCanceled)
				if u := k.Unfinished(); len(u) != 0 {
					t.Errorf("%d unfinished runs remain", len(u))
				}
			},
		},
		{
			Name: "ShutdownJoinsAnInFlightSweep", Head: fs + "TestShutdownDrainsSweepTicker_RealStore",
			Setup:    "the dispatcher's waiting_deps listing is parked for 2.5s of fake time",
			Action:   "Shutdown with a 15s bound once the listing is parked, then close the store",
			Expected: "Shutdown returns nil only after the parked listing has finished; closing the store afterwards is safe",
			Run: func(t *testing.T, k *Kit) {
				p := k.Probe("gotham.patrol")
				k.Boot(Defs(p.Def()))
				var inside, finished atomic.Bool
				k.Hooks().OnList(func(q tasks.RunQuery) bool { return slices.Contains(q.States, tasks.StateWaitingDeps) }, func() {
					inside.Store(true)
					time.Sleep(2500 * time.Millisecond)
					finished.Store(true)
				})
				for range 200 {
					if inside.Load() {
						break
					}
					time.Sleep(10 * time.Millisecond)
				}
				if !inside.Load() {
					t.Fatal("the waiting_deps listing never ran")
				}
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				if err := k.X.Shutdown(ctx); err != nil {
					t.Errorf("Shutdown = %v, want nil", err)
				}
				if !finished.Load() {
					t.Error("Shutdown returned while the sweep was still in flight")
				}
				k.CloseStore()
				k.Quiet(time.Second)
			},
		},
		{
			Name: "LeakedExecutorSurvivesAClosedStore", Head: fs + "TestLeakedRegistrySweepDoesNotPanicOnClosedStore",
			Setup:    "a running executor whose store is closed without Shutdown",
			Action:   "let many dispatch and sweep ticks pass, then Shutdown",
			Expected: "no panic; Shutdown returns nil",
			Run: func(t *testing.T, k *Kit) {
				p := k.Probe("gotham.patrol")
				k.Boot(Defs(p.Def()), Cfg(func(c *executorConfig) { c.DispatchTick = 5 * time.Millisecond }))
				k.CloseStore()
				k.Quiet(250 * time.Millisecond)
				if err := k.X.Shutdown(context.Background()); err != nil {
					t.Errorf("Shutdown = %v, want nil", err)
				}
			},
		},
	}
}
