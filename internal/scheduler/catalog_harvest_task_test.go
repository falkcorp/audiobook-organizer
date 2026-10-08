// file: internal/scheduler/catalog_harvest_task_test.go
// version: 1.0.0
// guid: 191cfdce-5898-40bb-901c-6fb9d408282e
// last-edited: 2026-10-07

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

// catalogHarvestTaskFixture builds a scheduler whose registry carries a stub
// catalog.harvest-authors def, with catalog.enabled on, recording every row
// the registry inserts.
func catalogHarvestTaskFixture(t *testing.T, active ...database.OperationV2Row) (*TaskScheduler, *[]database.OperationV2Row) {
	t.Helper()
	restoreCfg := config.Snapshot()
	t.Cleanup(func() { config.AppConfig = restoreCfg })
	config.ResetToDefaults()
	config.AppConfig.Catalog.Enabled = true

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
		ID: catalogHarvestOpID, Plugin: "test",
		ResumePolicy: opsregistry.ResumeDrop, Liveness: opsregistry.LivenessManual,
		Run: func(context.Context, json.RawMessage, opsregistry.Reporter) error { return nil },
	}))
	deps := testDeps()
	deps.Store = func() SchedulerStore { return store }
	deps.OpRegistry = reg
	return NewTaskScheduler(deps), &inserted
}

// The catalog_harvest task enqueues catalog.harvest-authors LIVE
// (dry_run=false: the op's default is a census that writes nothing), daily
// by default, over every author (no authors filter), never on startup or in
// the maintenance window.
func TestCatalogHarvestTask_EnqueuesLiveRunOverEveryAuthor(t *testing.T) {
	ts, inserted := catalogHarvestTaskFixture(t)
	task, ok := ts.GetTask(catalogHarvestTaskName)
	require.True(t, ok)
	assert.True(t, task.IsEnabled())
	assert.False(t, task.RunOnStart())
	assert.False(t, task.RunInMaintenanceWindow())
	assert.False(t, ts.inMaintenanceOrder(catalogHarvestTaskName))
	assert.Equal(t, 24*time.Hour, task.GetInterval())
	assert.Equal(t, catalogHarvestOpID, taskV2DefIDs[catalogHarvestTaskName])

	op, err := task.TriggerFn("test")
	require.NoError(t, err)
	require.NotNil(t, op)
	require.Len(t, *inserted, 1)
	row := (*inserted)[0]
	require.Equal(t, catalogHarvestOpID, row.DefID)
	var params map[string]any
	require.NoError(t, json.Unmarshal([]byte(row.Params), &params))
	assert.Equal(t, false, params["dry_run"])
	assert.NotContains(t, params, "authors")
}

// A harvest already queued or running skips the tick.
func TestCatalogHarvestTask_SkipsWhileAHarvestIsActive(t *testing.T) {
	ts, inserted := catalogHarvestTaskFixture(t,
		database.OperationV2Row{ID: "manual", DefID: catalogHarvestOpID, Status: "running"})
	task, _ := ts.GetTask(catalogHarvestTaskName)
	op, err := task.TriggerFn("test")
	require.NoError(t, err)
	assert.Nil(t, op)
	assert.Empty(t, *inserted)
}

// catalog.enabled off (the op would refuse) or interval 0 turns it off.
func TestCatalogHarvestTask_DisabledWithoutCatalogOrInterval(t *testing.T) {
	ts, _ := catalogHarvestTaskFixture(t)
	task, _ := ts.GetTask(catalogHarvestTaskName)
	config.AppConfig.Catalog.Enabled = false
	assert.False(t, task.IsEnabled())
	config.AppConfig.Catalog.Enabled = true
	config.AppConfig.Scheduled.CatalogHarvest.Interval = 0
	assert.False(t, task.IsEnabled())
}
