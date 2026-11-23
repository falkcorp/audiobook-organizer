// file: core/internal/executor/cascade_test.go
// version: 0.1.0
// guid: b84d20f6-91c3-4e7a-8a5b-6c1f3d9e07a2
// last-edited: 2026-11-22

package executor

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/falkcorp/mediacore/tasks"
)

// F5-18 C6: a cancel of a run takes the runs it enqueued with it. The fixtures
// are chains of kinds: gotham.<prefix>0 enqueues gotham.<prefix>1 and awaits it,
// and the last kind of the chain is the leaf that does the work.

// chainKind is level i of the chain called prefix.
func chainKind(prefix string, i int) tasks.Kind {
	return tasks.Kind(fmt.Sprintf("gotham.%s%d", prefix, i))
}

// chainRes is the resource level i of the chain writes: one per level, so no two
// runs of a tree (or of two trees) share a write set and the dispatcher admits
// them all at once.
func chainRes(prefix string, i int) tasks.Resource {
	return tasks.Res("gotham", fmt.Sprintf("%s%d", prefix, i))
}

// chainDefs is depth kinds. Every level but the last enqueues the next and awaits
// it (an error or a child that did not succeed ends it with that); the last runs
// leaf, or signals started and blocks until its context ends. edit adjusts a level's
// Common after the defaults (a cooperative cancel with a one second grace).
func chainDefs(prefix string, depth int, started chan<- tasks.RunID,
	leaf func(rc *tasks.RC, p noParams) (string, error), edit func(i int, c *tasks.Common)) []tasks.Definition {
	var defs []tasks.Definition
	for i := range depth {
		body := func(rc *tasks.RC, _ noParams) (string, error) {
			ref, err := rc.Enqueue(chainKind(prefix, i+1), noParams{})
			if err != nil {
				return "", err
			}
			out, err := rc.Await(ref)
			if err != nil {
				return "", err
			}
			if out.State != tasks.StateSucceeded {
				return "", fmt.Errorf("the child %s ended %s", ref.ID, out.State)
			}
			return "", nil
		}
		if i == depth-1 {
			body = blockUntilCtx(started)
			if leaf != nil {
				body = leaf
			}
		}
		defs = append(defs, taskDef(string(chainKind(prefix, i)), body, func(c *tasks.Common) {
			c.Effects = tasks.Writes(chainRes(prefix, i))
			c.Cancel = tasks.Cooperative(time.Second)
			if edit != nil {
				edit(i, c)
			}
		}))
	}
	return defs
}

// cascadeOpts is the executor options the chains run under: a system actor of
// gotham may start its own kinds (as awaitRig's does).
func cascadeOpts(t testing.TB, tick time.Duration) func(*Options) {
	return func(o *Options) {
		o.Config = Config{DispatchTick: tick, WatchdogInterval: time.Hour}
		o.Catalog = catalogWith(t, "gotham:run")
	}
}

