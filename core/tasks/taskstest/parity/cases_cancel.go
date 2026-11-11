// file: core/tasks/taskstest/parity/cases_cancel.go
// version: 1.0.1
// guid: 472e70eb-50aa-44b3-9b02-6f9848ea070a
// last-edited: 2026-11-10

package parity

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/tasks"
)

func (k *Kit) cancel(id tasks.RunID) error { return k.X.Cancel(context.Background(), id) }

func (k *Kit) mustCancel(id tasks.RunID) {
	k.T.Helper()
	if err := k.cancel(id); err != nil {
		k.T.Fatalf("Cancel(%s) = %v, want nil", id, err)
	}
}

func isNotFound(err error) bool { return errors.Is(err, tasks.ErrRunNotFound) }

// toState matches a transition of run id into state.
func toState(id tasks.RunID, s tasks.RunState) func(TransitionEvent) bool {
	return func(ev TransitionEvent) bool { return ev.ID == id && ev.Next.State == s }
}

// claimOf matches the dispatcher's claim of run id (queued -> running).
func claimOf(id tasks.RunID) func(TransitionEvent) bool {
	return func(ev TransitionEvent) bool {
		return ev.ID == id && ev.Want.State == tasks.StateQueued && ev.Next.State == tasks.StateRunning
	}
}

// wantCause asserts the next cause a cooperative body saw.
func wantCause(t *testing.T, p *Probe, want error) {
	t.Helper()
	select {
	case got := <-p.Cause:
		if !errors.Is(got, want) {
			t.Errorf("%s saw cause %v, want %v", p.Kind, got, want)
		}
	case <-time.After(30 * time.Second):
		t.Errorf("%s: no cause seen (its context never ended)", p.Kind)
	}
}

// stays asserts the run keeps state for the settle period.
func (k *Kit) staysTerminal(id tasks.RunID, s tasks.RunState) {
	k.T.Helper()
	k.Stays(id, s, settle)
}

// quiesceVictim returns a Yields kind that blocks on its context and the
// holder kind that stands it down, both booted; the victim is not started.
func (k *Kit) quiescePair() (victim, holder *Probe, held func() bool) {
	victim = k.Probe("gotham.patrol").Blocks()
	h, flag := k.Holder("gotham.lockdown", "gotham.patrol")
	return victim, h, flag.Load
}

