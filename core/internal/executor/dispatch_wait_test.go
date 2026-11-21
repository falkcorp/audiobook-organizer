// file: core/internal/executor/dispatch_wait_test.go
// version: 0.1.0
// guid: 8e2d6a41-5c7b-4f93-a0e8-3b1c9d7f5a62
// last-edited: 2026-11-21

package executor

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cockroachdb/pebble/v2/vfs"
	dto "github.com/prometheus/client_model/go"

	"github.com/falkcorp/mediacore/metrics"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/tasks"
)

// F5-18i: a run held by a dispatch gate is visible. The run view (GET /runs,
// the Activity page) names the gate's reason, the running run behind a key or
// write-set deferral and when the deferral began; the wait gauge carries the
// longest wait per kind, reason and trigger for the alert. Neither changes
// which runs are deferred or the dispatch_deferred counter (once per run and
// reason).

// fakeClock is Options.Now for tests that read the gauge: the dispatcher keeps
// its real tick, and the test moves the clock the waits are measured on.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

// newFakeClock starts at the real time, so the deadlines the executor derives
// from its clock (a run's timeout) are not already past on the real one.
func newFakeClock() *fakeClock { return &fakeClock{t: time.Now()} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// writesOf declares the resources a fixture kind writes.
func writesOf(names ...string) func(*tasks.Common) {
	return func(c *tasks.Common) {
		refs := make([]tasks.Resource, len(names))
		for i, n := range names {
			refs[i] = tasks.Res("gotham", n)
		}
		c.Effects = tasks.Writes(refs...)
	}
}

// metricHarness is newHarness with the instruments on, in a core ops scope of
// its own provider, and clk as the executor's clock.
func metricHarness(t *testing.T, defs []tasks.Definition, clk *fakeClock) (*harness, *metrics.Provider) {
	t.Helper()
	p, err := metrics.New(context.Background(), metrics.Options{AppID: "gotham", MetricNamespace: "gotham", Prometheus: true})
	if err != nil {
		t.Fatalf("metrics.New: %v", err)
	}
	t.Cleanup(func() { _ = p.Shutdown(context.Background()) })
	sc, err := p.Core("ops", Attributes()...)
	if err != nil {
		t.Fatalf("core ops scope: %v", err)
	}
	h := newHarness(t, defs, func(o *Options) {
		o.Metrics, o.InstrumentsOff, o.Now = sc, false, clk.Now
	})
	return h, p
}

// waitSample is one sample of the wait gauge.
type waitSample struct {
	kind, reason, trigger string
	seconds               float64
}

// family gathers p and returns the family named name, or nil.
func family(t *testing.T, p *metrics.Provider, name string) *dto.MetricFamily {
	t.Helper()
	mfs, err := p.Gatherer().Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	for _, mf := range mfs {
		if mf.GetName() == name {
			return mf
		}
	}
	return nil
}

func label(m *dto.Metric, name string) string {
	for _, l := range m.GetLabel() {
		if l.GetName() == name {
			return l.GetValue()
		}
	}
	return ""
}

func waitSamples(t *testing.T, p *metrics.Provider) []waitSample {
	t.Helper()
	var out []waitSample
	if mf := family(t, p, "gotham_ops_dispatch_wait_seconds"); mf != nil {
		for _, m := range mf.GetMetric() {
			out = append(out, waitSample{label(m, "def_id"), label(m, "reason"), label(m, "trigger"), m.GetGauge().GetValue()})
		}
	}
	return out
}

// deferredCount is the dispatch_deferred counter for reason.
func deferredCount(t *testing.T, p *metrics.Provider, reason string) float64 {
	t.Helper()
	if mf := family(t, p, "gotham_ops_dispatch_deferred_total"); mf != nil {
		for _, m := range mf.GetMetric() {
			if label(m, "reason") == reason {
				return m.GetCounter().GetValue()
			}
		}
	}
	return 0
}

// eventually polls cond for up to 10s of real time.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// viewOf is the run's read model as GET /runs serves it.
func (h *harness) viewOf(id tasks.RunID) tasks.RunView {
	h.t.Helper()
	v, err := h.x.Get(context.Background(), id)
	if err != nil {
		h.t.Fatalf("get view %s: %v", id, err)
	}
	return v
}

// TestRunView_NamesWhatBlocksAQueuedRun: a run held by the write-set gate
// reads back with the reason, the holder and when the hold began, the start of
// the hold does not move while the run waits, and the fields vanish when the
// run starts. The stored row never carries them.
func TestRunView_NamesWhatBlocksAQueuedRun(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		started := make(chan tasks.RunID, 1)
		h := newHarness(t, []tasks.Definition{
			taskDef("gotham.evidence", blockUntilCtx(started)),
			taskDef("gotham.forgery", okBody),
		}, synctestConfig)
		holder := h.start("gotham.evidence", tasks.StartRequest{})
		<-started
		waiter := h.start("gotham.forgery", tasks.StartRequest{})
		h.stays(waiter, tasks.StateQueued, 5*time.Second)

		v := h.viewOf(waiter)
		if v.Deferred != deferWriteSet || v.BlockedBy != holder {
			t.Fatalf("view = deferred %q blocked by %q, want %q behind %q", v.Deferred, v.BlockedBy, deferWriteSet, holder)
		}
		if v.DeferredSince.IsZero() || time.Since(v.DeferredSince) < 4*time.Second {
			t.Errorf("deferred since %v, want the first pass 5s ago", v.DeferredSince)
		}
		since := v.DeferredSince
		h.stays(waiter, tasks.StateQueued, 5*time.Second)
		if v := h.viewOf(waiter); v.DeferredSince != since {
			t.Errorf("deferred since moved from %v to %v while the run kept waiting", since, v.DeferredSince)
		}
		if r := h.get(waiter); r.Deferred != "" || r.BlockedBy != "" || !r.DeferredSince.IsZero() {
			t.Errorf("stored row carries deferral fields: %+v", r.RunView)
		}
		// The holder is running, not queued: nothing to say about it.
		if v := h.viewOf(holder); v.Deferred != "" || v.BlockedBy != "" || !v.DeferredSince.IsZero() {
			t.Errorf("a running run reads back deferred: %+v", v)
		}

		if err := h.x.Cancel(context.Background(), holder); err != nil {
			t.Fatal(err)
		}
		h.waitState(waiter, tasks.StateSucceeded)
		if v := h.viewOf(waiter); v.Deferred != "" || v.BlockedBy != "" || !v.DeferredSince.IsZero() {
			t.Errorf("a finished run reads back deferred: %+v", v)
		}
	})
}

