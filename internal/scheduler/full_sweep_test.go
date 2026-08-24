// file: internal/scheduler/full_sweep_test.go
// version: 1.0.0
// guid: a8eab374-460f-4ba3-816a-4e5d365ca8f6
// last-edited: 2026-08-24

package scheduler

import (
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testWeek = 168 * time.Hour

// TestFullSweepDue covers the due-check guard, including the deliberate
// never-run behaviour: a zero timestamp is NOT due, so the first tick after a
// deploy seeds the clock instead of starting a multi-hour force_update re-hash.
func TestFullSweepDue(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		name   string
		last   time.Time
		period time.Duration
		want   bool
	}{
		{"never run is not due", time.Time{}, testWeek, false},
		{"never run stays not due even with a tiny period", time.Time{}, time.Nanosecond, false},
		{"zero period is never due", now.Add(-testWeek * 10), 0, false},
		{"negative period is never due", now.Add(-testWeek * 10), -testWeek, false},
		{"just swept is not due", now, testWeek, false},
		{"one hour short is not due", now.Add(-testWeek + time.Hour), testWeek, false},
		{"exactly a period is due", now.Add(-testWeek), testWeek, true},
		{"long overdue is due", now.Add(-testWeek * 3), testWeek, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, fullSweepDue(tc.last, now, tc.period))
		})
	}
}

// TestLibraryScanFullIsScheduledUnderDefaultConfig asserts the exact gate
// Start uses (IsEnabled && GetInterval > 0) against the shipped defaults.
func TestLibraryScanFullIsScheduledUnderDefaultConfig(t *testing.T) {
	restore := config.Snapshot()
	t.Cleanup(func() { config.AppConfig = restore })
	config.ResetToDefaults()

	ts := NewTaskScheduler(testDeps())
	task, ok := ts.GetTask("library_scan_full")
	require.True(t, ok, "library_scan_full should be registered")

	assert.True(t, task.IsEnabled(), "the weekly sweep must be enabled under default config")
	assert.Greater(t, task.GetInterval(), time.Duration(0),
		"a zero interval means Start never creates a ticker and the sweep can never run")
}

// TestLibraryScanFullTickerIsNotTheSweepPeriod is the regression test for the
// bug this task was built around.
//
// scheduler.Start drives tasks from an in-memory time.NewTicker with no
// persisted last-run state, so an interval longer than the process's uptime
// NEVER FIRES -- while logging the same healthy "Scheduled task interval" line
// as a task that works. Production recorded 146 service starts in the 30 days
// to 2026-08-24, a mean uptime near 5 hours; a 168h ticker would have been
// reset every time and would have fired exactly zero sweeps.
//
// So GetInterval must stay a short DUE-CHECK cadence and must never be
// "simplified" into returning the sweep period. If someone makes GetInterval
// return PeriodHours, this fails.
func TestLibraryScanFullTickerIsNotTheSweepPeriod(t *testing.T) {
	restore := config.Snapshot()
	t.Cleanup(func() { config.AppConfig = restore })
	config.ResetToDefaults()

	period := time.Duration(config.AppConfig.Scheduled.LibraryScanFull.PeriodHours) * time.Hour
	require.Equal(t, testWeek, period, "default sweep period should be weekly (168h)")

	ts := NewTaskScheduler(testDeps())
	task, ok := ts.GetTask("library_scan_full")
	require.True(t, ok)

	interval := task.GetInterval()
	assert.NotEqual(t, period, interval,
		"GetInterval must be the due-check cadence, not the sweep period: an in-memory ticker "+
			"longer than process uptime never fires")
	assert.LessOrEqual(t, interval, time.Hour,
		"the due-check cadence must stay far below any realistic process uptime")
}

// TestLibraryScanFullIntervalIsConfigurable proves both knobs are read from
// config rather than hard-coded, and that disabling the task stops the ticker.
func TestLibraryScanFullIntervalIsConfigurable(t *testing.T) {
	restore := config.Snapshot()
	t.Cleanup(func() { config.AppConfig = restore })
	config.ResetToDefaults()

	ts := NewTaskScheduler(testDeps())
	task, ok := ts.GetTask("library_scan_full")
	require.True(t, ok)

	config.AppConfig.Scheduled.LibraryScanFull.Interval = 15
	assert.Equal(t, 15*time.Minute, task.GetInterval())

	config.AppConfig.Scheduled.LibraryScanFull.Interval = 0
	assert.Equal(t, time.Duration(0), task.GetInterval(), "a zero interval must disable the ticker")

	config.AppConfig.Scheduled.LibraryScanFull.Interval = 60
	config.AppConfig.Scheduled.LibraryScanFull.Enabled = false
	assert.False(t, task.IsEnabled())
	assert.Equal(t, time.Duration(0), task.GetInterval(),
		"a disabled sweep must report no interval even when one is configured")
}