func cancelCases() []Case {
	const fp = "cancel_persisted_test.go:"
	const fr = "cancel_requeue_race_test.go:"
	const fc = "cancel_row_cas_test.go:"
	return []Case{
		{
			Name: "PersistedNonRunningRunBecomesCanceled", Head: fp + "TestCancel_PersistedNonRunningRowBecomesCanceled",
			Setup:    "an executor that was never run (no live attempts) and one persisted run in each of interrupted_quiesced, waiting_deps, running, awaiting_decision, interrupted and queued, with params",
			Action:   "Cancel each",
			Expected: "Cancel returns nil; the run is canceled with Finished and a reason set; it is not a resume candidate",
			Run: func(t *testing.T, k *Kit) {
				p := k.Probe("gotham.patrol")
				k.BootNoRun(Defs(p.Def()))
				states := []tasks.RunState{tasks.StateInterruptedQuiesced, tasks.StateWaitingDeps, tasks.StateRunning, tasks.StateAwaitingDecision, tasks.StateInterrupted, tasks.StateQueued}
				ids := map[tasks.RunState]tasks.RunID{}
				for _, s := range states {
					ids[s] = k.Plant(p.Kind, s, WithParams(Params{Idx: 3}))
				}
				for _, s := range states {
					k.mustCancel(ids[s])
					r := k.Get(ids[s])
					if r.State != tasks.StateCanceled || r.Finished.IsZero() || r.Reason == "" {
						t.Errorf("from %s: state %s finished %v reason %q, want canceled, finished, a reason", s, r.State, r.Finished, r.Reason)
					}
				}
				for _, u := range k.Unfinished() {
					t.Errorf("run %s (%s) is still unfinished after Cancel", u.ID, u.State)
				}
			},
		},
		{
			Name: "TerminalRunIsNotFound", Head: fp + "TestCancel_TerminalRowIsNotFound",
			Setup:    "persisted runs in succeeded, failed, canceled, interrupted_dropped and timed_out",
			Action:   "Cancel each",
			Expected: "ErrRunNotFound and the state is unchanged",
			Run: func(t *testing.T, k *Kit) {
				p := k.Probe("gotham.patrol")
				k.BootNoRun(Defs(p.Def()))
				for _, s := range []tasks.RunState{tasks.StateSucceeded, tasks.StateFailed, tasks.StateCanceled, tasks.StateInterruptedDropped, tasks.StateTimedOut} {
					id := k.Plant(p.Kind, s)
					if err := k.cancel(id); !isNotFound(err) {
						t.Errorf("Cancel of a %s run = %v, want ErrRunNotFound", s, err)
					}
					if got := k.Get(id).State; got != s {
						t.Errorf("Cancel rewrote %s to %s", s, got)
					}
				}
			},
		},
		{
			Name: "UnknownIDIsNotFound", Head: fp + "TestCancel_UnknownIDIsNotFound",
			Setup:    "an empty store",
			Action:   "Cancel a well-formed ID nothing was stored under",
			Expected: "ErrRunNotFound",
			Run: func(t *testing.T, k *Kit) {
				p := k.Probe("gotham.patrol")
				k.BootNoRun(Defs(p.Def()))
				id, err := k.Raw().Runs.NewID(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				if err := k.cancel(id); !isNotFound(err) {
					t.Errorf("Cancel(unknown) = %v, want ErrRunNotFound", err)
				}
			},
		},
		{
			Name: "CancelRacingAResumeThatBecameLive", Head: fr + "TestCancel_RequeueClaimedByWorkerBeforeTheCancelWriteCancelsTheRun",
			Setup:    "an interrupted_quiesced run; just before Cancel's write lands, the run is re-queued and a worker claims and starts it",
			Action:   "Cancel(id) with that interleaving armed",
			Expected: "Cancel returns nil; the body ran once and its context ends with the cancel cause; the run ends canceled and stays canceled",
			Run: func(t *testing.T, k *Kit) {
				p := k.Probe("gotham.patrol").Blocks()
				id := k.Plant(p.Kind, tasks.StateInterruptedQuiesced)
				k.PlantStandDown("gotham.patrol", id)
				k.Boot(Defs(p.Def()))
				k.WaitState(id, tasks.StateInterruptedQuiesced)
				k.Hooks().BeforeTransition(toState(id, tasks.StateCanceled), func(TransitionEvent) {
					if err := k.Requeue(id); err != nil {
						t.Errorf("requeue: %v", err)
						return
					}
					k.WaitStarted(p)
				})
				k.mustCancel(id)
				wantCause(t, p, tasks.ErrCanceled)
				k.WaitState(id, tasks.StateCanceled)
				k.staysTerminal(id, tasks.StateCanceled)
				wantCalls(t, k, p, 1)
			},
		},
		{
			Name: "CancelAfterAQuiesceStampStaysCanceled", Head: fc + "TestCancel_LostQueuedCASToThePickupGateCancelsTheQuiescedRow",
			Setup:    "a quiesced run is re-queued, claimed and stood down again by a holder while Cancel's write is in flight",
			Action:   "Cancel, then release the holder",
			Expected: "Cancel returns nil; the run is canceled right after Cancel (not left quiesced); after the hold is released it is not resumed and stays canceled",
			Run: func(t *testing.T, k *Kit) {
				victim, holder, _ := k.quiescePair()
				id := k.Plant(victim.Kind, tasks.StateInterruptedQuiesced)
				k.PlantStandDown("gotham.patrol", id)
				k.Boot(Defs(victim.Def(Yields()), holder.Def()))
				k.WaitState(id, tasks.StateInterruptedQuiesced)
				var hid tasks.RunID
				k.Hooks().BeforeTransition(toState(id, tasks.StateCanceled), func(TransitionEvent) {
					if err := k.Requeue(id); err != nil {
						t.Errorf("requeue: %v", err)
						return
					}
					k.WaitStarted(victim)
					hid = k.Start(holder.Kind, nil)
					k.WaitStarted(holder)
					k.WaitState(id, tasks.StateInterruptedQuiesced)
				})
				k.mustCancel(id)
				if got := k.Get(id).State; got != tasks.StateCanceled {
					t.Fatalf("state right after Cancel = %s, want canceled", got)
				}
				holder.Free()
				k.WaitState(hid, tasks.StateSucceeded)
				k.staysTerminal(id, tasks.StateCanceled)
				wantCalls(t, k, victim, 1)
			},
		},
		{
			Name: "WonCancelIsNotOverwrittenByALateQuiesceStamp", Head: fc + "TestCancel_WonQueuedCASIsNotOverwrittenByThePickupGate",
			Setup:    "a running quiesce-governed run is stood down; the run is stamped canceled at the instant before the worker writes its interrupted_quiesced stamp",
			Action:   "let the late stamp land, then release the hold",
			Expected: "the late stamp is rejected: the run stays canceled, its body ran once, it is not resumed",
			Run: func(t *testing.T, k *Kit) {
				victim, holder, _ := k.quiescePair()
				k.Boot(Defs(victim.Def(Yields()), holder.Def()))
				id := k.Start(victim.Kind, nil)
				k.WaitStarted(victim)
				k.Hooks().BeforeTransition(toState(id, tasks.StateInterruptedQuiesced), func(TransitionEvent) {
					if err := k.RawMove(id, func(r *tasks.RunRecord) { r.State, r.Reason = tasks.StateCanceled, "cancel" }); err != nil {
						t.Errorf("raw cancel: %v", err)
					}
				})
				hid := k.Start(holder.Kind, nil)
				k.WaitStarted(holder)
				k.Quiet(settle)
				if got := k.Get(id).State; got != tasks.StateCanceled {
					t.Fatalf("after the late stamp the run is %s, want canceled", got)
				}
				holder.Free()
				k.WaitState(hid, tasks.StateSucceeded)
				k.staysTerminal(id, tasks.StateCanceled)
				wantCalls(t, k, victim, 1)
			},
		},
		{
			Name: "CancelAtTheClaimStillCancelsTheRun", Head: fc + "TestCancel_LiveHandleAtThePickupGateStillCancelsTheRow",
			Setup:    "a queued run; Cancel is called at the instant the dispatcher claims it",
			Action:   "boot",
			Expected: "Cancel returns nil; the run ends canceled; its body never runs; it is not resumed",
			Run: func(t *testing.T, k *Kit) {
				p := k.Probe("gotham.patrol").Blocks()
				id := k.Plant(p.Kind, tasks.StateQueued)
				var cerr Box[error]
				k.Hooks().BeforeTransition(claimOf(id), func(TransitionEvent) { cerr.Set(k.cancel(id)) })
				k.Boot(Defs(p.Def()))
				k.WaitState(id, tasks.StateCanceled)
				if err, _ := cerr.Get(); err != nil {
					t.Errorf("Cancel at the claim = %v, want nil", err)
				}
				k.staysTerminal(id, tasks.StateCanceled)
				wantCalls(t, k, p, 0)
			},
		},
		{
			Name: "CancelRacingAPromotedWaitingRun", Head: fc + "TestCancel_WaitingDepsPromotedAndClaimedBeforeTheCancelWriteCancelsTheRun",
			Setup:    "a waiting_deps run (NotWhile a blocker); just before Cancel's write the blocker ends, the run is promoted, claimed and started",
			Action:   "Cancel",
			Expected: "Cancel returns nil; the body ran once and its context ends with the cancel cause; the run ends canceled",
			Run: func(t *testing.T, k *Kit) {
				blocker := k.Probe("gotham.lockdown").WaitsRelease()
				p := k.Probe("gotham.patrol").Blocks()
				k.Boot(Defs(blocker.Def(), p.Def(NotWhile("gotham.lockdown"))))
				k.Start(blocker.Kind, nil)
				k.WaitStarted(blocker)
				id := k.Start(p.Kind, nil)
				k.WaitState(id, tasks.StateWaitingDeps)
				k.Hooks().BeforeTransition(toState(id, tasks.StateCanceled), func(TransitionEvent) {
					blocker.Free()
					k.WaitStarted(p)
				})
				k.mustCancel(id)
				wantCause(t, p, tasks.ErrCanceled)
				k.WaitState(id, tasks.StateCanceled)
				wantCalls(t, k, p, 1)
			},
		},
		{
			Name: "CancelAfterTheRunFinishedIsNotFound", Head: fc + "TestCancel_RunFinishedBeforeTheReReadIsNotFound",
			Setup:    "an interrupted_quiesced run; before Cancel's write the run is re-queued, runs and succeeds",
			Action:   "Cancel",
			Expected: "ErrRunNotFound; the run stays succeeded",
			Run: func(t *testing.T, k *Kit) {
				p := k.Probe("gotham.patrol")
				id := k.Plant(p.Kind, tasks.StateInterruptedQuiesced)
				k.PlantStandDown("gotham.patrol", id)
				k.Boot(Defs(p.Def()))
				k.WaitState(id, tasks.StateInterruptedQuiesced)
				k.Hooks().BeforeTransition(toState(id, tasks.StateCanceled), func(TransitionEvent) {
					if err := k.Requeue(id); err != nil {
						t.Errorf("requeue: %v", err)
						return
					}
					k.WaitState(id, tasks.StateSucceeded)
				})
				if err := k.cancel(id); !isNotFound(err) {
					t.Errorf("Cancel = %v, want ErrRunNotFound", err)
				}
				if got := k.Get(id).State; got != tasks.StateSucceeded {
					t.Errorf("state = %s, want succeeded", got)
				}
			},
		},
		{
			Name: "CancelBetweenTheQuiesceDecisionAndItsWrite", Head: fc + "TestCancel_BetweenTheRunsFinalStatusAndItsWriteKeepsTheRowCanceled",
			Setup:    "a running run is stood down; Cancel lands from inside the worker's interrupted_quiesced write",
			Action:   "stand down, then release the hold",
			Expected: "Cancel returns nil; the run ends canceled (not interrupted_quiesced); its body ran once; releasing the hold does not resume it",
			Run: func(t *testing.T, k *Kit) {
				victim, holder, _ := k.quiescePair()
				k.Boot(Defs(victim.Def(Yields()), holder.Def()))
				id := k.Start(victim.Kind, nil)
				k.WaitStarted(victim)
				var cerr Box[error]
				k.Hooks().BeforeTransition(toState(id, tasks.StateInterruptedQuiesced), func(TransitionEvent) { cerr.Set(k.cancel(id)) })
				hid := k.Start(holder.Kind, nil)
				k.WaitStarted(holder)
				k.Quiet(settle)
				holder.Free()
				k.WaitState(hid, tasks.StateSucceeded)
				if err, _ := cerr.Get(); err != nil {
					t.Errorf("Cancel = %v, want nil", err)
				}
				if got := k.Get(id).State; got != tasks.StateCanceled {
					t.Errorf("state = %s, want canceled", got)
				}
				k.Quiet(settle)
				wantCalls(t, k, victim, 1)
			},
		},
		{
			Name: "CancelRacingCompletionKeepsSucceeded", Head: fc + "TestCancel_InTheReleaseGapOfACompletedRunKeepsCompleted",
			Setup:    "a running run whose body is about to return nil; Cancel fires just before the final succeeded write",
			Action:   "free the body",
			Expected: "Cancel returns nil; the run ends succeeded with no leftover cancel reason; the last terminal event is succeeded",
			Run: func(t *testing.T, k *Kit) {
				p := k.Probe("gotham.patrol").WaitsRelease()
				k.Boot(Defs(p.Def()))
				id := k.Start(p.Kind, nil)
				k.WaitStarted(p)
				var cerr Box[error]
				k.Hooks().BeforeTransition(toState(id, tasks.StateSucceeded), func(TransitionEvent) { cerr.Set(k.cancel(id)) })
				p.Free()
				r := k.WaitState(id, tasks.StateSucceeded, tasks.StateCanceled)
				if err, _ := cerr.Get(); err != nil {
					t.Errorf("Cancel = %v, want nil", err)
				}
				if r.State != tasks.StateSucceeded || r.Reason == "cancel" || r.Error != "" {
					t.Errorf("final: state %s reason %q error %q, want succeeded without a cancel reason", r.State, r.Reason, r.Error)
				}
				if last := k.Terminals(id); len(last) == 0 || last[len(last)-1] != tasks.StateSucceeded {
					t.Errorf("terminal events %v, want the last to be succeeded", last)
				}
			},
		},
		{
			Name: "ShutdownStampsACanceledRunCanceled", Head: fc + "TestShutdown_StampedCanceledRunPublishesOpTerminal",
			Setup:    "a running run whose body ignores its context; Cancel leaves it stopping; Shutdown with a 200ms bound",
			Action:   "Cancel, then Shutdown",
			Expected: "after Cancel the run is stopping (not terminal); after Shutdown it is stamped canceled (not interrupted); at least one terminal event is published and every one is canceled",
			Run: func(t *testing.T, k *Kit) {
				p := k.Probe("gotham.patrol").Stubborn()
				k.Boot(Defs(p.Def()), Cfg(func(c *executorConfig) { c.ShutdownDrain = 200 * time.Millisecond }))
				id := k.Start(p.Kind, nil)
				k.WaitStarted(p)
				k.mustCancel(id)
				if got := k.Get(id).State; got != tasks.StateStopping {
					t.Fatalf("after Cancel the run is %s, want stopping", got)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
				defer cancel()
				if err := k.X.Shutdown(ctx); err != nil {
					t.Errorf("Shutdown: %v", err)
				}
				if got := k.Get(id).State; got != tasks.StateCanceled {
					t.Fatalf("after Shutdown the run is %s, want canceled", got)
				}
				p.Free()
				k.Quiet(settle)
				terms := k.Terminals(id)
				if len(terms) == 0 || slices.ContainsFunc(terms, func(s tasks.RunState) bool { return s != tasks.StateCanceled }) {
					t.Errorf("terminal events %v, want at least one and all canceled", terms)
				}
			},
		},
		{
			Name: "LateQuiesceCompletionPublishesNothingOverACancel", Head: fc + "TestCancel_WorkerDoesNotPublishAStaleInterruptedStatus",
			Setup:    "a run is stood down and stamped interrupted_quiesced; Cancel then lands; the hold is released",
			Action:   "Cancel, release the hold, let time pass",
			Expected: "Cancel returns nil; the run is canceled right away; it is never resumed; no interrupted_quiesced terminal event follows the cancel; the last terminal event is canceled",
			Run: func(t *testing.T, k *Kit) {
				victim, holder, _ := k.quiescePair()
				k.Boot(Defs(victim.Def(Yields()), holder.Def()))
				id := k.Start(victim.Kind, nil)
				k.WaitStarted(victim)
				hid := k.Start(holder.Kind, nil)
				k.WaitStarted(holder)
				k.WaitState(id, tasks.StateInterruptedQuiesced)
				k.mustCancel(id)
				if got := k.Get(id).State; got != tasks.StateCanceled {
					t.Fatalf("state right after Cancel = %s, want canceled", got)
				}
				holder.Free()
				k.WaitState(hid, tasks.StateSucceeded)
				k.staysTerminal(id, tasks.StateCanceled)
				wantCalls(t, k, victim, 1)
				terms := k.Terminals(id)
				if len(terms) == 0 || terms[len(terms)-1] != tasks.StateCanceled || slices.Contains(terms, tasks.StateInterruptedQuiesced) {
					t.Errorf("terminal events %v, want a last canceled and no interrupted_quiesced", terms)
				}
			},
		},
	}
}
