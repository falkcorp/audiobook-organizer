// file: internal/scheduler/full_sweep_test.go
// version: 1.2.0
// guid: a8eab374-460f-4ba3-816a-4e5d365ca8f6
// last-edited: 2026-08-24

package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	dbmocks "github.com/falkcorp/audiobook-organizer/internal/database/mocks"
	"github.com/falkcorp/audiobook-organizer/internal/operations"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
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

// fullSweepHarness wires a mock store and a registry holding a stub
// library.scan def, and returns the rows the task actually enqueued.
func fullSweepHarness(t *testing.T, store *dbmocks.MockStore) (*TaskScheduler, *[]database.OperationV2Row) {
	t.Helper()

	inserted := &[]database.OperationV2Row{}
	store.EXPECT().UpsertOpDefinitionV2(mock.Anything).Return(nil).Maybe()
	store.EXPECT().InsertOperationV2(mock.Anything).
		RunAndReturn(func(row database.OperationV2Row) error {
			*inserted = append(*inserted, row)
			return nil
		}).Maybe()
	store.EXPECT().GetOperationV2(mock.Anything).Return(nil, nil).Maybe()
	store.EXPECT().ListActiveOperationsV2().Return(nil, nil).Maybe()
	store.EXPECT().GetDepRev(mock.Anything).Return(0, nil).Maybe()

	reg := opsregistry.New(store, slog.New(slog.DiscardHandler), 1, nil)
	require.NoError(t, reg.RegisterOp(opsregistry.OperationDef{
		ID:           "library.scan",
		Plugin:       "test",
		ResumePolicy: opsregistry.ResumeDrop,
		Liveness:     opsregistry.LivenessManual,
		Run: func(context.Context, json.RawMessage, opsregistry.Reporter) error {
			return nil
		},
	}))

	deps := testDeps()
	deps.Store = func() SchedulerStore { return store }
	deps.OpRegistry = reg
	return NewTaskScheduler(deps), inserted
}

func pastSweepSetting(age time.Duration) *database.Setting {
	return &database.Setting{
		Key:   fullSweepLastRunSetting,
		Value: time.Now().Add(-age).Format(time.RFC3339),
		Type:  "string",
	}
}

// TestFullSweepEnqueuesForceUpdateParams pins the params the sweep actually
// sends. This is load-bearing twice over: force_update is what makes it a full
// re-read at all, and the params being UNEQUAL to the incremental's {} is the
// only reason EnqueueOp queues a second row instead of collapsing the sweep
// into a running incremental scan. Drop include_root_dir and the params stay
// distinct -- so no other test fails -- while the sweep quietly stops covering
// the organized library root.
func TestFullSweepEnqueuesForceUpdateParams(t *testing.T) {
	restore := config.Snapshot()
	t.Cleanup(func() { config.AppConfig = restore })
	config.ResetToDefaults()

	store := dbmocks.NewMockStore(t)
	store.EXPECT().GetSetting(fullSweepLastRunSetting).Return(pastSweepSetting(2*testWeek), nil).Maybe()
	store.EXPECT().SetSetting(mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil).Maybe()

	ts, inserted := fullSweepHarness(t, store)
	task, ok := ts.GetTask("library_scan_full")
	require.True(t, ok)

	op, err := task.TriggerFn(operations.TriggerScheduled)
	require.NoError(t, err)
	require.NotNil(t, op, "an overdue sweep must enqueue")

	require.Len(t, *inserted, 1)
	assert.JSONEq(t, `{"force_update":true,"include_root_dir":true}`, string((*inserted)[0].Params),
		"the sweep must force a full re-read AND include the organized root")
}

// TestFullSweepStoreErrorDoesNotReseed is the regression test for the worst
// failure this task could have: an unreadable settings store was reported as
// "never run", so the caller wrote a fresh timestamp and pushed the sweep out
// another full period. A ~1-in-168 read-failure rate was then enough to stop
// the weekly sweep from EVER firing, and the log line blamed a first run.
//
// The assertion is the ABSENCE of a write: no SetSetting expectation is
// registered, so mockery fails the test if the task re-seeds.
func TestFullSweepStoreErrorDoesNotReseed(t *testing.T) {
	restore := config.Snapshot()
	t.Cleanup(func() { config.AppConfig = restore })
	config.ResetToDefaults()

	store := dbmocks.NewMockStore(t)
	store.EXPECT().GetSetting(fullSweepLastRunSetting).
		Return(nil, errors.New("pebble: temporarily unavailable")).Maybe()

	ts, inserted := fullSweepHarness(t, store)
	task, ok := ts.GetTask("library_scan_full")
	require.True(t, ok)

	op, err := task.TriggerFn(operations.TriggerScheduled)
	require.NoError(t, err, "a store hiccup must not fail the tick")
	assert.Nil(t, op, "a store error must not enqueue a sweep")
	assert.Empty(t, *inserted)
}

