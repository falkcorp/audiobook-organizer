// file: internal/plugins/maintenance/opchange_book_index.go
// version: 1.0.0
// guid: d634d53a-0455-470e-8154-9d375b37ef69
// last-edited: 2026-10-03

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// --- opchange_by_book: index rebuild op ---
//
// The index, its invariant and its one-time startup backfill live in
// internal/database/pebble_store_opchange_index.go. This op is the operator's
// handle for the rollback runbook: a binary that predates the index writes
// opchange rows without entries, so after any rollback-then-roll-forward the
// index can under-report a book's journal. The rebuild clears the sentinel
// (GetBookChanges falls back to the full scan at once), re-indexes every row
// from the start, and sets the sentinel again. If it is cut, the next startup
// backfill resumes from its cursor.
//
// It resolves the store method through database.AsCapability, not a bare
// assertion: OpsStore() is server.indexedStore in production, which hides
// every *PebbleStore method outside database.Store. The rebuild writes index
// entries only, never journal rows.

type opChangeIndexRebuilder interface {
	RebuildOpChangeByBookIndex(ctx context.Context) (database.OpChangeByBookBackfillResult, error)
}

func (p *Plugin) opChangeBookIndexRebuildDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:          "maintenance.opchange-book-index-rebuild",
		Liveness:    sdk.LivenessRunItems,
		Plugin:      "maintenance",
		DisplayName: "Operation-journal by-book index rebuild",
		Description: "Rebuilds the opchange_by_book: index that GetBookChanges reads, ignoring the " +
			"one-time startup sentinel. Run after any rollback to a build that predates the index. " +
			"GetBookChanges uses the full journal scan while it runs. Writes index keys only, never " +
			"journal rows.",
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.opchange-book-index",
		Cancellable:     true,
		Timeout:         60 * time.Minute,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
		Run:             p.runOpChangeBookIndexRebuild,
	}
}

func (p *Plugin) runOpChangeBookIndexRebuild(ctx context.Context, _ json.RawMessage, reporter sdk.Reporter) error {
	store := p.deps.OpsStore()
	if store == nil {
		return fmt.Errorf("database not initialized")
	}
	r, ok := database.AsCapability[opChangeIndexRebuilder](store)
	if !ok {
		return fmt.Errorf("cannot run: store is %T, which has no opchange_by_book: index "+
			"(and no store in its decorator chain does)", store)
	}
	return rebuildOpChangeBookIndex(ctx, r, reporter)
}

func rebuildOpChangeBookIndex(ctx context.Context, r opChangeIndexRebuilder, reporter sdk.Reporter) error {
	_ = reporter.UpdateProgress(0, 1, "rebuilding opchange_by_book: index")
	res, err := r.RebuildOpChangeByBookIndex(ctx)
	if err != nil {
		return fmt.Errorf("rebuild opchange_by_book index (scanned %d before failing; the sentinel "+
			"stays cleared, so GetBookChanges uses the full scan until the next startup backfill "+
			"resumes and finishes it): %w", res.Scanned, err)
	}
	msg := fmt.Sprintf("rebuilt opchange_by_book index: %d rows scanned, %d indexed, %d undecodable, %d commits",
		res.Scanned, res.Indexed, res.Undecodable, res.Commits)
	_ = reporter.UpdateProgress(1, 1, msg)
	if res.Undecodable > 0 {
		// Same stance as the book_atpath rebuild: undecodable rows make every
		// GetBookChanges fail (as they did before the index), so not green.
		_ = reporter.Log(slog.LevelError, msg)
		return fmt.Errorf("opchange_by_book index rebuilt, but %d journal row(s) cannot be decoded; "+
			"GetBookChanges fails for every book until each is rewritten or removed", res.Undecodable)
	}
	_ = reporter.Log(slog.LevelInfo, msg)
	return nil
}
