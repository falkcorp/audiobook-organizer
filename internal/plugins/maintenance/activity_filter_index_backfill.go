// file: internal/plugins/maintenance/activity_filter_index_backfill.go
// version: 1.6.0
// guid: d7602035-c210-4555-84a3-7a28e0a22f83
// last-edited: 2026-11-13

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
	"github.com/falkcorp/mediacore/tasks"
)

// ActivityFilterIndexBackfillDefID builds the Pebble activity store's
// source/type/level indexes over rows written before they existed.
const ActivityFilterIndexBackfillDefID = "maintenance.activity-filter-index-backfill"

// activityFilterIndexWindow is one work item's slice of history. A day keeps an
// item to seconds of work on production volumes (the heaviest day is a few
// hundred thousand rows), so a restart repeats little.
const activityFilterIndexWindow = 24 * time.Hour

// activityFilterIndexReconciler is the boot mode's whole need from the store,
// asserted on the store ActivityFilterIndexBackfiller returns so that interface
// stays narrow (the full backfill never needs it). *PebbleActivityStore
// implements it.
type activityFilterIndexReconciler interface {
	ReconcileFilterIndexCoverage(ctx context.Context, progress database.FilterIndexProgress) (database.FilterIndexReconcileResult, error)
}

// activityFilterIndexBackfillParams is both the op's params and its checkpoint:
// the registry merges the checkpoint back into params on ResumeRestart.
//
// The window PLAN is frozen into the checkpoint, not recomputed on resume. The
// watermark counts windows from PlanStartNanos, and the store's oldest row moves
// between attempts (nightly compaction and retention delete from the old end),
// so re-deriving the start on resume would shift every window under the
// watermark and silently skip a stretch of history.
type activityFilterIndexBackfillParams struct {
	// Force re-runs even when the sentinel says the indexes are complete (after
	// a rollback to a build that wrote rows without them, for instance).
	Force bool `json:"force,omitempty"`

	// Reconcile is the boot mode (04-P6): instead of the full backfill, run
	// ReconcileFilterIndexCoverage, which indexes only rows a rolled-back build
	// wrote without filter keys. The default (non-force) path returns "already
	// complete" without looking at such a gap, so it is NOT equivalent. It
	// cannot be combined with Force. It never checkpoints: a reconcile is
	// idempotent, and a resumed one starts over.
	Reconcile bool `json:"reconcile,omitempty"`

	// Workers sizes the window worker pool; 0 (the default) means
	// runtime.NumCPU(). Lower it to leave cores free on a busy host.
	Workers int `json:"workers,omitempty"`

	PlanStartNanos int64 `json:"planStartNanos,omitempty"`
	PlanWindows    int   `json:"planWindows,omitempty"`
	ResumeFrom     int   `json:"resumeFrom,omitempty"`
	// IndexedBefore carries the rows indexed by earlier attempts, for the
	// result only. It can over-count: a window completed above the watermark
	// is re-run (idempotently) on resume and counted again.
	IndexedBefore int64 `json:"indexedBefore,omitempty"`
}

// ActivityFilterIndexBackfillResult is the op's persisted result.
type ActivityFilterIndexBackfillResult struct {
	Windows        int   `json:"windows"`
	RowsIndexed    int64 `json:"rows_indexed"`
	AlreadyDone    bool  `json:"already_done"`
	SentinelMarked bool  `json:"sentinel_marked"`
}

// activityFilterIndexBackfillHelp is the def's Description, which the native kind
// carries as its Help.
const activityFilterIndexBackfillHelp = "Builds the source, type and level indexes of the Pebble activity log over rows recorded before they existed, then enables indexed filtering. Until it completes, activity filters other than operation and book only search the newest 20,000 entries. Enqueued with {\"reconcile\": true} (as boot does) it only indexes rows a rolled-back build wrote without filter keys."

