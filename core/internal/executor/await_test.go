// file: core/internal/executor/await_test.go
// version: 1.0.2
// guid: 5e0b3f52-8a1d-4c6e-9b47-2d6a1f0c8e93
// last-edited: 2026-11-15

package executor

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/falkcorp/mediacore/tasks"
)

// rc.Await relays the child's liveness to the awaiting run: a parent blocked
// on a child that is still alive is not silent to the watchdog. These tests
// run under synctest (the fake clock) with the watchdog on a 30s pass and
// the parent's watchdog limit (ProgressTimeout) at one minute, so a parent
// that goes silent is struck within 90s.

const (
	awaitParent = "gotham.stakeout"
	awaitChild  = "gotham.longcase"
	awaitLimit  = time.Minute // the parent's ProgressTimeout in these tests
)

// awaitSaw is what a fixture parent saw when its Await returned.
type awaitSaw struct {
	out        tasks.Outcome
	err        error
	returnedAt time.Time
	lastReport time.Time // the parent attempt's liveness stamp as Await returned
	child      tasks.RunID
}

// awaitRig boots an executor with a parent kind that enqueues the child kind
// and awaits it. progTO is the parent's watchdog limit; after (optional)
// runs once Await has returned without error; parentEdits adjust the
// parent's Common.
func awaitRig(t *testing.T, progTO time.Duration, child tasks.Definition, after func(rc *tasks.RC) error,
	parentEdits ...func(*tasks.Common)) (*harness, <-chan awaitSaw) {
	t.Helper()
	h, got, _ := awaitRigDefs(t, progTO, []tasks.Definition{child}, after, parentEdits...)
	return h, got
}

// awaitRigDefs is awaitRig with any definitions beside the parent (the
// child kind among them); refs receives the child's run ID as soon as the
// parent has enqueued it.
func awaitRigDefs(t *testing.T, progTO time.Duration, defs []tasks.Definition, after func(rc *tasks.RC) error,
	parentEdits ...func(*tasks.Common)) (*harness, <-chan awaitSaw, <-chan tasks.RunID) {
	t.Helper()
	got := make(chan awaitSaw, 4)
	refs := make(chan tasks.RunID, 4)
	var h *harness
	edits := append([]func(*tasks.Common){
		func(c *tasks.Common) { c.ProgressTimeout = progTO; c.Cancel = tasks.Cooperative(time.Second) },
		withResume(tasks.ResumeDrop),
	}, parentEdits...)
	parent := taskDef(awaitParent, func(rc *tasks.RC, _ noParams) (string, error) {
		ref, err := rc.Enqueue(awaitChild, noParams{})
		if err != nil {
			return "", err
		}
		refs <- ref.ID
		out, err := rc.Await(ref)
		got <- awaitSaw{out: out, err: err, returnedAt: time.Now(), lastReport: h.lastReportOf(rc.RunID()), child: ref.ID}
		if err != nil {
			return "", err
		}
		if after != nil {
			return "", after(rc)
		}
		return "", nil
	}, edits...)
	h = newHarness(t, append([]tasks.Definition{parent}, defs...), func(o *Options) {
		o.Config = Config{DispatchTick: time.Second, WatchdogInterval: 30 * time.Second}
		o.Catalog = catalogWith(t, "gotham:run") // gotham's system actor may start its own kinds
	})
	return h, got, refs
}

// lastReportOf reads the liveness stamp of run id's live attempt.
func (h *harness) lastReportOf(id tasks.RunID) time.Time {
	h.t.Helper()
	h.e.mu.Lock()
	defer h.e.mu.Unlock()
	a := h.e.running[id]
	if a == nil {
		h.t.Fatalf("run %s has no live attempt", id)
	}
	return time.Unix(0, a.lastReport.Load())
}

// lanternChild is a Task child that works for d, ticking its own liveness
// every 20s (an alive child), then succeeds.
func lanternChild(d time.Duration) tasks.Definition {
	return taskDef(awaitChild, func(rc *tasks.RC, _ noParams) (string, error) {
		end := time.Now().Add(d)
		for time.Now().Before(end) {
			select {
			case <-rc.Context().Done():
				return "", context.Cause(rc.Context())
			case <-time.After(min(20*time.Second, time.Until(end))):
				rc.Progress().Tick()
			}
		}
		return "case closed", nil
	}, func(c *tasks.Common) {
		c.Effects = tasks.Writes(tasks.Res("gotham", "lab")) // disjoint from the parent's write set
		c.ProgressTimeout = time.Hour
	})
}

