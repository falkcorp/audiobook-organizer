// file: internal/scheduler/identification_advance_task_test.go
// version: 1.0.0
// guid: 98c67be8-967c-4bf7-a02a-5fbf94fbf6ed
// last-edited: 2026-10-16

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

// With the driver off (the default) the candidate_fetch tick sends exactly
// the params it always did: {"unfetched":true}, byte for byte, so the op
// selects and fetches as before and marks nothing.
func TestCandidateFetchTick_UnchangedWhenDisabled(t *testing.T) {
	ts, inserted := candidateFetchTaskFixture(t)
	require.False(t, config.AppConfig.IdentificationAdvanceEnabled, "the driver must ship off")
	task, _ := ts.GetTask(candidateFetchTaskName)
	_, err := task.TriggerFn("test")
	require.NoError(t, err)
	require.Len(t, *inserted, 1)
	assert.JSONEq(t, `{"unfetched":true}`, (*inserted)[0].Params)
	raw, err := json.Marshal(candidateFetchParams())
	require.NoError(t, err)
	assert.Equal(t, `{"unfetched":true}`, string(raw))
}

// With the driver on, the tick asks the op to MARK its selection
// (mark_dirty_only) instead of fetching it.
func TestCandidateFetchTick_MarksInsteadOfFetchingWhenEnabled(t *testing.T) {
	ts, inserted := candidateFetchTaskFixture(t)
	config.AppConfig.IdentificationAdvanceEnabled = true
	task, _ := ts.GetTask(candidateFetchTaskName)
	_, err := task.TriggerFn("test")
	require.NoError(t, err)
	require.Len(t, *inserted, 1)
	assert.JSONEq(t, `{"unfetched":true,"mark_dirty_only":true}`, (*inserted)[0].Params)
}

// dirtyStore is the mock store plus the dirty-set capability.
type dirtyStore struct {
	SchedulerStore
	marks int
}

func (d *dirtyStore) IdentDirtyPage(_ string, limit int) ([]database.IdentDirtyEntry, error) {
	if d.marks == 0 {
		return nil, nil
	}
	return []database.IdentDirtyEntry{{BookID: "book-000001", Epoch: 1}}[:min(limit, 1)], nil
}

func identAdvanceFixture(t *testing.T, marks int, active ...database.OperationV2Row) (*TaskScheduler, *[]database.OperationV2Row) {
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
	store.EXPECT().GetOperationV2(mock.Anything).Return(nil, nil).Maybe()
	store.EXPECT().ListActiveOperationsV2().Return(active, nil).Maybe()
	store.EXPECT().GetDepRev(mock.Anything).Return(0, nil).Maybe()
	reg := opsregistry.New(store, slog.New(slog.DiscardHandler), 1, nil)
	require.NoError(t, reg.RegisterOp(opsregistry.OperationDef{
		ID: identificationAdvanceOpID, Plugin: "test",
		ResumePolicy: opsregistry.ResumeDrop, Liveness: opsregistry.LivenessManual,
		Run: func(context.Context, json.RawMessage, opsregistry.Reporter) error { return nil },
	}))
	ds := &dirtyStore{SchedulerStore: store, marks: marks}
	deps := testDeps()
	deps.Store = func() SchedulerStore { return ds }
	deps.OpRegistry = reg
	return NewTaskScheduler(deps), &inserted
}

// The identification_advance tick is off by default, runs every 5 minutes
// when on, and enqueues a live run only while the dirty set is non-empty and
// no run is active.
func TestIdentificationAdvanceTask(t *testing.T) {
	ts, inserted := identAdvanceFixture(t, 0)
	task, ok := ts.GetTask(identificationAdvanceTaskName)
	require.True(t, ok)
	assert.False(t, task.IsEnabled(), "off unless identification_advance_enabled")
	config.AppConfig.IdentificationAdvanceEnabled = true
	assert.True(t, task.IsEnabled())
	assert.Equal(t, 5*time.Minute, task.GetInterval())
	assert.False(t, task.RunOnStart())
	assert.False(t, task.RunInMaintenanceWindow())
	assert.Equal(t, identificationAdvanceOpID, taskV2DefIDs[identificationAdvanceTaskName])

	op, err := task.TriggerFn("test")
	require.NoError(t, err)
	assert.Nil(t, op, "an empty dirty set enqueues nothing")
	assert.Empty(t, *inserted)

	ts, inserted = identAdvanceFixture(t, 1)
	config.AppConfig.IdentificationAdvanceEnabled = true
	task, _ = ts.GetTask(identificationAdvanceTaskName)
	op, err = task.TriggerFn("test")
	require.NoError(t, err)
	require.NotNil(t, op)
	require.Len(t, *inserted, 1)
	assert.JSONEq(t, `{"dry_run":false}`, (*inserted)[0].Params)

	ts, inserted = identAdvanceFixture(t, 1, database.OperationV2Row{ID: "r", DefID: identificationAdvanceOpID, Status: "running"})
	config.AppConfig.IdentificationAdvanceEnabled = true
	task, _ = ts.GetTask(identificationAdvanceTaskName)
	op, err = task.TriggerFn("test")
	require.NoError(t, err)
	assert.Nil(t, op, "an active run skips the tick")
	assert.Empty(t, *inserted)
}
