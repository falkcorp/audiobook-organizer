// file: internal/plugins/maintenance/metadata_standdown_test.go
// version: 1.1.0
// guid: 6f1d3b82-9a4e-4c57-8e20-d3a5b7c91f46
// last-edited: 2026-10-11

package maintenance

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/stretchr/testify/require"
)

// metadataScanDeps is fakeDeps with a steerable scan stand-down gate.
type metadataScanDeps struct {
	fakeDeps
	acquireErr error
	acquires   int
}

func (d *metadataScanDeps) AcquireScanStandDown(context.Context, string, string) (func(), error) {
	if d.acquireErr != nil {
		return nil, d.acquireErr
	}
	d.acquires++
	return func() {}, nil
}

func (d *metadataScanDeps) RenewScanStandDown(string) bool { return true }

// Metadata is never applied during a library scan. Ported from the scheduler
// package's test of the removed scheduler.metadata-refresh (04-P4c): the
// merged op must refuse, without running the scan body, when the scan does not
// park.
func TestMetadataRefresh_RefusesWhileScanHoldsTheLibrary(t *testing.T) {
	var bodyCtx context.Context
	d := &metadataScanDeps{
		fakeDeps:   fakeDeps{refreshCtx: &bodyCtx},
		acquireErr: errors.New("scan did not park within 5m0s"),
	}
	p := New(d)
	err := p.runMetadataRefresh(context.Background(), nil, &opIDReporter{id: "op-refresh"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "a library scan is running")
	require.Contains(t, err.Error(), "maintenance.metadata-refresh apply")
	require.Nil(t, bodyCtx, "the scan body must not run when the hold is refused")
}

func TestMetadataRefresh_ProceedsOnceScanParks(t *testing.T) {
	d := &metadataScanDeps{}
	p := New(d)
	require.NoError(t, p.runMetadataRefresh(context.Background(), nil, &opIDReporter{id: "op-refresh"}))
	require.Equal(t, 1, d.acquires)
}

// The scan must run on the op's own context: the def's Timeout, a user cancel,
// shutdown and the watchdog reach it only through that ctx.
func TestMetadataRefresh_PassesOpContext(t *testing.T) {
	var got context.Context
	p := New(fakeDeps{refreshCtx: &got})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.NoError(t, p.runMetadataRefresh(ctx, nil, &fakeReporter{}))
	require.NotNil(t, got, "RunMetadataRefreshScan was not called")
	require.Error(t, got.Err(), "the scan must receive the op ctx, which is already cancelled")
}

func TestMetadataRefreshDef_MergeContract(t *testing.T) {
	def := New(fakeDeps{}).metadataRefreshDef()
	require.Equal(t, []string{"scheduler.metadata-refresh"}, def.FormerIDs)
	require.Equal(t, []auth.Permission{auth.PermSettingsManage}, def.Permissions)
	require.GreaterOrEqual(t, def.Timeout, 4*time.Hour)
}
