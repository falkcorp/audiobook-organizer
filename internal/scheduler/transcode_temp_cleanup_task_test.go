// file: internal/scheduler/transcode_temp_cleanup_task_test.go
// version: 1.2.0
// guid: 8d41c7e2-3a96-4f05-b7d8-2e6f90a1c354
// last-edited: 2026-10-15

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
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// TestTranscodeTempCleanupTask pins the task that replaced the per-import-path
// transcode cleanup ticker goroutines: hourly, runs at start, outside the
// maintenance window, and enqueues its own def, maintenance.transcode-temp-cleanup
// (not the cron-scheduled temp-file-cleanup, whose Schedule makes the registry
// dedupe on def id alone), live with dry_run=false.
func TestTranscodeTempCleanupTask(t *testing.T) {
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
		ID:           "maintenance.transcode-temp-cleanup",
		Plugin:       "test",
		ResumePolicy: opsregistry.ResumeDrop,
		Liveness:     opsregistry.LivenessManual,
		Run: func(context.Context, json.RawMessage, opsregistry.Reporter) error {
			return nil
		},
	}))
	deps := testDeps()
	deps.Store = func() SchedulerStore { return store }
	deps.OpRegistry = reg

	ts := NewTaskScheduler(deps)
	task, ok := ts.GetTask("transcode_temp_cleanup")
	require.True(t, ok, "transcode_temp_cleanup must be registered")
	require.True(t, task.IsEnabled())
	require.Equal(t, time.Hour, task.GetInterval())
	require.True(t, task.RunOnStart(), "the retired ticker ran once immediately")
	require.False(t, task.RunInMaintenanceWindow(), "the hourly cadence is the interval, not the nightly window")
	require.False(t, ts.inMaintenanceOrder("transcode_temp_cleanup"))
	require.Equal(t, "maintenance.transcode-temp-cleanup", taskV2DefIDs["transcode_temp_cleanup"])

	_, err := task.TriggerFn("test")
	require.NoError(t, err)
	require.Len(t, inserted, 1)
	require.Equal(t, "maintenance.transcode-temp-cleanup", inserted[0].DefID)
	require.JSONEq(t, `{"dry_run":false}`, inserted[0].Params,
		"the op previews unless dry_run is false; the hourly task must say so")
}

// TestTempFileCleanupWindowSkipsWhileSiblingRuns: the two defs share a
// ConcurrencyKey, so the dispatcher would park the nightly temp_file_cleanup
// behind a running transcode sweep and stall the window. Reporting it running
// makes the window skip it instead.
func TestTempFileCleanupWindowSkipsWhileSiblingRuns(t *testing.T) {
	for _, tc := range []struct {
		name   string
		active []database.OperationV2Row
		want   bool
	}{
		{"sibling running", []database.OperationV2Row{{DefID: "maintenance.transcode-temp-cleanup"}}, true},
		{"own def running", []database.OperationV2Row{{DefID: "maintenance.temp-file-cleanup"}}, true},
		{"nothing running", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := dbmocks.NewMockStore(t)
			store.EXPECT().ListActiveOperationsV2().Return(tc.active, nil).Maybe()
			deps := testDeps()
			deps.Store = func() SchedulerStore { return store }
			ts := NewTaskScheduler(deps)
			require.Equal(t, tc.want, ts.isTaskRunning("temp_file_cleanup"))
		})
	}
}
