// file: internal/scheduler/collections_refresh_task_test.go
// version: 1.0.0
// guid: 3f7a1d58-92c4-4e06-b8a3-6d0e5c1f9b27
// last-edited: 2026-10-18

package scheduler

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	dbmocks "github.com/falkcorp/audiobook-organizer/internal/database/mocks"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// collectionsRefreshTaskFixture builds a scheduler whose registry carries a stub
// collections.refresh-relative-dynamic def, recording every row the registry
// inserts.
func collectionsRefreshTaskFixture(t *testing.T, active ...database.OperationV2Row) (*TaskScheduler, *[]database.OperationV2Row) {
	t.Helper()
	store := dbmocks.NewMockStore(t)
	var inserted []database.OperationV2Row
	store.EXPECT().UpsertOpDefinitionV2(mock.Anything).Return(nil).Maybe()
	store.EXPECT().InsertOperationV2(mock.Anything).
		RunAndReturn(func(row database.OperationV2Row) error {
			inserted = append(inserted, row)
			return nil
		}).Maybe()
	store.EXPECT().GetOperationV2(mock.Anything).Return(nil, nil).Maybe()
	store.EXPECT().ListActiveOperationsV2().Return(active, nil).Maybe()
	store.EXPECT().GetDepRev(mock.Anything).Return(0, nil).Maybe()

	reg := opsregistry.New(store, slog.New(slog.DiscardHandler), 1, nil)
	require.NoError(t, reg.RegisterOp(opsregistry.OperationDef{
		ID: collectionsRefreshOpID, Plugin: "test",
		ResumePolicy: opsregistry.ResumeDrop, Liveness: opsregistry.LivenessManual,
		Run: func(context.Context, json.RawMessage, opsregistry.Reporter) error { return nil },
	}))
	deps := testDeps()
	deps.Store = func() SchedulerStore { return store }
	deps.OpRegistry = reg
	return NewTaskScheduler(deps), &inserted
}

// The collections_refresh task enqueues the op LIVE (dry_run=false: the op's
// default is a preview that stores nothing) every hour, never on startup or in
// the maintenance window.
func TestCollectionsRefreshTask_EnqueuesLiveHourlyRun(t *testing.T) {
	ts, inserted := collectionsRefreshTaskFixture(t)
	task, ok := ts.GetTask(collectionsRefreshTaskName)
	require.True(t, ok)
	assert.True(t, task.IsEnabled())
	assert.False(t, task.RunOnStart())
	assert.False(t, task.RunInMaintenanceWindow())
	assert.False(t, ts.inMaintenanceOrder(collectionsRefreshTaskName))
	assert.Equal(t, time.Hour, task.GetInterval())
	assert.Equal(t, collectionsRefreshOpID, taskV2DefIDs[collectionsRefreshTaskName])
	assert.Equal(t, collectionsRefreshTaskName, legacyTaskNames[collectionsRefreshOpID])

	op, err := task.TriggerFn("test")
	require.NoError(t, err)
	require.NotNil(t, op)
	require.Len(t, *inserted, 1)
	row := (*inserted)[0]
	require.Equal(t, collectionsRefreshOpID, row.DefID)
	var params map[string]any
	require.NoError(t, json.Unmarshal([]byte(row.Params), &params))
	assert.Equal(t, false, params["dry_run"], "the scheduled run must be live, not the op's preview default")
}

// A refresh already queued or running skips the tick.
func TestCollectionsRefreshTask_SkipsWhileARefreshIsActive(t *testing.T) {
	ts, inserted := collectionsRefreshTaskFixture(t,
		database.OperationV2Row{ID: "manual", DefID: collectionsRefreshOpID, Status: "running"})
	task, _ := ts.GetTask(collectionsRefreshTaskName)
	op, err := task.TriggerFn("test")
	require.NoError(t, err)
	assert.Nil(t, op)
	assert.Empty(t, *inserted)
}

// With the op not registered in this binary the task is off, so it does not
// report itself enabled while enqueueing nothing.
func TestCollectionsRefreshTask_DisabledWithoutTheOp(t *testing.T) {
	ts := NewTaskScheduler(testDeps())
	task, ok := ts.GetTask(collectionsRefreshTaskName)
	require.True(t, ok)
	assert.False(t, task.IsEnabled())
}
