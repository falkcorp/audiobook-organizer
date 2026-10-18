// file: internal/plugins/maintenance/opchange_book_index.go
// version: 1.6.0
// guid: d634d53a-0455-470e-8154-9d375b37ef69
// last-edited: 2026-10-18

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/opmode"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// --- opchange_by_book: index rebuild op ---
//
// The index and its invariant live in
// internal/database/pebble_store_opchange_index.go. A binary that predates
// the index (a rollback) writes opchange rows without entries, and the
// sentinel cannot know, so the index is untrusted at every boot until a
// verify of the whole journal passes. This op owns that check and the
// operator's repair, in three modes:
//
//   - `{}` (preview, the default; owner rule 2026-09-25): the read-only verify;
//     reports how many journal rows have no entry, writes nothing.
//   - `{"ensure": true}`: EnsureOpChangeByBookIndex, the boot path. Boot
//     enqueues it on EVERY start (Server.startBackfills, bootOps; 04-P7
//     replaced the opchange-index-backfill goroutine): the resumable backfill,
//     then the verify, then a rebuild if the verify is not clean. The index is
//     trusted only at the end of a clean verify or a successful rebuild. An
//     explicit write opt-in like dry_run=false; it must not be combined with
//     dry_run.
//   - `{"dry_run": false}`: the forced rebuild. It clears trust and the
//     sentinel (GetBookChanges falls back to the full scan at once),
//     re-indexes every row from the start, sets the sentinel again and trusts
//     the index only on success. If it is cut, the next boot's ensure resumes
//     from its cursor and verifies before trusting.
//
// Until the index is trusted every GetBookChanges serves from the full journal
// scan, which is correct, only slower; that gate is in the store.
//
// Readiness: the def has no NoWarmupWait, so the registry's dispatcher holds a
// queued run until the memdb warmup finishes (D44, registry.WarmupGate,
// bounded by registry.WarmupWaitTimeout).
//
// Concurrency: every mode is this one def, so its ConcurrencyKey serializes a
// boot ensure with a manual rebuild in the registry; the store's index-pass
// slot (lockOpChangeIdxRun) serializes them again underneath.
//
// Liveness is manual: the store calls back once per committed chunk
// (backfill, rebuild), every 10,000 rows (verify), and every 30s while queued
// behind another index pass; each callback stamps UpdateProgress. The wait for
// that slot gives up when ctx ends.
//
// It resolves the store methods through database.AsCapability, not a bare
// assertion: OpsStore() can be a Store-embedding decorator, which hides every
// *PebbleStore method outside database.Store. Every mode writes index keys
// only, never journal rows.

type opChangeIndexRebuilder interface {
	VerifyOpChangeByBookIndex(ctx context.Context, progress database.OpChangeIndexProgress) (database.OpChangeByBookIndexReport, error)
	RebuildOpChangeByBookIndex(ctx context.Context, progress database.OpChangeIndexProgress) (database.OpChangeByBookBackfillResult, error)
	EnsureOpChangeByBookIndex(ctx context.Context, progress database.OpChangeIndexProgress) (database.OpChangeByBookEnsureResult, error)
}

// opChangeBookIndexParams: dry_run/dryRun as every preview-default op, plus
// ensure (the boot mode).
type opChangeBookIndexParams struct {
	opmode.DryRunParams
	Ensure bool `json:"ensure,omitempty"`
}

// opChangeBookIndexStartCheck refuses ensure=true at start: a preview or live
// start always carries dry_run, and Run refuses ensure beside dry_run, so the
// run could only fail. ensure is the boot path, enqueued by the server itself.
func opChangeBookIndexStartCheck(params map[string]json.RawMessage, _ bool) error {
	raw, ok := params["ensure"]
	if !ok {
		return nil
	}
	var ensure bool
	if err := json.Unmarshal(raw, &ensure); err != nil {
		return fmt.Errorf("ensure must be true or false")
	}
	if ensure {
		return fmt.Errorf("%s: ensure=true is the boot mode and cannot be combined with dry_run, which every start carries; "+
			"send dry_run alone (preview) or dry_run=false (rebuild)", opChangeBookIndexRebuildID)
	}
	return nil
}