// activityFilterIndexBackfillDef — see ActivityFilterIndexBackfillDefID.
//
// BOOT. {"reconcile": true} is the mode Server.startBackfills enqueues on every
// start (bootOps; 04-P6 replaced the reconcileFilterIndexAtBoot goroutine in
// internal/activity/register.go): normally it finds nothing above the
// seen-through mark and is a no-op tagged with opsregistry.NoOpAttr. Its
// liveness is manual (the store's progress callback stamps UpdateProgress at
// most once a second during the gap check and once per reindexed chunk). The
// def declares LivenessManual for both modes: the full backfill's RunItems
// stamps per window itself, so declaring RunItems for it would be wrong for
// the reconcile, which never goes through RunItems.
// A restart mid-reconcile resumes it (ResumeRestart) with the same params.
//
// GATE. The query planner ignores the new indexes until this op has covered
// every window and set database.ActivityFilterIndexBackfillKey; until then
// filtered queries take the old budgeted scan (now flagged partial when it
// truncates). Rows written by a build that has the indexes are indexed by
// Record itself, so the plan only has to reach "now" at plan time: its last
// window is open-ended anyway.
//
// CONCURRENCY. A delete racing a window that has already read the row would
// re-create that row's index keys after the delete removed them: a GHOST key.
// Ghost keys never change an answer — the planner Gets every candidate's
// primary row and skips a missing one, and RepairActivityIndexes deletes them
// nightly — they only cost space until then. To keep them rare the op shares
// maintenance.cleanup-activity-log's ConcurrencyKey with every OP that deletes
// activity rows: cleanup, compact, nightly compact and recompact. The one
// deleter that was not an op, the HTTP library wipe, was removed with its
// route (01-P81a); any ghost keys from a deleter outside the ops are handled
// as above.
// Within the op, windows are disjoint timestamp ranges, so the RunItems
// workers never write the same key.
//
// RESUME. ResumeRestart with a contiguous-completion watermark. Each window is
// idempotent (its writes are Sets of the exact bytes Record writes) and its last
// commit is synced before the item returns, so the watermark never names a
// window whose writes could be lost.
func (p *Plugin) activityFilterIndexBackfillDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:              ActivityFilterIndexBackfillDefID,
		Liveness:        sdk.LivenessManual,
		Plugin:          "maintenance",
		DisplayName:     "Backfill activity filter indexes",
		Description:     activityFilterIndexBackfillHelp,
		ResumePolicy:    sdk.ResumeRestart,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.cleanup-activity-log",
		Cancellable:     true,
		Isolate:         false,
		Timeout:         6 * time.Hour,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
		Run:             p.runActivityFilterIndexBackfill,
	}
}

// planActivityFilterIndexWindows returns the number of day windows from
// startNanos through now (at least 1).
func planActivityFilterIndexWindows(startNanos int64, now time.Time) int {
	span := now.UnixNano() - startNanos
	if span < 0 {
		return 1
	}
	return int(span/int64(activityFilterIndexWindow)) + 1
}

// activityFilterIndexWindowBounds returns window i's [from, to) and whether it
// is open-ended. Window 0 starts at 0 and the last window has no end, so the
// plan covers every possible key whatever the store holds.
func activityFilterIndexWindowBounds(p activityFilterIndexBackfillParams, i int) (from, to int64, openEnded bool) {
	w := int64(activityFilterIndexWindow)
	from = p.PlanStartNanos + int64(i)*w
	if i == 0 {
		from = 0
	}
	to = p.PlanStartNanos + int64(i+1)*w
	return from, to, i == p.PlanWindows-1
}

func (p *Plugin) runActivityFilterIndexBackfill(ctx context.Context, raw json.RawMessage, reporter sdk.Reporter) error {
	var params activityFilterIndexBackfillParams
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &params); err != nil {
			return fmt.Errorf("activity-filter-index-backfill: params: %w", err)
		}
	}
	if params.Reconcile && params.Force {
		return errReconcileWithForce
	}
	env := activityFilterEnv{
		t6g1Env:        t6g1HeadEnv(reporter, true),
		Items:          t6g1HeadItems(reporter),
		SaveCheckpoint: reporter.Checkpoint,
	}
	if store := p.deps.ActivityFilterIndexBackfiller(); store != nil {
		a := t6g1HeadActivity{store}
		env.Store, env.Writer = a, a
	}
	res, err := activityFilterIndexBackfillCore(ctx, params, env)
	var setErr error
	if res != nil {
		setErr = opsregistry.ReporterSetResult(reporter, *res)
	}
	if err != nil {
		return err
	}
	if setErr != nil && !res.AlreadyDone {
		return fmt.Errorf("activity-filter-index-backfill: persist result: %w", setErr)
	}
	return setErr
}

