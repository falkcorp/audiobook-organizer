// file: internal/scheduler/candidate_fetch_task_test.go
// version: 1.0.0
// guid: 1a7011f9-8a3c-46d3-841a-382e3e576d43
// last-edited: 2026-10-05

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

// candidateFetchTaskFixture builds a scheduler whose registry carries a stub
// metadata.candidate-fetch def, recording every row the registry inserts.
func candidateFetchTaskFixture(t *testing.T, active ...database.OperationV2Row) (*TaskScheduler, *[]database.OperationV2Row) {
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
		ID: candidateFetchOpID, Plugin: "test",
		ResumePolicy: opsregistry.ResumeDrop, Liveness: opsregistry.LivenessManual,
		Run: func(context.Context, json.RawMessage, opsregistry.Reporter) error { return nil },
	}))
	deps := testDeps()
	deps.Store = func() SchedulerStore { return store }
	deps.OpRegistry = reg
	return NewTaskScheduler(deps), &inserted
}

// The candidate_fetch task enqueues metadata.candidate-fetch with
// unfetched=true (the op selects its own books), every 6h by default, never
// on startup or in the maintenance window.
func TestCandidateFetchTask_EnqueuesUnfetchedRun(t *testing.T) {
	ts, inserted := candidateFetchTaskFixture(t)
	task, ok := ts.GetTask(candidateFetchTaskName)
	require.True(t, ok)
	assert.True(t, task.IsEnabled())
	assert.False(t, task.RunOnStart())
	assert.False(t, task.RunInMaintenanceWindow())
	assert.False(t, ts.inMaintenanceOrder(candidateFetchTaskName))
	assert.Equal(t, 6*time.Hour, task.GetInterval())
	assert.Equal(t, candidateFetchOpID, taskV2DefIDs[candidateFetchTaskName])

	op, err := task.TriggerFn("test")
	require.NoError(t, err)
	require.NotNil(t, op)
	require.Len(t, *inserted, 1)
	row := (*inserted)[0]
	require.Equal(t, candidateFetchOpID, row.DefID)
	var params map[string]any
	require.NoError(t, json.Unmarshal([]byte(row.Params), &params))
	assert.Equal(t, true, params["unfetched"])
	assert.NotContains(t, params, "book_ids")
	assert.NotContains(t, params, "force")
}

// Any candidate fetch already queued or running -- a hand-started one too --
// skips the tick: the op's ConcurrencyKey would queue a second run behind it.
func TestCandidateFetchTask_SkipsWhileAFetchIsActive(t *testing.T) {
	ts, inserted := candidateFetchTaskFixture(t,
		database.OperationV2Row{ID: "manual", DefID: candidateFetchOpID, Status: "running"})
	task, _ := ts.GetTask(candidateFetchTaskName)
	op, err := task.TriggerFn("test")
	require.NoError(t, err)
	assert.Nil(t, op)
	assert.Empty(t, *inserted)
}

// interval 0 turns it off.
func TestCandidateFetchTask_IntervalZeroDisables(t *testing.T) {
	ts, _ := candidateFetchTaskFixture(t)
	config.AppConfig.Scheduled.CandidateFetch.Interval = 0
	task, _ := ts.GetTask(candidateFetchTaskName)
	assert.False(t, task.IsEnabled())
}
