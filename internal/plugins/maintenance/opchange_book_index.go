// file: internal/plugins/maintenance/opchange_book_index.go
// version: 1.8.0
// guid: d634d53a-0455-470e-8154-9d375b37ef69
// last-edited: 2026-11-13

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/falkcorp/mediacore/tasks"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/operations/opmode"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// --- opchange_by_book: index rebuild op ---
//
// The index and its invariant live in
// internal/books/store/pebble_store_opchange_index.go. A binary that predates
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
//     then the resumable verify, then a rebuild if the verify is not clean. The
//     index is trusted only at the end of a clean verify pass that reached the
//     end of the journal, or a successful rebuild. An explicit write opt-in like
//     dry_run=false; it must not be combined with dry_run.
//
//     The verify keeps its cursor in the store (not in the registry's
//     checkpoint), so the next boot's NEW operation continues from it, and a
//     journal too big to verify inside one Timeout is finished across runs:
//     once less than opChangeVerifyYieldMargin of the deadline is left, the run
//     stops at a durable cursor and returns registry.ErrContinueInPlace; the
//     registry re-queues the SAME operation (ResumeRestart) with a fresh
//     Timeout. Once a pass has covered the journal, later boots verify only the
//     rows after the cursor (pebble_store_opchange_verify.go).
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
	opChangeIndexVerifier
	opChangeIndexBuilder
}

// opChangeIndexVerifier is the read half of the index surface: the verify.
type opChangeIndexVerifier interface {
	VerifyOpChangeByBookIndex(ctx context.Context, progress database.OpChangeIndexProgress) (database.OpChangeByBookIndexReport, error)
}

// opChangeIndexBuilder is the write half: the two modes that write index keys.
type opChangeIndexBuilder interface {
	RebuildOpChangeByBookIndex(ctx context.Context, progress database.OpChangeIndexProgress) (database.OpChangeByBookBackfillResult, error)
	EnsureOpChangeByBookIndexWith(ctx context.Context, opts database.OpChangeEnsureOptions) (database.OpChangeByBookEnsureResult, error)
}

// opChangeVerifyYieldMargin is the least time left before the run's deadline at
// which the ensure's verify stops at a durable cursor and hands the operation
// back (registry.ErrContinueInPlace). The store uses the larger of this and
// twice the slowest gap between its checkpoints, so a window that starts just
// before the line still finishes well inside the deadline. The same margin
// backfill-file-hashes and the 04-P6 boot ops use.
const opChangeVerifyYieldMargin = 5 * time.Minute

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

// opChangeBookIndexHelp is the def's Description, which the native kind carries
// as its Help.
const opChangeBookIndexHelp = "Checks and repairs the opchange_by_book: index that GetBookChanges reads. " +
	"Preview by default: reports journal rows with no index entry, writes nothing. With " +
	"ensure=true (boot enqueues this on every start) it builds or resumes the index, verifies " +
	"the journal against it (resuming from a saved cursor, so a journal too big for one run is " +
	"finished across runs, and checking only the rows added since once a pass has covered it), " +
	"rebuilds if any row lacks an entry (e.g. after a rollback) and only then lets GetBookChanges read it. With dry_run=false it rebuilds from scratch; " +
	"GetBookChanges uses the full journal scan while it runs and trusts the index again only " +
	"if it succeeds. Writes index keys only, never journal rows."

const opChangeBookIndexRebuildID = "maintenance.opchange-book-index-rebuild"

