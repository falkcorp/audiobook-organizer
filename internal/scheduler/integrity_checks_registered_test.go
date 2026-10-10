// file: internal/scheduler/integrity_checks_registered_test.go
// version: 1.1.0
// guid: 3e8a1c52-7b94-4d06-a1f3-5c2d9e60b718
// last-edited: 2026-10-10

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

var integrityCheckTasks = map[string]string{
	"file_integrity_check":      "maintenance.file-integrity-check",
	"orphan_book_files_cleanup": "maintenance.orphan-book-files-cleanup",
}

// The two report-only health checks declared a nightly cron on their defs but
// no scheduler task existed, so neither ever ran. They must be registered, in
// the maintenance window, mapped to their def (or isTaskRunning lies), and in
// the window's iteration order (or the window never reaches them).
func TestRegisteredTasks_IncludeIntegrityChecks(t *testing.T) {
	ts := NewTaskScheduler(reachabilityTestDeps())
	for name, defID := range integrityCheckTasks {
		task, ok := ts.tasks[name]
		if !ok {
			t.Errorf("task %q is not registered", name)
			continue
		}
		if task.Category != "maintenance" {
			t.Errorf("%s: category = %q, want maintenance", name, task.Category)
		}
		if !task.IsEnabled() || !task.RunInMaintenanceWindow() {
			t.Errorf("%s: want enabled and in the maintenance window", name)
		}
		if task.RunOnStart() {
			t.Errorf("%s: must not run on start", name)
		}
		if !ts.inMaintenanceOrder(name) {
			t.Errorf("%s: missing from maintenanceOrder, the window would never run it", name)
		}
		if got := TaskV2DefIDs()[name]; got != defID {
			t.Errorf("%s: taskV2DefIDs = %q, want %q", name, got, defID)
		}
	}
}

// Both checks run ONLY in the maintenance window. A positive interval would put
// them on the durable interval clock too (Start's `IsEnabled && GetInterval > 0`
// branch) and run each up to twice a day, once outside the window.
func TestIntegrityChecks_AreWindowOnly(t *testing.T) {
	ts := NewTaskScheduler(reachabilityTestDeps())
	for name := range integrityCheckTasks {
		task := ts.tasks[name]
		require.NotNil(t, task, name)
		assert.Equal(t, 0, int(task.GetInterval()), "%s: interval must be 0 (window-only)", name)
		assert.Empty(t, task.DailyAt, "%s: no daily wall-clock timer", name)
		assert.False(t, task.selfScheduled(), "%s: must not be on the interval branch", name)
		assert.True(t, task.RunInMaintenanceWindow(), name)
		assert.True(t, ts.inMaintenanceOrder(name), name)
	}
}

// Report-only is locked in at the enqueue: the params are exactly {}, so an op
// that gains a mode flag later cannot be switched on by the scheduler.
func TestIntegrityChecks_EnqueueEmptyParams(t *testing.T) {
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
	for _, defID := range integrityCheckTasks {
		require.NoError(t, reg.RegisterOp(opsregistry.OperationDef{
			ID: defID, Plugin: "test",
			ResumePolicy: opsregistry.ResumeDrop, Liveness: opsregistry.LivenessManual,
			Run: func(context.Context, json.RawMessage, opsregistry.Reporter) error { return nil },
		}))
	}
	deps := testDeps()
	deps.Store = func() SchedulerStore { return store }
	deps.OpRegistry = reg
	ts := NewTaskScheduler(deps)

	for name, defID := range integrityCheckTasks {
		inserted = nil
		task, ok := ts.GetTask(name)
		require.True(t, ok, name)
		op, err := task.TriggerFn("test")
		require.NoError(t, err, name)
		require.NotNil(t, op, name)
		require.Len(t, inserted, 1, name)
		assert.Equal(t, defID, inserted[0].DefID)
		assert.JSONEq(t, `{}`, inserted[0].Params, "%s must enqueue empty params", name)
	}
}

// TaskV2DefIDs hands out a copy: mutating it must not change the live map.
func TestTaskV2DefIDs_ReturnsACopy(t *testing.T) {
	m := TaskV2DefIDs()
	m["file_integrity_check"] = "tampered"
	if got := TaskV2DefIDs()["file_integrity_check"]; got != "maintenance.file-integrity-check" {
		t.Fatalf("live map was mutated through the accessor: %q", got)
	}
}
