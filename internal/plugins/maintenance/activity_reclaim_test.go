// file: internal/plugins/maintenance/activity_reclaim_test.go
// version: 1.1.0
// guid: 9e73f4a1-08c5-4d62-b91e-3a7d05f8c264
// last-edited: 2026-09-08

// maintenance.activity-reclaim deletes production data, so the property that
// matters most is what happens when it is triggered with NO parameters at all —
// which is how a human clicking "run" in the operations UI triggers it.
//
// Go's zero value for bool is false, so a `DryRun bool` field would make the
// no-params case mean "apply". That is why the param is a *bool: absent has to
// be distinguishable from an explicit false, and absent must mean dry run.
package maintenance

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// reclaimDeps records what the op asked for and returns a scripted result.
type reclaimDeps struct {
	fakeDeps
	gotDryRun bool
	gotRetain time.Duration
	called    bool
	result    database.ActivityReclaimResult
}

func (d *reclaimDeps) ReclaimMigratedActivity(_ context.Context, retain time.Duration, dryRun bool,
	onTier database.ActivityReclaimProgress,
) (database.ActivityReclaimResult, error) {
	d.called = true
	d.gotDryRun = dryRun
	d.gotRetain = retain
	if onTier != nil {
		onTier(database.ReclaimPhaseCensus, "change", 1, 7,
			database.ActivityReclaimTier{Eligible: d.result.EligibleRows})
		onTier(database.ReclaimPhasePrune, "change", 1, 7,
			database.ActivityReclaimTier{Deleted: d.result.RowsDeleted})
	}
	return d.result, nil
}

func newReclaimPlugin(t *testing.T, deps *reclaimDeps) *Plugin {
	t.Helper()
	p := New(deps)
	require.NotNil(t, p)
	return p
}

func TestActivityReclaim_DefaultsToDryRunWhenTriggeredWithNoParams(t *testing.T) {
	// THE safety test. Triggering from the UI with no body must never delete.
	for _, params := range []json.RawMessage{nil, json.RawMessage(`{}`)} {
		deps := &reclaimDeps{}
		rep := &fakeReporter{}
		require.NoError(t, newReclaimPlugin(t, deps).runActivityReclaim(context.Background(), params, rep))

		require.True(t, deps.called)
		assert.True(t, deps.gotDryRun,
			"absent dry_run must mean DRY RUN — a plain bool field would make this false and delete")
	}
}

func TestActivityReclaim_AppliesOnlyOnExplicitFalse(t *testing.T) {
	deps := &reclaimDeps{result: database.ActivityReclaimResult{RowsDeleted: 12}}
	rep := &fakeReporter{}
	require.NoError(t, newReclaimPlugin(t, deps).runActivityReclaim(
		context.Background(), json.RawMessage(`{"dry_run":false}`), rep))

	assert.False(t, deps.gotDryRun, "an explicit dry_run=false is the only way to delete")

	// And the operator is told that deleting keys has not yet freed any bytes.
	joined := strings.Join(rep.logs, "\n")
	assert.Contains(t, joined, "db-optimize",
		"the report must name the compaction that actually releases the space")
}

func TestActivityReclaim_HonoursRetainHours(t *testing.T) {
	deps := &reclaimDeps{}
	rep := &fakeReporter{}
	require.NoError(t, newReclaimPlugin(t, deps).runActivityReclaim(
		context.Background(), json.RawMessage(`{"retain_hours":96}`), rep))
	assert.Equal(t, 96*time.Hour, deps.gotRetain)
}

func TestActivityReclaim_ReportsTheRefusalReason(t *testing.T) {
	// A blocked run has to say what is blocking it. On production today
	// read_secondary is false, so this is the path the op actually takes.
	deps := &reclaimDeps{result: database.ActivityReclaimResult{
		Refused:       true,
		RefusedReason: "reads are still served from Pebble — the SQLite cutover has not completed",
		PrimaryRows:   4_000_000,
		SecondaryRows: 1_800_000,
	}}
	rep := &fakeReporter{}
	require.NoError(t, newReclaimPlugin(t, deps).runActivityReclaim(context.Background(), nil, rep))

	joined := strings.Join(rep.logs, "\n")
	assert.Contains(t, joined, "still served from Pebble", "the refusal reason must reach the operator")
	assert.Contains(t, joined, "4000000", "the census must be reported even on a refusal")
	assert.Contains(t, joined, "1800000")
}

func TestActivityReclaim_RejectsMalformedParams(t *testing.T) {
	deps := &reclaimDeps{}
	err := newReclaimPlugin(t, deps).runActivityReclaim(
		context.Background(), json.RawMessage(`{"dry_run":`), &fakeReporter{})
	require.Error(t, err)
	assert.False(t, deps.called, "a parse failure must not reach the delete path")
}

func TestActivityReclaimDef_DeclaresARealProgressBudget(t *testing.T) {
	// Prune is one blocking call per tier with no callback, so LivenessManual
	// would invite the same 5m stuck-strike that killed maintenance.db-optimize
	// every week. LivenessNone requires an explicit budget; assert it is set.
	def := newReclaimPlugin(t, &reclaimDeps{}).activityReclaimDef()
	assert.Positive(t, def.ProgressTimeout,
		"LivenessNone without a ProgressTimeout is an op with no liveness contract at all")
	assert.Nil(t, def.Schedule,
		"this deletes data on a safety flag an operator should be watching; it is triggered, not scheduled")
}
