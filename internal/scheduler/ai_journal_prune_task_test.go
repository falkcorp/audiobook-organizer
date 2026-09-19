// file: internal/scheduler/ai_journal_prune_task_test.go
// version: 1.0.0
// guid: 2b599be4-5f4a-43b0-923b-4f443d3a4e78
// last-edited: 2026-09-19

package scheduler

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
)

func TestAIJournalPruneTask_RegisteredInWindowWithRetention(t *testing.T) {
	restore := config.Snapshot()
	t.Cleanup(func() { config.AppConfig = restore })
	config.ResetToDefaults()

	assert.Equal(t, 30, config.AppConfig.AIJournalRetentionDays, "default retention is 30 days")

	ts := NewTaskScheduler(testDeps())
	task, ok := ts.GetTask("ai_journal_prune")
	require.True(t, ok, "ai_journal_prune must be a registered TaskScheduler task")
	assert.True(t, task.IsEnabled())
	assert.True(t, task.RunInMaintenanceWindow())
	assert.True(t, ts.inMaintenanceOrder("ai_journal_prune"))
	assert.False(t, task.RunOnStart())
	assert.Equal(t, 24*time.Hour, task.GetInterval())
	assert.Equal(t, "maintenance.prune-ai-journal", taskV2DefIDs["ai_journal_prune"],
		"isTaskRunning reads this map; a missing key answers 'not running' silently")

	config.AppConfig.AIJournalRetentionDays = 0
	assert.False(t, task.IsEnabled(), "retention 0 keeps entries forever: the task must not run")
}
