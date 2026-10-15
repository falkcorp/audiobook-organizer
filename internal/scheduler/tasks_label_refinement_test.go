// file: internal/scheduler/tasks_label_refinement_test.go
// version: 1.1.1
// guid: 3f1a6c92-8d47-4b0e-9a25-1c7e6b4d0f83
// last-edited: 2026-10-15

package scheduler

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/stretchr/testify/assert"
)

// TestLabelRefinementRegistered verifies the built-in-disabled scheduled chain
// is registered (INIT-1 T6).
func TestLabelRefinementRegistered(t *testing.T) {
	ts := NewTaskScheduler(testDeps())
	def, ok := ts.GetTask("label_refinement")
	assert.True(t, ok, "label_refinement task should be registered")
	assert.Equal(t, "label_refinement", def.Name)
	assert.Equal(t, "maintenance", def.Category)
}

// TestLabelRefinementDisabledByDefault verifies the task is inert under the
// shipped default (scheduled.label_refinement.enabled=false). Provably-inert is
// the CRITICAL requirement for this task: it must never schedule or run on
// startup unless an owner opts in.
func TestLabelRefinementDisabledByDefault(t *testing.T) {
	// Default/zero-value config ⇒ Enabled=false, OnStartup=false, Interval=0.
	config.AppConfig.Scheduled.LabelRefinement = config.ScheduledTaskConfig{}

	ts := NewTaskScheduler(testDeps())
	def, ok := ts.GetTask("label_refinement")
	assert.True(t, ok)
	assert.False(t, def.IsEnabled(), "label_refinement must be disabled by default")
	assert.False(t, def.RunOnStart(), "label_refinement must not run on startup by default")
	assert.Zero(t, def.GetInterval(), "disabled task must have zero interval (never scheduled)")
	// Not part of the maintenance window, and absent from maintenanceOrder — the
	// only two paths that could otherwise run it despite IsEnabled()==false.
	assert.False(t, def.RunInMaintenanceWindow(), "label_refinement must not run in the maintenance window")
	assert.NotContains(t, ts.maintenanceOrder, "label_refinement",
		"label_refinement must not be in maintenanceOrder (keeps it inert on the maintenance path)")
}

// TestLabelRefinementIntervalGuard verifies a zero/negative interval never
// yields a positive duration (so an enabled task with Interval<=0 cannot
// busy-loop the ticker), and that a positive interval maps to minutes.
func TestLabelRefinementIntervalGuard(t *testing.T) {
	ts := NewTaskScheduler(testDeps())
	def, ok := ts.GetTask("label_refinement")
	assert.True(t, ok)

	config.AppConfig.Scheduled.LabelRefinement.Interval = 0
	assert.Zero(t, def.GetInterval(), "interval 0 ⇒ manual only, never scheduled")
	config.AppConfig.Scheduled.LabelRefinement.Interval = -5
	assert.Zero(t, def.GetInterval(), "negative interval ⇒ zero duration (no busy-loop)")
	config.AppConfig.Scheduled.LabelRefinement.Interval = 10080
	assert.Equal(t, int64(10080*60), int64(def.GetInterval().Seconds()), "positive interval maps to minutes")

	config.AppConfig.Scheduled.LabelRefinement = config.ScheduledTaskConfig{} // reset
}

// TestLabelRefinementEnqueuesTheParentOp pins the 04-P9 contract: the task is
// one enqueue of the dedup.label-refinement parent, and taskV2DefIDs names it so
// isTaskRunning sees the run. The enqueue itself (one v2 row, no legacy row,
// returned id matches) is exercised for every taskV2DefIDs entry by
// TestScheduledTasksDoNotWriteOrphanLegacyRows. The children's empty-params
// (report-only) guarantee moved to the plugin: see
// TestLabelRefinementChildrenGetEmptyParams in internal/plugins/dedup.
func TestLabelRefinementEnqueuesTheParentOp(t *testing.T) {
	assert.Equal(t, "dedup.label-refinement", taskV2DefIDs["label_refinement"])
}

// TestLabelRefinementNeedsTheDedupPlugin: the parent def is registered only when
// the dedup plugin is built (engine AND embedding store), so an enabled task
// without it must stay inert rather than enqueue an unregistered def id. The
// engine alone is not enough, and an unwired dep counts as absent.
func TestLabelRefinementNeedsTheDedupPlugin(t *testing.T) {
	prev := config.AppConfig.Scheduled.LabelRefinement
	t.Cleanup(func() { config.AppConfig.Scheduled.LabelRefinement = prev })
	config.AppConfig.Scheduled.LabelRefinement = config.ScheduledTaskConfig{Enabled: true, Interval: 60}

	deps := testDeps()
	deps.HasDedupEngine = func() bool { return true }
	deps.HasDedupPlugin = nil
	def, ok := NewTaskScheduler(deps).GetTask("label_refinement")
	assert.True(t, ok)
	assert.False(t, def.IsEnabled(), "an unwired HasDedupPlugin must read as absent")

	deps.HasDedupPlugin = func() bool { return false }
	def, _ = NewTaskScheduler(deps).GetTask("label_refinement")
	assert.False(t, def.IsEnabled(), "engine without the embedding store (no plugin) must be inert")

	deps.HasDedupPlugin = func() bool { return true }
	def, _ = NewTaskScheduler(deps).GetTask("label_refinement")
	assert.True(t, def.IsEnabled())
}