// errReconcileWithForce is the refusal of reconcile=true with force=true.
var errReconcileWithForce = errors.New("activity-filter-index-backfill: reconcile=true cannot be combined with force; " +
	"send {\"reconcile\": true} (index only rows written without filter keys) or {\"force\": true} (full rebuild)")

// activityFilterIndexBackfillKind is rule R3's kind for the def above.
const activityFilterIndexBackfillKind = tasks.Kind("books." + ActivityFilterIndexBackfillDefID)

// activityFilterStore is the reads the op makes of the activity store.
type activityFilterStore interface {
	FilterIndexBackfillDone() bool
	FilterIndexEarliestNanos(ctx context.Context) (int64, bool, error)
	// ReconcileSupport is nil when the store can reconcile, and says what it is
	// when it cannot (the boot mode refuses at once, before it reads anything).
	ReconcileSupport() error
}

// activityFilterWriter is the writes the op makes: the sentinel, the windows
// it indexes and the boot mode's reconcile.
type activityFilterWriter interface {
	ClearFilterIndexBackfillDone() error
	MarkFilterIndexBackfillDone() error
	BackfillFilterIndexWindow(ctx context.Context, from, to int64, openEnded bool) (int, error)
	ReconcileFilterIndexCoverage(ctx context.Context, progress database.FilterIndexProgress) (database.FilterIndexReconcileResult, error)
}

// activityFilterEnv is what the core takes from its shell. A nil Store is "no
// Pebble activity store is wired".
type activityFilterEnv struct {
	t6g1Env
	Store  activityFilterStore
	Writer activityFilterWriter
	// Items runs the window pool.
	Items t6g1ItemsFunc
	// SaveCheckpoint stores the run's checkpoint (activityFilterIndexBackfillParams).
	SaveCheckpoint func(state any) error
}

// activityFilterIndexBackfillParallelism is the window pool: Workers, default
// every CPU (HEAD's runtime.NumCPU()).
func activityFilterIndexBackfillParallelism() tasks.Parallelism { return tasks.CPU() }

// activityFilterNativeParams are the native kind's params: HEAD's without the
// checkpoint fields, which rc.Restore carries.
type activityFilterNativeParams struct {
	Force     bool `json:"force,omitempty"`
	Reconcile bool `json:"reconcile,omitempty"`
	Workers   int  `json:"workers,omitempty"`
}

// activityFilterIndexBackfillNative is the native kind. HEAD declared no
// Preview and the op writes, so a Preview run is refused.
func (p *Plugin) activityFilterIndexBackfillNative(deps t6g1Deps, sched func(tasks.Kind) *tasks.Schedule) tasks.Definition {
	c := t6g1Common(activityFilterIndexBackfillKind, sched)
	c.Title = "Backfill activity filter indexes"
	c.Help = activityFilterIndexBackfillHelp
	c.Effects = tasks.Writes(tasks.Res("books", "operations"))
	// The run checkpoints its window watermark and restores it (rc.Restore).
	c.Resume = tasks.ResumeContinue
	c.Priority = tasks.PriorityLow
	c.ConcurrencyKey = tasks.Key("books.maintenance.cleanup-activity-log")
	c.Timeout = 6 * time.Hour
	return tasks.Task(activityFilterIndexBackfillKind, tasks.TaskSpec[activityFilterNativeParams, ActivityFilterIndexBackfillResult]{
		Common: c,
		Run: func(rc *tasks.RC, np activityFilterNativeParams) (ActivityFilterIndexBackfillResult, error) {
			if rc.Mode() != tasks.ModeLive {
				return ActivityFilterIndexBackfillResult{}, fmt.Errorf("%s: preview not supported; start with mode live", activityFilterIndexBackfillKind)
			}
			params := activityFilterIndexBackfillParams{Force: np.Force, Reconcile: np.Reconcile, Workers: np.Workers}
			if params.Reconcile && params.Force {
				return ActivityFilterIndexBackfillResult{}, errReconcileWithForce
			}
			var cp activityFilterIndexBackfillParams
			ok, err := rc.Restore(&cp)
			if err != nil {
				return ActivityFilterIndexBackfillResult{}, fmt.Errorf("activity-filter-index-backfill: restore checkpoint: %w", err)
			}
			if ok {
				params.PlanStartNanos, params.PlanWindows = cp.PlanStartNanos, cp.PlanWindows
				params.ResumeFrom, params.IndexedBefore = cp.ResumeFrom, cp.IndexedBefore
			}
			env := activityFilterEnv{
				t6g1Env:        t6g1NativeEnv(rc),
				Writer:         deps.NewWriter(rc.Writer()),
				Items:          t6g1NativeItems(rc),
				SaveCheckpoint: func(state any) error { return rc.Checkpoint(state) },
			}
			if deps.ActivityFilter != nil {
				env.Store = deps.ActivityFilter
			}
			res, err := activityFilterIndexBackfillCore(rc.Context(), params, env)
			if res == nil {
				return ActivityFilterIndexBackfillResult{}, err
			}
			return *res, err
		},
	})
}

