// file: internal/scheduler/tasks_label_refinement_test.go
// version: 1.0.0
// guid: 3f1a6c92-8d47-4b0e-9a25-1c7e6b4d0f83
// last-edited: 2026-07-12

package scheduler

import (
	"encoding/json"
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

// TestLabelRefinementChainPassesNoApply asserts the params the scheduled chain
// passes to BOTH dedup ops carry no "apply" key — the entire "the scheduled
// loop never applies" guarantee.
//
// "No apply key" equals "dry-run" ONLY because both ops default Apply=false when
// the key is absent. That default is pinned by the companion canaries
// TestRebuildGoldLabelsParamsDefaultNoApply (rebuild_gold_labels_test.go) and
// TestCalibrateCompositeParamsDefaultNoApply (calibrate_composite_test.go): if a
// future edit flips either default, those canaries fail and this empty-params
// chain is caught before it silently becomes a timed prod mutation.
//
// These are the exact param values runLabelRefinementChain passes to EnqueueOp.
func TestLabelRefinementChainPassesNoApply(t *testing.T) {
	rebuild, err := json.Marshal(labelRefinementRebuildParams{})
	assert.NoError(t, err)
	assert.JSONEq(t, "{}", string(rebuild), "rebuild params must be empty (no apply key)")
	assert.NotContains(t, string(rebuild), "apply")

	calibrate, err := json.Marshal(labelRefinementCalibrateParams{})
	assert.NoError(t, err)
	assert.JSONEq(t, "{}", string(calibrate), "calibrate params must be empty (no apply key)")
	assert.NotContains(t, string(calibrate), "apply")
}