// silentChild never reports and never ends on its own; its own watchdog
// limit is progTO.
func silentChild(progTO time.Duration) tasks.Definition {
	return taskDef(awaitChild, func(rc *tasks.RC, _ noParams) (string, error) {
		<-rc.Context().Done()
		return "", context.Cause(rc.Context())
	}, func(c *tasks.Common) {
		c.Effects = tasks.Writes(tasks.Res("gotham", "lab"))
		c.ProgressTimeout = progTO
		c.Cancel = tasks.Cooperative(time.Second)
		c.Resume = tasks.ResumeDrop
	})
}

// settleGoroutines fails the test when goroutines outlive base once the
// bubble is quiet.
func settleGoroutines(t *testing.T, base int) {
	t.Helper()
	synctest.Wait()
	if n := runtime.NumGoroutine(); n > base {
		t.Errorf("%d goroutines after the runs ended, want at most the %d from before", n, base)
	}
}

// TestAwait_ParentSurvivesChildLongerThanItsWatchdogLimit: a parent whose
// ProgressTimeout is one minute awaits a child that works for five. The
// executor ticks the parent's liveness while the child is alive, so the
// watchdog never strikes the parent and Await returns the child's outcome.
func TestAwait_ParentSurvivesChildLongerThanItsWatchdogLimit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, got := awaitRig(t, awaitLimit, lanternChild(5*time.Minute), nil)
		base := runtime.NumGoroutine()
		began := time.Now()
		parent := h.start(awaitParent, tasks.StartRequest{})
		// Sample the parent's stamp age while it waits. A store read takes no
		// fake time, so a relay period equal to the limit would still pass the
		// watchdog's gap <= limit test here; the age itself must stay a
		// third of the limit, within one dispatch tick.
		var oldest time.Duration
		for time.Since(began) < 4*time.Minute+50*time.Second {
			time.Sleep(5 * time.Second)
			oldest = max(oldest, time.Since(h.lastReportOf(parent)))
		}
		if limit := awaitLimit / 3; oldest > limit+time.Second {
			t.Errorf("oldest parent stamp seen while waiting was %s old, want at most %s (the limit %s over three, plus a dispatch tick)", oldest, limit+time.Second, awaitLimit)
		}
		time.Sleep(70 * time.Second)
		synctest.Wait()
		r := h.get(parent)
		if r.State != tasks.StateSucceeded {
			t.Fatalf("parent after a 5m child with a 1m watchdog limit: %s/%q (%q), want succeeded", r.State, r.Reason, r.Error)
		}
		if s := h.strikesOf(parent); len(s) != 0 {
			t.Errorf("parent strikes %v, want none", s)
		}
		saw := <-got
		if saw.err != nil || saw.out.State != tasks.StateSucceeded {
			t.Errorf("Await returned %s, %v, want the child's succeeded outcome", saw.out.State, saw.err)
		}
		if waited := saw.returnedAt.Sub(began); waited < 5*time.Minute {
			t.Errorf("Await returned after %s, want it to have waited out the 5m child", waited)
		}
		if c := h.get(saw.child); c.State != tasks.StateSucceeded {
			t.Errorf("child %s, want succeeded", c.State)
		}
		settleGoroutines(t, base)
	})
}