// childOf is the one child of run id; it waits (on the test's clock) for it to exist.
func (h *harness) childOf(id tasks.RunID) tasks.RunID {
	h.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if kids := h.children(id); len(kids) == 1 {
			return kids[0]
		} else if len(kids) > 1 {
			h.t.Fatalf("run %s has children %v, want one", id, kids)
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("run %s never enqueued its child", id)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// writeLog is a leaf that writes once a second and records what its writes saw.
type writeLog struct {
	mu      sync.Mutex
	wrote   []time.Time // when fn ran
	refused error       // the first refusal
	cause   error       // the run context's cause when the refusal came
	at      time.Time   // when the first refusal came
}

// leaf is the body: write every second until a write is refused, then end with that
// error. The write is the real gated path (Writer.Do): the body never looks at its
// context, so only the writer's refusal can stop it.
func (l *writeLog) leaf(res tasks.Resource, started chan<- tasks.RunID) func(rc *tasks.RC, _ noParams) (string, error) {
	return func(rc *tasks.RC, _ noParams) (string, error) {
		started <- rc.RunID()
		for {
			err := rc.Writer().Do(context.Background(), tasks.Write{Name: "gotham.note", Resource: res,
				Subject: tasks.ResID("gotham", "lab", "riddler")}, func(context.Context) ([]tasks.Change, error) {
				l.mu.Lock()
				l.wrote = append(l.wrote, time.Now())
				l.mu.Unlock()
				return nil, nil
			})
			if err != nil {
				l.mu.Lock()
				if l.refused == nil {
					l.refused, l.cause, l.at = err, context.Cause(rc.Context()), time.Now()
				}
				l.mu.Unlock()
				return "", err
			}
			time.Sleep(time.Second)
		}
	}
}

func (l *writeLog) writesAfter(t time.Time) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, w := range l.wrote {
		if w.After(t) {
			n++
		}
	}
	return n
}

// TestCancelCascade_ParentCancelStopsTheChildsWrites (F5-18 C6): canceling a
// parent stops the child in flight at its next write, long before the child's
// own timeout (an hour here). The child's body never reads its context: the
// executor cancels it with a cause naming the parent and its writer refuses, so a
// write after the cancel never runs fn. The child's row ends canceled, not stuck
// running, and the parent's wait ends with the parent's own cancel.
func TestCancelCascade_ParentCancelStopsTheChildsWrites(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		started := make(chan tasks.RunID, 1)
		var l writeLog
		defs := chainDefs("wc", 2, started, l.leaf(chainRes("wc", 1), started), func(i int, c *tasks.Common) {
			if i == 1 {
				c.Timeout = time.Hour
			}
		})
		h := newHarness(t, defs, cascadeOpts(t, time.Second))
		parent := h.start(string(chainKind("wc", 0)), tasks.StartRequest{})
		child := <-started
		time.Sleep(3500 * time.Millisecond)
		if n := len(l.wrote); n < 3 {
			t.Fatalf("the child made %d writes in 3.5s, want it writing", n)
		}
		canceledAt := time.Now()
		if err := h.x.CancelAs(context.Background(), parent, tasks.Actor{UserID: "gordon"}); err != nil {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Second) // well inside the child's hour
		synctest.Wait()

		if n := l.writesAfter(canceledAt); n != 0 {
			t.Errorf("%d child writes ran after the parent's cancel, want none", n)
		}
		if !errors.Is(l.refused, tasks.ErrFenced) {
			t.Errorf("the child's first write after the cancel: %v, want ErrFenced", l.refused)
		}
		if !errors.Is(l.cause, tasks.ErrCanceled) || !strings.Contains(l.cause.Error(), string(parent)) {
			t.Errorf("the child was canceled with %v, want a cancel that names parent %s", l.cause, parent)
		}
		if lag := l.at.Sub(canceledAt); lag > 2*time.Second {
			t.Errorf("the child's writes stopped %s after the cancel, want within its next write", lag)
		}
		if c := h.get(child); c.State != tasks.StateCanceled || c.Reason != reasonCancel || c.Finished.IsZero() {
			t.Errorf("child %s/%q finished %v, want canceled/cancel and finished", c.State, c.Reason, c.Finished)
		}
		if p := h.get(parent); p.State != tasks.StateCanceled || p.Reason != reasonCancel {
			t.Errorf("parent %s/%q, want canceled/cancel", p.State, p.Reason)
		}
	})
}

// TestCancelCascade_ReachesGrandchildren: the cascade walks the tree, so a child
// that is itself a parent takes its own child down with it, one scan, no goroutines.
func TestCancelCascade_ReachesGrandchildren(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		started := make(chan tasks.RunID, 1)
		h := newHarness(t, chainDefs("gc", 3, started, nil, nil), cascadeOpts(t, time.Second))
		base := goroutinesNow(t)
		root := h.start(string(chainKind("gc", 0)), tasks.StartRequest{})
		leaf := <-started
		mid := h.childOf(root)
		if err := h.x.CancelAs(context.Background(), root, tasks.Actor{UserID: "gordon"}); err != nil {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Second)
		for name, id := range map[string]tasks.RunID{"root": root, "child": mid, "grandchild": leaf} {
			if r := h.get(id); r.State != tasks.StateCanceled || r.Reason != reasonCancel {
				t.Errorf("%s %s/%q, want canceled/cancel", name, r.State, r.Reason)
			}
		}
		settleGoroutines(t, base)
	})
}

