// file: core/tasks/taskstest/schedule/schedule.go
// version: 1.0.0
// guid: 42d6120e-8654-41af-936c-10636802a5fa
// last-edited: 2026-11-11

// Package schedule is the schedule-parity harness (05-tasks 5T.9, F2-04): it
// runs core's scheduler over a set of definitions, a config and a fake clock,
// and returns the sequence the scheduler produced (clock seeds and advances,
// enqueues, on-start runs, folds and window openings with the members each
// would run), so that F5-14's TestScheduleParity_AllDefinitions and
// TestCutover_NoCatchUpStorm can compare core's sequence with HEAD's.
//
// Nothing sleeps: each Step sets the fake clock and calls the scheduler's
// Boot or Poll directly. The store is pebble over an in-memory file system
// (storage.Open over a MemFS, never coretest.NewStore: core/tasks is in the
// kernel's import graph). The executor is built but never run, so runs stay
// queued, no body executes, and a later fire folds into a queued run until a
// Step finishes it. Names coined by F2-04.
package schedule

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

	"github.com/falkcorp/mediacore/internal/executor"
	"github.com/falkcorp/mediacore/internal/scheduler"
	"github.com/falkcorp/mediacore/internal/taskstore"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/tasks"
)

// Window is one core.tasks.windows.<name> entry: hours 0-23 (wrapping
// midnight when End <= Start) and the enable flag.
type Window struct {
	Start, End int
	Enabled    bool
}

// Config is what the harness serves the scheduler. Schedules holds every
// schedule key's value; a key a definition names that is missing here is an
// undeclared key (a schedule error at each poll). Windows holds the windows;
// each name gets a window kind (tasks.WindowKind) registered with the
// executor. Poll is core.tasks.schedule_poll (0 = 1m) and Zone
// core.tasks.timezone (nil = UTC, so a sequence never depends on the host).
type Config struct {
	Schedules map[string]tasks.ScheduleConfig
	Windows   map[string]Window
	Poll      time.Duration
	Zone      *time.Location
}

// Step is one moment of a scenario. The harness applies, in order: Clocks,
// Edit, Finish, Held, then Boot (a fresh scheduler, as after a restart; the
// first step always boots), then one Poll at At unless NoPoll.
type Step struct {
	At     time.Time                  // the fake clock for this step; never earlier than the previous step's
	Clocks map[tasks.Kind]tasks.Clock // stored as-is first (the cutover's import of HEAD's clocks)
	Edit   func(c *Config)            // a hot config change before the poll
	Finish []tasks.Kind               // complete every active run of these kinds (so the next fire is not folded)
	Held   []tasks.Kind               // when non-nil: exactly these kinds are held from this step on
	Boot   bool                       // restart: a new scheduler over the same store; its Boot fires OnStart
	NoPoll bool                       // boot (or apply the edits) without polling
}

// EventType names what an Event records.
type EventType string

const (
	EventSeed    EventType = "seed"    // first sight: the clock was created; nothing fired
	EventAdvance EventType = "advance" // the clock moved (a due check, a gate edge)
	EventEnqueue EventType = "enqueue" // a run was inserted (Trigger: schedule, on_start or window)
	EventFold    EventType = "fold"    // a fire found an active run of the kind and mode and inserted nothing
	EventGated   EventType = "gated"   // a fire's When gate was closed: the clock advanced, no run
	EventFailed  EventType = "failed"  // a fire's start failed: the clock advanced alone
	EventWindow  EventType = "window"  // a window opened; Members are what its run would start, in order
)

// Event is one observed scheduler write. It is comparable field by field
// except Members; String renders it for golden files.
type Event struct {
	At      time.Time
	Type    EventType
	Kind    tasks.Kind
	Trigger tasks.Trigger // enqueue, fold, gated and failed
	Missed  int           // the fire's missed count
	LastDue time.Time     // seed and advance: the clock's LastDue afterwards
	Members []tasks.Kind  // window
	Err     string        // failed: the start's error
}

// String renders e on one line, times in UTC.
func (e Event) String() string {
	at := e.At.UTC().Format(time.RFC3339)
	switch e.Type {
	case EventSeed, EventAdvance:
		return fmt.Sprintf("%s %s %s last_due=%s", at, e.Type, e.Kind, e.LastDue.UTC().Format(time.RFC3339))
	case EventWindow:
		ms := make([]string, len(e.Members))
		for i, m := range e.Members {
			ms[i] = string(m)
		}
		return fmt.Sprintf("%s window %s members=[%s]", at, e.Kind, strings.Join(ms, " "))
	}
	s := fmt.Sprintf("%s %s %s trigger=%s", at, e.Type, e.Kind, e.Trigger)
	if e.Missed > 0 {
		s += fmt.Sprintf(" missed=%d", e.Missed)
	}
	if e.Err != "" {
		s += " err=" + e.Err
	}
	return s
}

