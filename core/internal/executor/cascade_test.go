// file: core/internal/executor/cascade_test.go
// version: 0.1.0
// guid: b84d20f6-91c3-4e7a-8a5b-6c1f3d9e07a2
// last-edited: 2026-11-22

package executor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/falkcorp/mediacore/rbac"
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

// refusal is the first refusal a write saw, under the log's lock.
func (l *writeLog) refusal() (refused, cause error, at time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.refused, l.cause, l.at
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
		if n := l.writesAfter(time.Time{}); n < 3 {
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
		refused, cause, at := l.refusal()
		if !errors.Is(refused, tasks.ErrFenced) {
			t.Errorf("the child's first write after the cancel: %v, want ErrFenced", refused)
		}
		if !errors.Is(cause, tasks.ErrCanceled) || !strings.Contains(cause.Error(), string(parent)) {
			t.Errorf("the child was canceled with %v, want a cancel that names parent %s", cause, parent)
		}
		if lag := at.Sub(canceledAt); lag > 2*time.Second {
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
		if err := h.e.stopOne(context.Background(), parent, tasks.Actor{UserID: "gordon"}, h.allReach(parent), reasonCancel, tasks.ErrCanceled); err != nil {
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
// its parent. Kinds that drop on a restart are ended canceled too, not
// interrupted_dropped: the boot pass, not the dispatch guard alone, decides it.
func TestCancelCascade_RestartMidCancelLeavesNoChildRunning(t *testing.T) {
	for _, c := range []struct {
		name       string
		parentEnds bool               // the parent's body returns on its context; else it ignores it and is still stopping at the crash
		resume     tasks.ResumePolicy // the children's: ResumeDrop would end an unhandled child interrupted_dropped, not canceled
	}{
		{"the parent had ended canceled", true, tasks.ResumeContinue},
		{"the parent was still stopping", false, tasks.ResumeContinue},
		{"the children drop on a restart", true, tasks.ResumeDrop},
	} {
		t.Run(c.name, func(t *testing.T) {
			leafStarts := make(chan tasks.RunID, 8)
			release := make(chan struct{})
			defs := chainDefs("rs", 3, leafStarts, nil, func(i int, cm *tasks.Common) {
				if i > 0 {
					cm.Resume = c.resume
				}
			})
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
			if err := h.e.stopOne(context.Background(), parent, tasks.Actor{UserID: "gordon"}, h.allReach(parent), reasonCancel, tasks.ErrCanceled); err != nil {
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

// tripCtx is a context that reports canceled once trip is: the caller of a cancel
// that gives up the moment the run it canceled stops.
type tripCtx struct {
	context.Context
	trip context.Context
}

func (c tripCtx) Err() error {
	if c.trip.Err() != nil {
		return context.Canceled
	}
	return c.Context.Err()
}
func (c tripCtx) Done() <-chan struct{} { return c.trip.Done() }

// TestCancelCascade_ACallerThatGivesUpDoesNotLoseTheCascade: the cancel's caller
// abandons its context the moment the parent's own context is canceled (the
// request that carried it was dropped), which is before the cascade has run. The
// cascade is not the caller's to abandon: the parent's stop has committed, so
// nothing else would stop the child. The cancel answers nil and the child ends
// canceled.
func TestCancelCascade_ACallerThatGivesUpDoesNotLoseTheCascade(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		started := make(chan tasks.RunID, 1)
		parentCtx := make(chan context.Context, 1)
		defs := chainDefs("gu", 2, started, nil, nil)
		defs[0] = taskDef(string(chainKind("gu", 0)), func(rc *tasks.RC, _ noParams) (string, error) {
			if _, err := rc.Enqueue(chainKind("gu", 1), noParams{}); err != nil {
				return "", err
			}
			parentCtx <- rc.Context()
			<-rc.Context().Done()
			return "", context.Cause(rc.Context())
		}, func(c *tasks.Common) {
			c.Effects = tasks.Writes(chainRes("gu", 0))
			c.Cancel = tasks.Cooperative(time.Second)
		})
		h := newHarness(t, defs, cascadeOpts(t, time.Second))
		parent := h.start(string(chainKind("gu", 0)), tasks.StartRequest{})
		child := <-started
		caller := tripCtx{Context: context.Background(), trip: <-parentCtx}
		if err := h.x.CancelAs(caller, parent, tasks.Actor{UserID: "gordon"}); err != nil {
			t.Fatalf("cancel whose caller gave up after the parent's stop: %v, want nil", err)
		}
		time.Sleep(5 * time.Second)
		if r := h.get(child); r.State != tasks.StateCanceled || r.Reason != reasonCancel {
			t.Errorf("child %s/%q, want canceled/cancel: the cascade was lost with the caller's context", r.State, r.Reason)
		}
	})
}

// TestCancelCascade_ARepeatCancelOfAnEndedParentFinishesTheCascade: a cancel that
// ended the parent but not its child (the first half is staged by stopOne) is
// finished by the repeat cancel even though the parent is terminal. The repeat
// still answers "not found" (HEAD's repeat-cancel rule); a parent the watchdog
// ended is not cascaded from, so a cancel of it leaves the child alone.
func TestCancelCascade_ARepeatCancelOfAnEndedParentFinishesTheCascade(t *testing.T) {
	for _, c := range []struct {
		name        string
		reason      string
		wantStopped bool
	}{
		{"ended by a cancel", reasonCancel, true},
		{"ended by the watchdog", reasonWatchdog, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				started := make(chan tasks.RunID, 1)
				h := newHarness(t, chainDefs("re", 2, started, nil, nil), cascadeOpts(t, time.Second))
				parent := h.start(string(chainKind("re", 0)), tasks.StartRequest{})
				child := <-started
				if err := h.e.stopOne(context.Background(), parent, tasks.Actor{UserID: "gordon"}, h.allReach(parent), c.reason, tasks.ErrCanceled); err != nil {
					t.Fatal(err)
				}
				time.Sleep(5 * time.Second)
				if r := h.get(parent); r.State != tasks.StateCanceled || r.Reason != c.reason {
					t.Fatalf("parent %s/%q, want canceled/%s", r.State, r.Reason, c.reason)
				}
				if r := h.get(child); r.State != tasks.StateRunning {
					t.Fatalf("child %s before the repeat, want running: stopOne alone does not cascade", r.State)
				}
				if err := h.x.CancelAs(context.Background(), parent, tasks.Actor{UserID: "gordon"}); !errors.Is(err, tasks.ErrRunNotFound) {
					t.Errorf("repeat cancel of an ended run: %v, want ErrRunNotFound", err)
				}
				time.Sleep(5 * time.Second)
				r := h.get(child)
				if c.wantStopped && (r.State != tasks.StateCanceled || r.Reason != reasonCancel) {
					t.Errorf("child %s/%q after the repeat cancel, want canceled/cancel", r.State, r.Reason)
				}
				if !c.wantStopped && r.State != tasks.StateRunning {
					t.Errorf("child %s/%q after a cancel of a run the watchdog ended, want running", r.State, r.Reason)
				}
			})
		})
	}
}

// TestCancelCascade_AParentThatIsNotExecutingStillTakesItsChild: a parent that is
// not executing in this process (queued behind a run that holds its resource) ends
// canceled at once, with no attempt to cancel, and its running child still goes
// with it. The child is started naming the queued run as its parent, which is how
// a run comes to have a child while it is not executing.
func TestCancelCascade_AParentThatIsNotExecutingStillTakesItsChild(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		started := make(chan tasks.RunID, 3)
		holder := taskDef("gotham.holder", blockUntilCtx(started), func(c *tasks.Common) {
			c.Effects = tasks.Writes(tasks.Res("gotham", "ledger"))
		})
		stakeout := taskDef("gotham.stakeout", okBody, func(c *tasks.Common) {
			c.Effects = tasks.Writes(tasks.Res("gotham", "ledger"))
		})
		child := taskDef("gotham.case", blockUntilCtx(started), func(c *tasks.Common) {
			c.Effects = tasks.Writes(tasks.Res("gotham", "case"))
		})
		h := newHarness(t, []tasks.Definition{holder, stakeout, child}, cascadeOpts(t, time.Second))
		hold := h.start("gotham.holder", tasks.StartRequest{})
		<-started
		parent := h.start("gotham.stakeout", tasks.StartRequest{})
		ref, err := h.x.Start(context.Background(), "gotham.case", tasks.StartRequest{
			Mode: tasks.ModeRequestLive, Parent: parent, Trigger: tasks.TriggerParent})
		if err != nil {
			t.Fatal(err)
		}
		<-started
		if r := h.get(parent); r.State != tasks.StateQueued {
			t.Fatalf("parent %s, want queued behind the holder", r.State)
		}
		if err := h.x.CancelAs(context.Background(), parent, tasks.Actor{UserID: "gordon"}); err != nil {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Second)
		if r := h.get(parent); r.State != tasks.StateCanceled || r.Reason != reasonCancel {
			t.Errorf("parent %s/%q, want canceled/cancel", r.State, r.Reason)
		}
		if r := h.get(ref.ID); r.State != tasks.StateCanceled || r.Reason != reasonCancel {
			t.Errorf("child %s/%q, want canceled/cancel: the parent never executed, the child did", r.State, r.Reason)
		}
		if r := h.get(hold); r.State != tasks.StateRunning {
			t.Errorf("the run holding the parent's resource is %s, want running", r.State)
		}
	})
}

// cancelEventAt is when run id recorded its first cancel event.
func (h *harness) cancelEventAt(id tasks.RunID) time.Time {
	h.t.Helper()
	for p, err := range h.stores.Runs.Pages(context.Background(), id, tasks.PageQuery{Page: tasks.PageEvents, Limit: 1000}) {
		if err != nil {
			h.t.Fatalf("events: %v", err)
		}
		if p.Event.Event == "cancel" {
			return p.Event.At
		}
	}
	h.t.Fatalf("run %s has no cancel event", id)
	return time.Time{}
}

// TestCancelCascade_TheParentIsStoppedBeforeItsChildren: the parent's own stop
// commits before any child's. A parent that is waiting on a child and is let see
// that child canceled while its own context is still live goes on to enqueue the
// next one, which no cascade is left to reach. The order is read off the runs'
// own cancel events (the real clock: a frozen one cannot order two commits).
func TestCancelCascade_TheParentIsStoppedBeforeItsChildren(t *testing.T) {
	started := make(chan tasks.RunID, 4)
	h := newHarness(t, chainDefs("so", 3, started, nil, nil), cascadeOpts(t, 20*time.Millisecond))
	parent := h.start(string(chainKind("so", 0)), tasks.StartRequest{})
	<-started
	child := h.childOf(parent)
	grand := h.childOf(child)
	if err := h.x.CancelAs(context.Background(), parent, tasks.Actor{UserID: "gordon"}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []tasks.RunID{parent, child, grand} {
		h.waitState(id, tasks.StateCanceled)
	}
	pAt, cAt, gAt := h.cancelEventAt(parent), h.cancelEventAt(child), h.cancelEventAt(grand)
	if cAt.Before(pAt) {
		t.Errorf("the child's cancel (%s) was committed before its parent's (%s)", cAt, pAt)
	}
	if gAt.Before(cAt) {
		t.Errorf("the grandchild's cancel (%s) was committed before its parent's (%s)", gAt, cAt)
	}
}

// TestCancelCascade_ARestartAfterAWatchdogStopResumesTheChild: only a cancel
// orphans a child. A parent the watchdog ended (canceled, reason watchdog) leaves
// its child's lifecycle as it was: after a restart the child resumes, as it would
// under any other parent.
func TestCancelCascade_ARestartAfterAWatchdogStopResumesTheChild(t *testing.T) {
	leafStarts := make(chan tasks.RunID, 8)
	defs := chainDefs("wd", 2, leafStarts, nil, nil)
	h := newHarness(t, defs, cascadeOpts(t, 20*time.Millisecond))
	parent := h.start(string(chainKind("wd", 0)), tasks.StartRequest{})
	<-leafStarts
	child := h.childOf(parent)
	if err := h.e.stopOne(context.Background(), parent, tasks.Actor{}, nil, reasonWatchdog, tasks.ErrCanceled); err != nil {
		t.Fatal(err)
	}
	if r := h.waitState(parent, tasks.StateCanceled); r.Reason != reasonWatchdog {
		t.Fatalf("parent %s/%q, want canceled/watchdog", r.State, r.Reason)
	}
	second := h.crashBoot(defs, cascadeOpts(t, 20*time.Millisecond))
	select {
	case id := <-leafStarts:
		if id != child {
			t.Fatalf("run %s started, want the child %s", id, child)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("the child never resumed after the restart: it is %s/%q", second.get(child).State, second.get(child).Reason)
	}
	if r := second.get(child); r.State != tasks.StateRunning {
		t.Errorf("child %s/%q after the restart, want running", r.State, r.Reason)
	}
}

// archiveUnderAParent boots a parent (gotham.watch, which blocks until its context
// ends) and a gotham.archive child started under it, blocks the child's item,
// shuts the executor down and reboots over the same store: the parent runs again
// and the child is awaiting_decision, as F5-18 F1's rig leaves a batch run. next
// is the second boot's archive kind.
func archiveUnderAParent(t *testing.T, next *finishOnlyRig) (h *harness, parent, child tasks.RunID) {
	t.Helper()
	opts := func(o *Options) { o.Catalog = catalogWith(t, "gotham:run") }
	watch := func() tasks.Definition {
		return taskDef("gotham.watch", blockUntilCtx(nil), func(c *tasks.Common) {
			c.Effects = tasks.Writes(tasks.Res("gotham", "watch"))
			c.Cancel = tasks.Cooperative(time.Second)
		})
	}
	first := &finishOnlyRig{block: "file-07", reach: make(chan struct{})}
	h = newHarness(t, []tasks.Definition{watch(), first.def()}, opts)
	parent = h.start("gotham.watch", tasks.StartRequest{})
	ref, err := h.x.Start(context.Background(), "gotham.archive", tasks.StartRequest{
		Mode: tasks.ModeRequestLive, Parent: parent, Trigger: tasks.TriggerParent})
	if err != nil {
		t.Fatal(err)
	}
	<-first.reach
	if err := h.x.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	h = h.reboot([]tasks.Definition{watch(), next.def()}, opts)
	h.waitState(ref.ID, tasks.StateAwaitingDecision)
	h.waitState(parent, tasks.StateRunning)
	return h, parent, ref.ID
}

// TestCancelCascade_ComposesWithFinishOnlyRuns (F5-18 C6 with F5-18 F1): a batch
// child a person has not decided about (awaiting_decision, finish-only marker set
// or not) and one whose Decide(finish) is running its Finish both end canceled
// when the parent is canceled, and the Finish does not run to its end.
func TestCancelCascade_ComposesWithFinishOnlyRuns(t *testing.T) {
	t.Run("awaiting a decision", func(t *testing.T) {
		next := &finishOnlyRig{}
		h, parent, child := archiveUnderAParent(t, next)
		if err := h.x.CancelAs(context.Background(), parent, tasks.Actor{UserID: "gordon"}); err != nil {
			t.Fatal(err)
		}
		if r := h.waitState(child, tasks.StateCanceled); r.Reason != reasonCancel || r.Finished.IsZero() {
			t.Errorf("child %s/%q finished %v, want canceled/cancel and finished", r.State, r.Reason, r.Finished)
		}
		if n := next.sources.Load() + next.items.Load() + next.finishes.Load(); n != 0 {
			t.Errorf("the canceled child ran work (%d source reads, items or finishes)", n)
		}
	})
	t.Run("running its finish", func(t *testing.T) {
		next := &finishOnlyRig{finishBlocks: true, finishStarted: make(chan struct{})}
		h, parent, child := archiveUnderAParent(t, next)
		if err := h.e.DecideAs(context.Background(), child, tasks.DecideFinish, gordon); err != nil {
			t.Fatal(err)
		}
		<-next.finishStarted
		if err := h.x.CancelAs(context.Background(), parent, tasks.Actor{UserID: "gordon"}); err != nil {
			t.Fatal(err)
		}
		if r := h.waitState(child, tasks.StateCanceled); r.Reason != reasonCancel || r.Finished.IsZero() {
			t.Errorf("child %s/%q finished %v, want canceled/cancel and finished (not awaiting_decision: a cancel is final)", r.State, r.Reason, r.Finished)
		}
	})
}

// TestCancelCascade_ACanceledChildReleasesItsStandDown (F5-18 C6 with the
// stand-down holds, 5T.3.7): a child that holds a stand-down on gotham.scan,
// with a scan quiesced under it, is canceled by its parent's cancel; its hold
// is released with it, so the quiesced scan is re-queued and finishes, and no
// stand-down marker outlives the child.
func TestCancelCascade_ACanceledChildReleasesItsStandDown(t *testing.T) {
	scanStarts := make(chan tasks.RunID, 4)
	holding := make(chan struct{}, 1)
	var scans atomic.Int64
	scan := taskDef("gotham.scan", func(rc *tasks.RC, _ noParams) (string, error) {
		if scans.Add(1) == 1 {
			return blockUntilCtx(scanStarts)(rc, noParams{})
		}
		return "scanned", nil
	}, func(c *tasks.Common) { c.Yields = true })
	compact := taskDef("gotham.compact", func(rc *tasks.RC, _ noParams) (string, error) {
		if _, err := rc.StandDown("gotham.scan"); err != nil {
			return "", err
		}
		holding <- struct{}{}
		<-rc.Context().Done()
		return "", context.Cause(rc.Context())
	}, func(c *tasks.Common) { c.Effects = tasks.Writes(tasks.Res("gotham", "store")) })
	watch := taskDef("gotham.watch", func(rc *tasks.RC, _ noParams) (string, error) {
		ref, err := rc.Enqueue("gotham.compact", noParams{})
		if err != nil {
			return "", err
		}
		if _, err := rc.Await(ref); err != nil {
			return "", err
		}
		return "", context.Cause(rc.Context())
	}, func(c *tasks.Common) {
		c.Effects = tasks.Writes(tasks.Res("gotham", "watch"))
		c.Cancel = tasks.Cooperative(time.Second)
	})
	h := newHarness(t, []tasks.Definition{scan, compact, watch}, cascadeOpts(t, 10*time.Millisecond))
	s := h.start("gotham.scan", tasks.StartRequest{})
	<-scanStarts
	parent := h.start("gotham.watch", tasks.StartRequest{})
	<-holding
	h.waitState(s, tasks.StateInterruptedQuiesced)
	child := h.childOf(parent)

	if err := h.x.CancelAs(context.Background(), parent, tasks.Actor{UserID: "gordon"}); err != nil {
		t.Fatal(err)
	}
	if r := h.waitState(child, tasks.StateCanceled); r.Reason != reasonCancel {
		t.Errorf("child %s/%q, want canceled/cancel", r.State, r.Reason)
	}
	if r := h.waitState(s, tasks.StateSucceeded); r.Attempt != 2 {
		t.Errorf("the quiesced scan ended %s on attempt %d, want succeeded on attempt 2", r.State, r.Attempt)
	}
	for m, err := range h.stores.StandDown.List(context.Background()) {
		if err != nil {
			t.Fatal(err)
		}
		t.Errorf("stand-down marker %s outlived its canceled holder", m.Key)
	}
}

// The reach of a cancel is the canceling caller's own (F5-18 C6, the cancel
// authority ruling): a descendant the caller may not cancel, by the rule a direct
// cancel of that run applies, is skipped and keeps running, as it did before any
// cascade existed, and so does everything beneath it. The fixtures: gotham.watch
// (started by gordon) enqueues gotham.yard and gotham.vault; gotham.vault enqueues
// gotham.cell. Everyone holds gotham:run; only gotham:vault cancels a vault.

const (
	scopeGothamRun = rbac.Permission("gotham:run")
	scopeVault     = rbac.Permission("gotham:vault")
)

type reachRig struct {
	h                       *harness
	parent                  tasks.RunID
	yard, vault, cell       tasks.RunID
	vaultStopped, cellStops *atomic.Bool
}

func newReachRig(t *testing.T, edits ...func(*Options)) *reachRig {
	t.Helper()
	return newReachRigWith(t, false, edits...)
}

// newReachRigHeld is newReachRig with a parent that ignores its context until
// the test ends (and a grace of an hour), so a cancel leaves it stopping, not
// ended: the state a second cancel finds while the first is still at work.
func newReachRigHeld(t *testing.T, edits ...func(*Options)) *reachRig {
	t.Helper()
	return newReachRigWith(t, true, edits...)
}

func newReachRigWith(t *testing.T, hold bool, edits ...func(*Options)) *reachRig {
	t.Helper()
	started := make(chan tasks.RunID, 8)
	r := &reachRig{vaultStopped: new(atomic.Bool), cellStops: new(atomic.Bool)}
	release := make(chan struct{})
	if hold {
		t.Cleanup(func() { close(release) }) // runs before the harness's shutdown: cleanups are LIFO
	}
	leaf := func(kind string, stopped *atomic.Bool, edit func(*tasks.Common)) tasks.Definition {
		return taskDef(kind, func(rc *tasks.RC, _ noParams) (string, error) {
			started <- rc.RunID()
			<-rc.Context().Done()
			if stopped != nil {
				stopped.Store(true)
			}
			return "", context.Cause(rc.Context())
		}, func(c *tasks.Common) {
			c.Effects = tasks.Writes(tasks.Res("gotham", kind))
			c.Cancel = tasks.Cooperative(time.Second)
			if edit != nil {
				edit(c)
			}
		})
	}
	watch := taskDef("gotham.watch", func(rc *tasks.RC, _ noParams) (string, error) {
		for _, k := range []tasks.Kind{"gotham.yard", "gotham.vault"} {
			if _, err := rc.Enqueue(k, noParams{}); err != nil {
				return "", err
			}
		}
		started <- rc.RunID()
		if hold {
			<-release
		} else {
			<-rc.Context().Done()
		}
		return "", context.Cause(rc.Context())
	}, func(c *tasks.Common) {
		c.Effects = tasks.Writes(tasks.Res("gotham", "watch"))
		c.Cancel = tasks.Cooperative(time.Second)
		if hold {
			c.Cancel = tasks.Cooperative(time.Hour)
		}
	})
	vault := taskDef("gotham.vault", func(rc *tasks.RC, _ noParams) (string, error) {
		if _, err := rc.Enqueue("gotham.cell", noParams{}); err != nil {
			return "", err
		}
		started <- rc.RunID()
		<-rc.Context().Done()
		r.vaultStopped.Store(true)
		return "", context.Cause(rc.Context())
	}, func(c *tasks.Common) {
		c.Effects = tasks.Writes(tasks.Res("gotham", "vault"))
		c.Cancel = tasks.Cooperative(time.Second)
		c.Perms.Cancel = rbac.Need(scopeVault)
	})
	defs := []tasks.Definition{watch, leaf("gotham.yard", nil, nil), vault, leaf("gotham.cell", r.cellStops, nil)}
	r.h = newHarness(t, defs, func(o *Options) {
		cascadeOpts(t, 10*time.Millisecond)(o)
		o.Catalog = catalogWith(t, scopeGothamRun, scopeVault)
		withPrincipal(o)
		for _, e := range edits {
			e(o)
		}
	})
	ref, err := r.h.x.Start(asUser("gordon", scopeGothamRun), "gotham.watch", tasks.StartRequest{Mode: tasks.ModeRequestLive})
	if err != nil {
		t.Fatal(err)
	}
	r.parent = ref.ID
	for range 4 { // watch, yard, vault, cell
		<-started
	}
	for _, id := range r.h.children(r.parent) {
		switch r.h.get(id).Kind {
		case "gotham.yard":
			r.yard = id
		case "gotham.vault":
			r.vault = id
		}
	}
	if kids := r.h.children(r.vault); len(kids) == 1 {
		r.cell = kids[0]
	}
	if r.yard == "" || r.vault == "" || r.cell == "" {
		t.Fatalf("the tree is incomplete: yard %q vault %q cell %q", r.yard, r.vault, r.cell)
	}
	return r
}

// logLines is the parent's run log, one message per line.
func (r *reachRig) logLines(id tasks.RunID) []string {
	r.h.t.Helper()
	var out []string
	for p, err := range r.h.stores.Runs.Pages(context.Background(), id, tasks.PageQuery{Page: tasks.PageLog, Limit: 1000}) {
		if err != nil {
			r.h.t.Fatal(err)
		}
		out = append(out, p.Log.Message)
	}
	return out
}

// TestCancelCascade_ASkippedChildKeepsRunning is cell (a) and (b): harley may
// cancel the parent and the yard (gotham:run) but not the vault (its kind's
// Perms.Cancel is gotham:vault, and harley did not start it), so the cancel leaves
// the vault running and, beneath it, the cell, which harley could cancel on its
// own but cannot reach through the vault. The skip is in the parent's log and in
// the cancel's report.
func TestCancelCascade_ASkippedChildKeepsRunning(t *testing.T) {
	var slogOut syncBuf
	r := newReachRig(t, func(o *Options) {
		o.Logger = slog.New(slog.NewTextHandler(&slogOut, nil))
	})
	ctx, rep := WithCancelReport(asUser("harley", scopeGothamRun))
	if err := r.h.x.Cancel(ctx, r.parent); err != nil {
		t.Fatalf("a cancel that skipped a run must still succeed: %v", err)
	}
	if p := r.h.waitState(r.parent, tasks.StateCanceled); p.Reason != reasonCancel {
		t.Errorf("parent %s/%q, want canceled/cancel", p.State, p.Reason)
	}
	if y := r.h.waitState(r.yard, tasks.StateCanceled); y.Reason != reasonCancel {
		t.Errorf("the yard (cancelable by harley) %s/%q, want canceled/cancel", y.State, y.Reason)
	}
	r.h.stays(r.vault, tasks.StateRunning, 300*time.Millisecond)
	r.h.stays(r.cell, tasks.StateRunning, 300*time.Millisecond)
	if r.vaultStopped.Load() || r.cellStops.Load() {
		t.Errorf("a skipped run's context ended (vault %v, cell %v)", r.vaultStopped.Load(), r.cellStops.Load())
	}
	if got := rep.Skipped(); len(got) != 1 || got[0] != r.vault {
		t.Errorf("report names %v, want exactly the vault %s (the cell is beneath it, not reached)", got, r.vault)
	}
	if out := slogOut.String(); !strings.Contains(out, "cancel did not reach a descendant") || !strings.Contains(out, string(r.vault)) {
		t.Errorf("the skipped vault %s is not named in the server log:\n%s", r.vault, out)
	}
	// The parent's run log is readable by whoever may view the parent's kind, who
	// may not be allowed to see the vault: the line counts the skipped runs and
	// names none of them (the ids are in the report, the audit record and slog).
	var counted bool
	for _, l := range r.logLines(r.parent) {
		for _, secret := range []string{string(r.vault), "gotham.vault", string(r.cell), "gotham.cell", string(r.yard)} {
			if strings.Contains(l, secret) {
				t.Errorf("the parent's log names %q: %q", secret, l)
			}
		}
		if strings.Contains(l, "did not reach 1 descendant run(s)") {
			counted = true
		}
	}
	if !counted {
		t.Errorf("the parent's log does not say how many runs the cancel left running: %q", r.logLines(r.parent))
	}
}

// TestCancelCascade_TheReachIsTheCallers: the same tree canceled by the user who
// started it (the run's own actor may cancel every run of it), by a holder of
// core:tasks.manage, by a holder of the vault's scope, and by a kernel-internal
// CancelAs (no checker limits it) loses every run and skips none.
func TestCancelCascade_TheReachIsTheCallers(t *testing.T) {
	for _, tc := range []struct {
		name string
		ctx  context.Context
		by   func(*reachRig) error
	}{
		{name: "the run's own actor", ctx: asUser("gordon", scopeGothamRun)},
		{name: "core:tasks.manage", ctx: asUser("harley", scopeGothamRun, scopeTasksManage)},
		{name: "the vault's scope", ctx: asUser("harley", scopeGothamRun, scopeVault)},
		{name: "a kernel-internal CancelAs", ctx: context.Background(), by: func(r *reachRig) error {
			return r.h.x.CancelAs(context.Background(), r.parent, tasks.Actor{UserID: "harley"})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newReachRig(t)
			ctx, rep := WithCancelReport(tc.ctx)
			var err error
			if tc.by != nil {
				err = tc.by(r)
			} else {
				err = r.h.x.Cancel(ctx, r.parent)
			}
			if err != nil {
				t.Fatal(err)
			}
			for name, id := range map[string]tasks.RunID{"parent": r.parent, "yard": r.yard, "vault": r.vault, "cell": r.cell} {
				if got := r.h.waitState(id, tasks.StateCanceled); got.Reason != reasonCancel {
					t.Errorf("%s %s/%q, want canceled/cancel", name, got.State, got.Reason)
				}
			}
			if got := rep.Skipped(); len(got) != 0 {
				t.Errorf("report names %v, want none", got)
			}
		})
	}
}

// allReach is the verdict a kernel-internal cancel of parent stores (All, for the
// parent's own actor): the tests that stage the first half of a cancel with
// stopOne pass it where Cancel would.
func (h *harness) allReach(parent tasks.RunID) *tasks.CancelReach {
	h.t.Helper()
	return &tasks.CancelReach{All: true, Actor: h.get(parent).Actor}
}

// The verdict a cancel stores (tasks.CancelReach) is what the dispatch guard and the
// boot pass read: they have no caller to ask. The fixtures: gotham.watch (started by
// gordon) enqueues gotham.yard and gotham.armory; gotham.yard enqueues gotham.annex.
// gotham.armory and gotham.safe are cancelable only with gotham:vault; harley holds
// gotham:run and cancels as a stranger. gotham.lockdown (also gordon's) holds the
// armory's resource, so the watch's armory is still queued when the cancel comes.

type verdictRig struct {
	h                                    *harness
	defs                                 []tasks.Definition
	opts                                 func(*Options)
	watch, lockdown, yard, armory, annex tasks.RunID
}

func newVerdictRig(t *testing.T) *verdictRig {
	t.Helper()
	park := func(rc *tasks.RC, _ noParams) (string, error) {
		<-rc.Context().Done()
		return "", context.Cause(rc.Context())
	}
	enqueueThenPark := func(kinds ...tasks.Kind) func(*tasks.RC, noParams) (string, error) {
		return func(rc *tasks.RC, p noParams) (string, error) {
			for _, k := range kinds {
				if _, err := rc.Enqueue(k, noParams{}); err != nil {
					return "", err
				}
			}
			return park(rc, p)
		}
	}
	res := func(name string) func(*tasks.Common) {
		return func(c *tasks.Common) {
			c.Effects = tasks.Writes(tasks.Res("gotham", name))
			c.Cancel = tasks.Cooperative(time.Second)
		}
	}
	vault := func(name string) func(*tasks.Common) {
		return func(c *tasks.Common) {
			res(name)(c)
			c.Perms.Cancel = rbac.Need(scopeVault)
		}
	}
	r := &verdictRig{}
	r.defs = []tasks.Definition{
		taskDef("gotham.watch", enqueueThenPark("gotham.yard", "gotham.armory"), res("watch")),
		taskDef("gotham.yard", enqueueThenPark("gotham.annex"), res("yard")),
		taskDef("gotham.annex", park, res("annex")),
		taskDef("gotham.armory", park, vault("armory")),
		taskDef("gotham.safe", park, vault("safe")),
		taskDef("gotham.lockdown", park, func(c *tasks.Common) { res("armory")(c) }),
	}
	r.opts = func(o *Options) {
		cascadeOpts(t, 10*time.Millisecond)(o)
		o.Catalog = catalogWith(t, scopeGothamRun, scopeVault)
		withPrincipal(o)
	}
	r.h = newHarness(t, r.defs, r.opts)
	start := func(kind string) tasks.RunID {
		ref, err := r.h.x.Start(asUser("gordon", scopeGothamRun), tasks.Kind(kind), tasks.StartRequest{Mode: tasks.ModeRequestLive})
		if err != nil {
			t.Fatal(err)
		}
		return ref.ID
	}
	r.lockdown = r.h.waitState(start("gotham.lockdown"), tasks.StateRunning).ID
	r.watch = start("gotham.watch")
	r.yard = r.child(r.watch, "gotham.yard")
	r.armory = r.child(r.watch, "gotham.armory")
	r.annex = r.child(r.yard, "gotham.annex")
	r.h.waitState(r.yard, tasks.StateRunning)
	r.h.waitState(r.annex, tasks.StateRunning)
	r.h.stays(r.armory, tasks.StateQueued, 50*time.Millisecond) // behind the lockdown
	return r
}

// child is the child of parent of the given kind; it waits for it to exist.
func (r *verdictRig) child(parent tasks.RunID, kind tasks.Kind) tasks.RunID {
	r.h.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		for _, id := range r.h.children(parent) {
			if r.h.get(id).Kind == kind {
				return id
			}
		}
		if time.Now().After(deadline) {
			r.h.t.Fatalf("%s has no %s child", parent, kind)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// late stages a child of parent after its cancel: the state the race between a
// parent's Enqueue and its cancel leaves. by names the actor it carries.
func (r *verdictRig) late(h *harness, parent tasks.RunID, kind tasks.Kind, by string) tasks.RunID {
	h.t.Helper()
	ref, err := h.x.Start(asUser(by, scopeGothamRun), kind, tasks.StartRequest{
		Mode: tasks.ModeRequestLive, Parent: parent, Trigger: tasks.TriggerParent})
	if err != nil {
		h.t.Fatal(err)
	}
	return ref.ID
}

// harleyCancels is a live cancel by someone who holds gotham:run only.
func (r *verdictRig) harleyCancels() {
	r.h.t.Helper()
	if err := r.h.x.Cancel(asUser("harley", scopeGothamRun), r.watch); err != nil {
		r.h.t.Fatal(err)
	}
	r.h.waitState(r.watch, tasks.StateCanceled)
	r.h.waitState(r.yard, tasks.StateCanceled)
	r.h.waitState(r.annex, tasks.StateCanceled)
}

// releaseArmory lets the queued armory through the gate: the lockdown ends.
func (r *verdictRig) releaseArmory() {
	r.h.t.Helper()
	if err := r.h.x.CancelAs(context.Background(), r.lockdown, tasks.Actor{UserID: "gordon"}); err != nil {
		r.h.t.Fatal(err)
	}
	r.h.waitState(r.lockdown, tasks.StateCanceled)
}

// TestCancelCascade_TheVerdictIsStoredAndCopied: a cancel by a caller who holds
// gotham:run stores the kinds it may cancel on the canceled run (not the vault's
// two), for the run's own actor, and every run the cascade ends carries the same
// copy. A kernel-internal cancel stores All.
func TestCancelCascade_TheVerdictIsStoredAndCopied(t *testing.T) {
	r := newVerdictRig(t)
	r.harleyCancels()
	for name, id := range map[string]tasks.RunID{"the canceled run": r.watch, "its child": r.yard, "its grandchild": r.annex} {
		got := r.h.get(id).CancelReach
		if got == nil || got.All || got.Actor != r.h.get(r.watch).Actor {
			t.Fatalf("%s carries %+v, want a verdict that is not All, for gordon's actor", name, got)
		}
		for _, k := range []tasks.Kind{"gotham.watch", "gotham.yard", "gotham.annex", "gotham.lockdown"} {
			if _, ok := slices.BinarySearch(got.Kinds, k); !ok {
				t.Errorf("%s: %s is not in the verdict %v, though harley may cancel it", name, k, got.Kinds)
			}
		}
		for _, k := range []tasks.Kind{"gotham.armory", "gotham.safe"} {
			if _, ok := slices.BinarySearch(got.Kinds, k); ok {
				t.Errorf("%s: %s is in the verdict %v, though harley may not cancel it", name, k, got.Kinds)
			}
		}
	}
	k := newVerdictRig(t)
	if err := k.h.x.CancelAs(context.Background(), k.watch, tasks.Actor{UserID: "harley"}); err != nil {
		t.Fatal(err)
	}
	k.h.waitState(k.watch, tasks.StateCanceled)
	if got := k.h.get(k.watch).CancelReach; got == nil || !got.All {
		t.Fatalf("a kernel-internal cancel stored %+v, want All", got)
	}
}

// TestCancelCascade_TheDispatchGuardAppliesTheStoredVerdict (c1, c3, c4, c6, c7): a
// child of a canceled run that the verdict covers is canceled at dispatch, and one
// it does not cover is dispatched as on HEAD.
func TestCancelCascade_TheDispatchGuardAppliesTheStoredVerdict(t *testing.T) {
	t.Run("c1 a skipped child still queued is not canceled", func(t *testing.T) {
		r := newVerdictRig(t)
		r.harleyCancels()
		r.releaseArmory()
		r.h.waitState(r.armory, tasks.StateRunning)
		r.h.stays(r.armory, tasks.StateRunning, 200*time.Millisecond)
	})
	t.Run("c3 a late child of a kind in the set is canceled", func(t *testing.T) {
		r := newVerdictRig(t)
		r.harleyCancels()
		late := r.late(r.h, r.watch, "gotham.yard", "gordon")
		if got := r.h.waitState(late, tasks.StateCanceled); got.Reason != reasonCancel {
			t.Errorf("late child %s/%q, want canceled/cancel", got.State, got.Reason)
		}
	})
	t.Run("c3 a late child under a cascaded child is judged by the original verdict", func(t *testing.T) {
		r := newVerdictRig(t)
		r.harleyCancels()
		in := r.late(r.h, r.yard, "gotham.annex", "gordon")
		out := r.late(r.h, r.yard, "gotham.safe", "gordon")
		r.h.waitState(in, tasks.StateCanceled)
		r.h.waitState(out, tasks.StateRunning)
		r.h.stays(out, tasks.StateRunning, 200*time.Millisecond)
	})
	t.Run("c4 a late child of a kind outside the set is left alone", func(t *testing.T) {
		r := newVerdictRig(t)
		r.harleyCancels()
		late := r.late(r.h, r.watch, "gotham.safe", "gordon")
		r.h.waitState(late, tasks.StateRunning)
		r.h.stays(late, tasks.StateRunning, 200*time.Millisecond)
	})
	t.Run("c6 a kernel-internal cancel cascades fully through the guard", func(t *testing.T) {
		r := newVerdictRig(t)
		if err := r.h.x.CancelAs(context.Background(), r.watch, tasks.Actor{UserID: "harley"}); err != nil {
			t.Fatal(err)
		}
		r.h.waitState(r.watch, tasks.StateCanceled)
		r.h.waitState(r.yard, tasks.StateCanceled)
		r.h.waitState(r.armory, tasks.StateCanceled) // queued, taken by the cascade itself
		r.releaseArmory()                            // a late armory is gated behind the lockdown until then
		for _, kind := range []tasks.Kind{"gotham.safe", "gotham.armory", "gotham.yard"} {
			late := r.late(r.h, r.watch, kind, "gordon")
			if got := r.h.waitState(late, tasks.StateCanceled); got.Reason != reasonCancel {
				t.Errorf("late %s %s/%q, want canceled/cancel", kind, got.State, got.Reason)
			}
		}
	})
	t.Run("c7 a child carrying another actor is left alone", func(t *testing.T) {
		r := newVerdictRig(t)
		r.harleyCancels()
		late := r.late(r.h, r.watch, "gotham.yard", "joker")
		r.h.waitState(late, tasks.StateRunning)
		r.h.stays(late, tasks.StateRunning, 200*time.Millisecond)
	})
}

// TestCancelCascade_TheBootPassAppliesTheStoredVerdict (c2, c5, c6, c7): the first
// half of a cancel is staged and the process dies before the cascade. At the next
// boot a child the stored verdict covers is ended canceled, with the grandchildren
// below it (judged by the ROOT's verdict, not the child's own), and one it does not
// cover is resumed as on HEAD.
func TestCancelCascade_TheBootPassAppliesTheStoredVerdict(t *testing.T) {
	harley := tasks.Actor{UserID: "harley", AuthMethod: "session"}
	half := func(r *verdictRig, reach *tasks.CancelReach) {
		t.Helper()
		if err := r.h.e.stopOne(context.Background(), r.watch, harley, reach, reasonCancel, tasks.ErrCanceled); err != nil {
			t.Fatal(err)
		}
		r.h.waitState(r.watch, tasks.StateCanceled)
	}
	t.Run("c2 a skipped child unfinished at restart is not ended", func(t *testing.T) {
		r := newVerdictRig(t)
		half(r, r.h.e.reachOf(r.h.get(r.watch), harley, grants{scopeGothamRun: true}))
		second := r.h.crashBoot(r.defs, r.opts)
		for name, id := range map[string]tasks.RunID{"the child": r.yard, "the grandchild": r.annex} {
			if got := second.waitState(id, tasks.StateCanceled); got.Reason != reasonCancel {
				t.Errorf("%s after the restart: %s/%q, want canceled/cancel", name, got.State, got.Reason)
			}
		}
		second.stays(r.armory, tasks.StateQueued, 200*time.Millisecond) // behind the resumed lockdown, not canceled
	})
	t.Run("c5 a parent with no stored verdict leaves its orphans alone", func(t *testing.T) {
		r := newVerdictRig(t)
		half(r, nil)
		second := r.h.crashBoot(r.defs, r.opts)
		for name, id := range map[string]tasks.RunID{"the yard": r.yard, "the armory": r.armory, "the annex": r.annex} {
			if got := second.get(id); got.State == tasks.StateCanceled {
				t.Errorf("%s was ended canceled at boot with no verdict stored", name)
			}
		}
		second.waitState(r.yard, tasks.StateRunning)
		second.stays(r.yard, tasks.StateRunning, 200*time.Millisecond)
	})
	t.Run("c6 a kernel-internal cancel ends every orphan", func(t *testing.T) {
		r := newVerdictRig(t)
		half(r, r.h.allReach(r.watch))
		second := r.h.crashBoot(r.defs, r.opts)
		for name, id := range map[string]tasks.RunID{"the yard": r.yard, "the armory": r.armory, "the annex": r.annex} {
			if got := second.waitState(id, tasks.StateCanceled); got.Reason != reasonCancel {
				t.Errorf("%s: %s/%q, want canceled/cancel", name, got.State, got.Reason)
			}
		}
		// An orphan the boot pass ended keeps the verdict: a child staged under it
		// later is judged by the original cancel.
		late := r.late(second, r.annex, "gotham.safe", "gordon")
		if got := second.waitState(late, tasks.StateCanceled); got.Reason != reasonCancel {
			t.Errorf("a late child of an orphan ended at boot: %s/%q, want canceled/cancel", got.State, got.Reason)
		}
	})
	t.Run("c7 an orphan carrying another actor is not ended", func(t *testing.T) {
		r := newVerdictRig(t)
		odd := r.late(r.h, r.yard, "gotham.safe", "joker") // a child of the yard, not enqueued by the run
		half(r, r.h.allReach(r.watch))
		second := r.h.crashBoot(r.defs, r.opts)
		second.waitState(r.annex, tasks.StateCanceled)
		second.waitState(r.yard, tasks.StateCanceled)
		if got := second.get(odd); got.State == tasks.StateCanceled {
			t.Errorf("the joker's child of the yard was ended at boot (%s/%q)", got.State, got.Reason)
		}
	})
}
