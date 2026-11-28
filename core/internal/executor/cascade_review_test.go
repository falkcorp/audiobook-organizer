// file: core/internal/executor/cascade_review_test.go
// version: 0.1.0
// guid: 01392f9d-7b0d-42d9-b2f4-f37bc7051140
// last-edited: 2026-11-28

package executor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"iter"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/internal/taskstore"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/tasks"
)

// F5-18 C6, the review of the stored per-kind verdict: a repeat or racing cancel
// is bounded by its own caller, a held child is left for its module and not
// reported, the cascade finds a child its parent enqueued late, runs on a bounded
// pool, and is the cancel's alone.

// syncBuf is a log sink two goroutines may write.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// cascadeSpy is a RunStore the cascade cells instrument: afterScan runs once, when
// the next Unfinished scan has been read to its end (the moment a child can still
// appear that the scan did not see), and onMove runs inside every Transition,
// where it may delay the move or refuse it.
type cascadeSpy struct {
	tasks.RunStore
	mu        sync.Mutex
	afterScan func()
	onMove    func(id tasks.RunID, next tasks.RunRecord) error
}

// install puts the spy between an executor and its store.
func (s *cascadeSpy) install(o *Options) {
	o.NewBatch = o.Stores.Runs.(batchOpener).NewBatch
	s.RunStore = o.Stores.Runs
	o.Stores.Runs = s
}

func (s *cascadeSpy) arm(f func()) {
	s.mu.Lock()
	s.afterScan = f
	s.mu.Unlock()
}

func (s *cascadeSpy) moves(f func(id tasks.RunID, next tasks.RunRecord) error) {
	s.mu.Lock()
	s.onMove = f
	s.mu.Unlock()
}

func (s *cascadeSpy) Unfinished(ctx context.Context) iter.Seq2[tasks.RunRecord, error] {
	return func(yield func(tasks.RunRecord, error) bool) {
		for rec, err := range s.RunStore.Unfinished(ctx) {
			if !yield(rec, err) {
				return
			}
		}
		s.mu.Lock()
		f := s.afterScan
		s.afterScan = nil
		s.mu.Unlock()
		if f != nil {
			f()
		}
	}
}

func (s *cascadeSpy) Transition(ctx context.Context, b *storage.Batch, id tasks.RunID, want tasks.Expect, next tasks.RunRecord) error {
	s.mu.Lock()
	f := s.onMove
	s.mu.Unlock()
	if f != nil {
		if err := f(id, next); err != nil {
			return err
		}
	}
	return s.RunStore.Transition(ctx, b, id, want, next)
}

// cancelEventActors is the actor of each cancel event run id recorded (nil for a
// cancel with no actor: the guard's and the boot pass's).
func (h *harness) cancelEventActors(id tasks.RunID) []*tasks.Actor {
	h.t.Helper()
	var out []*tasks.Actor
	for p, err := range h.stores.Runs.Pages(context.Background(), id, tasks.PageQuery{Page: tasks.PageEvents, Limit: 1000}) {
		if err != nil {
			h.t.Fatalf("events: %v", err)
		}
		if p.Event.Event == "cancel" {
			out = append(out, p.Event.Actor)
		}
	}
	return out
}

