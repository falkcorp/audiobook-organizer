// file: core/internal/scheduler/window_run_test.go
// version: 1.0.1
// guid: 3f5b8f0e-8a7d-4c1e-b6f2-9d0a4e7c2b13
// last-edited: 2026-11-11

package scheduler

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/tasks"
)

// fakeHost is a window run's RC as runWindow sees it. Await answers from
// outcomes by the awaited run's kind (succeeded when absent) and calls
// onAwait first, which a test uses to move the clock.
type fakeHost struct {
	ctx      context.Context
	id       tasks.RunID
	trigger  tasks.Trigger
	st       tasks.Stores
	outcomes map[tasks.Kind]tasks.Outcome
	awaitErr error
	onAwait  func(k tasks.Kind)
	awaited  []tasks.Kind
}

func (h *fakeHost) Context() context.Context { return h.ctx }
func (h *fakeHost) RunID() tasks.RunID       { return h.id }
func (h *fakeHost) Kind() tasks.Kind         { return tasks.WindowNightly }
func (h *fakeHost) Trigger() tasks.Trigger   { return h.trigger }
func (h *fakeHost) Log() *slog.Logger        { return quietLog() }

func (h *fakeHost) Await(ref tasks.RunRef) (tasks.Outcome, error) {
	r, err := h.st.Runs.Get(h.ctx, ref.ID)
	if err != nil {
		return tasks.Outcome{}, err
	}
	h.awaited = append(h.awaited, r.Kind)
	if h.onAwait != nil {
		h.onAwait(r.Kind)
	}
	if h.awaitErr != nil {
		return tasks.Outcome{}, h.awaitErr
	}
	if o, ok := h.outcomes[r.Kind]; ok {
		return o, nil
	}
	return tasks.Outcome{State: tasks.StateSucceeded}, nil
}

// pickyStarter refuses StartIn for one kind.
type pickyStarter struct {
	tasks.Stager
	refuse tasks.Kind
}

func (p pickyStarter) StartIn(ctx context.Context, b *storage.Batch, k tasks.Kind, req tasks.StartRequest) (tasks.RunRef, error) {
	if k == p.refuse {
		return tasks.RunRef{}, errors.New("penguin: refused")
	}
	return p.Stager.StartIn(ctx, b, k, req)
}

const (
	alpha   tasks.Kind = "gotham.alpha"
	bravo   tasks.Kind = "gotham.bravo"
	charlie tasks.Kind = "gotham.charlie"
	delta   tasks.Kind = "gotham.delta"
	echo    tasks.Kind = "gotham.echo"
)

// windowFixture: five gotham kinds in the nightly window (01-04 UTC); the
// window has fired once, and its queued run is the parent the members start
// under.
func windowFixture(t *testing.T, gate *bool) (*fixture, tasks.RunID) {
	t.Helper()
	key := func(k tasks.Kind) string {
		return "modules.gotham.night.schedules." + strings.TrimPrefix(string(k), "gotham.")
	}
	defs := []tasks.Definition{
		gotham(alpha, tasks.ScheduleFromConfig(key(alpha))),
		gotham(bravo, tasks.ScheduleFromConfig(key(bravo))),
		gotham(charlie, tasks.ScheduleFromConfig(key(charlie)).When(func() bool { return *gate })),
		gotham(delta, tasks.ScheduleFromConfig(key(delta))),
		gotham(echo, tasks.ScheduleFromConfig(key(echo))),
	}
	f := newFixture(t, defs, fixtureOpts{
		schedules: map[string]tasks.ScheduleConfig{
			key(alpha):   {Enabled: true, Window: "nightly", Order: 2},
			key(bravo):   {Enabled: true, Window: "nightly", Order: 1},
			key(charlie): {Enabled: true, Window: "nightly", Order: 1},
			key(delta):   {Enabled: true, Window: "nightly", Order: 3},
			key(echo):    {Enabled: false, Window: "nightly", Order: 0}, // disabled: not a member
		},
		windows: map[string]WindowConfig{"nightly": {Start: 1, End: 4, Enabled: true}},
	})
	f.boot(day(1, 12, 0))
	f.poll(day(1, 12, 0))
	f.poll(day(2, 1, 0))
	rs := runsOf(t, f.st, tasks.WindowNightly)
	if len(rs) != 1 || rs[0].Trigger != tasks.TriggerWindow {
		t.Fatalf("window runs %+v", rs)
	}
	f.rec.take()
	return f, rs[0].ID
}