// TestRunView_KeyDeferralNamesTheKeyHolder: a run held behind its
// concurrency key's holder says "key" and names the holder, and a run no gate
// holds says nothing.
func TestRunView_KeyDeferralNamesTheKeyHolder(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		started := make(chan tasks.RunID, 1)
		key := func(c *tasks.Common) { c.ConcurrencyKey = "gotham.signal" }
		h := newHarness(t, []tasks.Definition{
			taskDef("gotham.watch", blockUntilCtx(started), key, writesOf("lab")),
			taskDef("gotham.beacon", okBody, key, writesOf("vault")),
			taskDef("gotham.lantern", okBody, writesOf("roof")),
		}, synctestConfig)
		holder := h.start("gotham.watch", tasks.StartRequest{})
		<-started
		beacon := h.start("gotham.beacon", tasks.StartRequest{})
		free := h.start("gotham.lantern", tasks.StartRequest{})
		h.waitState(free, tasks.StateSucceeded)
		h.stays(beacon, tasks.StateQueued, 3*time.Second)
		if v := h.viewOf(beacon); v.Deferred != deferKey || v.BlockedBy != holder {
			t.Errorf("beacon view = deferred %q blocked by %q, want %q behind %q", v.Deferred, v.BlockedBy, deferKey, holder)
		}
		if v := h.viewOf(free); v.Deferred != "" || v.BlockedBy != "" {
			t.Errorf("a run no gate held reads back deferred: %+v", v)
		}
	})
}