// TestCancelCascade_ARepeatCancelIsBoundedByItsOwnCaller (B1): the cascade of a
// live cancel is the verdict the first cancel stored narrowed by the verdict of the
// caller itself. Whoever repeats a cancel (the first one never finished, or two
// cancels raced) ends no run its own authority does not cover, and one with more
// authority than the first caller gets no more than the first stored.
//
// The tree: gotham.watch (started by gordon) enqueues gotham.yard and
// gotham.vault; the vault enqueues gotham.cell. Everyone holds gotham:run; only
// gotham:vault cancels a vault, and harley is no one's owner.
func TestCancelCascade_ARepeatCancelIsBoundedByItsOwnCaller(t *testing.T) {
	gordon := tasks.Actor{UserID: "gordon", AuthMethod: "session"}
	harley := tasks.Actor{UserID: "harley", AuthMethod: "session"}
	stage := func(t *testing.T, r *reachRig, by tasks.Actor, reach *tasks.CancelReach) {
		t.Helper()
		if err := r.h.e.stopOne(context.Background(), r.parent, by, reach, reasonCancel, tasks.ErrCanceled); err != nil {
			t.Fatal(err)
		}
	}
	// untouched: the vault and the cell run on, with their contexts alive and no cancel event on either.
	untouched := func(t *testing.T, r *reachRig) {
		t.Helper()
		r.h.stays(r.vault, tasks.StateRunning, 300*time.Millisecond)
		r.h.stays(r.cell, tasks.StateRunning, 300*time.Millisecond)
		if r.vaultStopped.Load() || r.cellStops.Load() {
			t.Errorf("a run out of reach had its context canceled (vault %v, cell %v)", r.vaultStopped.Load(), r.cellStops.Load())
		}
		for name, id := range map[string]tasks.RunID{"vault": r.vault, "cell": r.cell} {
			if evs := r.h.cancelEventActors(id); len(evs) != 0 {
				t.Errorf("the %s carries cancel events %v, want none", name, evs)
			}
		}
	}
	skippedOnly := func(t *testing.T, rep *CancelReport, id tasks.RunID) {
		t.Helper()
		if got := rep.Skipped(); len(got) != 1 || got[0] != id {
			t.Errorf("report names %v, want exactly %s", got, id)
		}
		if !rep.Acted() {
			t.Errorf("a cancel that ended the yard and skipped the vault reports no action")
		}
	}
	t.Run("a narrower repeat ends nothing extra", func(t *testing.T) {
		r := newReachRig(t)
		stage(t, r, gordon, r.h.allReach(r.parent)) // the first half of the owner's cancel: the parent only
		r.h.waitState(r.parent, tasks.StateCanceled)
		ctx, rep := WithCancelReport(asUser("harley", scopeGothamRun))
		if err := r.h.x.Cancel(ctx, r.parent); !errors.Is(err, tasks.ErrRunNotFound) {
			t.Fatalf("repeat cancel of an ended run: %v, want ErrRunNotFound (HEAD's rule)", err)
		}
		if got := r.h.waitState(r.yard, tasks.StateCanceled); got.Reason != reasonCancel {
			t.Errorf("the yard (harley may cancel it) is %s/%q, want canceled/cancel", got.State, got.Reason)
		}
		untouched(t, r)
		skippedOnly(t, rep, r.vault)
	})
	t.Run("a broader repeat is bounded by what is stored", func(t *testing.T) {
		r := newReachRig(t)
		stage(t, r, harley, r.h.e.reachOf(r.h.get(r.parent), harley, grants{scopeGothamRun: true}))
		r.h.waitState(r.parent, tasks.StateCanceled)
		ctx, rep := WithCancelReport(asUser("gordon", scopeGothamRun)) // the owner: All, as a first canceler
		if err := r.h.x.Cancel(ctx, r.parent); !errors.Is(err, tasks.ErrRunNotFound) {
			t.Fatalf("repeat cancel of an ended run: %v, want ErrRunNotFound", err)
		}
		r.h.waitState(r.yard, tasks.StateCanceled)
		untouched(t, r)
		skippedOnly(t, rep, r.vault)
	})
	t.Run("the second of two racing cancels", func(t *testing.T) {
		// The parent is still stopping, so the second cancel's own stop is answered
		// nil (errAlreadyStopping) and its cascade runs from the verdict the first stored.
		r := newReachRigHeld(t)
		stage(t, r, gordon, r.h.allReach(r.parent))
		ctx, rep := WithCancelReport(asUser("harley", scopeGothamRun))
		if err := r.h.x.Cancel(ctx, r.parent); err != nil {
			t.Fatalf("second cancel of a stopping run: %v, want nil", err)
		}
		if got := r.h.get(r.parent); got.State != tasks.StateStopping || got.CancelReach == nil || !got.CancelReach.All {
			t.Fatalf("parent %s with verdict %+v, want stopping with the owner's All untouched", got.State, got.CancelReach)
		}
		r.h.waitState(r.yard, tasks.StateCanceled)
		untouched(t, r)
		skippedOnly(t, rep, r.vault)
	})
	t.Run("two concurrent cancels end what the first to commit allows", func(t *testing.T) {
		// Either order is possible; the outcome must follow the verdict that was
		// stored, never the other caller's: the vault ends exactly when the owner
		// stored first, and no cancel event on it ever names harley.
		for range 5 {
			r := newReachRigHeld(t)
			var wg sync.WaitGroup
			for _, who := range []string{"gordon", "harley"} {
				wg.Go(func() {
					_ = r.h.x.Cancel(asUser(who, scopeGothamRun), r.parent)
				})
			}
			wg.Wait()
			stored := r.h.get(r.parent).CancelReach
			if stored == nil {
				t.Fatal("no verdict stored by either cancel")
			}
			ended := r.h.get(r.vault).State != tasks.StateRunning
			if ended != stored.All {
				t.Errorf("stored All=%v but the vault ended=%v", stored.All, ended)
			}
			for _, a := range r.h.cancelEventActors(r.vault) {
				if a != nil && a.UserID == "harley" {
					t.Errorf("the vault's cancel is stamped harley: %+v", a)
				}
			}
		}
	})
	t.Run("a repeat while a late covered child waits behind a gate", func(t *testing.T) {
		// The armory (a vault kind, queued behind the lockdown that holds its
		// resource) is covered by the owner's stored verdict. The dispatch guard
		// cannot reach it until it is released; harley's repeat must not end it.
		r := newVerdictRig(t)
		if err := r.h.e.stopOne(context.Background(), r.watch, gordon, r.h.allReach(r.watch), reasonCancel, tasks.ErrCanceled); err != nil {
			t.Fatal(err)
		}
		r.h.waitState(r.watch, tasks.StateCanceled)
		ctx, rep := WithCancelReport(asUser("harley", scopeGothamRun))
		if err := r.h.x.Cancel(ctx, r.watch); !errors.Is(err, tasks.ErrRunNotFound) {
			t.Fatalf("repeat cancel: %v, want ErrRunNotFound", err)
		}
		r.h.waitState(r.yard, tasks.StateCanceled)
		r.h.waitState(r.annex, tasks.StateCanceled)
		r.h.stays(r.armory, tasks.StateQueued, 200*time.Millisecond)
		if evs := r.h.cancelEventActors(r.armory); len(evs) != 0 {
			t.Errorf("the armory carries cancel events %v, want none", evs)
		}
		if got := rep.Skipped(); len(got) != 1 || got[0] != r.armory {
			t.Errorf("report names %v, want the armory %s", got, r.armory)
		}
		// What the repeat ended carries the verdict the first cancel stored, not the
		// repeat caller's narrower one: the guard and the boot pass judge a later
		// descendant by the original authority.
		for name, id := range map[string]tasks.RunID{"yard": r.yard, "annex": r.annex} {
			if got := r.h.get(id).CancelReach; got == nil || !got.All {
				t.Errorf("the %s ended by the repeat carries %+v, want the stored All", name, got)
			}
		}
		// A vault-kind grandchild enqueued under the annex after all this is ended at
		// dispatch: harley's own verdict does not list the vault kinds.
		late := r.late(r.h, r.annex, "gotham.safe", "gordon")
		if got := r.h.waitState(late, tasks.StateCanceled); got.Reason != reasonCancel {
			t.Errorf("the late safe is %s/%q, want canceled/cancel", got.State, got.Reason)
		}
	})
}

