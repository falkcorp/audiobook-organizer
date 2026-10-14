// file: internal/scheduler/reconcile_scan_task_test.go
// version: 1.0.0
// guid: 4d9c6b18-2e57-4f03-a8b1-7c3e5d0f9a26
// last-edited: 2026-10-14

package scheduler

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	dbmocks "github.com/falkcorp/audiobook-organizer/internal/database/mocks"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// 04-P14c: the nightly reconcile_scan task enqueues reconcile.scan (the survivor
// of the merge with maintenance.reconcile-scan), with an empty JSON object as
// params: the survivor decodes its params, and the retired op's nil payload is
// not what a def that unmarshals them wants.
func TestReconcileScanTask_EnqueuesSurvivorWithEmptyParams(t *testing.T) {
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
	store.EXPECT().ListActiveOperationsV2().Return(nil, nil).Maybe()
	store.EXPECT().GetDepRev(mock.Anything).Return(0, nil).Maybe()

	reg := opsregistry.New(store, slog.New(slog.DiscardHandler), 1, nil)
	require.NoError(t, reg.RegisterOp(opsregistry.OperationDef{
		ID: "reconcile.scan", FormerIDs: []string{"maintenance.reconcile-scan"}, Plugin: "test",
		ResumePolicy: opsregistry.ResumeDrop, Liveness: opsregistry.LivenessManual,
		Run: func(context.Context, json.RawMessage, opsregistry.Reporter) error { return nil },
	}))
	deps := testDeps()
	deps.Store = func() SchedulerStore { return store }
	deps.OpRegistry = reg
	ts := NewTaskScheduler(deps)

	assert.Equal(t, "reconcile.scan", taskV2DefIDs["reconcile_scan"])

	task, ok := ts.GetTask("reconcile_scan")
	require.True(t, ok)
	op, err := task.TriggerFn("test")
	require.NoError(t, err)
	require.NotNil(t, op)
	require.Len(t, inserted, 1)
	assert.Equal(t, "reconcile.scan", inserted[0].DefID)
	assert.JSONEq(t, `{}`, inserted[0].Params)
}
