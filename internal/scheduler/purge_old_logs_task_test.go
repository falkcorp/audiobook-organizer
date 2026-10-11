// file: internal/scheduler/purge_old_logs_task_test.go
// version: 1.0.0
// guid: 8c4a2f61-3d9e-4b07-a5c2-e1f7b9d03a48
// last-edited: 2026-10-11

package scheduler

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
)

// log_retention_days was never read by InitConfig, so every install ran with
// 0 and this task's IsEnabled (then "> 0") was false everywhere: logs were
// never purged. 0 now means the default (90); only -1 turns the purge off.
// ResetToDefaults still leaves the field 0, which is exactly the zero-value
// path this has to survive.
func TestPurgeOldLogsTask_ZeroMeansDefaultOnlyNegativeDisables(t *testing.T) {
	restore := config.Snapshot()
	t.Cleanup(func() { config.AppConfig = restore })
	config.ResetToDefaults()

	ts := NewTaskScheduler(testDeps())
	task, ok := ts.GetTask("purge_old_logs")
	require.True(t, ok, "purge_old_logs must be a registered TaskScheduler task")

	config.AppConfig.LogRetentionDays = 0
	assert.True(t, task.IsEnabled(), "0 means the default 90 days, not keep-forever")

	config.AppConfig.LogRetentionDays = 30
	assert.True(t, task.IsEnabled())

	config.AppConfig.LogRetentionDays = config.LogRetentionKeepForever
	assert.False(t, task.IsEnabled(), "-1 keeps logs forever: the task must not run")
}
