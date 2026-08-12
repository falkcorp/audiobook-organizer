// file: internal/scheduler/periodic_library_scan_test.go
// version: 1.0.0
// guid: 8c5b1e74-2d06-4f39-a7b8-0e93c5d18a42
// last-edited: 2026-08-11

package scheduler

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLibraryScanIsScheduledUnderDefaultConfig is the regression test for
// "nothing scans automatically". Out of the box, library_scan's GetInterval
// returned a hard-coded 0, so Start's `IsEnabled() && GetInterval() > 0` gate
// never created a ticker and a book copied into an import path stayed invisible
// until somebody pressed Scan by hand.
//
// This asserts the exact gate Start uses, against the shipped default config.
func TestLibraryScanIsScheduledUnderDefaultConfig(t *testing.T) {
	restore := config.Snapshot()
	t.Cleanup(func() { config.AppConfig = restore })

	config.ResetToDefaults()

	// Guard rail: the fix must not be sneaking through the legacy flags. Under
	// defaults both of these are off, which is precisely why the old code was
	// inert.
	require.False(t, config.AppConfig.ScanOnStartup, "default config should still have scan_on_startup off")
	require.False(t, config.AppConfig.AutoScanEnabled, "default config should still have auto_scan_enabled off")

	ts := NewTaskScheduler(testDeps())
	task, ok := ts.GetTask("library_scan")
	require.True(t, ok, "library_scan should be registered")

	assert.True(t, task.IsEnabled(), "library_scan must be enabled under default config")
	interval := task.GetInterval()
	assert.Greater(t, interval, time.Duration(0),
		"library_scan must report a non-zero interval, otherwise Start never creates a ticker for it")
	assert.Equal(t, 6*time.Hour, interval, "default periodic scan interval should be 6h (360 min)")
}

// TestLibraryScanIntervalIsConfigurable proves the interval is read from
// config rather than being another hard-coded constant, including the two
// ways an operator can turn the periodic scan back off.
func TestLibraryScanIntervalIsConfigurable(t *testing.T) {
	restore := config.Snapshot()
	t.Cleanup(func() { config.AppConfig = restore })

	ts := NewTaskScheduler(testDeps())
	task, ok := ts.GetTask("library_scan")
	require.True(t, ok)

	cases := []struct {
		name     string
		enabled  bool
		interval int
		want     time.Duration
	}{
		{"custom interval honoured", true, 15, 15 * time.Minute},
		{"hourly", true, 60, time.Hour},
		{"disabled task means no ticker", false, 360, 0},
		{"zero interval means no ticker", true, 0, 0},
		{"negative interval means no ticker", true, -5, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			config.AppConfig.Scheduled.LibraryScan = config.ScheduledTaskConfig{
				Enabled:  tc.enabled,
				Interval: tc.interval,
			}
			assert.Equal(t, tc.want, task.GetInterval())
		})
	}
}

// TestLibraryScanEnabledPreservesLegacyScanOnStartup pins the compatibility
// path: IsEnabled gates the startup run as well as the ticker, so a user who
// only ever set scan_on_startup must not lose their startup scan just because
// the new scheduled.library_scan key is off.
func TestLibraryScanEnabledPreservesLegacyScanOnStartup(t *testing.T) {
	restore := config.Snapshot()
	t.Cleanup(func() { config.AppConfig = restore })

	ts := NewTaskScheduler(testDeps())
	task, ok := ts.GetTask("library_scan")
	require.True(t, ok)

	config.AppConfig.Scheduled.LibraryScan = config.ScheduledTaskConfig{Enabled: false, Interval: 0}
	config.AppConfig.ScanOnStartup = true

	assert.True(t, task.IsEnabled(), "legacy scan_on_startup must still enable the task")
	assert.True(t, task.RunOnStart(), "legacy scan_on_startup must still trigger a startup scan")
	assert.Equal(t, time.Duration(0), task.GetInterval(),
		"legacy flag alone must NOT start a periodic ticker")
}

