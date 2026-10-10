// file: internal/plugins/maintenance/metadata.go
// version: 1.5.1
// guid: a7b8c9d0-e1f2-3456-0123-678901234567
// last-edited: 2026-10-09

package maintenance

import (
	"context"
	"encoding/json"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"time"

	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// --- metadata-refresh ---

func (p *Plugin) metadataRefreshDef() sdk.OperationDef {
	sched := "0 6 * * *" // 06:00 daily
	return sdk.OperationDef{
		ID:              "maintenance.metadata-refresh",
		Liveness:        sdk.LivenessManual,
		Plugin:          "maintenance",
		DisplayName:     "Metadata refresh scan",
		Description:     "Re-fetches metadata for books with incomplete records.",
		ResumePolicy:    sdk.ResumeRequeue,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.metadata-refresh",
		Cancellable:     true,
		Isolate:         false,
		Timeout:         120 * time.Minute,
		Schedule:        &sched,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite, sdk.CapNetworkGeneric},
		Run:             p.runMetadataRefresh,
	}
}

func (p *Plugin) runMetadataRefresh(ctx context.Context, _ json.RawMessage, reporter sdk.Reporter) (retErr error) {
	// Metadata is never applied during a library scan: hold the scan stand-down
	// before the first write (fails the op if the scan does not park).
	hold, sdErr := registry.HoldScanStandDown(ctx, p.deps, reporter, "maintenance.metadata-refresh apply")
	if sdErr != nil {
		return sdErr
	}
	defer func() { retErr = hold.Finish(retErr) }()
	ctx, reporter = hold.Context(), hold.Reporter()
	return p.deps.RunMetadataRefreshScan(ctx, newOpsAdapter(reporter))
}

// --- metadata-upgrade ---
// Removed 2026-09-27: maintenance.metadata-upgrade duplicated
// scheduler.metadata-upgrade (internal/scheduler/extra_ops.go), which now
// lists this ID as a FormerID so old rows and enqueues still resolve.