// TestRunView_PausedDeferralHasNoBlocker: a reason that is not a run's doing
// (paused) carries no blocker, and the wait still starts.
func TestRunView_PausedDeferralHasNoBlocker(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, []tasks.Definition{taskDef("gotham.forgery", okBody)}, synctestConfig)
		h.e.setPaused(true)
		id := h.start("gotham.forgery", tasks.StartRequest{})
		h.stays(id, tasks.StateQueued, 3*time.Second)
		v := h.viewOf(id)
		if v.Deferred != deferPaused || v.BlockedBy != "" || v.DeferredSince.IsZero() {
			t.Errorf("view = %+v, want paused, no blocker, a start time", v)
		}
	})
}

// TestDispatchWait_BlockerChangeKeepsTheWaitAndTheCount: when one of two
// running holders ends and the other still blocks the run, the view names the
// remaining holder, the wait keeps its start, and dispatch_deferred does not
// count the run's writeset deferral again.
func TestDispatchWait_BlockerChangeKeepsTheWaitAndTheCount(t *testing.T) {
	clk := newFakeClock()
	startedA, startedB := make(chan tasks.RunID, 1), make(chan tasks.RunID, 1)
	h, p := metricHarness(t, []tasks.Definition{
		taskDef("gotham.evidence", blockUntilCtx(startedA), writesOf("lab")),
		taskDef("gotham.ballistics", blockUntilCtx(startedB), writesOf("vault")),
		taskDef("gotham.forgery", okBody, writesOf("lab", "vault")),
	}, clk)
	a := h.start("gotham.evidence", tasks.StartRequest{})
	b := h.start("gotham.ballistics", tasks.StartRequest{})
	<-startedA
	<-startedB
	w := h.start("gotham.forgery", tasks.StartRequest{})
	eventually(t, "the forgery to be deferred", func() bool { return h.viewOf(w).Deferred == deferWriteSet })
	first := h.viewOf(w)
	if first.BlockedBy != a && first.BlockedBy != b {
		t.Fatalf("blocked by %q, want one of the two holders %q or %q", first.BlockedBy, a, b)
	}
	clk.Advance(time.Hour)
	other, ended := b, a
	if first.BlockedBy == b {
		other, ended = a, b
	}
	if err := h.x.Cancel(context.Background(), ended); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the blocker to change", func() bool { return h.viewOf(w).BlockedBy == other })
	v := h.viewOf(w)
	if v.Deferred != deferWriteSet || !v.DeferredSince.Equal(first.DeferredSince) {
		t.Errorf("after the blocker changed: deferred %q since %v, want %q since %v", v.Deferred, v.DeferredSince, deferWriteSet, first.DeferredSince)
	}
	time.Sleep(150 * time.Millisecond) // several dispatch passes of the 20ms test tick
	if n := deferredCount(t, p, deferWriteSet); n != 1 {
		t.Errorf("dispatch_deferred{reason=writeset} = %v, want 1: a blocker change is not a new deferral", n)
	}
	if got := waitSamples(t, p); len(got) != 1 || got[0].reason != deferWriteSet || got[0].seconds < time.Hour.Seconds() {
		t.Errorf("wait gauge = %+v, want one writeset sample of at least an hour", got)
	}
}

