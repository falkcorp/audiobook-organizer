// file: internal/plugins/maintenance/metadata.go
// version: 1.3.0
// guid: a7b8c9d0-e1f2-3456-0123-678901234567
// last-edited: 2026-09-11

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
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

func (p *Plugin) runMetadataRefresh(ctx context.Context, _ json.RawMessage, reporter sdk.Reporter) error {
	return p.deps.RunMetadataRefreshScan(ctx, newOpsAdapter(reporter))
}

// --- metadata-upgrade ---

func (p *Plugin) metadataUpgradeDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:              "maintenance.metadata-upgrade",
		Liveness:        sdk.LivenessManual,
		Plugin:          "maintenance",
		DisplayName:     "Metadata source upgrade",
		Description:     "Upgrades metadata from lower-quality sources (Google Books) to richer ones (Hardcover, Audible) where a high-confidence match is available.",
		ResumePolicy:    sdk.ResumeRequeue,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.metadata-upgrade",
		Cancellable:     true,
		Isolate:         false,
		Timeout:         120 * time.Minute,
		Schedule:        nil,
		Capabilities: []sdk.Capability{
			sdk.CapLibraryRead, sdk.CapLibraryWrite,
			sdk.CapNetworkAudible, sdk.CapNetworkGeneric,
		},
		Run: p.runMetadataUpgrade,
	}
}

func (p *Plugin) runMetadataUpgrade(ctx context.Context, _ json.RawMessage, reporter sdk.Reporter) error {
	if !p.deps.HasMetadataFetchService() {
		return fmt.Errorf("metadata fetch service not initialized")
	}
	prog := sdk.NewProgress(reporter, 0)
	prog.Start("Scanning for books with upgradeable metadata sources...")
	_ = reporter.Log(slog.LevelInfo, "Scanning for books with upgradeable metadata sources...")
	// M7 (2026-07 error-correction sweep): thread the reporter through as a
	// progress sink so the 120-minute network-bound scan reports books
	// processed/total every 25 books instead of going silent between start
	// and result.
	checked, upgraded, skipped, errs, err := p.deps.MetadataUpgradeRun(ctx, 200, newOpsAdapter(reporter))
	if err != nil {
		return err
	}
	msg := fmt.Sprintf("Metadata upgrade complete: checked %d, upgraded %d, skipped %d, errors %d",
		checked, upgraded, skipped, errs)
	_ = reporter.Log(slog.LevelInfo, msg)
	prog.Done(msg)
	return nil
}

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

func (p *Plugin) runISBNEnrichment(ctx context.Context, _ json.RawMessage, reporter sdk.Reporter) error {
	if !p.deps.HasISBNEnrichment() {
		_ = reporter.Log(slog.LevelInfo, "ISBN enrichment service is not configured, skipping")
		return nil
	}
	opID := ctxOpID(ctx)
	return p.deps.RunIsbnEnrichment(ctx, newOpsAdapter(reporter), opID)
}
