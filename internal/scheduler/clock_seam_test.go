// file: internal/scheduler/clock_seam_test.go
// version: 0.1.0
// guid: 5d3f9a1c-8e24-4b6a-b07e-2c9d4f1e6a83
// last-edited: 2026-11-20

package scheduler

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	dbmocks "github.com/falkcorp/audiobook-organizer/internal/books/store/mocks"
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

// seamScheduler is a TaskScheduler on settings, the fake clock and the
// given zone, with one registered task.
func seamScheduler(t *testing.T, clk *fakeClock, zone *time.Location, def TaskDefinition) *TaskScheduler {
	t.Helper()
	settings := map[string]string{}
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
	ts.RegisterTask(def)
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

	var timer chan time.Time
	select {
	case timer = <-clk.timers:
	case <-time.After(5 * time.Second):
		t.Fatal("Start built no timer through SchedulerDeps.Tick")
	}

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
