// file: internal/plugins/maintenance/book_atpath_index.go
// version: 1.6.0
// guid: 6d2a9e41-7c3b-4f85-a0d6-8b1e5c9f2a74
// last-edited: 2026-10-25

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
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
//     With {"if_needed": true} it is instead the startup build: the
//     sentinel-gated BackfillBookAtPathIndex, a no-op once the first build has
//     set the sentinel. Boot enqueues it that way (Server.startBackfills,
//     bootOps; 04-P6 replaced the book-atpath-backfill goroutine).
//
// Both resolve their store method through database.AsCapability, not a bare
// assertion: OpsStore() can be a Store-embedding decorator (server.indexedStore, until
// 07-S3), which hides every *PebbleStore method outside database.Store.
// Neither method writes book rows, so unwrapping past a decorator's book-write
// overrides bypasses nothing.
//
// Readiness: the def has no NoWarmupWait, so the registry's dispatcher holds a
// queued run until the memdb warmup finishes (D44, registry.WarmupGate), which
// is what the goroutine's own WaitForWarmup did, so the build does not compete
// with warmup's full book scan.
//
// Liveness is manual: the store calls back every 10,000 rows and after every
// chunk commit, and each callback stamps UpdateProgress, so a long first build
// is never silent for the watchdog's 5 minutes. (Both modes: the forced
// rebuild had the same single stamp before a pass of the same length.)
//
// The rebuild fans out inside the store: backfillBookAtPath runs one iterator
// feeding a runtime.NumCPU() worker pool, each worker with its own batch. The
// verify stays two single-cursor scans from one snapshot, because its per-row
// step is a map insert and the diff needs the whole expected set in one place.

type bookAtPathVerifier interface {
	VerifyBookAtPathIndex(ctx context.Context) (database.BookAtPathIndexReport, error)
}

type bookAtPathRebuilder interface {
	RebuildBookAtPathIndex(ctx context.Context, progress database.BookAtPathProgress) (database.BookAtPathBackfillResult, error)
}

// bookAtPathBackfiller is the sentinel-gated startup build ({"if_needed": true}).
type bookAtPathBackfiller interface {
	BackfillBookAtPathIndex(ctx context.Context, progress database.BookAtPathProgress) (database.BookAtPathBackfillResult, error)
}

// bookAtPathBackfillParams are the backfill op's params. Absent params (an
// operator's run) decode to the zero value: the forced rebuild, as before.
type bookAtPathBackfillParams struct {
	// IfNeeded takes the sentinel-gated path boot used to run: skip when an
	// earlier pass set the one-time sentinel.
	IfNeeded bool `json:"if_needed,omitempty"`
}