// TestDispatchWait_GaugeSplitsByTriggerAndForgetsRunsThatLeft: the gauge reports
// the longest wait per kind, reason and trigger (a scheduled run is told apart
// from a manual one), grows with the clock, and drops a run's sample once the
// run is canceled while still queued: a stale entry would page for a run that
// is gone.
func TestDispatchWait_GaugeSplitsByTriggerAndForgetsRunsThatLeft(t *testing.T) {
	clk := newFakeClock()
	started := make(chan tasks.RunID, 1)
	h, p := metricHarness(t, []tasks.Definition{
		taskDef("gotham.evidence", blockUntilCtx(started)),
		taskDef("gotham.forgery", okBody),
		taskDef("gotham.audit", okBody),
	}, clk)
	h.start("gotham.evidence", tasks.StartRequest{})
	<-started
	manual := h.start("gotham.forgery", tasks.StartRequest{Trigger: tasks.TriggerManual})
	sched := h.start("gotham.audit", tasks.StartRequest{Trigger: tasks.TriggerSchedule})
	eventually(t, "both runs deferred", func() bool {
		return h.viewOf(manual).Deferred != "" && h.viewOf(sched).Deferred != ""
	})
	clk.Advance(3 * time.Hour)
	find := func(trigger string) (waitSample, bool) {
		for _, s := range waitSamples(t, p) {
			if s.trigger == trigger {
				return s, true
			}
		}
		return waitSample{}, false
	}
	s, ok := find("schedule")
	if !ok || s.kind != "gotham.audit" || s.reason != deferWriteSet || s.seconds < 3*3600 || s.seconds > 3*3600+60 {
		t.Errorf("scheduled sample = %+v (found %v), want gotham.audit, writeset, about 3h", s, ok)
	}
	m, ok := find("manual")
	if !ok || m.kind != "gotham.forgery" || m.reason != deferWriteSet {
		t.Errorf("manual sample = %+v (found %v), want gotham.forgery, writeset", m, ok)
	}
	if err := h.x.Cancel(context.Background(), sched); err != nil {
		t.Fatal(err)
	}
	h.waitState(sched, tasks.StateCanceled)
	eventually(t, "the canceled run's sample to go", func() bool { _, still := find("schedule"); return !still })
	if _, still := h.e.waitsNow()[sched]; still {
		t.Errorf("the canceled run is still in the pass record")
	}
	if _, ok := find("manual"); !ok {
		t.Errorf("the manual run's sample went with the canceled one")
	}
}