// TestCancelCascade_NeverReachesARunThatIsNotADescendant: only the canceled run's
// own children are canceled. A second tree, a run with no parent and a child
// canceled by hand are all left alone, and the cascade only goes down: canceling a
// child does not cancel its parent.
func TestCancelCascade_NeverReachesARunThatIsNotADescendant(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		started := make(chan tasks.RunID, 4)
		patrol := taskDef("gotham.patrol", blockUntilCtx(started), func(c *tasks.Common) {
			c.Effects = tasks.Writes(tasks.Res("gotham", "patrol"))
		})
		defs := append(append(chainDefs("ta", 2, started, nil, nil), chainDefs("tb", 2, started, nil, nil)...), patrol)
		h := newHarness(t, defs, cascadeOpts(t, time.Second))
		a := h.start(string(chainKind("ta", 0)), tasks.StartRequest{})
		b := h.start(string(chainKind("tb", 0)), tasks.StartRequest{})
		s := h.start("gotham.patrol", tasks.StartRequest{})
		for range 3 {
			<-started
		}
		a1, b1 := h.childOf(a), h.childOf(b)

		if err := h.x.CancelAs(context.Background(), a, tasks.Actor{UserID: "gordon"}); err != nil {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Second)
		for name, id := range map[string]tasks.RunID{"canceled parent": a, "its child": a1} {
			if r := h.get(id); r.State != tasks.StateCanceled {
				t.Errorf("%s is %s, want canceled", name, r.State)
			}
		}
		for name, id := range map[string]tasks.RunID{"the other tree's parent": b, "the other tree's child": b1, "a run with no parent": s} {
			if r := h.get(id); r.State != tasks.StateRunning {
				t.Errorf("%s is %s/%q, want running: it is not a descendant of the canceled run", name, r.State, r.Reason)
			}
		}

		// Down only: canceling the other tree's child ends its parent by the child's outcome
		// (the fixture parent fails on a child that did not succeed), never by a cancel.
		if err := h.x.CancelAs(context.Background(), b1, tasks.Actor{UserID: "gordon"}); err != nil {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Second)
		if r := h.get(b); r.State != tasks.StateFailed {
			t.Errorf("parent of a child canceled by hand is %s/%q, want failed (not canceled)", r.State, r.Reason)
		}
		if r := h.get(s); r.State != tasks.StateRunning {
			t.Errorf("the run with no parent is %s, want running", r.State)
		}
	})
}

// TestCancelCascade_AnRCCannotAdoptAnotherRunAsItsParent: nothing names a run to
// cancel. A child's parent is the run that enqueued it; an Enqueue option that
// names another run as the parent is refused (so a run cannot hang itself under a
// run it does not belong to), and canceling that run leaves the enqueuer alone.
func TestCancelCascade_AnRCCannotAdoptAnotherRunAsItsParent(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		started := make(chan tasks.RunID, 2)
		victim := make(chan tasks.RunID, 1)
		got := make(chan error, 1)
		patrol := taskDef("gotham.patrol", blockUntilCtx(started), func(c *tasks.Common) {
			c.Effects = tasks.Writes(tasks.Res("gotham", "patrol"))
		})
		forger := taskDef("gotham.forger", func(rc *tasks.RC, _ noParams) (string, error) {
			v := <-victim
			_, err := rc.Enqueue("gotham.case", noParams{}, func(r *tasks.StartRequest) { r.Parent = v })
			got <- err
			started <- rc.RunID()
			<-rc.Context().Done()
			return "", context.Cause(rc.Context())
		}, func(c *tasks.Common) {
			c.Effects = tasks.Writes(tasks.Res("gotham", "forger"))
			c.Cancel = tasks.Cooperative(time.Second)
		})
		child := taskDef("gotham.case", blockUntilCtx(started), func(c *tasks.Common) {
			c.Effects = tasks.Writes(tasks.Res("gotham", "case"))
		})
		h := newHarness(t, []tasks.Definition{patrol, forger, child}, cascadeOpts(t, time.Second))
		v := h.start("gotham.patrol", tasks.StartRequest{})
		<-started
		f := h.start("gotham.forger", tasks.StartRequest{})
		victim <- v
		if err := <-got; !errors.Is(err, ErrNotPermitted) {
			t.Fatalf("Enqueue naming another run as the parent: %v, want ErrNotPermitted", err)
		}
		<-started
		if n := h.count("gotham.case"); n != 0 {
			t.Fatalf("a refused enqueue left %d runs", n)
		}
		if err := h.x.CancelAs(context.Background(), v, tasks.Actor{UserID: "gordon"}); err != nil {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Second)
		if r := h.get(v); r.State != tasks.StateCanceled {
			t.Errorf("the canceled run is %s, want canceled", r.State)
		}
		if r := h.get(f); r.State != tasks.StateRunning {
			t.Errorf("the run that tried to adopt it is %s, want running", r.State)
		}
	})
}