// TestAwait_TicksStopWhenAwaitReturns: once the child is done and Await
// returns, nothing keeps stamping the parent. A parent that then goes quiet
// is struck within its limit (never_reported: waiting is not reporting);
// and the stamp it carried as Await
// returned was written while the child was still alive, never at the moment
// the child's terminal state was read.
func TestAwait_TicksStopWhenAwaitReturns(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		quiet := func(rc *tasks.RC) error {
			<-rc.Context().Done()
			return context.Cause(rc.Context())
		}
		// 5m07s is no multiple of the 20s tick, so the last tick falls
		// strictly before the child's end.
		h, got := awaitRig(t, time.Minute, lanternChild(5*time.Minute+7*time.Second), quiet)
		base := runtime.NumGoroutine()
		parent := h.start(awaitParent, tasks.StartRequest{})
		saw := func() awaitSaw {
			time.Sleep(5*time.Minute + 8*time.Second)
			synctest.Wait()
			return <-got
		}()
		if saw.err != nil || saw.out.State != tasks.StateSucceeded {
			t.Fatalf("Await returned %s, %v, want succeeded", saw.out.State, saw.err)
		}
		if !saw.lastReport.Before(saw.returnedAt) {
			t.Errorf("parent stamped at %s as Await returned at %s: a stamp at the moment the child was found terminal means ticks outlive the child",
				saw.lastReport.Format(time.TimeOnly), saw.returnedAt.Format(time.TimeOnly))
		}
		time.Sleep(2 * time.Minute) // limit 1m, pass every 30s
		synctest.Wait()
		r := h.get(parent)
		if r.State != tasks.StateCanceled || r.Reason != reasonWatchdog {
			t.Fatalf("quiet parent after Await: %s/%q, want canceled/watchdog (ticks must stop with Await)", r.State, r.Reason)
		}
		if s := h.strikesOf(parent); !equalStrings(s, []string{strikeNeverReported}) {
			t.Errorf("strikes %v, want [never_reported]: the relay stamps liveness, it does not make the run a reporter", s)
		}
		settleGoroutines(t, base)
	})
}

// TestAwait_CanceledParentReturnsTheCancelAndStopsWaiting: Cancel on a
// parent blocked in Await ends the wait with the cancel cause, leaves the
// parent canceled by that cancel (not by the watchdog), and leaks nothing.
// The child belongs to its own lifecycle and is canceled separately.
func TestAwait_CanceledParentReturnsTheCancelAndStopsWaiting(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, got := awaitRig(t, time.Minute, silentChild(time.Hour), nil)
		base := runtime.NumGoroutine()
		parent := h.start(awaitParent, tasks.StartRequest{})
		// Past the 1m limit, so only the relayed ticks keep the parent alive,
		// and 10s after a 20s relay tick: the cancel must end the wait itself,
		// not wait for the next tick.
		time.Sleep(110 * time.Second)
		synctest.Wait()
		if r := h.get(parent); r.State != tasks.StateRunning {
			t.Fatalf("parent at 110s: %s/%q, want running (alive child)", r.State, r.Reason)
		}
		canceledAt := time.Now()
		if err := h.x.Cancel(context.Background(), parent); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Second)
		synctest.Wait()
		saw := <-got
		if !errors.Is(saw.err, tasks.ErrCanceled) {
			t.Errorf("Await err %v, want the cancel cause", saw.err)
		}
		// The 1s cancel grace would end the run's row (and wake every waiter)
		// a second later regardless; the wait itself must end at the cancel.
		if lag := saw.returnedAt.Sub(canceledAt); lag > 100*time.Millisecond {
			t.Errorf("Await returned %s after the cancel, want it to end with the context", lag)
		}
		if r := h.get(parent); r.State != tasks.StateCanceled || r.Reason != reasonCancel {
			t.Errorf("parent %s/%q, want canceled/cancel", r.State, r.Reason)
		}
		if err := h.x.Cancel(context.Background(), saw.child); err != nil {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Second)
		settleGoroutines(t, base)
	})
}

// TestAwait_StalledChildDoesNotKeepParentAlivePastItsOwnStop: the child
// reports nothing and its own watchdog (limit 2m) stops it. The parent
// (limit 1m) is kept alive while the child lives, and as soon as the child
// is stopped Await returns the child's canceled outcome and the parent goes
// on: no strike, and it ends within a second of the child.
func TestAwait_StalledChildDoesNotKeepParentAlivePastItsOwnStop(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, got := awaitRig(t, time.Minute, silentChild(2*time.Minute), nil)
		base := runtime.NumGoroutine()
		parent := h.start(awaitParent, tasks.StartRequest{})
		time.Sleep(5 * time.Minute)
		synctest.Wait()
		saw := <-got
		child := h.get(saw.child)
		if child.State != tasks.StateCanceled || child.Reason != reasonWatchdog {
			t.Fatalf("stalled child %s/%q, want canceled/watchdog", child.State, child.Reason)
		}
		if saw.err != nil || saw.out.State != tasks.StateCanceled {
			t.Errorf("Await returned %s, %v, want the child's canceled outcome", saw.out.State, saw.err)
		}
		r := h.get(parent)
		if r.State != tasks.StateSucceeded {
			t.Fatalf("parent %s/%q (%q), want succeeded: alive until the child's stop, then free to proceed", r.State, r.Reason, r.Error)
		}
		if s := h.strikesOf(parent); len(s) != 0 {
			t.Errorf("parent strikes %v, want none", s)
		}
		if lag := r.Finished.Sub(child.Finished); lag < 0 || lag > time.Second {
			t.Errorf("parent ended %s after the child, want within 1s: it must not outlive the child's stop", lag)
		}
		if s := h.strikesOf(saw.child); len(s) != 1 {
			t.Errorf("child strikes %v, want one", s)
		}
		settleGoroutines(t, base)
	})
}

