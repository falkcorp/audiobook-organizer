// file: core/internal/scheduler/fixture_test.go
// version: 1.0.1
// guid: 6a0e3a4c-5d0b-4f61-9a43-2b8f3f1c7e21
// last-edited: 2026-11-11

package scheduler

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/internal/executor"
	"github.com/falkcorp/mediacore/internal/taskstore"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/tasks"
)

// This package's scenario tests build their own fixture: the
// schedule-parity harness (tasks/taskstest/schedule) imports this package,
// so an in-package test cannot use it. The fixture is the same shape: pebble
// over a MemFS (storage.Open, never coretest.NewStore), an executor that is
// built but never run (runs stay queued, no body executes), a fake clock the
// test sets, and Boot/Poll called directly. Nothing sleeps.

const storePath = "/wayne-manor/scheduler.pebble"

// openStores opens c/tasks/ over fs.
func openStores(t *testing.T, fs *vfs.MemFS) (tasks.Stores, func() *storage.Batch) {
	t.Helper()
	ctx := context.Background()
	o, err := storage.Open(ctx, storage.OpenOptions{
		Path:            storePath,
		Owners:          []storage.OwnerSpec{{Owner: taskstore.Owner, Spec: taskstore.StorageSpec(), Enabled: true}},
		Build:           "scheduler+test",
		FreeSpaceMargin: 1,
		FS:              storage.FSOf(fs),
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	db, err := o.Prepare(ctx)
	if err == nil {
		err = db.Migrate(ctx)
	}
	if err != nil {
		t.Fatalf("boot: %v", errors.Join(err, o.Close()))
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	})
	ks, ok := db.Keyspace(taskstore.Owner)
	if !ok {
		t.Fatalf("owner %s has no keyspace", taskstore.Owner)
	}
	st := tasks.NativeStores(ks)
	return st, st.Runs.(batchOpener).NewBatch
}

// fakeConfig serves Config from maps a test edits between polls.
type fakeConfig struct {
	mu        sync.Mutex
	schedules map[string]tasks.ScheduleConfig
	windows   map[string]WindowConfig
}

func (c *fakeConfig) Schedule(key string) (tasks.ScheduleConfig, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.schedules[key]
	if !ok {
		return tasks.ScheduleConfig{}, fmt.Errorf("key %s is not declared", key)
	}
	return v, nil
}

func (c *fakeConfig) Window(name string) (WindowConfig, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	w, ok := c.windows[name]
	return w, ok
}

func (c *fakeConfig) set(key string, v tasks.ScheduleConfig) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.schedules[key] = v
}

func (c *fakeConfig) dropWindow(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.windows, name)
}

func (c *fakeConfig) setWindow(name string, w WindowConfig) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.windows[name] = w
}

// recorder is the Observer and Metrics of a fixture.
type recorder struct {
	mu     sync.Mutex
	now    func() time.Time
	events []string
	lags   map[tasks.Kind][]time.Duration
	missed map[tasks.Kind]int64
	errs   map[tasks.Kind]int
}

func (r *recorder) add(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, r.now().UTC().Format("01-02T15:04")+" "+fmt.Sprintf(format, args...))
}

func (r *recorder) Advanced(k tasks.Kind, _ tasks.Clock, had bool, next tasks.Clock) {
	verb := "advance"
	if !had {
		verb = "seed"
	}
	closed := ""
	if !next.GateClosedAt.IsZero() {
		closed = " closed"
	}
	r.add("%s %s last_due=%s%s", verb, k, next.LastDue.UTC().Format("01-02T15:04"), closed)
}

func (r *recorder) Fired(f Fire) {
	verb := "enqueue"
	switch {
	case f.Err != nil:
		verb = "failed"
	case f.Gated:
		verb = "gated"
	case f.Folded:
		verb = "fold"
	}
	s := fmt.Sprintf("%s %s %s", verb, f.Kind, f.Trigger)
	if f.Missed > 0 {
		s += fmt.Sprintf(" missed=%d", f.Missed)
	}
	r.add("%s", s)
}

func (r *recorder) WindowOpened(name string, members []tasks.Kind) {
	r.add("window %s %v", name, members)
}

func (r *recorder) ScheduleLag(k tasks.Kind, lag time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lags[k] = append(r.lags[k], lag)
}

func (r *recorder) ScheduleMissed(k tasks.Kind, n int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.missed[k] += n
}

func (r *recorder) ScheduleError(k tasks.Kind) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.errs[k]++
}

// take returns and clears the events recorded so far.
func (r *recorder) take() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.events
	r.events = nil
	return out
}