// TestCancelCascade_CancelWithNoChildInFlightIsClean: a cancel of a run that has
// no unfinished child cancels the run and nothing else, answers nil, and leaves
// a child that already finished exactly as it was (same record, same event
// count); nothing leaks.
func TestCancelCascade_CancelWithNoChildInFlightIsClean(t *testing.T) {
	t.Run("never enqueued anything", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			started := make(chan tasks.RunID, 2)
			patrol := taskDef("gotham.patrol", blockUntilCtx(started), func(c *tasks.Common) {
				c.Effects = tasks.Writes(tasks.Res("gotham", "patrol"))
				c.Cancel = tasks.Cooperative(time.Second)
			})
			bystander := taskDef("gotham.bystander", blockUntilCtx(started), func(c *tasks.Common) {
				c.Effects = tasks.Writes(tasks.Res("gotham", "bystander"))
			})
			h := newHarness(t, []tasks.Definition{patrol, bystander}, cascadeOpts(t, time.Second))
			base := goroutinesNow(t)
			id := h.start("gotham.patrol", tasks.StartRequest{})
			other := h.start("gotham.bystander", tasks.StartRequest{})
			<-started
			<-started
			if err := h.x.CancelAs(context.Background(), id, tasks.Actor{UserID: "gordon"}); err != nil {
				t.Fatalf("cancel of a run with no children: %v, want nil", err)
			}
			time.Sleep(5 * time.Second)
			if r := h.get(id); r.State != tasks.StateCanceled || r.Reason != reasonCancel {
				t.Errorf("run %s/%q, want canceled/cancel", r.State, r.Reason)
			}
			if r := h.get(other); r.State != tasks.StateRunning {
				t.Errorf("the other run is %s, want running", r.State)
			}
			if err := h.x.CancelAs(context.Background(), other, tasks.Actor{UserID: "gordon"}); err != nil {
				t.Fatal(err)
			}
			time.Sleep(5 * time.Second)
			settleGoroutines(t, base)
		})
	})
	t.Run("its only child already finished", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			parent := taskDef("gotham.stakeout", func(rc *tasks.RC, _ noParams) (string, error) {
				ref, err := rc.Enqueue("gotham.case", noParams{})
				if err != nil {
					return "", err
				}
				if _, err := rc.Await(ref); err != nil {
					return "", err
				}
				<-rc.Context().Done() // the child is done; the parent goes on working
				return "", context.Cause(rc.Context())
			}, func(c *tasks.Common) { c.Cancel = tasks.Cooperative(time.Second) })
			child := taskDef("gotham.case", okBody, func(c *tasks.Common) { c.Effects = tasks.Writes(tasks.Res("gotham", "case")) })
			h := newHarness(t, []tasks.Definition{parent, child}, cascadeOpts(t, time.Second))
			id := h.start("gotham.stakeout", tasks.StartRequest{})
			kid := h.childOf(id)
			h.waitState(kid, tasks.StateSucceeded)
			before := h.get(kid)
			if err := h.x.CancelAs(context.Background(), id, tasks.Actor{UserID: "gordon"}); err != nil {
				t.Fatalf("cancel of a run whose child finished: %v, want nil", err)
			}
			time.Sleep(5 * time.Second)
			after := h.get(kid)
			if after.State != tasks.StateSucceeded || after.Seq != before.Seq || after.Reason != before.Reason || !after.Finished.Equal(before.Finished) {
				t.Errorf("the finished child changed: %s/%q seq %v, was %s/%q seq %v", after.State, after.Reason, after.Seq, before.State, before.Reason, before.Seq)
			}
			if r := h.get(id); r.State != tasks.StateCanceled {
				t.Errorf("parent is %s, want canceled", r.State)
			}
		})
	})
}