func (p *Plugin) bookAtPathIndexVerifyDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:          "maintenance.book-atpath-index-verify",
		Liveness:    sdk.LivenessRunItems,
		Plugin:      "maintenance",
		DisplayName: "Book path-set index verify",
		Description: "Diffs the book_atpath: index (every book at each path) against the book " +
			"rows from one snapshot. Fails if any book, live OR soft-deleted, has no index key at " +
			"its path, which would let LiveBookIDsAtPath (live books) or BookIDsAtPath (every row; " +
			"the purge's check before it removes a file) report a taken path as free. Extras are " +
			"reported but do not fail. REPORT-ONLY: modifies nothing.",
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
		Liveness:    sdk.LivenessManual,
		Plugin:      "maintenance",
		DisplayName: "Book path-set index rebuild",
		Description: "Rewrites the book_atpath: index key for every book, ignoring the " +
			"one-time startup sentinel, then sets it. Run after any rollback to a build that " +
			"predates the index, then run the verify op. Writes index keys only, never book rows. " +
			"Enqueued with {\"if_needed\": true} (as boot does) it builds the index only when the " +
			"one-time sentinel is not yet set.",
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
			"extra_row_gone=%d extra_row_moved=%d malformed=%d undecodable_rows=%d "+
			"stale_markers=%d unmarked_undecodable=%d markers_not_in_set=%d",
		rep.SentinelSet, rep.BooksScanned, rep.IndexKeysScanned, rep.MissingLive,
		rep.MissingTrashed, rep.ExtraRowGone, rep.ExtraRowMoved, rep.Malformed, rep.UndecodableRows,
		rep.StaleMarkers, rep.UnmarkedUndecodable, rep.MarkersNotInSet)
	_ = reporter.Log(slog.LevelInfo, summary)
	if b, mErr := json.Marshal(rep); mErr == nil {
		_ = reporter.Log(slog.LevelInfo, "report: "+string(b))
	}
	_ = reporter.UpdateProgress(1, 1, summary)

	if rep.MissingLive > 0 {
		return fmt.Errorf("book_atpath index is INCOMPLETE: %d live book(s) have no key at their "+
			"path, so LiveBookIDsAtPath and BookIDsAtPath can report a taken path as free; run "+
			"maintenance.book-atpath-index-backfill (%s)", rep.MissingLive, summary)
	}
	if rep.MissingTrashed > 0 {
		// A trashed book is restorable and wants its audio. BookIDsAtPath
		// counts trashed rows, and a purge trusts it before removing a file.
		return fmt.Errorf("book_atpath index is INCOMPLETE: %d soft-deleted book(s) have no key at "+
			"their path, so BookIDsAtPath can report a path a restorable book names as free and a "+
			"purge with delete_files would remove its file (sample: %v); run "+
			"maintenance.book-atpath-index-backfill (%s)", rep.MissingTrashed, rep.SampleMissingTrashed, summary)
	}
	if rep.SentinelSet && rep.UnmarkedUndecodable > 0 {
		return fmt.Errorf("book_atpath index cannot see %d undecodable book row(s) (no marker), so "+
			"LiveBookIDsAtPath can report their path as free; run maintenance.book-atpath-index-backfill (%s)",
			rep.UnmarkedUndecodable, summary)
	}
	if rep.MarkersNotInSet > 0 {
		// The set is supposed to be a superset of disk, so something lost
		// these ids, and every lookup skips those undecodable rows until the
		// rebuild re-notes them. Verify stays read-only and only reports.
		return fmt.Errorf("book_atpath index: %d undecodable marker(s) on disk are missing from the in-memory "+
			"marker set, so LiveBookIDsAtPath fails open for them (sample ids: %v); run "+
			"maintenance.book-atpath-index-backfill, and report this as a marker-set bookkeeping bug (%s)",
			rep.MarkersNotInSet, rep.SampleNotInSet, summary)
	}
	if !rep.Complete() {
		return fmt.Errorf("book_atpath index verify is incomplete: %d book row(s) did not decode "+
			"and could not be checked (%s)", rep.UndecodableRows, summary)
	}
	return nil
}

// bookAtPathProgressTo turns the store's per-chunk callbacks into liveness
// stamps. The row total is unknown up front, so total is 0 and the message
// carries the count. Safe for the concurrent workers that call it.
func bookAtPathProgressTo(reporter sdk.Reporter) database.BookAtPathProgress {
	return func(scanned int) {
		_ = reporter.UpdateProgress(scanned, 0, fmt.Sprintf("book_atpath index: %d book rows staged", scanned))
	}
}

func (p *Plugin) runBookAtPathIndexBackfill(ctx context.Context, raw json.RawMessage, reporter sdk.Reporter) error {
	var params bookAtPathBackfillParams
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &params); err != nil {
			return fmt.Errorf("book-atpath-index-backfill: params: %w", err)
		}
	}
	store := p.deps.OpsStore()
	if store == nil {
		return fmt.Errorf("database not initialized")
	}
	if params.IfNeeded {
		b, ok := database.AsCapability[bookAtPathBackfiller](store)
		if !ok {
			// Boot enqueues this on every start. A store with no index (not
			// Pebble) has nothing to build and LiveBookIDsAtPath scans, as it
			// always has, so a red row on every boot would be noise. The
			// goroutine logged the same Warn. An operator's rebuild still
			// fails below.
			_ = reporter.Log(slog.LevelWarn, fmt.Sprintf("store is %T, which has no book_atpath: index "+
				"(and no store in its decorator chain does); nothing to build, LiveBookIDsAtPath stays on "+
				"the full scan", store), opsregistry.NoOpAttr())
			return nil
		}
		return backfillBookAtPathIfNeeded(ctx, b, reporter)
	}
	r, ok := database.AsCapability[bookAtPathRebuilder](store)
	if !ok {
		return fmt.Errorf("cannot run: store is %T, which has no book_atpath: index "+
			"(and no store in its decorator chain does)", store)
	}
	_ = reporter.UpdateProgress(0, 1, "rebuilding book_atpath: index")
	res, err := r.RebuildBookAtPathIndex(ctx, bookAtPathProgressTo(reporter))
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
		return bookAtPathUndecodableError(res)
	}
	_ = reporter.Log(slog.LevelInfo, msg)
	return nil
}

