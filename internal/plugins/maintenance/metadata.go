// file: internal/plugins/maintenance/metadata.go
// version: 1.4.0
// guid: a7b8c9d0-e1f2-3456-0123-678901234567
// last-edited: 2026-09-27

package maintenance

import (
	"context"
	"encoding/json"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"log/slog"
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

// --- isbn-enrichment ---
// Hard rule: ResumeRestart. The resume position is NOT a reporter checkpoint;
// see the ResumePolicy comment below for where it actually lives.

func (p *Plugin) isbnEnrichmentDef() sdk.OperationDef {
	sched := "0 7 * * *" // 07:00 daily
	return sdk.OperationDef{
		ID:          "maintenance.isbn-enrichment",
		Liveness:    sdk.LivenessManual,
		Plugin:      "maintenance",
		DisplayName: "ISBN enrichment",
		Description: "Searches external metadata sources for missing ISBN identifiers. " +
			"Each run is a bounded batch (isbn_enrichment_batch_limit, default 100) that resumes " +
			"from a persistent sweep cursor, so successive runs walk the whole library.",
		// RESUME AUDIT 2026-09-11 (c): kept, with no reporter.Checkpoint, and
		// that is correct here. The Description used to claim "checkpoints every
		// 100 books", which was false — nothing on this path ever called
		// Checkpoint. What makes a from-zero restart safe is inside
		// metafetch.EnrichMissingISBNs: it loads a PERSISTED sweep cursor
		// (isbnEnrichCursorKey) before its loop, skips every book that already
		// has an ISBN, and stops after `limit` attempted books, so a restart is
		// at most one more bounded batch from where the sweep left off — the
		// same cost as the next scheduled run, never a whole-library re-walk.
		ResumePolicy:    sdk.ResumeRestart,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.isbn-enrichment",
		Cancellable:     true,
		Isolate:         false,
		Timeout:         120 * time.Minute,
		Schedule:        &sched,
		Capabilities: []sdk.Capability{
			sdk.CapLibraryRead, sdk.CapLibraryWrite,
			sdk.CapNetworkOpenLibrary, sdk.CapNetworkGoogleBooks,
		},
		Run: p.runISBNEnrichment,
	}
}

func (p *Plugin) runISBNEnrichment(ctx context.Context, _ json.RawMessage, reporter sdk.Reporter) (retErr error) {
	// Metadata is never applied during a library scan: hold the scan stand-down
	// before the first write (fails the op if the scan does not park).
	hold, sdErr := registry.HoldScanStandDown(ctx, p.deps, reporter, "maintenance.isbn-enrichment apply")
	if sdErr != nil {
		return sdErr
	}
	defer func() { retErr = hold.Finish(retErr) }()
	ctx, reporter = hold.Context(), hold.Reporter()
	if !p.deps.HasISBNEnrichment() {
		_ = reporter.Log(slog.LevelInfo, "ISBN enrichment service is not configured, skipping")
		return nil
	}
	opID := ctxOpID(ctx)
	return p.deps.RunIsbnEnrichment(ctx, newOpsAdapter(reporter), opID)
}