// TestLibraryScanIsReachableFromMaintenanceWindow pins the dead-config fix:
// the maintenance-window op only ever iterates MaintenanceOrder(), so a task
// absent from that list can never be run by the window no matter what
// maintenance.library_scan is set to.
func TestLibraryScanIsReachableFromMaintenanceWindow(t *testing.T) {
	ts := NewTaskScheduler(testDeps())

	found := false
	for _, name := range ts.MaintenanceOrder() {
		if name == "library_scan" {
			found = true
			break
		}
	}
	assert.True(t, found,
		"library_scan must be in MaintenanceOrder(), otherwise the maintenance.library_scan setting is unreachable")
}

// TestStartCreatesTickerForNonZeroInterval pins the mechanic the fix above
// depends on: Start must actually create a running ticker for an enabled task
// whose GetInterval is > 0. Without this, TestLibraryScanIsScheduledUnder-
// DefaultConfig would pass on a scheduler that never ticks.
//
// A bare TaskScheduler is used rather than NewTaskScheduler so only the probe
// task is registered and the real tasks' startup runs stay out of the way.
func TestStartCreatesTickerForNonZeroInterval(t *testing.T) {
	restore := config.Snapshot()
	t.Cleanup(func() { config.AppConfig = restore })
	// Keep the maintenance-window goroutine out of this test.
	config.AppConfig.Maintenance.Enabled = false

	ts := &TaskScheduler{
		deps:    testDeps(),
		tasks:   make(map[string]*TaskDefinition),
		lastRun: make(map[string]time.Time),
	}

	var fired atomic.Int32
	ts.RegisterTask(TaskDefinition{
		Name:     "ticker_probe",
		Category: "test",
		TriggerFn: func(string) (*database.Operation, error) {
			fired.Add(1)
			return nil, nil
		},
		IsEnabled:              func() bool { return true },
		GetInterval:            func() time.Duration { return 5 * time.Millisecond },
		RunOnStart:             func() bool { return false },
		RunInMaintenanceWindow: func() bool { return false },
	})

	shutdown := make(chan struct{})
	var wg sync.WaitGroup
	ts.Start(shutdown, &wg)
	defer func() {
		close(shutdown)
		wg.Wait()
	}()

	deadline := time.After(3 * time.Second)
	for {
		if fired.Load() > 0 {
			return
		}
		select {
		case <-deadline:
			t.Fatal("Start never fired the ticker for an enabled task with a non-zero interval")
		case <-time.After(time.Millisecond):
		}
	}
}

// TestStartCreatesNoTickerForZeroInterval is the paired negative: a task with
// interval 0 stays manual-only. This is the behaviour every other task in
// tasks.go relies on, so the library_scan fix must not have loosened it.
func TestStartCreatesNoTickerForZeroInterval(t *testing.T) {
	restore := config.Snapshot()
	t.Cleanup(func() { config.AppConfig = restore })
	config.AppConfig.Maintenance.Enabled = false

	ts := &TaskScheduler{
		deps:    testDeps(),
		tasks:   make(map[string]*TaskDefinition),
		lastRun: make(map[string]time.Time),
	}

	var fired atomic.Int32
	ts.RegisterTask(TaskDefinition{
		Name:     "manual_only_probe",
		Category: "test",
		TriggerFn: func(string) (*database.Operation, error) {
			fired.Add(1)
			return nil, nil
		},
		IsEnabled:              func() bool { return true },
		GetInterval:            func() time.Duration { return 0 },
		RunOnStart:             func() bool { return false },
		RunInMaintenanceWindow: func() bool { return false },
	})

	shutdown := make(chan struct{})
	var wg sync.WaitGroup
	ts.Start(shutdown, &wg)
	time.Sleep(50 * time.Millisecond)
	close(shutdown)
	wg.Wait()

	assert.Equal(t, int32(0), fired.Load(), "a zero-interval task must never be ticked")
}
