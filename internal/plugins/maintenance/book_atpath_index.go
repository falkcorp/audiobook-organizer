// file: internal/plugins/maintenance/book_atpath_index.go
// version: 1.1.0
// guid: 6d2a9e41-7c3b-4f85-a0d6-8b1e5c9f2a74
// last-edited: 2026-09-12

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

// --- book_atpath: index ops ---
//
// The index itself, its invariant and its startup backfill live in
// internal/database/pebble_store_atpath_index.go. These two ops are the
// operator's handles on it:
//
//   - maintenance.book-atpath-index-verify: read-only diff of index vs rows.
//   - maintenance.book-atpath-index-backfill: unconditional rebuild, for the
//     rollback runbook (a pre-index binary ran and moved books without keys).
//
// Both resolve their store method through database.AsCapability, not a bare
// assertion: OpsStore() is server.indexedStore in production, which embeds the
// database.Store interface and so hides every *PebbleStore method outside it.
// Neither method writes book rows, so unwrapping past indexedStore's
// CreateBook/UpdateBook/DeleteBook overrides bypasses nothing.
//
// The rebuild fans out inside the store: backfillBookAtPath runs one iterator
// feeding a runtime.NumCPU() worker pool, each worker with its own batch. The
// verify stays two single-cursor scans from one snapshot, because its per-row
// step is a map insert and the diff needs the whole expected set in one place.

type bookAtPathVerifier interface {
	VerifyBookAtPathIndex(ctx context.Context) (database.BookAtPathIndexReport, error)
}

type bookAtPathRebuilder interface {
	RebuildBookAtPathIndex(ctx context.Context) (database.BookAtPathBackfillResult, error)
}

func (p *Plugin) bookAtPathIndexVerifyDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:          "maintenance.book-atpath-index-verify",
		Liveness:    sdk.LivenessRunItems,
		Plugin:      "maintenance",
		DisplayName: "Book path-set index verify",
		Description: "Diffs the book_atpath: index (every book at each path) against the book " +
			"rows from one snapshot. Fails if any LIVE book has no index key at its path, which " +
			"would let LiveBookIDsAtPath report a taken path as free. Extras and trashed-book " +
			"gaps are reported but do not fail. REPORT-ONLY: modifies nothing.",
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.book-atpath-index",
		Cancellable:     true,
		Timeout:         30 * time.Minute,
		// 🔴 READ ONLY. Never requests CapLibraryWrite, which is what makes it
		// safe to run against production at any time, including mid-scan.
		Capabilities: []sdk.Capability{sdk.CapLibraryRead},
		Run:          p.runBookAtPathIndexVerify,
	}
}

func (p *Plugin) bookAtPathIndexBackfillDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:          "maintenance.book-atpath-index-backfill",
		Liveness:    sdk.LivenessRunItems,
		Plugin:      "maintenance",
		DisplayName: "Book path-set index rebuild",
		Description: "Rewrites the book_atpath: index key for every book, ignoring the " +
			"one-time startup sentinel, then sets it. Run after any rollback to a build that " +
			"predates the index, then run the verify op. Writes index keys only, never book rows.",
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityLow,
		// Shares the verify op's key so a verify never reads a half-rebuilt index.
		ConcurrencyKey: "maintenance.book-atpath-index",
		Cancellable:    true,
		Timeout:        30 * time.Minute,
		Capabilities:   []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
		Run:            p.runBookAtPathIndexBackfill,
	}
}

func (p *Plugin) runBookAtPathIndexVerify(ctx context.Context, _ json.RawMessage, reporter sdk.Reporter) error {
	store := p.deps.OpsStore()
	if store == nil {
		return fmt.Errorf("database not initialized")
	}
	v, ok := database.AsCapability[bookAtPathVerifier](store)
	if !ok {
		return fmt.Errorf("cannot run: store is %T, which has no book_atpath: index "+
			"(and no store in its decorator chain does)", store)
	}
	return verifyBookAtPathIndex(ctx, v, reporter)
}

func verifyBookAtPathIndex(ctx context.Context, v bookAtPathVerifier, reporter sdk.Reporter) error {
	_ = reporter.UpdateProgress(0, 1, "verifying book_atpath: index")
	rep, err := v.VerifyBookAtPathIndex(ctx)
	if err != nil {
		return fmt.Errorf("verify book_atpath index: %w", err)
	}
	summary := fmt.Sprintf(
		"sentinel_set=%t books=%d keys=%d missing_live=%d missing_trashed=%d "+
			"extra_row_gone=%d extra_row_moved=%d malformed=%d undecodable_rows=%d",
		rep.SentinelSet, rep.BooksScanned, rep.IndexKeysScanned, rep.MissingLive,
		rep.MissingTrashed, rep.ExtraRowGone, rep.ExtraRowMoved, rep.Malformed, rep.UndecodableRows)
	_ = reporter.Log(slog.LevelInfo, summary)
	if b, mErr := json.Marshal(rep); mErr == nil {
		_ = reporter.Log(slog.LevelInfo, "report: "+string(b))
	}
	_ = reporter.UpdateProgress(1, 1, summary)

	if rep.MissingLive > 0 {
		return fmt.Errorf("book_atpath index is INCOMPLETE: %d live book(s) have no key at their "+
			"path, so LiveBookIDsAtPath can report a taken path as free; run "+
			"maintenance.book-atpath-index-backfill (%s)", rep.MissingLive, summary)
	}
	if !rep.Complete() {
		return fmt.Errorf("book_atpath index verify is incomplete: %d book row(s) did not decode "+
			"and could not be checked (%s)", rep.UndecodableRows, summary)
	}
	return nil
}

func (p *Plugin) runBookAtPathIndexBackfill(ctx context.Context, _ json.RawMessage, reporter sdk.Reporter) error {
	store := p.deps.OpsStore()
	if store == nil {
		return fmt.Errorf("database not initialized")
	}
	r, ok := database.AsCapability[bookAtPathRebuilder](store)
	if !ok {
		return fmt.Errorf("cannot run: store is %T, which has no book_atpath: index "+
			"(and no store in its decorator chain does)", store)
	}
	_ = reporter.UpdateProgress(0, 1, "rebuilding book_atpath: index")
	res, err := r.RebuildBookAtPathIndex(ctx)
	if err != nil {
		return fmt.Errorf("rebuild book_atpath index (scanned %d before failing; sentinel "+
			"state unchanged by this run): %w", res.Scanned, err)
	}
	msg := fmt.Sprintf("rebuilt book_atpath index: %d books, %d commits, %d undecodable",
		res.Scanned, res.Commits, res.UndecodableRows)
	_ = reporter.UpdateProgress(1, 1, msg)
	if res.UndecodableRows > 0 {
		// Same stance as the verify op: rows that could not be decoded were
		// not indexed, so the run is reported as incomplete, not green.
		_ = reporter.Log(slog.LevelError, msg)
		return fmt.Errorf("book_atpath index rebuilt, but %d book row(s) did not decode and "+
			"were not indexed (sample ids: %v)", res.UndecodableRows, res.SampleUndecodable)
	}
	_ = reporter.Log(slog.LevelInfo, msg)
	return nil
}