// TestDispatchWait_ConcurrentDeferralsReadersAndCancels (run with -race): the
// dispatcher defers a crowd of runs while Start adds more, Cancel removes some,
// and readers hit the run view, the run list and the metrics gather. The pass
// record is replaced whole under e.mu and read without it, so the race detector
// decides; the assertions pin the end state: every waiter that survived read
// back deferred, and none of the others left an entry in the pass record.
func TestDispatchWait_ConcurrentDeferralsReadersAndCancels(t *testing.T) {
	clk := newFakeClock()
	started := make(chan tasks.RunID, 1)
	h, p := metricHarness(t, []tasks.Definition{
		taskDef("gotham.evidence", blockUntilCtx(started)),
		taskDef("gotham.forgery", okBody),
	}, clk)
	holder := h.start("gotham.evidence", tasks.StartRequest{})
	<-started

	const starters, perStarter = 6, 8
	var mu sync.Mutex
	var ids []tasks.RunID
	var wg sync.WaitGroup
	for s := range starters {
		wg.Go(func() {
			for n := range perStarter {
				// A distinct case each, or a start folds into the queued run of
				// the same kind and params (dedupe) and returns its ID.
				id := h.start("gotham.forgery", tasks.StartRequest{Params: mustParams(t, fmt.Sprintf("case-%d-%d", s, n))})
				mu.Lock()
				ids = append(ids, id)
				mu.Unlock()
			}
		})
	}
	stop := make(chan struct{})
	var readers sync.WaitGroup
	var reads atomic.Int64
	read := func(f func()) {
		readers.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
					f()
					reads.Add(1)
				}
			}
		})
	}
	read(func() { family(t, p, "gotham_ops_dispatch_wait_seconds") })
	read(func() {
		for v, err := range h.x.Runs(context.Background(), tasks.RunQuery{}) {
			if err == nil && v.Deferred != "" && v.DeferredSince.IsZero() {
				t.Errorf("run %s reads deferred %q with no start time", v.ID, v.Deferred)
			}
		}
	})
	read(func() {
		mu.Lock()
		snap := append([]tasks.RunID(nil), ids...)
		mu.Unlock()
		for _, id := range snap {
			if v, err := h.x.Get(context.Background(), id); err == nil && v.Deferred != "" && v.BlockedBy != holder {
				t.Errorf("run %s deferred %q behind %q, want the only holder %q", id, v.Deferred, v.BlockedBy, holder)
			}
		}
	})
	wg.Wait()

	// Cancel every third waiter while the readers run, once every run has been
	// counted (each is deferred once, by the write-set gate).
	canceled := map[tasks.RunID]bool{}
	mu.Lock()
	all := append([]tasks.RunID(nil), ids...)
	mu.Unlock()
	eventually(t, "every run to be counted once", func() bool {
		return deferredCount(t, p, deferWriteSet) == float64(len(all))
	})
	for i, id := range all {
		if i%3 == 0 {
			if err := h.x.Cancel(context.Background(), id); err != nil {
				t.Fatalf("cancel %s: %v", id, err)
			}
			canceled[id] = true
		}
	}
	survivors := len(all) - len(canceled)
	eventually(t, "every surviving waiter to read deferred", func() bool {
		for _, id := range all {
			if !canceled[id] && h.viewOf(id).Deferred != deferWriteSet {
				return false
			}
		}
		return true
	})
	close(stop)
	readers.Wait()
	if reads.Load() == 0 {
		t.Fatal("the readers never ran")
	}
	eventually(t, "the canceled waiters to leave the pass record", func() bool {
		w := h.e.waitsNow()
		for id := range canceled {
			if _, still := w[id]; still {
				return false
			}
		}
		return len(w) == survivors
	})
	// Exactly one count per run: a Cancel while a pass holds the run as a
	// candidate must not make that pass count it again (deferRun refuses a run
	// that went terminal since the pass read the queue).
	if n := deferredCount(t, p, deferWriteSet); n != float64(len(all)) {
		t.Errorf("dispatch_deferred{reason=writeset} = %v, want %d: one per run, canceled or not", n, len(all))
	}
	if err := h.x.Cancel(context.Background(), holder); err != nil {
		t.Fatal(err)
	}
	for _, id := range all {
		if !canceled[id] {
			h.waitState(id, tasks.StateSucceeded)
		}
	}
	eventually(t, "an empty pass record once every run left the queue", func() bool { return len(h.e.waitsNow()) == 0 })
	if got := waitSamples(t, p); len(got) != 0 {
		t.Errorf("wait gauge after every run left the queue = %+v, want none", got)
	}
}