// TestAwait_ParentIsBoundedByItsOwnDeadline: a child that is alive but never
// finishes (no watchdog of its own that fires) cannot hold the parent past
// the parent's attempt Timeout: Await ends at the deadline with the timeout
// cause, and the run ends timed out. With a 20s relay tick, a 10m10s deadline
// falls 10s after a tick, so the deadline itself must end the wait (the
// abandon after the 1s grace would wake it a second late); a 10m deadline
// falls on a tick, where the relay's run read can lose the race with the
// deadline and must still report the timeout cause, not the bare context
// error.
func TestAwait_ParentIsBoundedByItsOwnDeadline(t *testing.T) {
	for _, c := range []struct {
		name    string
		timeout time.Duration
		timed   bool // assert the wait ended at the deadline, not the grace's abandon
	}{
		{"deadline between ticks", 10*time.Minute + 10*time.Second, true},
		{"deadline on a tick", 10 * time.Minute, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h, got := awaitRig(t, time.Minute, silentChild(10*time.Hour), nil, withTimeout(c.timeout))
				base := runtime.NumGoroutine()
				began := time.Now()
				parent := h.start(awaitParent, tasks.StartRequest{})
				time.Sleep(11 * time.Minute)
				synctest.Wait()
				saw := <-got
				if !errors.Is(saw.err, tasks.ErrTimedOut) {
					t.Errorf("Await err %v, want the timeout cause", saw.err)
				}
				if waited := saw.returnedAt.Sub(began); waited < c.timeout || c.timed && waited > c.timeout+100*time.Millisecond {
					t.Errorf("Await returned after %s, want the parent's %s deadline itself", waited, c.timeout)
				}
				if r := h.get(parent); r.State != tasks.StateTimedOut {
					t.Errorf("parent %s/%q, want timed_out", r.State, r.Reason)
				}
				if err := h.x.Cancel(context.Background(), saw.child); err != nil {
					t.Fatal(err)
				}
				time.Sleep(5 * time.Second)
				settleGoroutines(t, base)
			})
		})
	}
}

// TestAwait_ContinueInPlaceAfterAwaitStillNeedsRealProgress: the ticks Await
// stamps are liveness only. A parent that waited out a child past its
// watchdog limit may yield (ErrContinueInPlace) and is re-queued under the
// same run when it reported progress and checkpointed; one that only waited
// is failed for reporting no progress, exactly as before.
func TestAwait_ContinueInPlaceAfterAwaitStillNeedsRealProgress(t *testing.T) {
	for _, c := range []struct {
		name     string
		progress bool
		want     tasks.RunState
		reason   string
	}{
		{"progress and checkpoint", true, tasks.StateSucceeded, ""},
		{"only waited", false, tasks.StateFailed, "yield_without_progress"},
	} {
		t.Run(c.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				parent := taskDef(awaitParent, func(rc *tasks.RC, _ noParams) (string, error) {
					if rc.Resumed() {
						return "case closed", nil
					}
					ref, err := rc.Enqueue(awaitChild, noParams{})
					if err != nil {
						return "", err
					}
					if _, err := rc.Await(ref); err != nil {
						return "", err
					}
					if c.progress {
						rc.Progress().Set(1, tasks.Known(2))
					}
					if err := rc.Checkpoint(map[string]int{"page": 1}); err != nil {
						return "", err
					}
					return "", tasks.ErrContinueInPlace
				}, func(cm *tasks.Common) { cm.ProgressTimeout = time.Minute; cm.Cancel = tasks.Cooperative(time.Second) })
				h := newHarness(t, []tasks.Definition{parent, lanternChild(3 * time.Minute)}, func(o *Options) {
					o.Config = Config{DispatchTick: time.Second, WatchdogInterval: 30 * time.Second, MinCheckpointInterval: time.Nanosecond}
					o.Catalog = catalogWith(t, "gotham:run")
				})
				base := runtime.NumGoroutine()
				id := h.start(awaitParent, tasks.StartRequest{})
				time.Sleep(4 * time.Minute)
				synctest.Wait()
				r := h.get(id)
				if r.State != c.want || r.Reason != c.reason {
					t.Fatalf("parent %s/%q (%q), want %s/%q", r.State, r.Reason, r.Error, c.want, c.reason)
				}
				if c.progress && (r.Attempt != 2 || r.Yields != 1) {
					t.Errorf("attempt %d yields %d, want the same run re-queued once", r.Attempt, r.Yields)
				}
				if !c.progress && !strings.Contains(r.Error, "reported no progress") {
					t.Errorf("error %q does not name the missing progress", r.Error)
				}
				settleGoroutines(t, base)
			})
		})
	}
}