// TestCancelCascade_ARepeatCancelRetriesTheCascade: a cancel that reached the
// parent but not its children (a store error between the two, or a crash) is
// answered nil by the repeat cancel of the run it left stopping, and that repeat
// reaches the children. The first cancel is staged by stopOne, the half of stop
// that precedes the cascade.
func TestCancelCascade_ARepeatCancelRetriesTheCascade(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		started := make(chan tasks.RunID, 1)
		release := make(chan struct{})
		defs := chainDefs("rc", 2, started, nil, nil)
		// The parent ignores its context, so it stays stopping for a cancel (a long grace) while its
		// child is alive.
		defs[0] = taskDef(string(chainKind("rc", 0)), func(rc *tasks.RC, _ noParams) (string, error) {
			if _, err := rc.Enqueue(chainKind("rc", 1), noParams{}); err != nil {
				return "", err
			}
			<-release
			return "", nil
		}, func(c *tasks.Common) {
			c.Effects = tasks.Writes(chainRes("rc", 0))
			c.Cancel = tasks.Cooperative(time.Hour)
		})
		h := newHarness(t, defs, cascadeOpts(t, time.Second))
		defer close(release)
		parent := h.start(string(chainKind("rc", 0)), tasks.StartRequest{})
		child := <-started
		if err := h.e.stopOne(context.Background(), parent, tasks.Actor{UserID: "gordon"}, reasonCancel, tasks.ErrCanceled); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Second)
		if r := h.get(parent); r.State != tasks.StateStopping || r.Reason != reasonCancel {
			t.Fatalf("parent %s/%q, want stopping/cancel", r.State, r.Reason)
		}
		if r := h.get(child); r.State != tasks.StateRunning {
			t.Fatalf("child %s, want running: the first cancel did not cascade", r.State)
		}
		if err := h.x.CancelAs(context.Background(), parent, tasks.Actor{UserID: "gordon"}); err != nil {
			t.Fatalf("repeat cancel: %v, want nil", err)
		}
		time.Sleep(5 * time.Second)
		if r := h.get(child); r.State != tasks.StateCanceled || r.Reason != reasonCancel {
			t.Errorf("child %s/%q after the repeat cancel, want canceled/cancel", r.State, r.Reason)
		}
	})
}

// TestCancelCascade_RestartMidCancelLeavesNoChildRunning: the process dies between
// the parent's cancel and the cascade (the first cancel is staged by stopOne, the
// half of stop that precedes it). At the next boot no child runs and none resumes
// (these kinds resume with ResumeContinue, so an unhandled child would be
// requeued and its body would start again): each is ended canceled, whether the
// parent had finished canceling or was still stopping, and a grandchild goes with
// its parent.
func TestCancelCascade_RestartMidCancelLeavesNoChildRunning(t *testing.T) {
	for _, c := range []struct {
		name       string
		parentEnds bool // the parent's body returns on its context; else it ignores it and is still stopping at the crash
	}{
		{"the parent had ended canceled", true},
		{"the parent was still stopping", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			leafStarts := make(chan tasks.RunID, 8)
			release := make(chan struct{})
			defs := chainDefs("rs", 3, leafStarts, nil, nil)
			if !c.parentEnds {
				defs[0] = taskDef(string(chainKind("rs", 0)), func(rc *tasks.RC, _ noParams) (string, error) {
					if _, err := rc.Enqueue(chainKind("rs", 1), noParams{}); err != nil {
						return "", err
					}
					<-release
					return "", nil
				}, func(cm *tasks.Common) {
					cm.Effects = tasks.Writes(chainRes("rs", 0))
					cm.Cancel = tasks.Cooperative(time.Hour)
				})
			}
			h := newHarness(t, defs, cascadeOpts(t, 20*time.Millisecond))
			t.Cleanup(func() { close(release) }) // runs before the harness's Shutdown, which would wait out its drain on the parked body
			parent := h.start(string(chainKind("rs", 0)), tasks.StartRequest{})
			<-leafStarts
			child := h.childOf(parent)
			grand := h.childOf(child)
			// The cancel's first half: the parent is stopped, nothing below it is.
			if err := h.e.stopOne(context.Background(), parent, tasks.Actor{UserID: "gordon"}, reasonCancel, tasks.ErrCanceled); err != nil {
				t.Fatal(err)
			}
			if c.parentEnds {
				h.waitState(parent, tasks.StateCanceled)
			} else if r := h.get(parent); r.State != tasks.StateStopping || r.Reason != reasonCancel {
				t.Fatalf("parent %s/%q, want stopping/cancel", r.State, r.Reason)
			}
			if r := h.get(child); r.State != tasks.StateRunning {
				t.Fatalf("child %s before the crash, want running", r.State)
			}

			second := h.crashBoot(defs, cascadeOpts(t, 20*time.Millisecond))
			for name, id := range map[string]tasks.RunID{"parent": parent, "child": child, "grandchild": grand} {
				if r := second.waitState(id, tasks.StateCanceled); r.Reason != reasonCancel || r.Finished.IsZero() {
					t.Errorf("%s after the restart: %s/%q finished %v, want canceled/cancel and finished", name, r.State, r.Reason, r.Finished)
				}
			}
			time.Sleep(300 * time.Millisecond) // a requeued child would have been claimed by now
			if n := len(leafStarts); n != 0 {
				t.Errorf("the leaf's body started %d more time(s) after the restart, want none", n)
			}
			for _, id := range []tasks.RunID{child, grand} {
				if r := second.get(id); r.State != tasks.StateCanceled {
					t.Errorf("run %s is %s after the restart settled, want canceled", id, r.State)
				}
			}
		})
	}
}