// TestCancelCascade_AHeldChildUnderACancelWithAKindIndependentArm (S1): a child of a
// disabled module has no definition, so no per-kind verdict can cover its kind. The
// kind-independent arms (the run's own actor, core:tasks.manage) admit every kind,
// held ones included: the owner's cancel leaves the held child alone while its module
// is off, reports no skip, and ends it when the module is back. A caller with only
// the per-kind arm never covers a kind it cannot read, so the child runs on.
func TestCancelCascade_AHeldChildUnderACancelWithAKindIndependentArm(t *testing.T) {
	const scopeArkham = "arkham:run"
	for _, tc := range []struct {
		name      string
		who       context.Context
		wantEnded bool
	}{
		{name: "the run's own actor", who: asUser("gordon", scopeGothamRun), wantEnded: true},
		{name: "core:tasks.manage", who: asUser("harley", scopeGothamRun, scopeTasksManage), wantEnded: true},
		{name: "a stranger with the per-kind arm only", who: asUser("harley", scopeGothamRun), wantEnded: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			var bodies atomic.Int32
			patrol := taskDef("gotham.patrol", blockUntilCtx(nil))
			arkham := func() []tasks.Definition {
				return []tasks.Definition{taskDef("arkham.intake", func(rc *tasks.RC, _ noParams) (string, error) {
					bodies.Add(1)
					<-rc.Context().Done()
					return "", context.Cause(rc.Context())
				})}
			}
			opts := func(extra ...func(*Options)) func(*Options) {
				return func(o *Options) {
					withPrincipal(o)
					o.Config = Config{DispatchTick: 10 * time.Millisecond, WatchdogInterval: time.Hour}
					o.Catalog = catalogWith(t, scopeGothamRun, scopeArkham)
					for _, e := range extra {
						e(o)
					}
				}
			}
			h := newHarness(t, append([]tasks.Definition{patrol}, arkham()...), opts())
			if err := h.x.Pause(ctx, "lockdown"); err != nil {
				t.Fatal(err)
			}
			gordon := asUser("gordon", scopeGothamRun, scopeArkham)
			ref, err := h.x.Start(gordon, "gotham.patrol", tasks.StartRequest{Mode: tasks.ModeRequestLive})
			if err != nil {
				t.Fatal(err)
			}
			child, err := h.x.Start(gordon, "arkham.intake", tasks.StartRequest{Mode: tasks.ModeRequestLive, Parent: ref.ID, Trigger: tasks.TriggerParent})
			if err != nil {
				t.Fatal(err)
			}
			if err := h.x.Shutdown(ctx); err != nil {
				t.Fatal(err)
			}
			held := h.reboot([]tasks.Definition{patrol}, opts(disabledArkham))
			cctx, rep := WithCancelReport(tc.who)
			if err := held.x.Cancel(cctx, ref.ID); err != nil {
				t.Fatalf("cancel with a held child: %v, want nil", err)
			}
			if r := held.get(child.ID); r.State != tasks.StateQueued {
				t.Fatalf("the held child is %s, want queued and untouched", r.State)
			}
			if got := rep.Skipped(); len(got) != 0 {
				t.Errorf("a held child was reported as a skip: %v (it is left for its module, not refused)", got)
			}
			if err := held.x.Shutdown(ctx); err != nil {
				t.Fatal(err)
			}
			live := held.reboot(append([]tasks.Definition{patrol}, arkham()...), opts())
			if err := live.x.Unpause(ctx); err != nil {
				t.Fatal(err)
			}
			if tc.wantEnded {
				if r := live.waitState(child.ID, tasks.StateCanceled); r.Reason != reasonCancel {
					t.Errorf("the child ended %s/%q, want canceled/cancel", r.State, r.Reason)
				}
				if n := bodies.Load(); n != 0 {
					t.Errorf("the child's body started %d time(s), want never", n)
				}
				return
			}
			live.waitState(child.ID, tasks.StateRunning)
			live.stays(child.ID, tasks.StateRunning, 200*time.Millisecond)
		})
	}
}