func (p *Plugin) opChangeBookIndexRebuildDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:          opChangeBookIndexRebuildID,
		Preview:     sdk.PreviewSpec{Params: opChangeBookIndexParams{}, StartCheck: opChangeBookIndexStartCheck},
		Liveness:    sdk.LivenessManual,
		Plugin:      "maintenance",
		DisplayName: "Operation-journal by-book index rebuild",
		Description: opChangeBookIndexHelp,
		// ResumeRestart, not ResumeDrop: the registry honours
		// ErrContinueInPlace only for a def that resumes in place, and the ensure
		// yields with one (a verify too big for one Timeout). Every mode is
		// idempotent and keeps its resume point in the STORE (the backfill and
		// verify cursors), so a restart or a yield needs no registry checkpoint.
		// A forced rebuild (dry_run=false) interrupted by a restart starts over
		// from the first row, as an operator's re-run would; the boot ensure
		// resumes the cursor it left. Because the cursors are the store's, Run
		// never calls reporter.Checkpoint; internal/arch's
		// restartWithoutCheckpoint names this def with that reason, and
		// TestResumeRestartDefsCheckpoint fails if either side changes.
		ResumePolicy:    sdk.ResumeRestart,
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
	env := opChangeBookIndexEnv{
		t6g6Env:         t6g6HeadEnv(reporter, !dryRun),
		ContinueInPlace: opsregistry.ErrContinueInPlace,
	}
	// A nil OpsStore leaves env.Store nil (a typed-nil adapter would hide it), so
	// the core's "database not initialized" check still fires.
	if store := p.deps.OpsStore(); store != nil {
		env.Store = t6g6HeadStore{OpsStore: store}
		env.Writer = t6g6HeadWriter{deps: p.deps, store: store}
	}
	return opChangeBookIndexCore(ctx, params.Ensure, env)
}

// opChangeBookIndexKind is rule R3's kind for the def above.
const opChangeBookIndexKind = tasks.Kind("books." + opChangeBookIndexRebuildID)

// opChangeBookIndexStore is the read the op makes: it resolves the store's verify
// surface through database.AsCapability, which sees through the Store-embedding
// decorators.
type opChangeBookIndexStore interface {
	// OpChangeIndexVerifier returns the verify surface and a description of the
	// store ("%T") for the refusal messages; ok is false when no store in the
	// decorator chain has the index.
	OpChangeIndexVerifier() (v opChangeIndexVerifier, desc string, ok bool)
}

// opChangeBookIndexWriter is the writes the op makes: the two modes that write
// index keys, never journal rows. Whether the store has the index is the
// Store's answer (OpChangeIndexVerifier), asked first.
type opChangeBookIndexWriter interface {
	opChangeIndexBuilder
}

// opChangeBookIndexEnv is what the core takes from its shell.
type opChangeBookIndexEnv struct {
	t6g6Env
	Store  opChangeBookIndexStore
	Writer opChangeBookIndexWriter
	// ContinueInPlace is the error the ensure's yield wraps: the registry's
	// ErrContinueInPlace at HEAD, tasks.ErrContinueInPlace natively.
	ContinueInPlace error
}

// opChangeBookIndexParallelism is the op's concurrency. HEAD declared none and
// the store's index passes are serialized by its own slot.
func opChangeBookIndexParallelism() tasks.Parallelism {
	return tasks.Sequential("HEAD declared no concurrency; the store's index-pass slot serializes every mode")
}

// opChangeBookIndexNativeParams are the native kind's params: HEAD's without
// dry_run, which the run's mode carries (Live writes, Preview verifies).
type opChangeBookIndexNativeParams struct {
	Ensure bool `json:"ensure,omitempty"`
}

// opChangeBookIndexNative is the native kind. It writes index keys, and only in
// Live: a Preview run is the read-only verify; ensure is a write mode and is
// refused in Preview.
func (p *Plugin) opChangeBookIndexNative(deps t6g6Deps, sched func(tasks.Kind) *tasks.Schedule) tasks.Definition {
	c := t6g6Common(opChangeBookIndexKind, sched)
	c.Title = "Operation-journal by-book index rebuild"
	c.Help = opChangeBookIndexHelp
	c.Effects = tasks.Writes(tasks.Res("books", "operations"))
	c.Resume = tasks.ResumeContinue
	c.Priority = tasks.PriorityLow
	c.ConcurrencyKey = tasks.Key("books.maintenance.opchange-book-index")
	c.Timeout = 60 * time.Minute
	return tasks.Task(opChangeBookIndexKind, tasks.TaskSpec[opChangeBookIndexNativeParams, struct{}]{
		Common: c,
		Run: func(rc *tasks.RC, np opChangeBookIndexNativeParams) (struct{}, error) {
			if np.Ensure && rc.Mode() != tasks.ModeLive {
				return struct{}{}, fmt.Errorf("%s: ensure=true writes index keys; start with mode live", opChangeBookIndexKind)
			}
			env := opChangeBookIndexEnv{
				t6g6Env:         t6g6NativeEnv(rc),
				ContinueInPlace: tasks.ErrContinueInPlace,
			}
			if deps.Store != nil {
				env.Store = deps.Store
			}
			// A Preview run is read-only and has no writer to build; the verify
			// goes through the store.
			if rc.Mode() == tasks.ModeLive {
				env.Writer = deps.NewWriter(rc.Writer())
			}
			return struct{}{}, opChangeBookIndexCore(rc.Context(), np.Ensure, env)
		},
	})
}