func bookAtPathUndecodableError(res database.BookAtPathBackfillResult) error {
	return fmt.Errorf("book_atpath index built, but %d book row(s) cannot be decoded (sample ids: "+
		"%v); LiveBookIDsAtPath refuses until each is rewritten or removed out of band, then run "+
		"maintenance.book-atpath-index-backfill", res.UndecodableRows, res.SampleUndecodable)
}

// backfillBookAtPathIfNeeded is the boot mode ({"if_needed": true}): the
// sentinel-gated build the goroutine ran. The outcomes match it, with each
// failure now a red op row instead of a log line only:
//
//   - sentinel already set (every boot after the first): nothing to do, logged
//     with opsregistry.NoOpAttr so the default activity feed hides it.
//   - ctx cancelled (shutdown, an operator cancel): an interruption, not a
//     failure. Logged at Info, ctx's error returned so the registry records the
//     run as canceled. The sentinel is not set; the next boot builds again.
//   - any other error, the def's Timeout (DeadlineExceeded) included: Error,
//     and the op fails. LiveBookIDsAtPath stays on the full scan.
//   - built, but some book rows cannot be decoded: the op fails, as the forced
//     rebuild does, because LiveBookIDsAtPath refuses while such a row stays.
//
// Until the sentinel is set LiveBookIDsAtPath serves from a full scan, so
// nothing is wrong while the build is pending, only slow.
func backfillBookAtPathIfNeeded(ctx context.Context, b bookAtPathBackfiller, reporter sdk.Reporter) error {
	_ = reporter.Log(slog.LevelDebug, "Starting book_atpath index backfill (skipped when the one-time sentinel is set)")
	_ = reporter.UpdateProgress(0, 1, "checking book_atpath: index sentinel")
	res, err := b.BackfillBookAtPathIndex(ctx, bookAtPathProgressTo(reporter))
	if err != nil {
		// Only a cancel is an interruption. The def's Timeout ends ctx with
		// DeadlineExceeded: a build too slow to finish leaves the sentinel
		// unset, which is a failure, not a clean stop.
		if errors.Is(ctx.Err(), context.Canceled) || (ctx.Err() == nil && errors.Is(err, context.Canceled)) {
			_ = reporter.Log(slog.LevelInfo, "book_atpath index build interrupted; LiveBookIDsAtPath stays on "+
				"the full scan until the next boot builds it", slog.String("error", err.Error()))
			if ctxErr := ctx.Err(); ctxErr != nil && !errors.Is(err, ctxErr) {
				return fmt.Errorf("book_atpath index build interrupted (%s): %w", err.Error(), ctxErr)
			}
			return fmt.Errorf("book_atpath index build interrupted: %w", err)
		}
		_ = reporter.Log(slog.LevelError, "book_atpath index not built; LiveBookIDsAtPath stays on the full "+
			"scan until a later boot or maintenance.book-atpath-index-backfill succeeds",
			slog.String("error", err.Error()))
		return fmt.Errorf("backfill book_atpath index (scanned %d before failing; the sentinel is not set): %w",
			res.Scanned, err)
	}
	if res.Skipped {
		_ = reporter.Log(slog.LevelInfo, "book_atpath index already built; nothing to do", opsregistry.NoOpAttr())
		return nil
	}
	msg := fmt.Sprintf("built book_atpath index: %d books, %d commits, %d undecodable",
		res.Scanned, res.Commits, res.UndecodableRows)
	_ = reporter.UpdateProgress(1, 1, msg)
	if res.UndecodableRows > 0 {
		_ = reporter.Log(slog.LevelError, msg)
		return bookAtPathUndecodableError(res)
	}
	_ = reporter.Log(slog.LevelInfo, msg)
	return nil
}