// TestCancelCascade_AGrandchildEnqueuedBeforeItsParentIsStoppedIsStopped (S3): the
// cascade reads each level's children after the level above has been stopped. A
// child the yard enqueues after the cancel read the watch's children but before
// the yard's own stop is a running run no guard ever sees; a single read taken at
// the start would leave it running until the next restart.
func TestCancelCascade_AGrandchildEnqueuedBeforeItsParentIsStoppedIsStopped(t *testing.T) {
	spy := &cascadeSpy{}
	park := func(rc *tasks.RC, _ noParams) (string, error) {
		<-rc.Context().Done()
		return "", context.Cause(rc.Context())
	}
	res := func(name string) func(*tasks.Common) {
		return func(c *tasks.Common) {
			c.Effects = tasks.Writes(tasks.Res("gotham", name))
			c.Cancel = tasks.Cooperative(time.Second)
		}
	}
	defs := []tasks.Definition{
		taskDef("gotham.watch", func(rc *tasks.RC, p noParams) (string, error) {
			if _, err := rc.Enqueue("gotham.yard", noParams{}); err != nil {
				return "", err
			}
			return park(rc, p)
		}, res("watch")),
		taskDef("gotham.yard", park, res("yard")),
		taskDef("gotham.annex", park, res("annex")),
	}
	h := newHarness(t, defs, func(o *Options) {
		cascadeOpts(t, 10*time.Millisecond)(o)
		o.Catalog = catalogWith(t, scopeGothamRun)
		withPrincipal(o)
		spy.install(o)
	})
	gordon := asUser("gordon", scopeGothamRun)
	ref, err := h.x.Start(gordon, "gotham.watch", tasks.StartRequest{Mode: tasks.ModeRequestLive})
	if err != nil {
		t.Fatal(err)
	}
	yard := h.childOf(ref.ID)
	h.waitState(yard, tasks.StateRunning)
	var annex tasks.RunID
	spy.arm(func() {
		// The scan that found the yard is over, and the yard has not been stopped:
		// it enqueues its own child now, and that child is running before the yard ends.
		a, err := h.x.Start(gordon, "gotham.annex", tasks.StartRequest{Mode: tasks.ModeRequestLive, Parent: yard, Trigger: tasks.TriggerParent})
		if err != nil {
			t.Error(err)
			return
		}
		annex = a.ID
		h.waitState(annex, tasks.StateRunning)
	})
	if err := h.x.Cancel(gordon, ref.ID); err != nil {
		t.Fatal(err)
	}
	if annex == "" {
		t.Fatal("the hook never ran: the cascade did not scan the unfinished runs")
	}
	h.waitState(yard, tasks.StateCanceled)
	if got := h.waitState(annex, tasks.StateCanceled); got.Reason != reasonCancel {
		t.Errorf("the yard's late child is %s/%q, want canceled/cancel", got.State, got.Reason)
	}
}

