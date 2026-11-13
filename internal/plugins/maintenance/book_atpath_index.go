// file: internal/plugins/maintenance/book_atpath_index.go
// version: 1.7.1
// guid: 6d2a9e41-7c3b-4f85-a0d6-8b1e5c9f2a74
// last-edited: 2026-11-13

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
	"github.com/falkcorp/mediacore/tasks"
)

// --- book_atpath: index ops ---
//
// The index itself, its invariant and its startup backfill live in
// internal/books/store/pebble_store_atpath_index.go. These two ops are the
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

// The two native kinds of this file (F5-07, rule R3: "books." + the def's ID).
const (
	t6g2KindBookAtPathIndexVerify   tasks.Kind = "books.maintenance.book-atpath-index-verify"
	t6g2KindBookAtPathIndexBackfill tasks.Kind = "books.maintenance.book-atpath-index-backfill"
)

// DisplayName and Description of the two defs, shared by HEAD's def and the
// native kind so the two cannot drift.
const (
	t6g2BookAtPathIndexVerifyTitle = "Book path-set index verify"
	t6g2BookAtPathIndexVerifyHelp  = "Diffs the book_atpath: index (every book at each path) against the book " +
		"rows from one snapshot. Fails if any book, live OR soft-deleted, has no index key at " +
		"its path, which would let LiveBookIDsAtPath (live books) or BookIDsAtPath (every row; " +
		"the purge's check before it removes a file) report a taken path as free. Extras are " +
		"reported but do not fail. REPORT-ONLY: modifies nothing."

	t6g2BookAtPathIndexBackfillTitle = "Book path-set index rebuild"
	t6g2BookAtPathIndexBackfillHelp  = "Rewrites the book_atpath: index key for every book, ignoring the " +
		"one-time startup sentinel, then sets it. Run after any rollback to a build that " +
		"predates the index, then run the verify op. Writes index keys only, never book rows. " +
		"Enqueued with {\"if_needed\": true} (as boot does) it builds the index only when the " +
		"one-time sentinel is not yet set."
)

func (p *Plugin) bookAtPathIndexVerifyDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:              "maintenance.book-atpath-index-verify",
		Liveness:        sdk.LivenessRunItems,
		Plugin:          "maintenance",
		DisplayName:     t6g2BookAtPathIndexVerifyTitle,
		Description:     t6g2BookAtPathIndexVerifyHelp,
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
		ID:              "maintenance.book-atpath-index-backfill",
		Liveness:        sdk.LivenessManual,
		Plugin:          "maintenance",
		DisplayName:     t6g2BookAtPathIndexBackfillTitle,
		Description:     t6g2BookAtPathIndexBackfillHelp,
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
	_, err := runBookAtPathIndexVerifyCore(ctx, t6g2NoParams{}, headT6G2Wire(headT6G2Env(p, reporter), p, store))
	return err
}

// t6g2NoParams is the params type of a def that takes none.
type t6g2NoParams = struct{}

// runBookAtPathIndexVerifyCore is the verify body shared by HEAD's shell and the
// native kind. It reads through env.Store only.
func runBookAtPathIndexVerifyCore(ctx context.Context, _ t6g2NoParams, env t6g2Env) (t6g2NoResult, error) {
	if !env.Store.Supports(t6g2CapAtPathVerify) {
		return t6g2NoResult{}, fmt.Errorf("cannot run: store is %s, which has no book_atpath: index "+
			"(and no store in its decorator chain does)", env.Store.Describe())
	}
	return t6g2NoResult{}, t6g2VerifyBookAtPathIndex(ctx, env.Store, env)
}

func verifyBookAtPathIndex(ctx context.Context, v bookAtPathVerifier, reporter sdk.Reporter) error {
	return t6g2VerifyBookAtPathIndex(ctx, v, headT6G2Env(nil, reporter))
}

func t6g2VerifyBookAtPathIndex(ctx context.Context, v bookAtPathVerifier, env t6g2Env) error {
	env.Progress(0, 1, "verifying book_atpath: index")
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
	env.Log(slog.LevelInfo, summary)
	if b, mErr := json.Marshal(rep); mErr == nil {
		env.Log(slog.LevelInfo, "report: "+string(b))
	}
	env.Progress(1, 1, summary)

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
	_, err := runBookAtPathIndexBackfillCore(ctx, params, headT6G2Wire(headT6G2Env(p, reporter), p, store))
	return err
}