// TestRunWindow_OrderSkipsAndFailures: a window run starts its members one
// after another in (Order, kind) order, each a child of the window run with
// trigger window; it skips a member whose gate is closed and one already
// active, and reports the members that failed to start or did not succeed.
func TestRunWindow_OrderSkipsAndFailures(t *testing.T) {
	gate := true
	f, parent := windowFixture(t, &gate)
	s := f.scheduler()
	if got := s.Members("nightly"); !slices.Equal(got, []tasks.Kind{bravo, charlie, alpha, delta}) {
		t.Fatalf("Members = %v, want bravo charlie alpha delta", got)
	}
	h := &fakeHost{ctx: context.Background(), id: parent, trigger: tasks.TriggerWindow, st: f.st}
	f.setNow(day(2, 1, 1))
	if err := s.runWindow(h); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(h.awaited, []tasks.Kind{bravo, charlie, alpha, delta}) {
		t.Fatalf("awaited %v", h.awaited)
	}
	for _, k := range h.awaited {
		rs := runsOf(t, f.st, k)
		if len(rs) != 1 || rs[0].Parent != parent || rs[0].Trigger != tasks.TriggerWindow {
			t.Fatalf("%s runs %+v, want one child of the window run", k, rs)
		}
	}
	f.rec.take()

	// Second opening: charlie gated off, alpha still active (folded), delta
	// fails, bravo cannot start.
	f.finish(bravo, tasks.StateSucceeded)
	f.finish(charlie, tasks.StateSucceeded)
	f.finish(delta, tasks.StateSucceeded)
	gate = false
	f.wrap = func(o *Options) { o.Starter = pickyStarter{Stager: o.Starter, refuse: bravo} }
	f.boot(day(3, 1, 0))
	s = f.scheduler()
	h = &fakeHost{ctx: context.Background(), id: parent, trigger: tasks.TriggerWindow, st: f.st,
		outcomes: map[tasks.Kind]tasks.Outcome{delta: {State: tasks.StateFailed, Err: "joker"}}}
	err := s.runWindow(h)
	if err == nil || !strings.Contains(err.Error(), "2 of 4 members did not succeed: gotham.bravo, gotham.delta") {
		t.Fatalf("runWindow = %v", err)
	}
	if !slices.Equal(h.awaited, []tasks.Kind{delta}) {
		t.Fatalf("awaited %v, want delta alone", h.awaited)
	}
	if got := f.rec.take(); len(got) != 3 || !strings.Contains(got[0], "failed gotham.bravo window") ||
		!strings.Contains(got[1], "fold gotham.alpha window") || !strings.Contains(got[2], "enqueue gotham.delta window") {
		t.Fatalf("observed %q", got)
	}
}

// TestRunWindow_ClosesMidRun: a scheduled window run stops starting members
// once the window has closed; a manual run (HEAD's ignore_window) runs them
// all.
func TestRunWindow_ClosesMidRun(t *testing.T) {
	gate := true
	f, parent := windowFixture(t, &gate)
	s := f.scheduler()
	h := &fakeHost{ctx: context.Background(), id: parent, trigger: tasks.TriggerWindow, st: f.st,
		onAwait: func(tasks.Kind) { f.setNow(day(2, 4, 0)) }}
	f.setNow(day(2, 3, 59))
	if err := s.runWindow(h); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(h.awaited, []tasks.Kind{bravo}) {
		t.Fatalf("scheduled run after the window closed: awaited %v, want bravo alone", h.awaited)
	}
	f.finish(bravo, tasks.StateSucceeded)
	h = &fakeHost{ctx: context.Background(), id: parent, trigger: tasks.TriggerManual, st: f.st}
	f.setNow(day(2, 12, 0))
	if err := s.runWindow(h); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(h.awaited, []tasks.Kind{bravo, charlie, alpha, delta}) {
		t.Fatalf("manual run: awaited %v", h.awaited)
	}

	// A disabled window counts as closed for a scheduled run.
	for _, k := range []tasks.Kind{alpha, bravo, charlie, delta} {
		f.finish(k, tasks.StateSucceeded)
	}
	f.cfg.setWindow("nightly", WindowConfig{Start: 1, End: 4})
	h = &fakeHost{ctx: context.Background(), id: parent, trigger: tasks.TriggerWindow, st: f.st}
	f.setNow(day(3, 2, 0))
	if err := s.runWindow(h); err != nil || len(h.awaited) != 0 {
		t.Fatalf("disabled window: %v, awaited %v", err, h.awaited)
	}
}