// TestCancelCascade_ABatchOfChildrenIsStoppedByABoundedPool (S5): the children of
// one level are stopped by several workers at once, never more than cascadeWorkers.
// Each stop is delayed inside its transition so that overlapping workers show.
func TestCancelCascade_ABatchOfChildrenIsStoppedByABoundedPool(t *testing.T) {
	const kids = 40
	spy := &cascadeSpy{}
	park := func(rc *tasks.RC, _ noParams) (string, error) {
		<-rc.Context().Done()
		return "", context.Cause(rc.Context())
	}
	armory := func(c *tasks.Common) {
		c.Effects = tasks.Writes(tasks.Res("gotham", "armory"))
		c.Cancel = tasks.Cooperative(time.Second)
	}
	defs := []tasks.Definition{
		taskDef("gotham.watch", func(rc *tasks.RC, p noParams) (string, error) {
			for i := range kids { // distinct params: identical enqueues fold into one run
				if _, err := rc.Enqueue("gotham.armory", noParams{Case: fmt.Sprint(i)}); err != nil {
					return "", err
				}
			}
			return park(rc, p)
		}, func(c *tasks.Common) {
			c.Effects = tasks.Writes(tasks.Res("gotham", "watch"))
			c.Cancel = tasks.Cooperative(time.Second)
		}),
		taskDef("gotham.armory", park, armory),
		taskDef("gotham.lockdown", park, armory), // holds the armory's resource: the kids stay queued
	}
	h := newHarness(t, defs, func(o *Options) {
		cascadeOpts(t, 10*time.Millisecond)(o)
		o.Catalog = catalogWith(t, scopeGothamRun)
		withPrincipal(o)
		spy.install(o)
	})
	gordon := asUser("gordon", scopeGothamRun)
	lock, err := h.x.Start(gordon, "gotham.lockdown", tasks.StartRequest{Mode: tasks.ModeRequestLive})
	if err != nil {
		t.Fatal(err)
	}
	h.waitState(lock.ID, tasks.StateRunning)
	ref, err := h.x.Start(gordon, "gotham.watch", tasks.StartRequest{Mode: tasks.ModeRequestLive})
	if err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(10 * time.Second); len(h.children(ref.ID)) < kids; {
		if time.Now().After(deadline) {
			t.Fatalf("the watch enqueued %d children, want %d", len(h.children(ref.ID)), kids)
		}
		time.Sleep(5 * time.Millisecond)
	}
	var inflight, peak atomic.Int32
	spy.moves(func(_ tasks.RunID, next tasks.RunRecord) error {
		if next.Parent == "" || next.State != tasks.StateCanceled || next.Reason != reasonCancel {
			return nil
		}
		n := inflight.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		inflight.Add(-1)
		return nil
	})
	if err := h.x.Cancel(gordon, ref.ID); err != nil {
		t.Fatal(err)
	}
	for _, id := range h.children(ref.ID) {
		if got := h.get(id); got.State != tasks.StateCanceled || got.Reason != reasonCancel {
			t.Errorf("child %s is %s/%q, want canceled/cancel", id, got.State, got.Reason)
		}
	}
	if p := peak.Load(); p < 2 || p > cascadeWorkers {
		t.Errorf("at most %d children were being stopped at once, want between 2 and %d: a sequential walk, or an unbounded one", p, cascadeWorkers)
	}
}

