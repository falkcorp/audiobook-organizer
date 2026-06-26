// file: internal/plugins/maintenance/reconcile.go
// version: 1.1.0
// guid: b8c9d0e1-f2a3-4567-1234-789012345678
// last-edited: 2026-06-25

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/operations"
	"github.com/falkcorp/audiobook-organizer/internal/reconcile"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// Hard rule: reconcile-scan = ResumeDrop (per UOS-12 spec; a full file-hash
// sweep that takes ~45 min; restarting mid-run would jam the queue).

func (p *Plugin) reconcileScanDef() sdk.OperationDef {
	sched := "0 3 * * *" // 03:00 daily
	return sdk.OperationDef{
		ID:              "maintenance.reconcile-scan",
		Plugin:          "maintenance",
		DisplayName:     "Reconcile scan",
		Description:     "Finds books with missing files and attempts to match them to untracked files on disk.",
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityNormal,
		ConcurrencyKey:  "maintenance.reconcile-scan",
		Cancellable:     true,
		Isolate:         false,
		Timeout:         180 * time.Minute,
		Schedule:        &sched,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapFilesRead},
		Run:             p.runReconcileScan,
	}
}

func (p *Plugin) runReconcileScan(ctx context.Context, _ json.RawMessage, reporter sdk.Reporter) error {
	store := p.deps.Store()
	if store == nil {
		return fmt.Errorf("database not initialized")
	}

	opID := ctxOpID(ctx)
	adapter := newOpsAdapter(reporter)
	reconcileLog := operations.LoggerFromReporter(adapter)

	result, err := reconcile.BuildReconcilePreviewWithProgress(store, reconcileLog)
	if err != nil {
		return fmt.Errorf("reconcile scan failed: %w", err)
	}

	resultJSON, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("failed to marshal scan results: %w", err)
	}
	if opID != "" {
		if err := store.UpdateOperationResultData(opID, string(resultJSON)); err != nil {
			return fmt.Errorf("failed to store scan results: %w", err)
		}
	}

	summary := fmt.Sprintf("Found %d broken records, %d matches, %d unmatched",
		len(result.BrokenRecords), len(result.Matches), len(result.UnmatchedBooks))
	_ = reporter.Log(slog.LevelInfo, summary)
	return nil
}

// itunesHealDef registers the iTunes XML-driven path healing operation.
// It parses the iTunes Library.xml, finds tracks whose expected path is
// missing on disk, locates the file via filename+author+track-number
// matching, and refllinks it back — 16 workers, completes in minutes.
func (p *Plugin) itunesHealDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:              "maintenance.itunes-heal",
		Plugin:          "maintenance",
		DisplayName:     "iTunes path heal",
		Description:     "Heals iTunes tracks moved by the organize operation: parses iTunes XML, finds each missing file in the library by filename/author/track, and reflinks it back to the expected path.",
		ResumePolicy:    sdk.ResumeRestart, // reflink is idempotent
		DefaultPriority: sdk.PriorityNormal,
		ConcurrencyKey:  "maintenance.itunes-heal",
		Cancellable:     true,
		Isolate:         false,
		Timeout:         60 * time.Minute,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapFilesRead, sdk.CapFilesWrite},
		Run:             p.runITunesHeal,
	}
}

func (p *Plugin) runITunesHeal(ctx context.Context, params json.RawMessage, reporter sdk.Reporter) error {
	return reconcile.RunITunesHeal(ctx, reporter, params)
}