// opChangeBookIndexCore is the body of runOpChangeBookIndexRebuild after the
// params are decoded and the mode resolved: env.Live is "not a dry run".
func opChangeBookIndexCore(ctx context.Context, ensure bool, env opChangeBookIndexEnv) error {
	if env.Store == nil {
		return fmt.Errorf("database not initialized")
	}
	v, desc, ok := env.Store.OpChangeIndexVerifier()
	if !ok {
		if ensure {
			// Boot enqueues the ensure on every start. A store with no index
			// (not Pebble) has nothing to check and GetBookChanges scans, as
			// it always has, so this is not a failure: a red row on every
			// boot would be noise. The goroutine logged the same Warn. An
			// operator's preview or rebuild still fails below.
			env.Log(slog.LevelWarn, fmt.Sprintf("store is %s, which has no opchange_by_book: index "+
				"(and no store in its decorator chain does); nothing to ensure, GetBookChanges uses the "+
				"full journal scan", desc), opsregistry.NoOpAttr())
			return nil
		}
		return fmt.Errorf("cannot run: store is %s, which has no opchange_by_book: index "+
			"(and no store in its decorator chain does)", desc)
	}
	switch {
	case ensure:
		return ensureOpChangeBookIndexVia(ctx, env.Writer, env)
	case !env.Live:
		return previewOpChangeBookIndexVia(ctx, v, env)
	default:
		return rebuildOpChangeBookIndexVia(ctx, env.Writer, env)
	}
}

// opChangeIndexProgressTo turns the store's progress callbacks into liveness
// stamps. The row total is unknown up front, so total is 0 and the message
// carries the count.
func opChangeIndexProgressTo(reporter sdk.Reporter) database.OpChangeIndexProgress {
	return t6g6OpChangeIndexProgressVia(t6g6HeadEnvForOpChange(reporter))
}

// t6g6OpChangeIndexProgressVia is opChangeIndexProgressTo over the env's hooks.
func t6g6OpChangeIndexProgressVia(env opChangeBookIndexEnv) database.OpChangeIndexProgress {
	return func(phase string, rows int) {
		msg := fmt.Sprintf("opchange_by_book index %s: %d journal rows read", phase, rows)
		if phase == "waiting" {
			msg = "waiting for another opchange_by_book index pass (e.g. the boot ensure or a manual rebuild) to finish"
		}
		env.Progress(rows, 0, msg)
	}
}

// opChangeEnsureProgress is opChangeIndexProgressTo for the ensure: the verify
// phase reports a determinate (rows, total) once the store has sized the pass,
// because the registry counts progress toward a yield only against a total
// (an indeterminate report advances nothing), and rows is absolute across the
// runs of one verify. The other phases stay indeterminate.
func opChangeEnsureProgress(reporter sdk.Reporter) (database.OpChangeIndexProgress, func(total int)) {
	return t6g6OpChangeEnsureProgressVia(t6g6HeadEnvForOpChange(reporter))
}

// t6g6OpChangeEnsureProgressVia is opChangeEnsureProgress over the env's hooks.
func t6g6OpChangeEnsureProgressVia(env opChangeBookIndexEnv) (database.OpChangeIndexProgress, func(total int)) {
	var total atomic.Int64
	progress := func(phase string, rows int) {
		if phase != "verify" {
			t6g6OpChangeIndexProgressVia(env)(phase, rows)
			return
		}
		t := max(int(total.Load()), rows)
		env.Progress(rows, t, fmt.Sprintf("opchange_by_book index verify: %d of %d journal rows checked", rows, t))
	}
	return progress, func(n int) { total.Store(int64(n)) }
}