func (r *recorder) errCount(k tasks.Kind) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.errs[k]
}

// fixture is one scenario's store, executor, config, clock and scheduler.
type fixture struct {
	t     *testing.T
	fs    *vfs.MemFS
	st    tasks.Stores
	nb    func() *storage.Batch
	x     tasks.Executor
	defs  []tasks.Definition
	cfg   *fakeConfig
	rec   *recorder
	mu    sync.Mutex
	now   time.Time
	held  map[tasks.Kind]bool
	zone  *time.Location
	sched *Scheduler
	// wrap, when set, decorates the scheduler's Options before New.
	wrap func(*Options)
}

type fixtureOpts struct {
	schedules map[string]tasks.ScheduleConfig
	windows   map[string]WindowConfig
	zone      *time.Location
	crashable bool
}

func newFixture(t *testing.T, defs []tasks.Definition, o fixtureOpts) *fixture {
	t.Helper()
	f := &fixture{t: t, held: map[tasks.Kind]bool{}, zone: o.zone}
	if f.zone == nil {
		f.zone = time.UTC
	}
	f.fs = vfs.NewMem()
	if o.crashable {
		f.fs = vfs.NewCrashableMem()
	}
	f.st, f.nb = openStores(t, f.fs)
	f.cfg = &fakeConfig{schedules: maps.Clone(o.schedules), windows: maps.Clone(o.windows)}
	if f.cfg.schedules == nil {
		f.cfg.schedules = map[string]tasks.ScheduleConfig{}
	}
	if f.cfg.windows == nil {
		f.cfg.windows = map[string]WindowConfig{}
	}
	f.rec = &recorder{now: f.clock, lags: map[tasks.Kind][]time.Duration{}, missed: map[tasks.Kind]int64{}, errs: map[tasks.Kind]int{}}
	f.defs = slices.Clone(defs)
	for _, name := range slices.Sorted(maps.Keys(f.cfg.windows)) {
		f.defs = append(f.defs, WindowDefinition(name, func(rc *tasks.RC) error { return f.scheduler().RunWindow(rc) }))
	}
	cat, err := testCatalog(f.defs)
	if err != nil {
		t.Fatal(err)
	}
	x, err := executor.New(executor.Options{
		Stores: f.st, NewBatch: f.nb, Definitions: f.defs, InstrumentsOff: true, Catalog: cat,
		Logger: quietLog(), Now: f.clock, Getenv: func(string) string { return "" },
	})
	if err != nil {
		t.Fatalf("executor.New: %v", err)
	}
	t.Cleanup(func() {
		if err := x.Shutdown(context.Background()); err != nil {
			t.Errorf("executor shutdown: %v", err)
		}
	})
	f.x = x
	return f
}

func testCatalog(defs []tasks.Definition) (*rbac.Catalog, error) {
	specs := rbac.CoreScopes()
	known := map[rbac.Permission]bool{}
	for _, sp := range specs {
		known[sp.Name] = true
	}
	for _, d := range defs {
		for _, p := range tasks.CommonOf(d).Perms.Start.Permissions() {
			if !known[p] {
				known[p] = true
				specs = append(specs, rbac.PermissionSpec{Name: p, Title: string(p), Description: "test scope " + string(p)})
			}
		}
	}
	return rbac.NewCatalog(specs...)
}

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func (f *fixture) clock() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func (f *fixture) setNow(t time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = t
}

func (f *fixture) isHeld(k tasks.Kind) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.held[k]
}

func (f *fixture) hold(k tasks.Kind, on bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.held[k] = on
}

func (f *fixture) scheduler() *Scheduler {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sched
}

func (f *fixture) options() Options {
	o := Options{
		Stores: f.st, Starter: f.x, NewBatch: f.nb, Definitions: f.defs, Config: f.cfg,
		Poll: time.Minute, Location: f.zone, Held: f.isHeld, Observer: f.rec, Metrics: f.rec,
		Logger: quietLog(), Now: f.clock,
	}
	if f.wrap != nil {
		f.wrap(&o)
	}
	return o
}

// boot builds a fresh scheduler at now (as after a restart) and runs Boot.
func (f *fixture) boot(now time.Time) {
	f.t.Helper()
	f.setNow(now)
	s, err := New(f.options())
	if err != nil {
		f.t.Fatalf("New: %v", err)
	}
	f.mu.Lock()
	f.sched = s
	f.mu.Unlock()
	s.Boot(context.Background())
}

// poll sets the clock and polls once.
func (f *fixture) poll(now time.Time) {
	f.t.Helper()
	f.setNow(now)
	f.scheduler().Poll(context.Background())
}

