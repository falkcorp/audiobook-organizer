// file: internal/scheduler/metadata_upgrade_registration_test.go
// version: 1.0.0
// guid: 3c8f0a61-9e27-4d5b-b4c3-6f1a7e2d9b08
// last-edited: 2026-09-27

package scheduler

import (
	"log/slog"
	"testing"

	dbmocks "github.com/falkcorp/audiobook-organizer/internal/database/mocks"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// The metadata upgrade is registered once, as scheduler.metadata-upgrade, and
// the retired maintenance.metadata-upgrade ID resolves to that same def, so an
// old operations_v2 row or an enqueue by the old ID runs the one op.
func TestMetadataUpgradeOp_SingleRegistrationAnswersToFormerID(t *testing.T) {
	m := dbmocks.NewMockStore(t)
	m.EXPECT().UpsertOpDefinitionV2(mock.Anything).Return(nil).Maybe()
	reg := opsregistry.New(m, slog.New(slog.DiscardHandler), 1, nil)
	require.NoError(t, NewExtraOpsRegistrar(nil, ExtraOpsDeps{}).RegisterMetadataUpgradeOp(reg))

	def, ok := reg.Def("scheduler.metadata-upgrade")
	require.True(t, ok)
	former, ok := reg.Def("maintenance.metadata-upgrade")
	require.True(t, ok, "the retired maintenance ID must still resolve")
	require.Equal(t, def.ID, former.ID)
	require.Equal(t, "scheduler.metadata-upgrade", reg.CanonicalDefID("maintenance.metadata-upgrade"))

	require.Contains(t, def.Capabilities, opsregistry.CapNetworkAudible)
	require.Contains(t, def.Capabilities, opsregistry.CapNetworkGeneric)
}

// metadata_upgrade runs on its own interval and is no longer in the
// maintenance window's order.
func TestMetadataUpgradeTask_NotInMaintenanceWindow(t *testing.T) {
	ts := NewTaskScheduler(SchedulerDeps{
		Store:               func() SchedulerStore { return nil },
		HasDedupEngine:      func() bool { return false },
		HasMetadataFetchSvc: func() bool { return true },
		HasActivitySvc:      func() bool { return false },
		HasBatchPoller:      func() bool { return false },
	})
	require.False(t, ts.inMaintenanceOrder("metadata_upgrade"))
	task, ok := ts.GetTask("metadata_upgrade")
	require.True(t, ok)
	require.False(t, task.RunInMaintenanceWindow())
}