// previewOpChangeBookIndex is the read-only mode: it reports what a rebuild
// would fix and writes nothing.
func previewOpChangeBookIndex(ctx context.Context, r opChangeIndexRebuilder, reporter sdk.Reporter) error {
	return previewOpChangeBookIndexVia(ctx, r, t6g6HeadEnvForOpChange(reporter))
}

// previewOpChangeBookIndexVia is previewOpChangeBookIndex over the env's hooks.
func previewOpChangeBookIndexVia(ctx context.Context, r opChangeIndexVerifier, env opChangeBookIndexEnv) error {
	env.Progress(0, 1, "verifying opchange_by_book: index (preview, writes nothing)")
	rep, err := r.VerifyOpChangeByBookIndex(ctx, t6g6OpChangeIndexProgressVia(env))
	if err != nil {
		return fmt.Errorf("verify opchange_by_book index: %w", err)
	}
	msg := fmt.Sprintf("preview: sentinel_set=%t rows=%d indexable=%d missing_entries=%d "+
		"undecodable=%d unmarked_undecodable=%d; run with dry_run=false to rebuild",
		rep.SentinelSet, rep.Rows, rep.Indexable, rep.MissingEntries, rep.Undecodable, rep.UnmarkedUndecodable)
	env.Log(slog.LevelInfo, msg)
	if len(rep.SampleMissing) > 0 {
		env.Log(slog.LevelInfo, fmt.Sprintf("sample rows with no entry: %v", rep.SampleMissing))
	}
	env.Progress(1, 1, msg)
	return nil
}

func rebuildOpChangeBookIndex(ctx context.Context, r opChangeIndexRebuilder, reporter sdk.Reporter) error {
	return rebuildOpChangeBookIndexVia(ctx, r, t6g6HeadEnvForOpChange(reporter))
}

// rebuildOpChangeBookIndexVia is rebuildOpChangeBookIndex over the env's hooks.
func rebuildOpChangeBookIndexVia(ctx context.Context, r opChangeIndexBuilder, env opChangeBookIndexEnv) error {
	env.Progress(0, 1, "rebuilding opchange_by_book: index")
	res, err := r.RebuildOpChangeByBookIndex(ctx, t6g6OpChangeIndexProgressVia(env))
	if err != nil {
		return fmt.Errorf("rebuild opchange_by_book index (scanned %d before failing; the sentinel "+
			"and trust stay cleared, so GetBookChanges uses the full scan until the next boot's "+
			"ensure resumes, verifies and trusts it): %w", res.Scanned, err)
	}
	msg := fmt.Sprintf("rebuilt opchange_by_book index: %d rows scanned, %d indexed, %d undecodable, %d commits",
		res.Scanned, res.Indexed, res.Undecodable, res.Commits)
	env.Progress(1, 1, msg)
	if res.Undecodable > 0 {
		// Same stance as the book_atpath rebuild: undecodable rows make every
		// GetBookChanges fail (as they did before the index), so not green.
		env.Log(slog.LevelError, msg)
		return fmt.Errorf("opchange_by_book index rebuilt, but %d journal row(s) cannot be decoded; "+
			"GetBookChanges fails for every book until each is rewritten or removed", res.Undecodable)
	}
	env.Log(slog.LevelInfo, msg)
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
	return ensureOpChangeBookIndexVia(ctx, r, t6g6HeadEnvForOpChange(reporter))
}