// clockOf reads k's stored clock.
func (f *fixture) clockOf(k tasks.Kind) (tasks.Clock, bool) {
	f.t.Helper()
	c, ok, err := f.st.Clocks.Get(context.Background(), k)
	if err != nil {
		f.t.Fatalf("clock %s: %v", k, err)
	}
	return c, ok
}

// runsOf lists every run of k (any state), oldest first.
func runsOf(t *testing.T, st tasks.Stores, k tasks.Kind) []tasks.RunRecord {
	t.Helper()
	var out []tasks.RunRecord
	q := tasks.RunQuery{Kinds: []tasks.Kind{k}, Limit: 1000}
	for r, err := range st.Runs.List(context.Background(), q) {
		if err != nil {
			t.Fatalf("list %s: %v", k, err)
		}
		out = append(out, r)
	}
	slices.SortFunc(out, func(a, b tasks.RunRecord) int { return a.Created.Compare(b.Created) })
	return out
}

// activeStates are the unfinished states finish closes.
func activeStates() []tasks.RunState {
	return []tasks.RunState{tasks.StateQueued, tasks.StateWaitingDeps, tasks.StateRunning}
}

// finish completes every active run of k, so its next fire is not folded.
func (f *fixture) finish(k tasks.Kind, state tasks.RunState) {
	f.t.Helper()
	ctx := context.Background()
	for _, r := range runsOf(f.t, f.st, k) {
		if !slices.Contains(activeStates(), r.State) {
			continue
		}
		next := r
		next.State, next.Finished = state, f.clock()
		want := tasks.Expect{State: r.State, Attempt: r.Attempt, FenceEpoch: r.FenceEpoch}
		b := f.nb()
		err := f.st.Runs.Transition(ctx, b, r.ID, want, next)
		if err == nil {
			err = b.Commit(ctx, storage.Sync)
		}
		_ = b.Close()
		if err != nil {
			f.t.Fatalf("finish %s: %v", r.ID, err)
		}
	}
}

// plantClock stores c as k's clock (the cutover's import).
func (f *fixture) plantClock(k tasks.Kind, c tasks.Clock) {
	f.t.Helper()
	ctx := context.Background()
	prev, had, err := f.st.Clocks.Get(ctx, k)
	if err != nil {
		f.t.Fatal(err)
	}
	b := f.nb()
	defer func() { _ = b.Close() }()
	if err := f.st.Clocks.Advance(ctx, b, k, prev, had, c); err != nil {
		f.t.Fatal(err)
	}
	if err := b.Commit(ctx, storage.Sync); err != nil {
		f.t.Fatal(err)
	}
}

// expect checks the events recorded since the last expect.
func (f *fixture) expect(want ...string) {
	f.t.Helper()
	got := f.rec.take()
	if !slices.Equal(got, want) {
		f.t.Fatalf("events:\n  %s\nwant:\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

// expectKind checks the events of kind k recorded since the last expect;
// other kinds' events are dropped.
func (f *fixture) expectKind(k tasks.Kind, want ...string) {
	f.t.Helper()
	var got []string
	for _, e := range f.rec.take() {
		if strings.Contains(e, " "+string(k)+" ") {
			got = append(got, e)
		}
	}
	if !slices.Equal(got, want) {
		f.t.Fatalf("%s events:\n  %s\nwant:\n  %s", k, strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

// gotham is a read-only gotham kind with schedule s.
func gotham(kind tasks.Kind, s *tasks.Schedule) tasks.Definition {
	return tasks.Task(kind, tasks.TaskSpec[struct{}, struct{}]{
		Title: string(kind), Effects: tasks.ReadOnly(), Resume: tasks.ResumeDrop,
		Perms: tasks.Permissions{Start: rbac.Need("gotham:run")}, Schedule: s,
		Run: func(*tasks.RC, struct{}) (struct{}, error) { return struct{}{}, nil },
	})
}

// gothamWriter is a writing gotham kind with schedule s.
func gothamWriter(kind tasks.Kind, s *tasks.Schedule) tasks.Definition {
	return tasks.Task(kind, tasks.TaskSpec[struct{}, struct{}]{
		Title: string(kind), Effects: tasks.Writes(tasks.Res("gotham", "cases")), Resume: tasks.ResumeDrop,
		Perms: tasks.Permissions{Start: rbac.Need("gotham:run")}, Schedule: s,
		Run: func(*tasks.RC, struct{}) (struct{}, error) { return struct{}{}, nil },
	})
}

func every(d time.Duration) tasks.ScheduleConfig {
	return tasks.ScheduleConfig{Enabled: true, Every: config.Duration(d)}
}