// TestCancelCascade_AnErrorAfterTheStopCommittedIsStillAnAction (S2, executor half):
// a child that cannot be stopped fails the cancel, but the run's own stop had
// committed and the others had ended, so the cancel's report says it acted: the HTTP
// route audits it. A repeat finishes the cascade.
func TestCancelCascade_AnErrorAfterTheStopCommittedIsStillAnAction(t *testing.T) {
	spy := &cascadeSpy{}
	r := newReachRig(t, spy.install)
	spy.moves(func(id tasks.RunID, next tasks.RunRecord) error {
		if id == r.vault && next.Reason == reasonCancel {
			return errors.New("the Batcave store is down")
		}
		return nil
	})
	ctx, rep := WithCancelReport(asUser("gordon", scopeGothamRun))
	err := r.h.x.Cancel(ctx, r.parent)
	if err == nil || !strings.Contains(err.Error(), string(r.vault)) {
		t.Fatalf("cancel with a child that cannot be stopped: %v, want an error naming the vault", err)
	}
	if !rep.Acted() {
		t.Errorf("the report says nothing was done, but the parent was stopped and the yard ended")
	}
	r.h.waitState(r.parent, tasks.StateCanceled)
	r.h.waitState(r.yard, tasks.StateCanceled)
	r.h.stays(r.vault, tasks.StateRunning, 100*time.Millisecond)
	spy.moves(nil)
	if err := r.h.x.Cancel(asUser("gordon", scopeGothamRun), r.parent); !errors.Is(err, tasks.ErrRunNotFound) {
		t.Fatalf("repeat cancel: %v, want ErrRunNotFound (HEAD's rule)", err)
	}
	r.h.waitState(r.vault, tasks.StateCanceled)

	// A repeat that finds nothing to do did nothing, and says so.
	idle, repIdle := WithCancelReport(asUser("gordon", scopeGothamRun))
	if err := r.h.x.Cancel(idle, r.parent); !errors.Is(err, tasks.ErrRunNotFound) {
		t.Fatalf("second repeat: %v, want ErrRunNotFound", err)
	}
	if repIdle.Acted() || len(repIdle.Skipped()) != 0 {
		t.Errorf("a repeat that ended nothing reports acted=%v skipped=%v", repIdle.Acted(), repIdle.Skipped())
	}
}

// TestCancelCascade_OnlyACancelStopCascades (N1): a cancel call that finds the run
// already stopping for the watchdog or its timeout is answered nil, as before, and
// ends nothing beneath it: the verdict is the cancel's, and no cancel stored one.
func TestCancelCascade_OnlyACancelStopCascades(t *testing.T) {
	for _, reason := range []string{reasonWatchdog, reasonTimeout} {
		t.Run(reason, func(t *testing.T) {
			r := newReachRigHeld(t)
			if err := r.h.e.stopOne(context.Background(), r.parent, tasks.Actor{System: "core"}, nil, reason, tasks.ErrCanceled); err != nil {
				t.Fatal(err)
			}
			if got := r.h.get(r.parent); got.State != tasks.StateStopping || got.Reason != reason {
				t.Fatalf("parent %s/%q, want stopping/%s", got.State, got.Reason, reason)
			}
			if err := r.h.x.Cancel(asUser("gordon", scopeGothamRun), r.parent); err != nil {
				t.Fatalf("cancel of a run stopping for the %s: %v, want nil", reason, err)
			}
			for name, id := range map[string]tasks.RunID{"yard": r.yard, "vault": r.vault, "cell": r.cell} {
				r.h.stays(id, tasks.StateRunning, 150*time.Millisecond)
				if evs := r.h.cancelEventActors(id); len(evs) != 0 {
					t.Errorf("the %s carries cancel events %v, want none", name, evs)
				}
			}
			if got := r.h.get(r.parent); got.CancelReach != nil || got.Reason != reason {
				t.Errorf("the parent carries verdict %+v, reason %q: the %s stop must stay as it was", got.CancelReach, got.Reason, reason)
			}
		})
	}
}