// activityFilterIndexBackfillCore is the body of runActivityFilterIndexBackfill
// from the reconcile/force check on. The result is nil when the run set none.
func activityFilterIndexBackfillCore(ctx context.Context, params activityFilterIndexBackfillParams, env activityFilterEnv) (*ActivityFilterIndexBackfillResult, error) {
	if params.Reconcile && params.Force {
		return nil, errReconcileWithForce
	}
	store := env.Store
	if params.Reconcile {
		// Before the "already complete" return below: the usual boot has the
		// sentinel set, and that return would skip the reconcile entirely.
		return runActivityFilterIndexReconcileCore(ctx, env)
	}
	if store == nil {
		return nil, errors.New("activity-filter-index-backfill: no Pebble activity store is wired")
	}

	result := ActivityFilterIndexBackfillResult{}
	if store.FilterIndexBackfillDone() && !params.Force {
		msg := "Activity filter indexes are already complete; nothing to do (pass {\"force\": true} to rebuild)"
		env.Log(slog.LevelInfo, msg)
		env.Progress(0, 0, msg)
		result.AlreadyDone = true
		return &result, nil
	}

	// A forced rebuild exists because the indexes are NOT trusted (a rollback,
	// a suspected defect). Withdraw the sentinel before the first write so the
	// planner falls back to the budgeted scan — which says partial — for the
	// whole rebuild instead of answering from a half-rebuilt index. A resumed
	// forced run clears it again, which is harmless.
	if params.Force {
		if err := env.Writer.ClearFilterIndexBackfillDone(); err != nil {
			return nil, fmt.Errorf("activity-filter-index-backfill: clear sentinel: %w", err)
		}
	}

	if params.PlanWindows <= 0 {
		earliest, ok, err := store.FilterIndexEarliestNanos(ctx)
		if err != nil {
			return nil, fmt.Errorf("activity-filter-index-backfill: plan: %w", err)
		}
		if !ok {
			earliest = time.Now().UnixNano()
		}
		params.PlanStartNanos = earliest
		params.PlanWindows = planActivityFilterIndexWindows(earliest, time.Now())
		params.ResumeFrom = 0
	}
	resumeFrom := min(max(params.ResumeFrom, 0), params.PlanWindows)
	result.Windows = params.PlanWindows

	startMsg := fmt.Sprintf("Indexing activity rows by source/type/level in %d day window(s) from %s",
		params.PlanWindows, time.Unix(0, params.PlanStartNanos).UTC().Format(time.DateOnly))
	if resumeFrom > 0 {
		startMsg += fmt.Sprintf(" (resuming at window %d)", resumeFrom+1)
	}
	logger.InfoCtx(ctx, startMsg)
	env.Log(slog.LevelInfo, startMsg)

	var indexed atomic.Int64
	workers := params.Workers
	if workers <= 0 {
		workers = runtime.NumCPU()
	}

	// CONCURRENCY (CLAUDE.md mandate): each window is a disjoint timestamp
	// range and its work is a JSON decode per row, so it is CPU-bound and
	// parallel across cores with no shared mutable state but the atomic.
	runErr := env.Items(ctx, params.PlanWindows, t6g1ItemsOptions{
		Concurrency:     workers,
		ResumeFrom:      resumeFrom,
		ProgressTotal:   params.PlanWindows,
		CheckpointEvery: 8,
		CheckpointState: func(_ context.Context, watermark int) error {
			return env.SaveCheckpoint(activityFilterIndexBackfillParams{
				Force:          params.Force,
				Workers:        params.Workers,
				PlanStartNanos: params.PlanStartNanos,
				PlanWindows:    params.PlanWindows,
				ResumeFrom:     watermark,
				IndexedBefore:  params.IndexedBefore + indexed.Load(),
			})
		},
		// Label runs inside the worker goroutines: it reads the atomic only.
		Label: func(i, total int) string {
			return fmt.Sprintf("Window %d/%d (rows indexed=%d)", i+1, total, params.IndexedBefore+indexed.Load())
		},
	}, func(ctx context.Context, i int) error {
		from, to, open := activityFilterIndexWindowBounds(params, i)
		n, err := env.Writer.BackfillFilterIndexWindow(ctx, from, to, open)
		indexed.Add(int64(n))
		return err
	})
	result.RowsIndexed = params.IndexedBefore + indexed.Load()

	if runErr == nil {
		if err := env.Writer.MarkFilterIndexBackfillDone(); err != nil {
			runErr = fmt.Errorf("mark sentinel: %w", err)
		} else {
			result.SentinelMarked = true
		}
	}

	doneMsg := fmt.Sprintf("Indexed %d activity row(s) over %d window(s); indexed filtering enabled", result.RowsIndexed, result.Windows)
	if runErr != nil {
		doneMsg = fmt.Sprintf("FAILED after indexing %d activity row(s); indexed filtering stays OFF: %v", result.RowsIndexed, runErr)
		env.Log(slog.LevelError, doneMsg)
	} else {
		env.Log(slog.LevelInfo, doneMsg)
	}
	logger.InfoCtx(ctx, doneMsg)

	if runErr != nil {
		return &result, fmt.Errorf("activity-filter-index-backfill: %w", runErr)
	}
	return &result, nil
}