// TestAwait_ChildThatIgnoresItsStopEndsTheWaitAtAbandon: the child's own
// watchdog stops it but its body ignores the cancel; after its grace the
// executor abandons it (the row ends, the goroutine lingers as a zombie)
// and that terminal transition ends the parent's wait. The parent is not
// held past the abandon.
func TestAwait_ChildThatIgnoresItsStopEndsTheWaitAtAbandon(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		stubborn := taskDef(awaitChild, func(rc *tasks.RC, _ noParams) (string, error) {
			<-release // ignores the cancel
			return "", nil
		}, func(c *tasks.Common) {
			c.Effects = tasks.Writes(tasks.Res("gotham", "lab"))
			c.ProgressTimeout = 2 * time.Minute
			c.Cancel = tasks.Cooperative(time.Second)
			c.Resume = tasks.ResumeDrop
		})
		h, got := awaitRig(t, time.Minute, stubborn, nil)
		base := runtime.NumGoroutine()
		parent := h.start(awaitParent, tasks.StartRequest{})
		time.Sleep(5 * time.Minute)
		synctest.Wait()
		saw := <-got
		child := h.get(saw.child)
		if child.State != tasks.StateCanceled || child.Reason != reasonWatchdog {
			t.Fatalf("stubborn child %s/%q, want canceled/watchdog (abandoned)", child.State, child.Reason)
		}
		if saw.err != nil || saw.out.State != tasks.StateCanceled {
			t.Errorf("Await returned %s, %v, want the child's canceled outcome", saw.out.State, saw.err)
		}
		if r := h.get(parent); r.State != tasks.StateSucceeded {
			t.Errorf("parent %s/%q, want succeeded: the abandon ends the wait", r.State, r.Reason)
		}
		close(release) // the zombie's goroutine returns
		time.Sleep(time.Second)
		settleGoroutines(t, base)
	})
}

// TestAwaitAlive pins which child states keep a waiting run alive: the ones
// the executor advances by itself, a quiesced one included (its hold ends with its
// holder or its lease). A child that needs a restart or a person's decision
// does not, so a parent waiting on it stays subject to its own watchdog.
func TestAwaitAlive(t *testing.T) {
	for _, c := range []struct {
		state tasks.RunState
		want  bool
	}{
		{tasks.StateQueued, true},
		{tasks.StateWaitingDeps, true},
		{tasks.StateRunning, true},
		{tasks.StateStopping, true},
		{tasks.StateInterrupted, false},
		{tasks.StateInterruptedQuiesced, true}, // the hold ends with its holder or its lease, then the run is re-queued
		{tasks.StateAwaitingDecision, false},
		{tasks.StateSucceeded, false},
		{tasks.StateFailed, false},
		{tasks.StateTimedOut, false},
		{tasks.StateCanceled, false},
		{tasks.StateInterruptedDropped, false},
	} {
		if got := awaitAlive(c.state); got != c.want {
			t.Errorf("awaitAlive(%s) = %v, want %v", c.state, got, c.want)
		}
	}
}

// holdupChild is a child kind that queues behind the "gotham.holdup" kind's
// key, and holdup the Task that holds that key and never ends.
func holdupDefs() (child, holdup tasks.Definition) {
	child = taskDef(awaitChild, okBody, func(c *tasks.Common) {
		c.Effects = tasks.Writes(tasks.Res("gotham", "lab"))
		c.ConcurrencyKey = "gotham.holdup"
	})
	holdup = taskDef("gotham.holdup", blockUntilCtx(nil), func(c *tasks.Common) {
		c.Effects = tasks.Writes(tasks.Res("gotham", "notes"))
		c.ProgressTimeout = time.Hour
		c.Cancel = tasks.Cooperative(time.Second)
		c.Resume = tasks.ResumeDrop
	})
	return child, holdup
}