// TestRunWindow_Errors: an Await error ends the run with it; a canceled run
// stops before the next member; a non-window kind is refused.
func TestRunWindow_Errors(t *testing.T) {
	gate := true
	f, parent := windowFixture(t, &gate)
	s := f.scheduler()
	f.setNow(day(2, 1, 1))
	cave := errors.New("cave-in")
	h := &fakeHost{ctx: context.Background(), id: parent, trigger: tasks.TriggerWindow, st: f.st, awaitErr: cave}
	if err := s.runWindow(h); !errors.Is(err, cave) {
		t.Fatalf("Await error: %v", err)
	}
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(cave)
	h = &fakeHost{ctx: ctx, id: parent, trigger: tasks.TriggerWindow, st: f.st}
	if err := s.runWindow(h); !errors.Is(err, cave) {
		t.Fatalf("canceled: %v", err)
	}
	if err := s.runWindow(notWindow{h}); err == nil || !strings.Contains(err.Error(), "is not a window kind") {
		t.Fatalf("non-window kind: %v", err)
	}
}

type notWindow struct{ *fakeHost }

func (notWindow) Kind() tasks.Kind { return "gotham.alpha" }

// TestPollWindow_Rules: a window fires only when enabled with valid hours;
// it folds into its own active run.
func TestPollWindow_Rules(t *testing.T) {
	gate := true
	f, _ := windowFixture(t, &gate)
	f.cfg.setWindow("nightly", WindowConfig{Start: 1, End: 25, Enabled: true})
	f.poll(day(3, 1, 0))
	if f.rec.errCount(tasks.WindowNightly) != 1 {
		t.Fatal("invalid window hours were not counted")
	}
	f.cfg.setWindow("nightly", WindowConfig{Start: 1, End: 4, Enabled: true})
	f.poll(day(3, 1, 1)) // the first opening's run is still queued: fold
	got := f.rec.take()
	if len(got) != 2 || !strings.Contains(got[1], "fold core.window.nightly window") {
		t.Fatalf("observed %q", got)
	}
	if rs := runsOf(t, f.st, tasks.WindowNightly); len(rs) != 1 {
		t.Fatalf("%d window runs, want 1", len(rs))
	}
}

// TestPollWindow_DeletedEntryIsCounted: a window kind whose config entry is
// gone (core.tasks.windows.<name> removed) never runs, and every poll counts
// it as a schedule error instead of skipping it silently.
func TestPollWindow_DeletedEntryIsCounted(t *testing.T) {
	gate := true
	f, _ := windowFixture(t, &gate)
	f.cfg.dropWindow("nightly")
	f.poll(day(3, 1, 0))
	f.poll(day(3, 1, 1))
	if n := f.rec.errCount(tasks.WindowNightly); n != 2 {
		t.Fatalf("schedule_errors for a deleted window entry = %d, want 2 (one per poll)", n)
	}
	if got := f.rec.take(); len(got) != 0 {
		t.Fatalf("a deleted window fired: %q", got)
	}
}

// TestWindowDefinition: the window kind's definition is core plumbing.
func TestWindowDefinition(t *testing.T) {
	d := WindowDefinition("nightly", nil)
	c := tasks.CommonOf(d)
	if d.Kind() != tasks.WindowNightly || c.Priority != tasks.PrioritySystem || c.Resume != tasks.ResumeDrop ||
		c.Timeout != 12*time.Hour || !c.Effects.IsReadOnly() || c.Schedule != nil {
		t.Fatalf("definition %s %+v", d.Kind(), c)
	}
}
