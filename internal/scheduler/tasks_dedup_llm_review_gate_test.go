// file: internal/scheduler/tasks_dedup_llm_review_gate_test.go
// version: 1.0.0
// guid: 6f1c2a9e-3d4b-4e8a-9c71-2b5e8d0f4a13
// last-edited: 2026-10-15

package scheduler

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// dedup.llm-review is registered by the dedup plugin, which needs the
// embedding store as well as the engine. The task must stay inert when the
// engine is wired but the plugin was never built.
func TestDedupLLMReviewNeedsTheDedupPlugin(t *testing.T) {
	deps := testDeps()
	deps.HasDedupEngine = func() bool { return true }
	deps.HasDedupPlugin = nil
	def, ok := NewTaskScheduler(deps).GetTask("dedup_llm_review")
	assert.True(t, ok)
	assert.False(t, def.IsEnabled(), "an unwired HasDedupPlugin must read as absent")

	deps.HasDedupPlugin = func() bool { return false }
	def, _ = NewTaskScheduler(deps).GetTask("dedup_llm_review")
	assert.False(t, def.IsEnabled(), "engine without the embedding store (no plugin) must be inert")

	deps.HasDedupPlugin = func() bool { return true }
	def, _ = NewTaskScheduler(deps).GetTask("dedup_llm_review")
	assert.True(t, def.IsEnabled())
}
