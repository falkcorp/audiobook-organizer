// file: internal/scheduler/interval_clock_test.go
// version: 1.0.0
// guid: c41a7b98-3e26-4d05-9f7a-2b8e6d015c3f
// last-edited: 2026-09-09

package scheduler

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	dbmocks "github.com/falkcorp/audiobook-organizer/internal/database/mocks"
)

// settingsBackedScheduler returns a TaskScheduler whose store keeps settings in
// a real map, so a clock written by one scheduler is visible to another. That
// second scheduler is the point: it stands in for the process restart that the
// old time.NewTicker could not survive.
func settingsBackedScheduler(t *testing.T, settings map[string]string) *TaskScheduler {
	t.Helper()
	store := dbmocks.NewMockStore(t)
	store.EXPECT().GetSetting(mock.Anything).RunAndReturn(func(key string) (*database.Setting, error) {
		v, ok := settings[key]
		if !ok {
			// Production's PebbleStore reports a missing key as
			// ErrSettingNotFound, not as (nil, nil). The fake reproduces that,
			// because loadIntervalLastTick tells a missing key from a backend
			// failure by that error type and a (nil, nil) fake would let a
			// regression in that branch pass.
			return nil, database.ErrSettingNotFound
		}
		return &database.Setting{Key: key, Value: v, Type: "string"}, nil
	}).Maybe()
	store.EXPECT().SetSetting(mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		RunAndReturn(func(key, value, _ string, _ bool) error {
			settings[key] = value
			return nil
		}).Maybe()

	deps := testDeps()
	deps.Store = func() SchedulerStore { return store }
	return NewTaskScheduler(deps)
}

// TestIntervalClock_SurvivesProcessRestart is the regression test for the defect
// that stopped activity-log compaction.
//
// cleanup_activity_log has a 24h interval and was driven by a time.NewTicker
// created at Start(). A ticker measures from process start, so on a host whose
// service lifetime is measured in tens of minutes the deadline was reset before
// it could ever elapse. Measured on production 2026-09-09: consecutive
// lifetimes of 1h02m and 37m, and the task fired twice in 30 days — on the only
// two days uptime passed 24 hours.
//
// The durable clock must therefore be readable by a DIFFERENT TaskScheduler
// instance than the one that wrote it, which is what this asserts.
func TestIntervalClock_SurvivesProcessRestart(t *testing.T) {
	settings := map[string]string{}

	first := settingsBackedScheduler(t, settings)
	_, ok := first.loadIntervalLastTick("cleanup_activity_log")
	require.False(t, ok, "a task with no stored clock must report 'not stored' so the caller seeds it")

	stamped := time.Now().Add(-25 * time.Hour)
	first.stampIntervalTick("cleanup_activity_log", stamped)

	// A brand new scheduler — the restarted process.
	second := settingsBackedScheduler(t, settings)
	got, ok := second.loadIntervalLastTick("cleanup_activity_log")
	require.True(t, ok, "the clock must survive the restart; this is the whole point")
	assert.WithinDuration(t, stamped, got, time.Second)
	assert.Greater(t, time.Since(got), 24*time.Hour,
		"a clock stamped 25h ago must read as DUE for a 24h interval after a restart")
}

// TestIntervalClock_SeedIsPersisted pins the failure mode that would silently
// reintroduce the original bug. If seeding did not persist, every boot would
// re-seed from zero and the task would never become due — identical symptom,
// no error, no log line.
func TestIntervalClock_SeedIsPersisted(t *testing.T) {
	settings := map[string]string{}
	ts := settingsBackedScheduler(t, settings)

	ts.stampIntervalTick("optimize_activity_db", time.Now())

	_, stored := settings[intervalLastTickKey("optimize_activity_db")]
	assert.True(t, stored, "the seed must be written to settings, not just held in memory")
}

// TestIntervalClock_UnparseableValueReSeeds covers the wedge case: a garbage
// timestamp must be treated as absent so the schedule recovers, rather than
// leaving the task permanently un-due.
func TestIntervalClock_UnparseableValueReSeeds(t *testing.T) {
	settings := map[string]string{
		intervalLastTickKey("cleanup_activity_log"): "not-a-timestamp",
	}
	ts := settingsBackedScheduler(t, settings)

	_, ok := ts.loadIntervalLastTick("cleanup_activity_log")
	assert.False(t, ok, "unparseable stored value must read as 'not stored' so the caller re-seeds")
}

// TestIntervalPollFor keeps the poll cadence from silently stretching a
// sub-minute interval out to a minute.
func TestIntervalPollFor(t *testing.T) {
	assert.Equal(t, intervalPollInterval, intervalPollFor(24*time.Hour))
	assert.Equal(t, intervalPollInterval, intervalPollFor(intervalPollInterval))
	assert.Equal(t, 10*time.Second, intervalPollFor(10*time.Second))
}

// TestCleanupActivityLogIsReachable pins that the activity-log jobs are wired to
// something that can actually run them.
//
// OperationDef.Schedule is decorative: registry.upsertDefToDB copies it into the
// ScheduleCron column of op_definitions_v2 and NOTHING reads that column back —
// there is no cron library in the module. A def that declares a Schedule and has
// no TaskScheduler task does not run, and says nothing about it. These two are
// the activity log's whole retention story, so they get an explicit assertion
// rather than relying on the generic reachability sweep.
func TestCleanupActivityLogIsReachable(t *testing.T) {
	ts := settingsBackedScheduler(t, map[string]string{})
	for _, name := range []string{"cleanup_activity_log", "optimize_activity_db"} {
		task, ok := ts.tasks[name]
		require.True(t, ok, "%s must be a registered TaskScheduler task", name)
		assert.Positive(t, task.GetInterval(), "%s needs an interval or it can only run in the window", name)
		assert.True(t, ts.inMaintenanceOrder(name), "%s must be in maintenanceOrder", name)
		assert.Equal(t, "maintenance."+map[string]string{
			"cleanup_activity_log": "cleanup-activity-log",
			"optimize_activity_db": "optimize-activity-db",
		}[name], taskV2DefIDs[name], "%s must map to the def it enqueues", name)
	}
}
