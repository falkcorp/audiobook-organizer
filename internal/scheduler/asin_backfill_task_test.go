// file: internal/scheduler/asin_backfill_task_test.go
// version: 1.0.0
// guid: 6e1c9a47-3b0d-4f82-a5e9-2d7c8b41f036
// last-edited: 2026-10-02

package scheduler

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	dbmocks "github.com/falkcorp/audiobook-organizer/internal/database/mocks"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// asinBackfillTaskFixture builds a scheduler whose registry carries a stub
// metafetch.asin-backfill def (the real one lives in the metafetch plugin),
// recording every row the registry inserts. prevStatus, when set, is the
// status GetOperationV2 reports for any earlier run.
func asinBackfillTaskFixture(t *testing.T, registerDef bool, prevStatus string, active ...database.OperationV2Row) (*TaskScheduler, *[]database.OperationV2Row) {
	t.Helper()
	restoreCfg := config.Snapshot()
	t.Cleanup(func() { config.AppConfig = restoreCfg })
	config.ResetToDefaults()

	store := dbmocks.NewMockStore(t)
	var inserted []database.OperationV2Row
	store.EXPECT().UpsertOpDefinitionV2(mock.Anything).Return(nil).Maybe()
	store.EXPECT().InsertOperationV2(mock.Anything).
		RunAndReturn(func(row database.OperationV2Row) error {
			inserted = append(inserted, row)
			return nil
		}).Maybe()
	store.EXPECT().GetOperationV2(mock.Anything).
		RunAndReturn(func(id string) (*database.OperationV2Row, error) {
			if prevStatus == "" {
				return nil, nil
			}
			return &database.OperationV2Row{ID: id, Status: prevStatus}, nil
		}).Maybe()
	store.EXPECT().ListActiveOperationsV2().Return(active, nil).Maybe()
	store.EXPECT().GetDepRev(mock.Anything).Return(0, nil).Maybe()

	reg := opsregistry.New(store, slog.New(slog.DiscardHandler), 1, nil)
	if registerDef {
		require.NoError(t, reg.RegisterOp(opsregistry.OperationDef{
			ID: asinBackfillOpID, Plugin: "test",
			ResumePolicy: opsregistry.ResumeDrop, Liveness: opsregistry.LivenessManual,
			Run: func(context.Context, json.RawMessage, opsregistry.Reporter) error { return nil },
		}))
	}
	deps := testDeps()
	deps.Store = func() SchedulerStore { return store }
	deps.OpRegistry = reg
	return NewTaskScheduler(deps), &inserted
}

// The asin_backfill task replaced isbn_enrichment. It must enqueue
// metafetch.asin-backfill with dry_run=false EXPLICITLY: the op resolves an
// omitted dry_run to a preview, so empty params would run a dry run every
// tick and write nothing.
func TestASINBackfillTask_EnqueuesLiveRun(t *testing.T) {
	ts, inserted := asinBackfillTaskFixture(t, true, "")
	task, ok := ts.GetTask(asinBackfillTaskName)
	require.True(t, ok)
	_, old := ts.GetTask("isbn_enrichment")
	assert.False(t, old, "isbn_enrichment is retired; its task must be gone")

	assert.True(t, task.IsEnabled())
	assert.False(t, task.RunOnStart())
	assert.False(t, task.RunInMaintenanceWindow())
	assert.False(t, ts.inMaintenanceOrder(asinBackfillTaskName))
	assert.False(t, ts.inMaintenanceOrder("isbn_enrichment"))
	assert.Equal(t, 6*time.Hour, task.GetInterval())
	assert.Equal(t, asinBackfillOpID, taskV2DefIDs[asinBackfillTaskName])

	op, err := task.TriggerFn("test")
	require.NoError(t, err)
	require.NotNil(t, op)
	require.Len(t, *inserted, 1)
	row := (*inserted)[0]
	require.Equal(t, asinBackfillOpID, row.DefID)
	var params map[string]any
	require.NoError(t, json.Unmarshal([]byte(row.Params), &params))
	dry, present := params["dry_run"]
	require.True(t, present, "dry_run must be sent, not omitted: params=%s", row.Params)
	assert.Equal(t, false, dry, "the scheduled run must be live")
}

// A run still queued or running blocks the next tick: the op's
// ConcurrencyKey would queue a duplicate full walk behind it.
func TestASINBackfillTask_SkipsWhilePreviousRunActive(t *testing.T) {
	ts, inserted := asinBackfillTaskFixture(t, true, "running")
	task, _ := ts.GetTask(asinBackfillTaskName)
	ts.setPreviousRunID(asinBackfillTaskName, "prev-op")
	op, err := task.TriggerFn("test")
	require.NoError(t, err)
	assert.Nil(t, op)
	assert.Empty(t, *inserted)
}

// After a restart the in-memory previous-run id is empty, but a resumed walk
// is in the store's active set: that alone must block the tick.
func TestASINBackfillTask_SkipsWhenActiveRowAfterRestart(t *testing.T) {
	ts, inserted := asinBackfillTaskFixture(t, true, "",
		database.OperationV2Row{ID: "resumed", DefID: asinBackfillOpID, Status: "running"})
	task, _ := ts.GetTask(asinBackfillTaskName)
	op, err := task.TriggerFn("test")
	require.NoError(t, err)
	assert.Nil(t, op)
	assert.Empty(t, *inserted)
}

// Without the op in the binary (the metafetch plugin unlinked), the task is
// disabled rather than failing every tick.
func TestASINBackfillTask_DisabledWithoutDef(t *testing.T) {
	ts, _ := asinBackfillTaskFixture(t, false, "")
	task, ok := ts.GetTask(asinBackfillTaskName)
	require.True(t, ok)
	assert.False(t, task.IsEnabled())
}

// scheduler.isbn-enrichment stays registered but refuses, so a stale schedule
// row or API call fails loudly instead of writing ASINs by title prefix.
func TestISBNEnrichmentOp_Retired(t *testing.T) {
	store := dbmocks.NewMockStore(t)
	store.EXPECT().UpsertOpDefinitionV2(mock.Anything).Return(nil).Maybe()
	reg := opsregistry.New(store, slog.New(slog.DiscardHandler), 1, nil)
	require.NoError(t, (&ExtraOpsRegistrar{}).RegisterISBNEnrichmentOp(reg))
	def, ok := reg.Def("scheduler.isbn-enrichment")
	require.True(t, ok, "the retired def must stay registered")
	err := def.Run(context.Background(), nil, nil)
	require.ErrorIs(t, err, ErrISBNEnrichmentRetired)
	assert.Contains(t, err.Error(), "retired: use metafetch.asin-backfill")
}