// Strings renders a sequence, one String per event.
func Strings(evs []Event) []string {
	out := make([]string, len(evs))
	for i, e := range evs {
		out[i] = e.String()
	}
	return out
}

// Run plays steps against defs and cfg and returns every event, in order.
// defs are the scheduled kinds (and any others); the harness adds a window
// kind for each configured window. It fails t on a setup error.
func Run(t testing.TB, defs []tasks.Definition, cfg Config, steps []Step) []Event {
	t.Helper()
	h := newHarness(t, defs, cfg)
	for i, st := range steps {
		if i > 0 && st.At.Before(h.now) {
			t.Fatalf("step %d: At %s is before the previous step's %s", i, st.At, h.now)
		}
		h.step(i, st)
	}
	return h.events()
}

// harness is one Run's state.
type harness struct {
	t     testing.TB
	defs  []tasks.Definition
	cfg   Config
	mu    sync.Mutex
	now   time.Time
	held  map[tasks.Kind]bool
	evs   []Event
	st    tasks.Stores
	nb    func() *storage.Batch
	x     tasks.Executor
	sched *scheduler.Scheduler
}

func newHarness(t testing.TB, defs []tasks.Definition, cfg Config) *harness {
	t.Helper()
	if cfg.Zone == nil {
		cfg.Zone = time.UTC
	}
	if cfg.Poll <= 0 {
		cfg.Poll = time.Minute
	}
	cfg.Schedules = maps.Clone(cfg.Schedules)
	cfg.Windows = maps.Clone(cfg.Windows)
	h := &harness{t: t, cfg: cfg, held: map[tasks.Kind]bool{}}
	h.st, h.nb = openStore(t)
	h.defs = slices.Clone(defs)
	for _, name := range slices.Sorted(maps.Keys(cfg.Windows)) {
		h.defs = append(h.defs, scheduler.WindowDefinition(name, func(rc *tasks.RC) error {
			return h.scheduler().RunWindow(rc)
		}))
	}
	cat, err := catalogFor(h.defs)
	if err != nil {
		t.Fatalf("schedule harness: %v", err)
	}
	x, err := executor.New(executor.Options{
		Stores: h.st, NewBatch: h.nb, Definitions: h.defs, InstrumentsOff: true, Catalog: cat,
		Logger: quiet(), Now: h.clock, Getenv: func(string) string { return "" },
	})
	if err != nil {
		t.Fatalf("schedule harness: executor.New: %v", err)
	}
	t.Cleanup(func() {
		if err := x.Shutdown(context.Background()); err != nil {
			t.Errorf("schedule harness: executor shutdown: %v", err)
		}
	})
	h.x = x
	return h
}

// catalogFor is core's scopes plus every other scope a definition's
// Perms.Start names, so each module's system actor may start its own kinds
// (the executor's StartIn rule, SEC2-15), as the kernel's catalog allows.
func catalogFor(defs []tasks.Definition) (*rbac.Catalog, error) {
	specs := rbac.CoreScopes()
	known := map[rbac.Permission]bool{}
	for _, sp := range specs {
		known[sp.Name] = true
	}
	for _, d := range defs {
		for _, p := range tasks.CommonOf(d).Perms.Start.Permissions() {
			if !known[p] {
				known[p] = true
				specs = append(specs, rbac.PermissionSpec{Name: p, Title: string(p), Description: "Harness scope " + string(p)})
			}
		}
	}
	return rbac.NewCatalog(specs...)
}

// openStore opens a pebble store over a MemFS with only c/tasks/ enabled.
func openStore(t testing.TB) (tasks.Stores, func() *storage.Batch) {
	t.Helper()
	ctx := context.Background()
	o, err := storage.Open(ctx, storage.OpenOptions{
		Path:            "/wayne-manor/schedule.pebble",
		Owners:          []storage.OwnerSpec{{Owner: taskstore.Owner, Spec: taskstore.StorageSpec(), Enabled: true}},
		Build:           "schedule+harness",
		FreeSpaceMargin: 1,
		FS:              storage.FSOf(vfs.NewMem()),
	})
	if err != nil {
		t.Fatalf("schedule harness: open: %v", err)
	}
	db, err := o.Prepare(ctx)
	if err == nil {
		err = db.Migrate(ctx)
	}
	if err != nil {
		t.Fatalf("schedule harness: boot: %v", errors.Join(err, o.Close()))
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("schedule harness: close: %v", err)
		}
	})
	ks, ok := db.Keyspace(taskstore.Owner)
	if !ok {
		t.Fatalf("schedule harness: owner %s has no keyspace", taskstore.Owner)
	}
	st := tasks.NativeStores(ks)
	return st, st.Runs.(interface{ NewBatch() *storage.Batch }).NewBatch
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func (h *harness) clock() time.Time {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.now
}

func (h *harness) scheduler() *scheduler.Scheduler {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.sched
}

func (h *harness) events() []Event {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.evs)
}

func (h *harness) record(e Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	e.At = h.now
	h.evs = append(h.evs, e)
}