// TestRunView_BlockedByNamesOnlyAViewableBlocker: the ID of the run behind a
// deferral is shown only to a caller who may view the blocker's kind. The
// waiter's own deferral (reason and start) is the caller's to see, but the
// ID of a run of a kind the caller may not view, and through it that run's
// existence and timing, is not. Every read path with a caller applies it: Get,
// Runs and Timeline (which reads through Runs).
func TestRunView_BlockedByNamesOnlyAViewableBlocker(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		started := make(chan tasks.RunID, 1)
		// Arkham's evidence run needs core:system.manage to be viewed; the
		// forgery run uses the default (core:tasks.view).
		secret := func(c *tasks.Common) { c.Perms.View = rbac.Need("core:system.manage") }
		h := newHarness(t, []tasks.Definition{
			taskDef("gotham.evidence", blockUntilCtx(started), secret),
			taskDef("gotham.forgery", okBody),
		}, func(o *Options) {
			synctestConfig(o)
			withPrincipal(o)
		})
		holder := h.start("gotham.evidence", tasks.StartRequest{})
		<-started
		waiter := h.start("gotham.forgery", tasks.StartRequest{})
		h.stays(waiter, tasks.StateQueued, 5*time.Second)

		manager := asUser("lucius", scopeTasksView, "core:system.manage")
		viewer := asUser("barbara", scopeTasksView)

		get := func(ctx context.Context) tasks.RunView {
			t.Helper()
			v, err := h.x.Get(ctx, waiter)
			if err != nil {
				t.Fatalf("get: %v", err)
			}
			return v
		}
		listed := func(ctx context.Context) tasks.RunView {
			t.Helper()
			for v, err := range h.x.Runs(ctx, tasks.RunQuery{}) {
				if err != nil {
					t.Fatalf("runs: %v", err)
				}
				if v.ID == waiter {
					return v
				}
			}
			t.Fatalf("run %s not listed", waiter)
			return tasks.RunView{}
		}
		timeline := func(ctx context.Context) tasks.RunView {
			t.Helper()
			vs, err := h.x.Timeline(ctx, tasks.TimelineQuery{})
			if err != nil {
				t.Fatalf("timeline: %v", err)
			}
			for _, v := range vs {
				if v.ID == waiter {
					return v
				}
			}
			t.Fatalf("run %s not on the timeline", waiter)
			return tasks.RunView{}
		}
		paths := map[string]func(context.Context) tasks.RunView{"Get": get, "Runs": listed, "Timeline": timeline}
		for name, read := range paths {
			if v := read(manager); v.BlockedBy != holder || v.Deferred != deferWriteSet || v.DeferredSince.IsZero() {
				t.Errorf("%s as a caller who may view the blocker = deferred %q blocked by %q since %v, want %q behind %q", name, v.Deferred, v.BlockedBy, v.DeferredSince, deferWriteSet, holder)
			}
			if v := read(viewer); v.BlockedBy != "" || v.Deferred != deferWriteSet || v.DeferredSince.IsZero() {
				t.Errorf("%s as a caller who may not view the blocker = deferred %q blocked by %q since %v, want %q behind no one, with its start", name, v.Deferred, v.BlockedBy, v.DeferredSince, deferWriteSet)
			}
		}
		// The blocker itself is invisible to the viewer, so nothing about it
		// can have reached them another way.
		if _, err := h.x.Get(viewer, holder); err == nil {
			t.Errorf("the viewer can get the blocker %s", holder)
		}
		// The view with no caller on it (OnTerminal and any event projection are
		// built this way) never names the blocker.
		rec, err := h.e.runs.Get(context.Background(), waiter)
		if err != nil {
			t.Fatal(err)
		}
		if v := h.e.view(rec); v.BlockedBy != "" || v.Deferred != deferWriteSet {
			t.Errorf("view without a caller = deferred %q blocked by %q, want %q behind no one", v.Deferred, v.BlockedBy, deferWriteSet)
		}
	})
}

// TestRunView_BlockedByNamesOnlyAViewableKeyHolder: the same authorization on
// the key gate, whose blocker is found by a different branch of the gate than
// the write-set one: the key holder's ID is shown to a caller who may view the
// holder's kind and withheld from one who may not, with the deferral reason
// kept for both.
func TestRunView_BlockedByNamesOnlyAViewableKeyHolder(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		started := make(chan tasks.RunID, 1)
		key := func(c *tasks.Common) { c.ConcurrencyKey = "gotham.signal" }
		secret := func(c *tasks.Common) { c.Perms.View = rbac.Need("core:system.manage") }
		h := newHarness(t, []tasks.Definition{
			taskDef("gotham.watch", blockUntilCtx(started), key, writesOf("lab"), secret),
			taskDef("gotham.beacon", okBody, key, writesOf("vault")),
		}, func(o *Options) {
			synctestConfig(o)
			withPrincipal(o)
		})
		holder := h.start("gotham.watch", tasks.StartRequest{})
		<-started
		waiter := h.start("gotham.beacon", tasks.StartRequest{})
		h.stays(waiter, tasks.StateQueued, 3*time.Second)
		seen := func(ctx context.Context) tasks.RunView {
			t.Helper()
			v, err := h.x.Get(ctx, waiter)
			if err != nil {
				t.Fatalf("get: %v", err)
			}
			return v
		}
		if v := seen(asUser("lucius", scopeTasksView, "core:system.manage")); v.Deferred != deferKey || v.BlockedBy != holder {
			t.Errorf("caller who may view the holder: deferred %q blocked by %q, want %q behind %q", v.Deferred, v.BlockedBy, deferKey, holder)
		}
		if v := seen(asUser("barbara", scopeTasksView)); v.Deferred != deferKey || v.BlockedBy != "" {
			t.Errorf("caller who may not view the holder: deferred %q blocked by %q, want %q behind no one", v.Deferred, v.BlockedBy, deferKey)
		}
	})
}