// runBookAtPathIndexBackfillCore is the backfill body shared by HEAD's shell and
// the native kind. It probes capabilities on env.Store and writes through
// env.Writer.
func runBookAtPathIndexBackfillCore(ctx context.Context, params bookAtPathBackfillParams, env t6g2Env) (t6g2NoResult, error) {
	if params.IfNeeded {
		if !env.Store.Supports(t6g2CapAtPathBackfill) {
			// Boot enqueues this on every start. A store with no index (not
			// Pebble) has nothing to build and LiveBookIDsAtPath scans, as it
			// always has, so a red row on every boot would be noise. The
			// goroutine logged the same Warn. An operator's rebuild still
			// fails below.
			env.Log(slog.LevelWarn, fmt.Sprintf("store is %s, which has no book_atpath: index "+
				"(and no store in its decorator chain does); nothing to build, LiveBookIDsAtPath stays on "+
				"the full scan", env.Store.Describe()), env.NoOp)
			return t6g2NoResult{}, nil
		}
		return t6g2NoResult{}, t6g2BackfillBookAtPathIfNeeded(ctx, env.Writer, env)
	}
	if !env.Store.Supports(t6g2CapAtPathRebuild) {
		return t6g2NoResult{}, fmt.Errorf("cannot run: store is %s, which has no book_atpath: index "+
			"(and no store in its decorator chain does)", env.Store.Describe())
	}
	env.Progress(0, 1, "rebuilding book_atpath: index")
	res, err := env.Writer.RebuildBookAtPathIndex(ctx, t6g2AtPathProgressTo(env))
	if err != nil {
		return t6g2NoResult{}, fmt.Errorf("rebuild book_atpath index (scanned %d before failing; sentinel "+
			"state unchanged by this run): %w", res.Scanned, err)
	}
	msg := fmt.Sprintf("rebuilt book_atpath index: %d books, %d commits, %d undecodable",
		res.Scanned, res.Commits, res.UndecodableRows)
	env.Progress(1, 1, msg)
	if res.UndecodableRows > 0 {
		// Same stance as the verify op: rows that could not be decoded were
		// not indexed, so the run is reported as incomplete, not green.
		env.Log(slog.LevelError, msg)
		return t6g2NoResult{}, bookAtPathUndecodableError(res)
	}
	env.Log(slog.LevelInfo, msg)
	return t6g2NoResult{}, nil
}

// t6g2AtPathProgressTo turns the store's per-chunk callbacks into liveness
// stamps. The row total is unknown up front, so total is 0 and the message
// carries the count. Safe for the concurrent workers that call it.
func t6g2AtPathProgressTo(env t6g2Env) database.BookAtPathProgress {
	return func(scanned int) {
		env.Progress(scanned, 0, fmt.Sprintf("book_atpath index: %d book rows staged", scanned))
	}
}

func bookAtPathUndecodableError(res database.BookAtPathBackfillResult) error {
	return fmt.Errorf("book_atpath index built, but %d book row(s) cannot be decoded (sample ids: "+
		"%v); LiveBookIDsAtPath refuses until each is rewritten or removed out of band, then run "+
		"maintenance.book-atpath-index-backfill", res.UndecodableRows, res.SampleUndecodable)
}

// t6g2BackfillBookAtPathIfNeeded is the boot mode ({"if_needed": true}): the
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
func t6g2BackfillBookAtPathIfNeeded(ctx context.Context, b bookAtPathBackfiller, env t6g2Env) error {
	env.Log(slog.LevelDebug, "Starting book_atpath index backfill (skipped when the one-time sentinel is set)")
	env.Progress(0, 1, "checking book_atpath: index sentinel")
	res, err := b.BackfillBookAtPathIndex(ctx, t6g2AtPathProgressTo(env))
	if err != nil {
		// Only a cancel is an interruption. The def's Timeout ends ctx with
		// DeadlineExceeded: a build too slow to finish leaves the sentinel
		// unset, which is a failure, not a clean stop.
		if errors.Is(ctx.Err(), context.Canceled) || (ctx.Err() == nil && errors.Is(err, context.Canceled)) {
			env.Log(slog.LevelInfo, "book_atpath index build interrupted; LiveBookIDsAtPath stays on "+
				"the full scan until the next boot builds it", slog.String("error", err.Error()))
			if ctxErr := ctx.Err(); ctxErr != nil && !errors.Is(err, ctxErr) {
				return fmt.Errorf("book_atpath index build interrupted (%s): %w", err.Error(), ctxErr)
			}
			return fmt.Errorf("book_atpath index build interrupted: %w", err)
		}
		env.Log(slog.LevelError, "book_atpath index not built; LiveBookIDsAtPath stays on the full "+
			"scan until a later boot or maintenance.book-atpath-index-backfill succeeds",
			slog.String("error", err.Error()))
		return fmt.Errorf("backfill book_atpath index (scanned %d before failing; the sentinel is not set): %w",
			res.Scanned, err)
	}
	if res.Skipped {
		env.Log(slog.LevelInfo, "book_atpath index already built; nothing to do", env.NoOp)
		return nil
	}
	msg := fmt.Sprintf("built book_atpath index: %d books, %d commits, %d undecodable",
		res.Scanned, res.Commits, res.UndecodableRows)
	env.Progress(1, 1, msg)
	if res.UndecodableRows > 0 {
		env.Log(slog.LevelError, msg)
		return bookAtPathUndecodableError(res)
	}
	env.Log(slog.LevelInfo, msg)
	return nil
}