func (h *harness) step(i int, st Step) {
	t := h.t
	t.Helper()
	ctx := context.Background()
	h.mu.Lock()
	h.now = st.At
	h.mu.Unlock()
	for _, k := range slices.Sorted(maps.Keys(st.Clocks)) {
		h.plantClock(i, k, st.Clocks[k])
	}
	if st.Edit != nil {
		st.Edit(&h.cfg)
	}
	for _, k := range st.Finish {
		h.finish(i, k)
	}
	if st.Held != nil {
		h.mu.Lock()
		h.held = map[tasks.Kind]bool{}
		for _, k := range st.Held {
			h.held[k] = true
		}
		h.mu.Unlock()
	}
	if st.Boot || h.scheduler() == nil {
		s, err := scheduler.New(scheduler.Options{
			Stores: h.st, Starter: h.x, NewBatch: h.nb, Definitions: h.defs, Config: harnessConfig{h},
			Poll: h.cfg.Poll, Location: h.cfg.Zone, Held: h.isHeld, Observer: observer{h},
			Logger: quiet(), Now: h.clock,
		})
		if err != nil {
			t.Fatalf("step %d: scheduler.New: %v", i, err)
		}
		h.mu.Lock()
		h.sched = s
		h.mu.Unlock()
		s.Boot(ctx)
	}
	if !st.NoPoll {
		h.scheduler().Poll(ctx)
	}
}

func (h *harness) isHeld(k tasks.Kind) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.held[k]
}

// plantClock stores c as k's clock, replacing whatever is there.
func (h *harness) plantClock(i int, k tasks.Kind, c tasks.Clock) {
	ctx := context.Background()
	prev, had, err := h.st.Clocks.Get(ctx, k)
	if err == nil {
		err = h.write(func(b *storage.Batch) error { return h.st.Clocks.Advance(ctx, b, k, prev, had, c) })
	}
	if err != nil {
		h.t.Fatalf("step %d: plant clock %s: %v", i, k, err)
	}
}

// finish completes every queued, waiting or running run of k.
func (h *harness) finish(i int, k tasks.Kind) {
	ctx := context.Background()
	q := tasks.RunQuery{Kinds: []tasks.Kind{k},
		States: []tasks.RunState{tasks.StateQueued, tasks.StateWaitingDeps, tasks.StateRunning}, Limit: 1000}
	var live []tasks.RunRecord
	for r, err := range h.st.Runs.List(ctx, q) {
		if err != nil {
			h.t.Fatalf("step %d: list %s: %v", i, k, err)
		}
		live = append(live, r)
	}
	for _, r := range live {
		next := r
		next.State, next.Finished = tasks.StateSucceeded, h.clock()
		want := tasks.Expect{State: r.State, Attempt: r.Attempt, FenceEpoch: r.FenceEpoch}
		if err := h.write(func(b *storage.Batch) error { return h.st.Runs.Transition(ctx, b, r.ID, want, next) }); err != nil {
			h.t.Fatalf("step %d: finish %s: %v", i, r.ID, err)
		}
	}
}

func (h *harness) write(stage func(b *storage.Batch) error) (err error) {
	b := h.nb()
	defer func() { err = errors.Join(err, b.Close()) }() // a no-op after Commit
	if err := stage(b); err != nil {
		return err
	}
	return b.Commit(context.Background(), storage.Sync)
}

// harnessConfig serves Config to the scheduler.
type harnessConfig struct{ h *harness }

func (c harnessConfig) Schedule(key string) (tasks.ScheduleConfig, error) {
	v, ok := c.h.cfg.Schedules[key]
	if !ok {
		return tasks.ScheduleConfig{}, fmt.Errorf("schedule harness: key %s is not declared", key)
	}
	return v, nil
}

func (c harnessConfig) Window(name string) (scheduler.WindowConfig, bool) {
	w, ok := c.h.cfg.Windows[name]
	return scheduler.WindowConfig{Start: w.Start, End: w.End, Enabled: w.Enabled}, ok
}

// observer records the scheduler's committed writes as Events.
type observer struct{ h *harness }

func (o observer) Advanced(k tasks.Kind, _ tasks.Clock, had bool, next tasks.Clock) {
	typ := EventAdvance
	if !had {
		typ = EventSeed
	}
	o.h.record(Event{Type: typ, Kind: k, LastDue: next.LastDue})
}

func (o observer) Fired(f scheduler.Fire) {
	e := Event{Kind: f.Kind, Trigger: f.Trigger, Missed: f.Missed}
	switch {
	case f.Err != nil:
		e.Type, e.Err = EventFailed, f.Err.Error()
	case f.Gated:
		e.Type = EventGated
	case f.Folded:
		e.Type = EventFold
	default:
		e.Type = EventEnqueue
	}
	o.h.record(e)
}

func (o observer) WindowOpened(name string, members []tasks.Kind) {
	o.h.record(Event{Type: EventWindow, Kind: tasks.WindowKind(name), Members: slices.Clone(members)})
}
