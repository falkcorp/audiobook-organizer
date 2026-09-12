// file: internal/scheduler/metadata_standdown_test.go
// version: 1.0.0
// guid: e5b40d97-3a1c-4f28-b6e0-9d7c2a18f453
// last-edited: 2026-09-12

package scheduler

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	dbmocks "github.com/falkcorp/audiobook-organizer/internal/database/mocks"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

type schedGate struct {
	acquireErr error
	acquired   int
}

func (g *schedGate) AcquireScanStandDown(context.Context, string, string) (func(), error) {
	if g.acquireErr != nil {
		return nil, g.acquireErr
	}
	g.acquired++
	return func() {}, nil
}
func (g *schedGate) RenewScanStandDown(string) bool { return true }

type schedReporter struct{ opsregistry.Reporter }

func (schedReporter) OpID() string                               { return "op-sched" }
func (schedReporter) UpdateProgress(int, int, string) error      { return nil }
func (schedReporter) Log(slog.Level, string, ...slog.Attr) error { return nil }
func (schedReporter) Logger() *slog.Logger                       { return slog.New(slog.DiscardHandler) }
func (schedReporter) SetCurrentItem(string)                      {}

func schedRun(t *testing.T, gate *schedGate, opID string, register func(*ExtraOpsRegistrar, *opsregistry.Registry) error) error {
	t.Helper()
	m := dbmocks.NewMockStore(t)
	m.EXPECT().UpsertOpDefinitionV2(mock.Anything).Return(nil).Maybe()
	reg := opsregistry.New(m, slog.New(slog.DiscardHandler), 1, nil)
	require.NoError(t, register(NewExtraOpsRegistrar(nil, ExtraOpsDeps{ScanStandDown: gate}), reg))
	def, ok := reg.Def(opID)
	require.True(t, ok)
	return def.Run(context.Background(), nil, schedReporter{})
}

func TestSchedulerMetadataOps_RefuseWhileScanHoldsTheLibrary(t *testing.T) {
	for opID, register := range map[string]func(*ExtraOpsRegistrar, *opsregistry.Registry) error{
		"scheduler.metadata-upgrade": (*ExtraOpsRegistrar).RegisterMetadataUpgradeOp,
		"scheduler.metadata-refresh": (*ExtraOpsRegistrar).RegisterMetadataRefreshOp,
	} {
		t.Run(opID, func(t *testing.T) {
			err := schedRun(t, &schedGate{acquireErr: errors.New("scan did not park within 5m0s")}, opID, register)
			require.Error(t, err)
			require.Contains(t, err.Error(), "a library scan is running")
		})
	}
}

func TestSchedulerMetadataUpgrade_ProceedsOnceScanParks(t *testing.T) {
	gate := &schedGate{}
	err := schedRun(t, gate, "scheduler.metadata-upgrade", (*ExtraOpsRegistrar).RegisterMetadataUpgradeOp)
	require.ErrorContains(t, err, "metadata fetch service not initialized")
	require.Equal(t, 1, gate.acquired)
}