const opChangeBookIndexRebuildID = "maintenance.opchange-book-index-rebuild"

func (p *Plugin) opChangeBookIndexRebuildDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:          opChangeBookIndexRebuildID,
		Preview:     sdk.PreviewSpec{Params: opChangeBookIndexParams{}, StartCheck: opChangeBookIndexStartCheck},
		Liveness:    sdk.LivenessManual,
		Plugin:      "maintenance",
		DisplayName: "Operation-journal by-book index rebuild",
		Description: "Checks and repairs the opchange_by_book: index that GetBookChanges reads. " +
			"Preview by default: reports journal rows with no index entry, writes nothing. With " +
			"ensure=true (boot enqueues this on every start) it builds or resumes the index, verifies " +
			"every journal row against it, rebuilds if any row lacks an entry (e.g. after a rollback) " +
			"and only then lets GetBookChanges read it. With dry_run=false it rebuilds from scratch; " +
			"GetBookChanges uses the full journal scan while it runs and trusts the index again only " +
			"if it succeeds. Writes index keys only, never journal rows.",
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
	var params opChangeBookIndexParams
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &params); err != nil {
			return fmt.Errorf("decode params: %w", err)
		}
	}
	if params.Ensure && (params.DryRun != nil || params.DryRunCamel != nil) {
		// ensure is its own write mode; dry_run beside it is either a
		// contradiction (true) or names a different write (false: rebuild).
		return fmt.Errorf("%s: ensure=true cannot be combined with dry_run; send one of {\"ensure\": true} "+
			"(verify, rebuild if needed) or {\"dry_run\": false} (forced rebuild)", opChangeBookIndexRebuildID)
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
		if params.Ensure {
			// Boot enqueues the ensure on every start. A store with no index
			// (not Pebble) has nothing to check and GetBookChanges scans, as
			// it always has, so this is not a failure: a red row on every
			// boot would be noise. The goroutine logged the same Warn. An
			// operator's preview or rebuild still fails below.
			_ = reporter.Log(slog.LevelWarn, fmt.Sprintf("store is %T, which has no opchange_by_book: index "+
				"(and no store in its decorator chain does); nothing to ensure, GetBookChanges uses the "+
				"full journal scan", store), opsregistry.NoOpAttr())
			return nil
		}
		return fmt.Errorf("cannot run: store is %T, which has no opchange_by_book: index "+
			"(and no store in its decorator chain does)", store)
	}
	switch {
	case params.Ensure:
		return ensureOpChangeBookIndex(ctx, r, reporter)
	case dryRun:
		return previewOpChangeBookIndex(ctx, r, reporter)
	default:
		return rebuildOpChangeBookIndex(ctx, r, reporter)
	}
}

