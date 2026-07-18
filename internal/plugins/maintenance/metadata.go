// file: internal/plugins/maintenance/metadata.go
// version: 1.1.0
// guid: a7b8c9d0-e1f2-3456-0123-678901234567
// last-edited: 2026-07-18

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
// Hard rule: ResumeRestart (checkpoint every 100 books).

func (p *Plugin) isbnEnrichmentDef() sdk.OperationDef {
	sched := "0 7 * * *" // 07:00 daily
	return sdk.OperationDef{
		ID:              "maintenance.isbn-enrichment",
		Plugin:          "maintenance",
		DisplayName:     "ISBN enrichment",
		Description:     "Searches external metadata sources for missing ISBN identifiers. Checkpoints every 100 books.",
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