// TestCancelCascade_TheGuardLeavesAChildOfAParentWithNoVerdictAlone (N3): a run
// whose parent was canceled with no stored verdict (a record from before the field,
// or a cancel that stored none) is dispatched as on HEAD: the guard never falls
// back to "allowed". The guard is also called directly, on a record that is queued.
func TestCancelCascade_TheGuardLeavesAChildOfAParentWithNoVerdictAlone(t *testing.T) {
	r := newVerdictRig(t)
	harley := tasks.Actor{UserID: "harley", AuthMethod: "session"}
	if err := r.h.e.stopOne(context.Background(), r.watch, harley, nil, reasonCancel, tasks.ErrCanceled); err != nil {
		t.Fatal(err)
	}
	r.h.waitState(r.watch, tasks.StateCanceled)
	if r.h.get(r.watch).CancelReach != nil {
		t.Fatal("the parent carries a verdict; the cell needs none")
	}
	probe := tasks.RunRecord{}
	probe.ID, probe.Kind, probe.Parent, probe.Actor = "gotham-probe", "gotham.safe", r.watch, r.h.get(r.watch).Actor
	if r.h.e.cancelOrphan(context.Background(), probe) {
		t.Errorf("cancelOrphan took a child of a parent with no verdict")
	}
	// (a safe, not a yard: the canceled watch's yard is still running and holds the yard's resource)
	late := r.late(r.h, r.watch, "gotham.safe", "gordon")
	r.h.waitState(late, tasks.StateRunning)
	r.h.stays(late, tasks.StateRunning, 200*time.Millisecond)
}

// TestCancelCascade_ARunWhoseRecordWillNotDecodeIsLeftUntouched (N3): an unreadable
// record is hidden from every sweep (5T.5): the cancel neither fails on it nor
// rewrites it, and a restart leaves its bytes as they were.
func TestCancelCascade_ARunWhoseRecordWillNotDecodeIsLeftUntouched(t *testing.T) {
	ctx := context.Background()
	r := newVerdictRig(t)
	st := taskstore.New(r.h.ks)
	key := taskstore.RunKey(string(r.yard))
	was, found, err := st.Get(ctx, key)
	if err != nil || !found {
		t.Fatalf("read the yard's record: found %v, %v", found, err)
	}
	cipher := []byte("the Riddler's cipher")
	swap := func(from, to []byte) error {
		return r.h.e.write(ctx, func(ctx context.Context, b *storage.Batch) error {
			if err := st.ExpectValue(b, key, from); err != nil {
				return err
			}
			return st.Set(b, key, to)
		})
	}
	if err := swap(was, cipher); err != nil {
		t.Fatal(err)
	}
	restore := func() {
		if err := swap(cipher, was); err != nil {
			t.Error(err)
		}
	}
	defer restore()
	if err := r.h.x.Cancel(asUser("gordon", scopeGothamRun), r.watch); err != nil {
		t.Fatalf("cancel with an unreadable child: %v, want nil", err)
	}
	r.h.waitState(r.watch, tasks.StateCanceled)
	r.h.waitState(r.armory, tasks.StateCanceled) // a readable sibling still goes with its parent
	if got, _, err := st.Get(ctx, key); err != nil || !bytes.Equal(got, cipher) {
		t.Errorf("the unreadable record is %q (%v) after the cancel, want its bytes untouched", got, err)
	}
	if err := r.h.x.Shutdown(ctx); err != nil {
		t.Logf("shutdown with an unreadable record: %v", err)
	}
	second := r.h.reboot(r.defs, r.opts)
	if got, _, err := st.Get(ctx, key); err != nil || !bytes.Equal(got, cipher) {
		t.Errorf("the unreadable record is %q (%v) after a restart, want its bytes untouched", got, err)
	}
	_ = second
}
