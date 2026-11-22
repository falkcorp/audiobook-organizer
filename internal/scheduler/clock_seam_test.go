// file: internal/scheduler/clock_seam_test.go
// version: 0.1.0
// guid: 5d3f9a1c-8e24-4b6a-b07e-2c9d4f1e6a83
// last-edited: 2026-11-20

package scheduler

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	dbmocks "github.com/falkcorp/audiobook-organizer/internal/books/store/mocks"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/operations"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// fakeClock is the SchedulerDeps clock seam driven by hand: Now reads at,
// and every Tick call hands its channel to the test on timers.
type fakeClock struct {
	mu     sync.Mutex
	at     time.Time
	timers chan chan time.Time
}

func newFakeClock(at time.Time) *fakeClock {
	return &fakeClock{at: at, timers: make(chan chan time.Time, 8)}
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *fakeClock) set(at time.Time) {
	c.mu.Lock()
	c.at = at
	c.mu.Unlock()
}

// tick builds one unbuffered timer: a send returns once the checker has
// taken the tick.
func (c *fakeClock) tick(time.Duration) (<-chan time.Time, func()) {
	ch := make(chan time.Time)
	c.timers <- ch
	return ch, func() {}
}

// step moves the clock to at and delivers a tick on timer, then a second
// tick at the same instant. A checker takes the second only after it has
// acted on the first, and a repeated check at one instant is idempotent, so
// step returns with the first tick's work done.
func (c *fakeClock) step(timer chan time.Time, at time.Time) {
	c.set(at)
	timer <- at
	timer <- at
}

// timer takes the next timer Start built through SchedulerDeps.Tick.
func (c *fakeClock) timer(t *testing.T) chan time.Time {
	t.Helper()
	select {
	case ch := <-c.timers:
		return ch
	case <-time.After(5 * time.Second):
		t.Fatal("Start built no timer through SchedulerDeps.Tick")
		return nil
	}
}