// ---------------------------------------------------------------------------
// Native kinds (F5-07).
// ---------------------------------------------------------------------------

// The verify op writes nothing and the rebuild writes index keys (HEAD's own
// comments: "Writes index keys only, never book rows"). The index keys sit under
// the books resource, as book-hash-index-repair declares for its own.
func t6g2BookAtPathIndexVerifyMeta() t6g2Meta {
	return t6g2Meta{
		Kind:     t6g2KindBookAtPathIndexVerify,
		Title:    t6g2BookAtPathIndexVerifyTitle,
		Help:     t6g2BookAtPathIndexVerifyHelp,
		Effects:  tasks.ReadOnly(),
		Resume:   tasks.ResumeDrop,
		Priority: tasks.PriorityLow,
		Timeout:  30 * time.Minute,
		Key:      "maintenance.book-atpath-index",
	}
}

func t6g2BookAtPathIndexBackfillMeta() t6g2Meta {
	return t6g2Meta{
		Kind:     t6g2KindBookAtPathIndexBackfill,
		Title:    t6g2BookAtPathIndexBackfillTitle,
		Help:     t6g2BookAtPathIndexBackfillHelp,
		Effects:  tasks.Writes(t6g2Res("books")...),
		Resume:   tasks.ResumeDrop,
		Priority: tasks.PriorityLow,
		Timeout:  30 * time.Minute,
		// Shares the verify op's key so a verify never reads a half-rebuilt index.
		Key: "maintenance.book-atpath-index",
	}
}

func t6g2BookAtPathIndexVerifyKind(deps t6g2Deps, sched func(tasks.Kind) *tasks.Schedule) tasks.Definition {
	m := t6g2BookAtPathIndexVerifyMeta()
	run := func(rc *tasks.RC, raw json.RawMessage) (t6g2NoResult, error) {
		if err := t6g2RefuseModeKeys(m.Kind, raw, "dry_run", "dryRun"); err != nil {
			return t6g2NoResult{}, err
		}
		env, err := t6g2NativeEnv(rc, deps, tasks.Parallelism{})
		if err != nil {
			return t6g2NoResult{}, err
		}
		return runBookAtPathIndexVerifyCore(rc.Context(), t6g2NoParams{}, env)
	}
	return tasks.Task(m.Kind, tasks.TaskSpec[json.RawMessage, t6g2NoResult]{Common: m.common(sched), Run: run})
}

func t6g2BookAtPathIndexBackfillKind(deps t6g2Deps, sched func(tasks.Kind) *tasks.Schedule) tasks.Definition {
	m := t6g2BookAtPathIndexBackfillMeta()
	run := func(rc *tasks.RC, raw json.RawMessage) (t6g2NoResult, error) {
		// A live-only writer (rule R7): no Preview at HEAD.
		if err := t6g2RefuseNotLive(rc, m.Kind); err != nil {
			return t6g2NoResult{}, err
		}
		if err := t6g2RefuseModeKeys(m.Kind, raw, "dry_run", "dryRun"); err != nil {
			return t6g2NoResult{}, err
		}
		var params bookAtPathBackfillParams
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &params); err != nil {
				return t6g2NoResult{}, fmt.Errorf("book-atpath-index-backfill: params: %w", err)
			}
		}
		env, err := t6g2NativeEnv(rc, deps, tasks.Parallelism{})
		if err != nil {
			return t6g2NoResult{}, err
		}
		return runBookAtPathIndexBackfillCore(rc.Context(), params, env)
	}
	return tasks.Task(m.Kind, tasks.TaskSpec[json.RawMessage, t6g2NoResult]{Common: m.common(sched), Run: run})
}