// ensureOpChangeBookIndexVia is ensureOpChangeBookIndex over the env's hooks.
func ensureOpChangeBookIndexVia(ctx context.Context, r opChangeIndexBuilder, env opChangeBookIndexEnv) error {
	env.Log(slog.LevelDebug, "Starting opchange_by_book index ensure (backfill if needed, verify, rebuild if dirty)")
	env.Progress(0, 1, "checking opchange_by_book: index")
	progress, setTotal := t6g6OpChangeEnsureProgressVia(env)
	res, err := r.EnsureOpChangeByBookIndexWith(ctx, database.OpChangeEnsureOptions{
		Progress: progress, VerifyTotal: setTotal, YieldMargin: opChangeVerifyYieldMargin,
	})
	if err != nil {
		// Only a cancel is an interruption (shutdown, an operator, or the
		// watchdog, which logs its own kill). The def's Timeout ends ctx with
		// DeadlineExceeded: a run too slow to finish leaves the index
		// untrusted, which is a failure, not a clean stop.
		if errors.Is(ctx.Err(), context.Canceled) || (ctx.Err() == nil && errors.Is(err, context.Canceled)) {
			env.Log(slog.LevelInfo, "opchange_by_book index check interrupted; GetBookChanges "+
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
		env.Log(slog.LevelError, "opchange_by_book index not trusted; GetBookChanges stays on the "+
			"full journal scan until a later ensure or rebuild succeeds", slog.String("error", err.Error()))
		return fmt.Errorf("ensure opchange_by_book index: %w", err)
	}
	if res.Yielded {
		// The verify reached its time budget at a durable cursor. Nothing is
		// trusted yet; the registry re-queues this operation and the next
		// attempt continues from the cursor with a fresh Timeout.
		env.Log(slog.LevelInfo, fmt.Sprintf("opchange_by_book index verify pausing at its time budget: "+
			"%d journal rows verified (%d this run); the operation continues from its cursor",
			res.Verify.VerifiedRows, res.Verify.Rows))
		return fmt.Errorf("opchange_by_book index verify: %w", env.ContinueInPlace)
	}
	if !res.Trusted {
		msg := fmt.Sprintf("opchange_by_book index not trusted after ensure (rebuilt=%t sentinel_set=%t "+
			"missing_entries=%d unmarked_undecodable=%d); GetBookChanges stays on the full journal scan",
			res.Rebuilt, res.Verify.SentinelSet, res.Verify.MissingEntries, res.Verify.UnmarkedUndecodable)
		env.Log(slog.LevelError, msg)
		return errors.New(msg)
	}
	undecodable := res.Verify.Undecodable
	if res.Rebuilt {
		undecodable = res.Rebuild.Undecodable
	}
	summary := fmt.Sprintf("opchange_by_book index trusted: backfill scanned=%d indexed=%d skipped=%t; "+
		"verify rows=%d (journal position %d, prior_coverage=%t) missing_entries=%d undecodable=%d; rebuilt=%t",
		res.Backfill.Scanned, res.Backfill.Indexed, res.Backfill.Skipped,
		res.Verify.Rows, res.Verify.VerifiedRows, res.Verify.PriorCoverage,
		res.Verify.MissingEntries, res.Verify.Undecodable, res.Rebuilt)
	if res.Rebuilt {
		summary += fmt.Sprintf(" (rebuild scanned=%d indexed=%d)", res.Rebuild.Scanned, res.Rebuild.Indexed)
	}
	env.Progress(1, 1, summary)
	if undecodable > 0 {
		env.Log(slog.LevelError, summary)
		return fmt.Errorf("opchange_by_book index trusted, but %d journal row(s) cannot be decoded; "+
			"GetBookChanges fails for every book until each is rewritten or removed", undecodable)
	}
	if res.Backfill.Skipped && !res.Rebuilt {
		env.Log(slog.LevelInfo, "opchange_by_book index already complete and verified; nothing to do: "+summary,
			opsregistry.NoOpAttr())
		return nil
	}
	env.Log(slog.LevelInfo, summary)
	return nil
}

// t6g6HeadEnvForOpChange is the env of a HEAD-shaped call: the reporter's hooks
// and the registry's yield sentinel.
func t6g6HeadEnvForOpChange(reporter sdk.Reporter) opChangeBookIndexEnv {
	return opChangeBookIndexEnv{t6g6Env: t6g6HeadEnv(reporter, true), ContinueInPlace: opsregistry.ErrContinueInPlace}
}
