// file: internal/scheduler/acoustid_backfill_task_test.go
// version: 1.0.0
// guid: 971b1c48-1a95-46b2-967f-21acd54eb8ec
// last-edited: 2026-09-12

package scheduler

import (
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAcoustIDBackfillTask_RegisteredAndDisabledByDefault pins the scheduled
// home of acoustid.backfill. Its op def used to carry a cron Schedule that
// nothing evaluates, so the backfill never ran unattended; the task below is
// the real schedule and must ship OFF on every automatic path. The enqueue
// itself is covered by TestScheduledTasksDoNotWriteOrphanLegacyRows, which
// iterates taskV2DefIDs.
func TestAcoustIDBackfillTask_RegisteredAndDisabledByDefault(t *testing.T) {
	restore := config.Snapshot()
	t.Cleanup(func() { config.AppConfig = restore })
	config.ResetToDefaults()

	ts := NewTaskScheduler(testDeps())
	task, ok := ts.GetTask("acoustid_backfill")
	require.True(t, ok, "acoustid_backfill must be a registered TaskScheduler task")

	assert.False(t, task.IsEnabled(), "must ship disabled: library-wide fingerprinting is an owner decision")
	assert.False(t, task.RunOnStart())
	assert.False(t, task.RunInMaintenanceWindow())
	assert.False(t, ts.inMaintenanceOrder("acoustid_backfill"))
	assert.Equal(t, 24*time.Hour, task.GetInterval())
	assert.Equal(t, "acoustid.backfill", taskV2DefIDs["acoustid_backfill"],
		"isTaskRunning reads this map; a missing key answers 'not running' silently")

	config.AppConfig.Scheduled.AcoustIDBackfill.Enabled = true
	assert.True(t, task.IsEnabled(), "the config key must actually turn it on")
}
