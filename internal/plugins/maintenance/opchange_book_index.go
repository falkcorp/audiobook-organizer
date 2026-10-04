// file: internal/plugins/maintenance/opchange_book_index.go
// version: 1.2.1
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
	"github.com/falkcorp/audiobook-organizer/internal/operations/opmode"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// --- opchange_by_book: index rebuild op ---
//
// The index, its invariant and its one-time startup backfill live in
// internal/database/pebble_store_opchange_index.go. This op is the operator's
// handle for the rollback runbook: a binary that predates the index writes
// opchange rows without entries, so after any rollback-then-roll-forward the
// index can under-report a book's journal. Every boot's
// EnsureOpChangeByBookIndex already verifies the index before readers trust
// it and rebuilds on its own when the verify is not clean, so this op is the
// manual handle for the same repair. The rebuild clears trust and the
// sentinel (GetBookChanges falls back to the full scan at once), re-indexes
// every row from the start, sets the sentinel again and trusts the index only
// on success. If it is cut, the next startup ensure resumes from its cursor
// and verifies before trusting.
//
// Liveness is manual: the store calls back once per committed chunk (rebuild),
// every 10,000 rows (verify), and every 30s while queued behind another index
// pass (the startup ensure holds the same slot); each callback stamps
// UpdateProgress. The wait for that slot gives up when ctx ends.
//
// It resolves the store method through database.AsCapability, not a bare
// assertion: OpsStore() is server.indexedStore in production, which hides
// every *PebbleStore method outside database.Store. The rebuild writes index
// entries only, never journal rows.
//
// Preview by default (owner rule 2026-09-25): `{}` runs the read-only verify
// and reports how many journal rows have no entry; `{"dry_run": false}`
// rebuilds.

type opChangeIndexRebuilder interface {
	VerifyOpChangeByBookIndex(ctx context.Context, progress database.OpChangeIndexProgress) (database.OpChangeByBookIndexReport, error)
	RebuildOpChangeByBookIndex(ctx context.Context, progress database.OpChangeIndexProgress) (database.OpChangeByBookBackfillResult, error)
}

const opChangeBookIndexRebuildID = "maintenance.opchange-book-index-rebuild"

func (p *Plugin) opChangeBookIndexRebuildDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:          opChangeBookIndexRebuildID,
		Liveness:    sdk.LivenessManual,
		Plugin:      "maintenance",
		DisplayName: "Operation-journal by-book index rebuild",
		Description: "Manual rebuild of the opchange_by_book: index that GetBookChanges reads. Every " +
			"boot already verifies the index before reads use it and rebuilds on its own when rows " +
			"lack entries (e.g. after a rollback), so this is the manual handle for the same repair. " +
			"Preview by default: reports journal rows with no index entry, writes nothing. With " +
			"dry_run=false it rebuilds; GetBookChanges uses the full journal scan while it runs and " +
			"trusts the index again only if it succeeds. Writes index keys only, never journal rows.",
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.opchange-book-index",
		Cancellable:     true,
		Timeout:         60 * time.Minute,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
		Run:             p.runOpChangeBookIndexRebuild,
	}
}

func (p *Plugin) runOpChangeBookIndexRebuild(ctx context.Context, raw json.RawMessage, reporter sdk.Reporter) error {
	var params opmode.DryRunParams
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &params); err != nil {
			return fmt.Errorf("decode params: %w", err)
		}
	}
	dryRun, err := opmode.ResolveDryRun(opChangeBookIndexRebuildID, params.DryRun, params.DryRunCamel)
	if err != nil {
		return err
	}
	store := p.deps.OpsStore()
	if store == nil {
		return fmt.Errorf("database not initialized")
	}
	r, ok := database.AsCapability[opChangeIndexRebuilder](store)
	if !ok {
		return fmt.Errorf("cannot run: store is %T, which has no opchange_by_book: index "+
			"(and no store in its decorator chain does)", store)
	}
	if dryRun {
		return previewOpChangeBookIndex(ctx, r, reporter)
	}
	return rebuildOpChangeBookIndex(ctx, r, reporter)
}

// opChangeIndexProgressTo turns the store's progress callbacks into liveness
// stamps. The row total is unknown up front, so total is 0 and the message
// carries the count.
func opChangeIndexProgressTo(reporter sdk.Reporter) database.OpChangeIndexProgress {
	return func(phase string, rows int) {
		msg := fmt.Sprintf("opchange_by_book index %s: %d journal rows read", phase, rows)
		if phase == "waiting" {
			msg = "waiting for another opchange_by_book index pass (e.g. the startup verify) to finish"
		}
		_ = reporter.UpdateProgress(rows, 0, msg)
	}
}

// previewOpChangeBookIndex is the read-only mode: it reports what a rebuild
// would fix and writes nothing.
func previewOpChangeBookIndex(ctx context.Context, r opChangeIndexRebuilder, reporter sdk.Reporter) error {
	_ = reporter.UpdateProgress(0, 1, "verifying opchange_by_book: index (preview, writes nothing)")
	rep, err := r.VerifyOpChangeByBookIndex(ctx, opChangeIndexProgressTo(reporter))
	if err != nil {
		return fmt.Errorf("verify opchange_by_book index: %w", err)
	}
	msg := fmt.Sprintf("preview: sentinel_set=%t rows=%d indexable=%d missing_entries=%d "+
		"undecodable=%d unmarked_undecodable=%d; run with dry_run=false to rebuild",
		rep.SentinelSet, rep.Rows, rep.Indexable, rep.MissingEntries, rep.Undecodable, rep.UnmarkedUndecodable)
	_ = reporter.Log(slog.LevelInfo, msg)
	if len(rep.SampleMissing) > 0 {
		_ = reporter.Log(slog.LevelInfo, fmt.Sprintf("sample rows with no entry: %v", rep.SampleMissing))
	}
	_ = reporter.UpdateProgress(1, 1, msg)
	return nil
}

func rebuildOpChangeBookIndex(ctx context.Context, r opChangeIndexRebuilder, reporter sdk.Reporter) error {
	_ = reporter.UpdateProgress(0, 1, "rebuilding opchange_by_book: index")
	res, err := r.RebuildOpChangeByBookIndex(ctx, opChangeIndexProgressTo(reporter))
	if err != nil {
		return fmt.Errorf("rebuild opchange_by_book index (scanned %d before failing; the sentinel "+
			"and trust stay cleared, so GetBookChanges uses the full scan until the next startup "+
			"ensure resumes, verifies and trusts it): %w", res.Scanned, err)
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