// TestCancelCascade_AChildQueuedAfterTheCancelIsCanceledNotClaimed: a child staged
// after the cascade read the unfinished runs (the parent's Enqueue was in flight
// while the cancel was applied) is canceled by the dispatcher instead of claimed;
// its body never starts. The record is built with Start naming the canceled
// parent, which is the state that race leaves.
func TestCancelCascade_AChildQueuedAfterTheCancelIsCanceledNotClaimed(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		started := make(chan tasks.RunID, 2)
		var bodies atomic.Int32
		defs := chainDefs("aq", 2, started, func(rc *tasks.RC, _ noParams) (string, error) {
			bodies.Add(1)
			<-rc.Context().Done()
			return "", context.Cause(rc.Context())
		}, nil)
		h := newHarness(t, defs, cascadeOpts(t, time.Second))
		parent := h.start(string(chainKind("aq", 0)), tasks.StartRequest{})
		h.childOf(parent)
		time.Sleep(2 * time.Second) // the first child is running
		if err := h.x.CancelAs(context.Background(), parent, tasks.Actor{UserID: "gordon"}); err != nil {
			t.Fatal(err)
		}
		time.Sleep(3 * time.Second)
		if h.get(parent).State != tasks.StateCanceled {
			t.Fatalf("parent is %s, want canceled", h.get(parent).State)
		}
		before := bodies.Load()
		late, err := h.x.Start(context.Background(), chainKind("aq", 1), tasks.StartRequest{
			Mode: tasks.ModeRequestLive, Parent: parent, Trigger: tasks.TriggerParent, Params: []byte(`{"case":"late"}`)})
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Second)
		if r := h.get(late.ID); r.State != tasks.StateCanceled || r.Reason != reasonCancel {
			t.Errorf("the late child is %s/%q, want canceled/cancel", r.State, r.Reason)
		}
		if n := bodies.Load(); n != before {
			t.Errorf("the late child's body started (%d bodies, was %d)", n, before)
		}
	})
}