// TestRunView_OnlyAQueuedRunReadsDeferred: the pass record is replaced each
// pass, so a run that left the queue since the last pass can still have an
// entry for a moment; the view must not show it for a run that is no longer
// queued.
func TestRunView_OnlyAQueuedRunReadsDeferred(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		started := make(chan tasks.RunID, 1)
		h := newHarness(t, []tasks.Definition{
			taskDef("gotham.evidence", blockUntilCtx(started)),
			taskDef("gotham.forgery", okBody),
		}, synctestConfig)
		h.start("gotham.evidence", tasks.StartRequest{})
		<-started
		waiter := h.start("gotham.forgery", tasks.StartRequest{})
		h.stays(waiter, tasks.StateQueued, 3*time.Second)
		if _, ok := h.e.waitsNow()[waiter]; !ok {
			t.Fatal("fixture: the waiter has no entry in the pass record")
		}
		rec := h.get(waiter)
		for _, st := range []tasks.RunState{tasks.StateRunning, tasks.StateCanceled, tasks.StateSucceeded} {
			r := rec
			r.State = st
			if v := h.e.view(r); v.Deferred != "" || v.BlockedBy != "" || !v.DeferredSince.IsZero() {
				t.Errorf("a %s run with a stale pass entry reads back deferred: %+v", st, v)
			}
		}
	})
}

// TestDispatchWait_GaugeIsTheLongestWaitOfTheKind: many queued runs of one kind
// held by one gate for one trigger are one sample, the oldest wait, not
// whichever run the pass record happens to list last.
func TestDispatchWait_GaugeIsTheLongestWaitOfTheKind(t *testing.T) {
	clk := newFakeClock()
	started := make(chan tasks.RunID, 1)
	h, p := metricHarness(t, []tasks.Definition{
		taskDef("gotham.evidence", blockUntilCtx(started)),
		taskDef("gotham.forgery", okBody),
	}, clk)
	h.start("gotham.evidence", tasks.StartRequest{})
	<-started
	const runs = 20
	for n := range runs {
		id := h.start("gotham.forgery", tasks.StartRequest{Params: mustParams(t, fmt.Sprintf("case-%d", n)), Trigger: tasks.TriggerManual})
		eventually(t, "the run to be deferred", func() bool { return h.viewOf(id).Deferred != "" })
		clk.Advance(10 * time.Minute)
	}
	clk.Advance(time.Minute)
	oldest := float64(runs*10+1) * 60
	eventually(t, "the kind's one sample", func() bool { return len(waitSamples(t, p)) == 1 })
	// Every gather, not one that happens to be right: the pass record is a map,
	// so an observe that kept whichever run it saw last would pass some gathers.
	for i := range 40 {
		ss := waitSamples(t, p)
		if len(ss) != 1 || ss[0].seconds < oldest || ss[0].seconds > oldest+60 {
			t.Fatalf("gather %d: samples %+v, want one of about %v s, the oldest wait", i, ss, oldest)
		}
	}
}