// activityFilterIndexProgressTo turns the reconcile's progress callbacks into
// liveness stamps. The row total is unknown up front, so total is 0 and the
// message carries the count.
func activityFilterIndexProgressTo(env t6g1Env) database.FilterIndexProgress {
	var (
		mu        sync.Mutex
		lastCheck time.Time
	)
	return func(phase string, rows int) {
		if phase == "check" {
			// The check reports every few dozen rows; one liveness stamp per
			// interval is plenty for the watchdog and keeps the op row from
			// being rewritten thousands of times. A reindex chunk (about 2,000
			// rows) always stamps.
			mu.Lock()
			now := activityFilterIndexNow()
			if !lastCheck.IsZero() && now.Sub(lastCheck) < activityFilterIndexCheckStampEvery {
				mu.Unlock()
				return
			}
			lastCheck = now
			mu.Unlock()
		}
		env.Progress(rows, 0, fmt.Sprintf("activity filter-index reconcile %s: %d rows", phase, rows))
	}
}

// activityFilterIndexCheckStampEvery is the minimum gap between "check"
// progress stamps, and activityFilterIndexNow its clock. Vars so tests can
// drive them.
var (
	activityFilterIndexCheckStampEvery = time.Second
	activityFilterIndexNow             = time.Now
)

// runActivityFilterIndexReconcile is the boot mode ({"reconcile": true}): the
// work reconcileFilterIndexAtBoot did, as an op. The outcomes:
//
//   - no Pebble activity store (nil, a non-Pebble backend): nothing to
//     reconcile. A Warn tagged opsregistry.NoOpAttr, success, so boot is not
//     red every start. The full backfill still refuses in that case.
//   - sentinel not set: the indexes are not complete, so every filtered query
//     already uses the budgeted scan and says partial, and the full backfill
//     covers every row when it runs (boot enqueues it in full mode when it
//     finds the sentinel unset). Nothing to reconcile; tagged no-op, and the
//     line names the op.
//   - nothing above the seen-through mark lacks filter keys: the usual boot,
//     tagged no-op.
//   - a gap was found and indexed: a Warn (not tagged), the result persisted.
//   - failed or cancelled after the gap was found: the store withdrew the
//     sentinel first, so queries are partial, never silently short, and they
//     stay so: the next reconcile, like a resumed one, returns at once on a
//     withdrawn sentinel. The Error line and the returned error therefore name
//     maintenance.activity-filter-index-backfill, on the cancel path too.
//   - cancelled before anything was withdrawn: an interruption, Info, ctx's
//     error returned.
func runActivityFilterIndexReconcileCore(ctx context.Context, env activityFilterEnv) (*ActivityFilterIndexBackfillResult, error) {
	store := env.Store
	env.Log(slog.LevelDebug, "Starting activity filter-index reconcile")
	if store == nil {
		env.Log(slog.LevelWarn, "no Pebble activity store is wired; nothing to reconcile",
			opsregistry.NoOpAttr())
		return nil, nil
	}
	// The shell's store answers whether it can reconcile (HEAD asserted the
	// activityFilterIndexReconciler interface on the store here, which is the
	// *PebbleActivityStore or nil, never a decorator that hides the method).
	if err := store.ReconcileSupport(); err != nil {
		return nil, err
	}
	env.Progress(0, 1, "checking activity rows for missing filter-index keys")
	if !store.FilterIndexBackfillDone() {
		env.Log(slog.LevelInfo, "activity filter indexes are not complete (never built, or withdrawn by a "+
			"failed reconcile), so filtered activity queries use the budgeted scan and report partial; "+
			"maintenance.activity-filter-index-backfill without params builds them (boot enqueues it that way "+
			"when it finds the indexes incomplete). Nothing to reconcile.", opsregistry.NoOpAttr())
		return nil, nil
	}

	res, err := env.Writer.ReconcileFilterIndexCoverage(ctx, activityFilterIndexProgressTo(env.t6g1Env))
	if err != nil {
		withdrawn := !store.FilterIndexBackfillDone()
		cancelled := errors.Is(ctx.Err(), context.Canceled) || (ctx.Err() == nil && errors.Is(err, context.Canceled))
		switch {
		case cancelled && !withdrawn:
			env.Log(slog.LevelInfo, "activity filter-index reconcile interrupted before it changed "+
				"anything; the next boot reconciles again", slog.String("error", err.Error()))
		case cancelled:
			env.Log(slog.LevelError, fmt.Sprintf("activity filter-index reconcile interrupted after it "+
				"withdrew the indexed-filtering sentinel (seen-through=%d, reindexed=%d); filtered activity queries "+
				"report partial until maintenance.activity-filter-index-backfill re-enables indexed filtering",
				res.SeenThrough, res.Reindexed), slog.String("error", err.Error()))
		default:
			env.Log(slog.LevelError, fmt.Sprintf("activity filter-index reconcile failed (seen-through=%d, "+
				"reindexed=%d); run maintenance.activity-filter-index-backfill to re-enable indexed filtering",
				res.SeenThrough, res.Reindexed), slog.String("error", err.Error()))
		}
		if cancelled {
			if ctxErr := ctx.Err(); ctxErr != nil && !errors.Is(err, ctxErr) {
				return nil, fmt.Errorf("activity-filter-index-backfill: reconcile interrupted (%s): %w", err.Error(), ctxErr)
			}
			if withdrawn {
				return nil, fmt.Errorf("activity-filter-index-backfill: reconcile interrupted with the sentinel withdrawn "+
					"(seen-through=%d, reindexed=%d); run maintenance.activity-filter-index-backfill to re-enable "+
					"indexed filtering: %w", res.SeenThrough, res.Reindexed, err)
			}
			return nil, fmt.Errorf("activity-filter-index-backfill: reconcile interrupted: %w", err)
		}
		return nil, fmt.Errorf("activity-filter-index-backfill: reconcile failed (seen-through=%d, reindexed=%d): %w "+
			"- run maintenance.activity-filter-index-backfill to re-enable indexed filtering",
			res.SeenThrough, res.Reindexed, err)
	}
	if !res.GapFound {
		env.Log(slog.LevelInfo, "no activity rows above the filter-index mark lack filter keys; nothing to do",
			opsregistry.NoOpAttr())
		return nil, nil
	}
	msg := fmt.Sprintf("activity filter-index reconcile indexed %d row(s) written without filter keys after "+
		"seen-through=%d; indexed filtering is enabled again", res.Reindexed, res.SeenThrough)
	env.Progress(1, 1, msg)
	env.Log(slog.LevelWarn, msg)
	return &ActivityFilterIndexBackfillResult{RowsIndexed: int64(res.Reindexed), SentinelMarked: true}, nil
}