// settingsStore is a mock store whose settings live in a map, plus the
// operations-v2 calls an opsregistry enqueue makes; inserted collects the
// enqueued rows.
func settingsStore(t *testing.T) (*dbmocks.MockStore, map[string]string, *[]database.OperationV2Row) {
	t.Helper()
	settings := map[string]string{}
	inserted := &[]database.OperationV2Row{}
	var mu sync.Mutex
	store := dbmocks.NewMockStore(t)
	store.EXPECT().GetSetting(mock.Anything).RunAndReturn(func(key string) (*database.Setting, error) {
		mu.Lock()
		defer mu.Unlock()
		v, ok := settings[key]
		if !ok {
			return nil, database.ErrSettingNotFound
		}
		return &database.Setting{Key: key, Value: v, Type: "string"}, nil
	}).Maybe()
	store.EXPECT().SetSetting(mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		RunAndReturn(func(key, value, _ string, _ bool) error {
			mu.Lock()
			defer mu.Unlock()
			settings[key] = value
			return nil
		}).Maybe()
	store.EXPECT().UpsertOpDefinitionV2(mock.Anything).Return(nil).Maybe()
	store.EXPECT().InsertOperationV2(mock.Anything).RunAndReturn(func(row database.OperationV2Row) error {
		mu.Lock()
		defer mu.Unlock()
		*inserted = append(*inserted, row)
		return nil
	}).Maybe()
	store.EXPECT().GetOperationV2(mock.Anything).Return(nil, nil).Maybe()
	store.EXPECT().ListActiveOperationsV2().Return(nil, nil).Maybe()
	store.EXPECT().GetDepRev(mock.Anything).Return(0, nil).Maybe()
	return store, settings, inserted
}

// testRegistry is an opsregistry over store with the given ops registered.
func testRegistry(t *testing.T, store *dbmocks.MockStore, ids ...string) *opsregistry.Registry {
	t.Helper()
	reg := opsregistry.New(store, slog.New(slog.DiscardHandler), 1, nil)
	for _, id := range ids {
		require.NoError(t, reg.RegisterOp(opsregistry.OperationDef{
			ID: id, Plugin: "test", ResumePolicy: opsregistry.ResumeDrop, Liveness: opsregistry.LivenessManual,
			Run: func(context.Context, json.RawMessage, opsregistry.Reporter) error { return nil },
		}))
	}
	return reg
}

// seamScheduler is a TaskScheduler on settings, the fake clock and the
// given zone, with one registered task (or none for an empty Name).
func seamScheduler(t *testing.T, clk *fakeClock, zone *time.Location, def TaskDefinition) *TaskScheduler {
	t.Helper()
	store, _, _ := settingsStore(t)
	return seamSchedulerOn(store, clk, zone, def)
}

// seamSchedulerOn is seamScheduler over a given store.
func seamSchedulerOn(store *dbmocks.MockStore, clk *fakeClock, zone *time.Location, def TaskDefinition) *TaskScheduler {
	ts := &TaskScheduler{
		deps: SchedulerDeps{
			Store:    func() SchedulerStore { return store },
			Now:      clk.now,
			Location: zone,
			Tick:     clk.tick,
		},
		tasks:   make(map[string]*TaskDefinition),
		lastRun: make(map[string]time.Time),
	}
	if def.Name != "" {
		ts.RegisterTask(def)
	}
	return ts
}

// TestClockSeam_DrivesTheIntervalDueRule (F5-14 pre-slice): with Now and
// Tick injected, the interval checker's seed, not-yet-due and due decisions
// follow the fake clock, not the wall clock, and the lastRun stamp is the
// fake instant. Without the seam the checker reads time.Now (no fire on a
// clock set a day ahead) and runs a real 60s ticker the test never sees.
func TestClockSeam_DrivesTheIntervalDueRule(t *testing.T) {
	t0 := time.Date(2030, 3, 7, 12, 0, 0, 0, time.UTC)
	clk := newFakeClock(t0)
	var fired atomic.Int32
	ts := seamScheduler(t, clk, time.UTC, TaskDefinition{
		Name:        "tombstone_cleanup",
		TriggerFn:   func(string) (*database.Operation, error) { fired.Add(1); return &database.Operation{ID: "joker"}, nil },
		IsEnabled:   func() bool { return true },
		GetInterval: func() time.Duration { return 24 * time.Hour },
		RunOnStart:  func() bool { return false },
	})
	shutdown := make(chan struct{})
	var wg sync.WaitGroup
	ts.Start(shutdown, &wg)
	defer func() { close(shutdown); wg.Wait() }()

	timer := clk.timer(t)

	clk.step(timer, t0) // first sight: seed
	clk.step(timer, t0.Add(23*time.Hour))
	require.Zero(t, fired.Load(), "fired before the interval elapsed on the fake clock")
	clk.step(timer, t0.Add(24*time.Hour))
	require.EqualValues(t, 1, fired.Load(), "the fake clock's due instant must fire exactly once")

	last, ok := ts.loadIntervalLastTick("tombstone_cleanup")
	require.True(t, ok)
	assert.True(t, last.Equal(t0.Add(24*time.Hour)), "the durable clock carries the fake instant, got %s", last)
	ts.mu.RLock()
	stamped := ts.lastRun["tombstone_cleanup"]
	ts.mu.RUnlock()
	assert.True(t, stamped.Equal(t0.Add(24*time.Hour)), "lastRun is the fake instant, got %s", stamped)
}

// TestClockSeam_MaintenanceDateUsesTheInjectedZone: the maintenance window's
// once-a-day date is taken in SchedulerDeps.Location, so 23:30 UTC on the
// 7th is already the 8th east of UTC and a run stamped on the 7th there no
// longer counts as today.
func TestClockSeam_MaintenanceDateUsesTheInjectedZone(t *testing.T) {
	east := time.FixedZone("gotham-east", 2*60*60)
	clk := newFakeClock(time.Date(2030, 3, 7, 20, 0, 0, 0, time.UTC))
	ts := seamScheduler(t, clk, east, TaskDefinition{
		Name: "riddle", IsEnabled: func() bool { return false },
		GetInterval: func() time.Duration { return 0 }, RunOnStart: func() bool { return false },
	})
	ts.saveLastMaintenanceRun()
	assert.Equal(t, "2030-03-07", ts.GetLastMaintenanceRunDate())
	assert.True(t, ts.hasRunToday())
	clk.set(time.Date(2030, 3, 7, 23, 30, 0, 0, time.UTC))
	assert.False(t, ts.hasRunToday(), "23:30 UTC is 01:30 on the 8th in the injected zone")
}

// TestClockSeam_NilKeepsTheRealClock: with the seam left nil the helpers are
// time.Now, time.Local and a real ticker, the values the package used before.
func TestClockSeam_NilKeepsTheRealClock(t *testing.T) {
	ts := &TaskScheduler{}
	assert.WithinDuration(t, time.Now(), ts.now(), time.Second)
	assert.Equal(t, time.Local, ts.zone())
	c, stop := ts.newTicker(time.Millisecond)
	defer stop()
	select {
	case <-c:
	case <-time.After(5 * time.Second):
		t.Fatal("the default ticker never ticked")
	}
}

// startSeam starts ts and stops it at the end of the test.
func startSeam(t *testing.T, ts *TaskScheduler) {
	t.Helper()
	shutdown := make(chan struct{})
	var wg sync.WaitGroup
	ts.Start(shutdown, &wg)
	t.Cleanup(func() { close(shutdown); wg.Wait() })
}

// TestClockSeam_DrivesTheDailyAtTimer: the DailyAt checker reads the fake
// clock and takes its wall time in SchedulerDeps.Location. 03:00 in a zone two
// hours east of UTC is 01:00 UTC; Start's immediate check seeds at 00:30 UTC
// and the tick at 01:00 UTC fires.
func TestClockSeam_DrivesTheDailyAtTimer(t *testing.T) {
	east := time.FixedZone("gotham-east", 2*60*60)
	t0 := time.Date(2030, 3, 7, 0, 30, 0, 0, time.UTC)
	clk := newFakeClock(t0)
	var fired atomic.Int32
	ts := seamScheduler(t, clk, east, TaskDefinition{
		Name: "nightly_activity_compaction",
		TriggerFn: func(string) (*database.Operation, error) {
			fired.Add(1)
			return &database.Operation{ID: "penguin"}, nil
		},
		IsEnabled:   func() bool { return true },
		GetInterval: func() time.Duration { return 0 },
		RunOnStart:  func() bool { return false },
		DailyAt:     "03:00",
	})
	startSeam(t, ts)
	timer := clk.timer(t) // built after Start's immediate check, which seeded at t0
	last, ok := ts.loadIntervalLastTick("nightly_activity_compaction")
	require.True(t, ok, "Start's immediate DailyAt check did not seed")
	require.True(t, last.Equal(t0), "seeded at %s, want the fake %s", last, t0)
	clk.step(timer, time.Date(2030, 3, 7, 0, 59, 0, 0, time.UTC))
	require.Zero(t, fired.Load(), "fired before 03:00 in the injected zone")
	clk.step(timer, time.Date(2030, 3, 7, 1, 0, 0, 0, time.UTC))
	require.EqualValues(t, 1, fired.Load(), "03:00 in the injected zone must fire once")
}

// TestClockSeam_DrivesTheMaintenanceChecker: the window checker reads the
// hour of the fake clock in SchedulerDeps.Location and its timer comes from
// Tick. The window's hours are still read from config.AppConfig.Maintenance by
// IsInMaintenanceWindowAt (unchanged; seams.d/F5-14.md).
func TestClockSeam_DrivesTheMaintenanceChecker(t *testing.T) {
	restore := config.Snapshot()
	t.Cleanup(func() { config.ReplaceForTest(restore) })
	config.SetForTest(func(c *config.Config) {
		c.Maintenance.Enabled = true
		c.Maintenance.WindowStart = 1
		c.Maintenance.WindowEnd = 4
	})

	east := time.FixedZone("gotham-east", 2*60*60)
	clk := newFakeClock(time.Date(2030, 3, 7, 22, 30, 0, 0, time.UTC)) // 00:30 east
	store, settings, inserted := settingsStore(t)
	ts := seamSchedulerOn(store, clk, east, TaskDefinition{})
	ts.deps.OpRegistry = testRegistry(t, store, "maintenance.window")
	ts.deps.Maintenance = MaintenanceSettings{Enabled: true, WindowStart: 1, WindowEnd: 4}
	startSeam(t, ts)
	timer := clk.timer(t)

	clk.step(timer, time.Date(2030, 3, 7, 22, 30, 0, 0, time.UTC))
	require.Empty(t, *inserted, "00:30 in the injected zone is outside 1-4")
	clk.step(timer, time.Date(2030, 3, 7, 23, 30, 0, 0, time.UTC)) // 01:30 east, on the 8th
	require.Len(t, *inserted, 1, "01:30 in the injected zone opens the window once")
	assert.Equal(t, "maintenance.window", (*inserted)[0].DefID)
	assert.Equal(t, "2030-03-08", settings["maintenance_window_last_run"], "the run date is the injected zone's")
}

// TestClockSeam_DrivesTheFullSweepClock: library_scan_full's seed, due check
// and stamp read SchedulerDeps.Now.
func TestClockSeam_DrivesTheFullSweepClock(t *testing.T) {
	restore := config.Snapshot()
	t.Cleanup(func() { config.ReplaceForTest(restore) })
	config.ResetToDefaultsForTest()
	period := time.Duration(config.Get().Scheduled.LibraryScanFull.PeriodHours) * time.Hour
	require.Positive(t, period)

	t0 := time.Date(2030, 3, 7, 12, 0, 0, 0, time.UTC)
	clk := newFakeClock(t0)
	store, settings, inserted := settingsStore(t)
	deps := testDeps()
	deps.Store = func() SchedulerStore { return store }
	deps.OpRegistry = testRegistry(t, store, "library.scan")
	deps.Now = clk.now
	ts := NewTaskScheduler(deps)
	task, ok := ts.GetTask("library_scan_full")
	require.True(t, ok)

	sweep := func(at time.Time) *database.Operation {
		clk.set(at)
		op, err := task.TriggerFn(operations.TriggerScheduled)
		require.NoError(t, err)
		return op
	}
	require.Nil(t, sweep(t0), "first sight seeds")
	assert.Equal(t, t0.Format(time.RFC3339), settings[fullSweepLastRunSetting])
	require.Nil(t, sweep(t0.Add(period-time.Hour)), "not due one hour before the period on the fake clock")
	require.NotNil(t, sweep(t0.Add(period)), "due at the period on the fake clock")
	require.Len(t, *inserted, 1)
	assert.Equal(t, t0.Add(period).Format(time.RFC3339), settings[fullSweepLastRunSetting])
}

// TestClockSeam_DrivesTheOpsScheduleLoop: NewTaskScheduler hands Now and
// Location to the ops.Schedule loop, and Start builds that loop's timer
// through Tick.
func TestClockSeam_DrivesTheOpsScheduleLoop(t *testing.T) {
	t0 := time.Date(2030, 3, 7, 12, 0, 0, 0, time.UTC)
	clk := newFakeClock(t0)
	east := time.FixedZone("gotham-east", 2*60*60)
	deps := reachabilityTestDeps()
	deps.Now, deps.Location, deps.Tick = clk.now, east, clk.tick
	ts := NewTaskScheduler(deps)
	require.True(t, ts.v3.clock().Equal(t0))
	require.Equal(t, east, ts.v3.zone())

	// Only the ops.Schedule loop: drop the TaskDefinitions so Start builds one timer.
	ts.tasks, ts.order = map[string]*TaskDefinition{}, nil
	var mu sync.Mutex
	var calls []string
	sched := &fakeSchedStore{}
	ts.v3.store = func() schedStore { return sched }
	ts.v3.hasActive = func(string) bool { return false }
	ts.v3.v2Cron = func(string) (string, bool) { return "", false }
	ts.v3.enqueue = func(_ context.Context, id string, _ json.RawMessage) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, id)
		return "op-" + id, nil
	}
	require.NoError(t, ts.RegisterSchedules([]ops.Definition{readDef("synthetic.every", ops.Every(time.Hour))}))
	startSeam(t, ts)
	timer := clk.timer(t)

	clk.step(timer, t0) // first sight seeds
	clk.step(timer, t0.Add(59*time.Minute))
	clk.step(timer, t0.Add(time.Hour))
	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, []string{"synthetic.every"}, calls, "one fire at the fake clock's due instant")
}