// TestDeferRun_ACanceledCandidateIsNotCountedAgain: a pass reads the queue, a
// Cancel finishes one of the runs it read, and the pass then reaches that run's
// gate. The terminal move deleted the run's dedupe entry, so recording the stale
// candidate would count a run that is gone a second time and leave an entry
// nothing deletes. The dispatcher is not running, so the interleaving is fixed:
// beginPass, read, Cancel, defer.
func TestDeferRun_ACanceledCandidateIsNotCountedAgain(t *testing.T) {
	p, err := metrics.New(context.Background(), metrics.Options{AppID: "gotham", MetricNamespace: "gotham", Prometheus: true})
	if err != nil {
		t.Fatalf("metrics.New: %v", err)
	}
	t.Cleanup(func() { _ = p.Shutdown(context.Background()) })
	sc, err := p.Core("ops", Attributes()...)
	if err != nil {
		t.Fatalf("core ops scope: %v", err)
	}
	fs := vfs.NewCrashableMem()
	stores, ks := bootStores(t, fs)
	h := onStores(t, fs, stores, ks, []tasks.Definition{taskDef("gotham.forgery", okBody)}, func(o *Options) {
		o.Metrics, o.InstrumentsOff = sc, false
	}, false)

	canceled := h.start("gotham.forgery", tasks.StartRequest{Params: mustParams(t, "case-a")})
	kept := h.start("gotham.forgery", tasks.StartRequest{Params: mustParams(t, "case-b")})
	h.e.beginPass()
	canceledRec, keptRec := h.get(canceled), h.get(kept)
	if canceledRec.State != tasks.StateQueued || keptRec.State != tasks.StateQueued {
		t.Fatalf("fixture: states %s and %s, want both queued", canceledRec.State, keptRec.State)
	}
	if err := h.x.Cancel(context.Background(), canceled); err != nil {
		t.Fatal(err)
	}
	h.waitState(canceled, tasks.StateCanceled)

	if h.e.deferRun(canceledRec, deferWriteSet) {
		t.Error("deferRun recorded a run that was canceled since the pass read it")
	}
	if !h.e.deferRun(keptRec, deferWriteSet) {
		t.Error("deferRun refused a run that is still queued")
	}
	if n := deferredCount(t, p, deferWriteSet); n != 1 {
		t.Errorf("dispatch_deferred{reason=writeset} = %v, want 1 (the run still queued)", n)
	}
	h.e.mu.Lock()
	_, entry := h.e.deferred[canceled]
	h.e.mu.Unlock()
	if entry {
		t.Error("the canceled run left a dedupe entry that nothing deletes")
	}

	// The next pass reads the queue after the cancel, so it never sees the run,
	// and the memory of it is dropped.
	h.e.beginPass()
	h.e.mu.Lock()
	_, remembered := h.e.left[canceled]
	h.e.mu.Unlock()
	if remembered {
		t.Error("the left set kept a run past the next pass")
	}
}

// TestDispatchOnce_OpensAPassAndForgetsTheLeft: the dispatcher opens a pass on
// every tick (the pass number advances), and a run that went terminal is
// forgotten by a later pass, so the left set stays bounded by what ended during
// one pass.
func TestDispatchOnce_OpensAPassAndForgetsTheLeft(t *testing.T) {
	h := newHarness(t, []tasks.Definition{taskDef("gotham.forgery", okBody)}, func(o *Options) {
		o.Config = Config{DispatchTick: 5 * time.Millisecond, WatchdogInterval: time.Hour}
	})
	id := h.start("gotham.forgery", tasks.StartRequest{})
	h.waitState(id, tasks.StateSucceeded)
	passes := func() uint64 { h.e.mu.Lock(); defer h.e.mu.Unlock(); return h.e.pass }
	before := passes()
	eventually(t, "two more passes", func() bool { return passes() >= before+2 })
	h.e.mu.Lock()
	_, remembered := h.e.left[id]
	h.e.mu.Unlock()
	if remembered {
		t.Error("a finished run is still in the left set two passes later")
	}
}