// TestAwait_ChildNeedingAPersonOrARestartDoesNotKeepParentAlive: a child
// that is queued is waited on with the relay, but once it is awaiting a
// decision or interrupted nothing the executor does will move it, so the
// parent is no longer stamped and its own watchdog stops it. (The state is
// written through the executor's run store: no definition reaches these
// states while its parent is running.)
func TestAwait_ChildNeedingAPersonOrARestartDoesNotKeepParentAlive(t *testing.T) {
	for _, state := range []tasks.RunState{tasks.StateAwaitingDecision, tasks.StateInterrupted} {
		t.Run(string(state), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				child, holdup := holdupDefs()
				h, got, refs := awaitRigDefs(t, awaitLimit, []tasks.Definition{child, holdup}, nil)
				h.start("gotham.holdup", tasks.StartRequest{})
				time.Sleep(time.Second)
				parent := h.start(awaitParent, tasks.StartRequest{})
				cid := <-refs
				time.Sleep(50 * time.Second) // past the limit: only the relay keeps the parent alive
				synctest.Wait()
				if c, r := h.get(cid), h.get(parent); c.State != tasks.StateQueued || r.State != tasks.StateRunning {
					t.Fatalf("at 50s: child %s, parent %s, want queued and running (alive child, relayed parent)", c.State, r.State)
				}
				_, err := h.e.move(context.Background(), cid, func(cur tasks.RunRecord) (tasks.RunRecord, tasks.RunEvent, error) {
					next := cur
					next.State = state
					return next, tasks.RunEvent{Event: "test"}, nil
				}, nil)
				if err != nil {
					t.Fatal(err)
				}
				time.Sleep(3 * time.Minute) // limit 1m, pass every 30s
				synctest.Wait()
				if r := h.get(parent); r.State != tasks.StateCanceled || r.Reason != reasonWatchdog {
					t.Fatalf("parent %s/%q with its child %s, want canceled/watchdog: nothing relays a child that waits for a person or a restart", r.State, r.Reason, state)
				}
				if saw := <-got; !errors.Is(saw.err, tasks.ErrCanceled) {
					t.Errorf("Await err %v, want the watchdog's cancel", saw.err)
				}
			})
		})
	}
}

// TestAwait_ChildBlockedByItsParentFailsAtOnce: a child the dispatch gates
// can never start while its parent runs (same ConcurrencyKey, overlapping
// write set, NotWhile naming the parent's kind) would otherwise hold the
// parent until its own Timeout, with the relay keeping the watchdog from
// breaking the deadlock. Await refuses it at once, naming the cause.
func TestAwait_ChildBlockedByItsParentFailsAtOnce(t *testing.T) {
	for _, c := range []struct {
		name string
		edit func(*tasks.Common)
		why  string
	}{
		{"same concurrency key", func(c *tasks.Common) { c.ConcurrencyKey = awaitParent }, "ConcurrencyKey"},
		{"overlapping write set", func(c *tasks.Common) { c.Effects = tasks.Writes(tasks.Res("gotham", "cases")) }, "write set"},
		{"NotWhile names the parent", func(c *tasks.Common) { c.NotWhile = []tasks.Kind{awaitParent} }, "NotWhile"},
	} {
		t.Run(c.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				child := taskDef(awaitChild, okBody, func(cm *tasks.Common) {
					cm.Effects = tasks.Writes(tasks.Res("gotham", "lab")) // disjoint unless the cell overlaps it
					c.edit(cm)
				})
				h, got := awaitRig(t, awaitLimit, child, nil)
				began := time.Now()
				parent := h.start(awaitParent, tasks.StartRequest{})
				time.Sleep(10 * time.Second)
				synctest.Wait()
				saw := <-got
				if !errors.Is(saw.err, ErrAwaitSelfBlocked) || !strings.Contains(saw.err.Error(), c.why) {
					t.Fatalf("Await err %v, want ErrAwaitSelfBlocked naming the %s", saw.err, c.why)
				}
				if waited := saw.returnedAt.Sub(began); waited > time.Second {
					t.Errorf("Await refused after %s, want at once", waited)
				}
				if r := h.get(parent); r.State != tasks.StateFailed {
					t.Errorf("parent %s/%q, want failed by the refusal", r.State, r.Reason)
				}
			})
		})
	}
}