// TestFullSweepManualTriggerBypassesDueCheck pins that pressing Run actually
// runs. Applying the weekly gate to a manual trigger made the button a no-op
// that still answered 202 "task triggered" on any day but the due one, and left
// no way at all to force a full sweep.
func TestFullSweepManualTriggerBypassesDueCheck(t *testing.T) {
	restore := config.Snapshot()
	t.Cleanup(func() { config.AppConfig = restore })
	config.ResetToDefaults()

	store := dbmocks.NewMockStore(t)
	// Swept one minute ago: nowhere near due.
	store.EXPECT().GetSetting(fullSweepLastRunSetting).Return(pastSweepSetting(time.Minute), nil).Maybe()
	store.EXPECT().SetSetting(mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil).Maybe()

	ts, inserted := fullSweepHarness(t, store)
	task, ok := ts.GetTask("library_scan_full")
	require.True(t, ok)

	scheduled, err := task.TriggerFn(operations.TriggerScheduled)
	require.NoError(t, err)
	require.Nil(t, scheduled, "a scheduled tick must respect the period")
	require.Empty(t, *inserted)

	manual, err := task.TriggerFn(operations.TriggerManual)
	require.NoError(t, err)
	require.NotNil(t, manual, "a manual trigger must run regardless of the period")
	require.Len(t, *inserted, 1)
}

// TestFullSweepSeedsOnErrSettingNotFound is the regression test for a fix that
// was briefly WORSE than the bug it replaced.
//
// Separating "store error" from "never run" is correct, but PebbleStore.
// GetSetting reports a MISSING KEY as a wrapped ErrSettingNotFound rather than
// (nil, nil). Treating every non-nil error as a backend failure therefore made
// the normal first-boot path decline WITHOUT seeding, so the clock was never
// written and the sweep could never become due -- permanently, silently.
//
// The mock returns (nil, nil) for a missing key, which production never does,
// so this asserts against the real error shape on purpose.
func TestFullSweepSeedsOnErrSettingNotFound(t *testing.T) {
	restore := config.Snapshot()
	t.Cleanup(func() { config.AppConfig = restore })
	config.ResetToDefaults()

	store := dbmocks.NewMockStore(t)
	store.EXPECT().GetSetting(fullSweepLastRunSetting).
		Return(nil, fmt.Errorf("setting not found: %s: %w", fullSweepLastRunSetting, database.ErrSettingNotFound)).
		Maybe()

	var seeded []string
	store.EXPECT().SetSetting(mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		RunAndReturn(func(k, v, typ string, secret bool) error {
			seeded = append(seeded, k)
			return nil
		}).Maybe()

	ts, inserted := fullSweepHarness(t, store)
	task, ok := ts.GetTask("library_scan_full")
	require.True(t, ok)

	op, err := task.TriggerFn(operations.TriggerScheduled)
	require.NoError(t, err)
	assert.Nil(t, op, "a first tick seeds the clock rather than sweeping")
	assert.Empty(t, *inserted)
	assert.Equal(t, []string{fullSweepLastRunSetting}, seeded,
		"a missing key must SEED the clock — declining without seeding wedges the schedule forever")
}

// TestFullSweepRefusesToEnqueueWhenTheClockCannotBePersisted pins the
// fail-closed ordering.
//
// Enqueue-then-stamp fails OPEN. Once a sweep COMPLETES it is in neither
// ListActiveOperationsV2 nor the ConcurrencyKey's active set, so nothing
// dedupes the next due-check against it: a swallowed stamp failure means a
// whole-library force_update re-hash running back to back forever. Skipping one
// sweep is the cheaper failure, so the stamp happens first and aborts.
func TestFullSweepRefusesToEnqueueWhenTheClockCannotBePersisted(t *testing.T) {
	restore := config.Snapshot()
	t.Cleanup(func() { config.AppConfig = restore })
	config.ResetToDefaults()

	store := dbmocks.NewMockStore(t)
	store.EXPECT().GetSetting(fullSweepLastRunSetting).Return(pastSweepSetting(2*testWeek), nil).Maybe()
	store.EXPECT().SetSetting(mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return(errors.New("pebble: read-only")).Maybe()

	ts, inserted := fullSweepHarness(t, store)
	task, ok := ts.GetTask("library_scan_full")
	require.True(t, ok)

	op, err := task.TriggerFn(operations.TriggerScheduled)
	require.Error(t, err, "an unpersistable clock must surface, not warn")
	assert.Contains(t, err.Error(), "re-enqueue")
	assert.Nil(t, op)
	assert.Empty(t, *inserted, "no sweep may be enqueued when its clock cannot be recorded")
}