// TestCancelCascade_AChildOfADisabledModuleWaitsForItsModule: a child of a held
// run is read-only (5T.3.11): neither the cascade nor a boot sweep touches it,
// and it ends canceled, unstarted, at the first boot with its module enabled.
func TestCancelCascade_AChildOfADisabledModuleWaitsForItsModule(t *testing.T) {
	ctx := context.Background()
	started := make(chan tasks.RunID, 4)
	var bodies atomic.Int32
	patrol := taskDef("gotham.patrol", blockUntilCtx(started))
	arkham := func() []tasks.Definition {
		return []tasks.Definition{taskDef("arkham.intake", func(rc *tasks.RC, _ noParams) (string, error) {
			bodies.Add(1)
			<-rc.Context().Done()
			return "", context.Cause(rc.Context())
		})}
	}
	// Boot 1, paused: the parent and its child are queued.
	h := newHarness(t, append([]tasks.Definition{patrol}, arkham()...), nil)
	if err := h.x.Pause(ctx, "lockdown"); err != nil {
		t.Fatal(err)
	}
	parent := h.start("gotham.patrol", tasks.StartRequest{})
	ref, err := h.x.Start(ctx, "arkham.intake", tasks.StartRequest{Mode: tasks.ModeRequestLive, Parent: parent, Trigger: tasks.TriggerParent})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.x.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	// Boot 2: arkham disabled. The parent is canceled; its child is held and untouched.
	held := h.reboot([]tasks.Definition{patrol}, disabledArkham)
	if err := held.x.Cancel(ctx, parent); err != nil {
		t.Fatalf("cancel with a held child: %v, want nil", err)
	}
	if r := held.get(parent); r.State != tasks.StateCanceled {
		t.Fatalf("parent %s, want canceled", r.State)
	}
	if r := held.get(ref.ID); r.State != tasks.StateQueued {
		t.Fatalf("the held child is %s, want queued and untouched", r.State)
	}
	if err := held.x.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	// Boot 3: arkham still disabled; the sweep sees a canceled parent and leaves the held child alone.
	again := held.reboot([]tasks.Definition{patrol}, disabledArkham)
	if r := again.get(ref.ID); r.State != tasks.StateQueued {
		t.Fatalf("the held child is %s after a boot sweep, want queued and untouched", r.State)
	}
	if err := again.x.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	// Boot 4: arkham enabled and unpaused: the child is canceled, never started.
	live := again.reboot(append([]tasks.Definition{patrol}, arkham()...), nil)
	if err := live.x.Unpause(ctx); err != nil {
		t.Fatal(err)
	}
	if r := live.waitState(ref.ID, tasks.StateCanceled); r.Reason != reasonCancel {
		t.Errorf("the child ended %s/%q, want canceled/cancel", r.State, r.Reason)
	}
	if n := bodies.Load(); n != 0 {
		t.Errorf("the child's body started %d time(s), want never", n)
	}
}

// TestCancelCascade_OnlyACancelCascades: a parent the watchdog stopped, or that
// timed out, leaves its child's lifecycle as it was: the cascade is the cancel's,
// not every way a run ends. (The child here is not awaited, so the parent is
// silent and the watchdog does strike it.)
func TestCancelCascade_OnlyACancelCascades(t *testing.T) {
	for _, c := range []struct {
		name  string
		edit  func(*tasks.Common)
		cause error
	}{
		{"the watchdog", func(c *tasks.Common) { c.ProgressTimeout = time.Minute }, tasks.ErrCanceled},
		{"the timeout", withTimeout(time.Minute), tasks.ErrTimedOut},
	} {
		t.Run(c.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				started := make(chan tasks.RunID, 1)
				defs := chainDefs("oc", 2, started, nil, nil)
				defs[0] = taskDef(string(chainKind("oc", 0)), func(rc *tasks.RC, _ noParams) (string, error) {
					if _, err := rc.Enqueue(chainKind("oc", 1), noParams{}); err != nil {
						return "", err
					}
					<-rc.Context().Done()
					return "", context.Cause(rc.Context())
				}, func(cm *tasks.Common) {
					cm.Effects = tasks.Writes(chainRes("oc", 0))
					cm.Cancel = tasks.Cooperative(time.Second)
					c.edit(cm)
				})
				h := newHarness(t, defs, func(o *Options) {
					cascadeOpts(t, time.Second)(o)
					o.Config.WatchdogInterval = 30 * time.Second
				})
				parent := h.start(string(chainKind("oc", 0)), tasks.StartRequest{})
				child := <-started
				time.Sleep(5 * time.Minute)
				if r := h.get(parent); r.State == tasks.StateRunning {
					t.Fatalf("parent still running after 5m: the fixture did not end it")
				}
				if r := h.get(child); r.State != tasks.StateRunning {
					t.Errorf("child %s/%q after its parent ended by %s, want running: only a cancel cascades", r.State, r.Reason, c.name)
				}
				if err := h.x.CancelAs(context.Background(), child, tasks.Actor{UserID: "gordon"}); err != nil {
					t.Fatal(err)
				}
				time.Sleep(5 * time.Second)
			})
		})
	}
}