// opChangeIndexProgressTo turns the store's progress callbacks into liveness
// stamps. The row total is unknown up front, so total is 0 and the message
// carries the count.
func opChangeIndexProgressTo(reporter sdk.Reporter) database.OpChangeIndexProgress {
	return func(phase string, rows int) {
		msg := fmt.Sprintf("opchange_by_book index %s: %d journal rows read", phase, rows)
		if phase == "waiting" {
			msg = "waiting for another opchange_by_book index pass (e.g. the boot ensure or a manual rebuild) to finish"
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
			"and trust stay cleared, so GetBookChanges uses the full scan until the next boot's "+
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

// ensureOpChangeBookIndex is the boot mode ({"ensure": true}): backfill or
// resume, verify, rebuild on a dirty verify (EnsureOpChangeByBookIndex). The
// outcomes match the goroutine it replaced, with each failure now a red op
// row instead of a log line only:
//
//   - ctx cancelled (shutdown, an operator cancel): an interruption, not a
//     failure. Logged at Info, and ctx's error is returned so the registry
//     records the run as canceled. Nothing is trusted; the next boot retries.
//   - any other error, the def's Timeout (DeadlineExceeded) included, or a
//     run that ends without a trusted index: Error, and the op fails.
//     GetBookChanges stays on the full scan.
//   - trusted, but the journal holds undecodable rows: the op fails, as the
//     rebuild mode does, because GetBookChanges fails for every book until
//     each such row is rewritten or removed.
//   - trusted with nothing written (backfill already done, verify clean, no
//     rebuild): the usual boot outcome, logged with opsregistry.NoOpAttr so
//     the default activity feed hides it.
//
// A store with no index at all (not Pebble) never reaches here: the run
// logs a Warn tagged NoOpAttr and succeeds (runOpChangeBookIndexRebuild).
func ensureOpChangeBookIndex(ctx context.Context, r opChangeIndexRebuilder, reporter sdk.Reporter) error {
	_ = reporter.Log(slog.LevelDebug, "Starting opchange_by_book index ensure (backfill if needed, verify, rebuild if dirty)")
	_ = reporter.UpdateProgress(0, 1, "checking opchange_by_book: index")
	res, err := r.EnsureOpChangeByBookIndex(ctx, opChangeIndexProgressTo(reporter))
	if err != nil {
		// Only a cancel is an interruption (shutdown, an operator, or the
		// watchdog, which logs its own kill). The def's Timeout ends ctx with
		// DeadlineExceeded: a run too slow to finish leaves the index
		// untrusted, which is a failure, not a clean stop.
		if errors.Is(ctx.Err(), context.Canceled) || (ctx.Err() == nil && errors.Is(err, context.Canceled)) {
			_ = reporter.Log(slog.LevelInfo, "opchange_by_book index check interrupted; GetBookChanges "+
				"uses the full journal scan until the next boot checks it",
				slog.String("error", err.Error()))
			// Keep the store's error when it already carries ctx's (it names
			// the step that stopped); otherwise return ctx's, so the
			// registry still sees the cancel.
			if ctxErr := ctx.Err(); ctxErr != nil && !errors.Is(err, ctxErr) {
				return fmt.Errorf("opchange_by_book index ensure interrupted (%s): %w", err.Error(), ctxErr)
			}
			return fmt.Errorf("opchange_by_book index ensure interrupted: %w", err)
		}
		_ = reporter.Log(slog.LevelError, "opchange_by_book index not trusted; GetBookChanges stays on the "+
			"full journal scan until a later ensure or rebuild succeeds", slog.String("error", err.Error()))
		return fmt.Errorf("ensure opchange_by_book index: %w", err)
	}
	if !res.Trusted {
		msg := fmt.Sprintf("opchange_by_book index not trusted after ensure (rebuilt=%t sentinel_set=%t "+
			"missing_entries=%d unmarked_undecodable=%d); GetBookChanges stays on the full journal scan",
			res.Rebuilt, res.Verify.SentinelSet, res.Verify.MissingEntries, res.Verify.UnmarkedUndecodable)
		_ = reporter.Log(slog.LevelError, msg)
		return errors.New(msg)
	}
	undecodable := res.Verify.Undecodable
	if res.Rebuilt {
		undecodable = res.Rebuild.Undecodable
	}
	summary := fmt.Sprintf("opchange_by_book index trusted: backfill scanned=%d indexed=%d skipped=%t; "+
		"verify rows=%d missing_entries=%d undecodable=%d; rebuilt=%t",
		res.Backfill.Scanned, res.Backfill.Indexed, res.Backfill.Skipped,
		res.Verify.Rows, res.Verify.MissingEntries, res.Verify.Undecodable, res.Rebuilt)
	if res.Rebuilt {
		summary += fmt.Sprintf(" (rebuild scanned=%d indexed=%d)", res.Rebuild.Scanned, res.Rebuild.Indexed)
	}
	_ = reporter.UpdateProgress(1, 1, summary)
	if undecodable > 0 {
		_ = reporter.Log(slog.LevelError, summary)
		return fmt.Errorf("opchange_by_book index trusted, but %d journal row(s) cannot be decoded; "+
			"GetBookChanges fails for every book until each is rewritten or removed", undecodable)
	}
	if res.Backfill.Skipped && !res.Rebuilt {
		_ = reporter.Log(slog.LevelInfo, "opchange_by_book index already complete and verified; nothing to do: "+summary,
			opsregistry.NoOpAttr())
		return nil
	}
	_ = reporter.Log(slog.LevelInfo, summary)
	return nil
}
