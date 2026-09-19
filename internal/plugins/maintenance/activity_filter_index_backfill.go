// file: internal/plugins/maintenance/activity_filter_index_backfill.go
// version: 1.0.0
// guid: d7602035-c210-4555-84a3-7a28e0a22f83
// last-edited: 2026-09-19

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"sync/atomic"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/logging"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// ActivityFilterIndexBackfillDefID builds the Pebble activity store's
// source/type/level indexes over rows written before they existed.
const ActivityFilterIndexBackfillDefID = "maintenance.activity-filter-index-backfill"

// activityFilterIndexWindow is one work item's slice of history. A day keeps an
// item to seconds of work on production volumes (the heaviest day is a few
// hundred thousand rows), so a restart repeats little.
const activityFilterIndexWindow = 24 * time.Hour

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

// activityFilterIndexBackfillDef — see ActivityFilterIndexBackfillDefID.
//
// GATE. The query planner ignores the new indexes until this op has covered
// every window and set database.ActivityFilterIndexBackfillKey; until then
// filtered queries take the old budgeted scan (now flagged partial when it
// truncates). Rows written by a build that has the indexes are indexed by
// Record itself, so the plan only has to reach "now" at plan time: its last
// window is open-ended anyway.
//
// CONCURRENCY. It shares maintenance.cleanup-activity-log's ConcurrencyKey on
// purpose: compaction, summarize and prune delete rows, and a delete racing a
// window that already read the row would re-create that row's index keys after
// the delete removed them — orphans. Serializing against every op that deletes
// activity rows closes that race; RepairActivityIndexes and the planner's
// existence check cover anything older. Within the op, windows are disjoint key
// ranges (by timestamp), so the RunItems workers never write the same key.
//
// RESUME. ResumeRestart with a contiguous-completion watermark. Each window is
// idempotent (its writes are Sets of the exact bytes Record writes) and its last
// commit is synced before the item returns, so the watermark never names a
// window whose writes could be lost.
func (p *Plugin) activityFilterIndexBackfillDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:              ActivityFilterIndexBackfillDefID,
		Liveness:        sdk.LivenessRunItems,
		Plugin:          "maintenance",
		DisplayName:     "Backfill activity filter indexes",
		Description:     "Builds the source, type and level indexes of the Pebble activity log over rows recorded before they existed, then enables indexed filtering. Until it completes, activity filters other than operation and book only search the newest 20,000 entries.",
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
	store := p.deps.ActivityFilterIndexBackfiller()
	if store == nil {
		return errors.New("activity-filter-index-backfill: no Pebble activity store is wired")
	}

	result := ActivityFilterIndexBackfillResult{}
	if store.FilterIndexBackfillDone() && !params.Force {
		msg := "Activity filter indexes are already complete; nothing to do (pass {\"force\": true} to rebuild)"
		_ = reporter.Log(slog.LevelInfo, msg)
		_ = reporter.UpdateProgress(0, 0, msg)
		result.AlreadyDone = true
		return opsregistry.ReporterSetResult(reporter, result)
	}

	if params.PlanWindows <= 0 {
		earliest, ok, err := store.FilterIndexEarliestNanos(ctx)
		if err != nil {
			return fmt.Errorf("activity-filter-index-backfill: plan: %w", err)
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
	logging.Info(ctx, startMsg)
	_ = reporter.Log(slog.LevelInfo, startMsg)

	windows := make([]int, params.PlanWindows)
	for i := range windows {
		windows[i] = i
	}
	var indexed atomic.Int64

	// CONCURRENCY (CLAUDE.md mandate): each window is a disjoint timestamp
	// range and its work is a JSON decode per row, so it is CPU-bound and
	// parallel across cores with no shared mutable state but the atomic.
	runErr := opsregistry.RunItems(ctx, reporter, windows, func(ctx context.Context, i int) error {
		from, to, open := activityFilterIndexWindowBounds(params, i)
		n, err := store.BackfillFilterIndexWindow(ctx, from, to, open)
		indexed.Add(int64(n))
		return err
	}, opsregistry.RunItemsOptions{
		Concurrency:     runtime.NumCPU(),
		ResumeFrom:      resumeFrom,
		ProgressTotal:   params.PlanWindows,
		CheckpointEvery: 8,
		CheckpointStateFn: func(_ context.Context, watermark int) error {
			return reporter.Checkpoint(activityFilterIndexBackfillParams{
				Force:          params.Force,
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
	})
	result.RowsIndexed = params.IndexedBefore + indexed.Load()

	if runErr == nil {
		if err := store.MarkFilterIndexBackfillDone(); err != nil {
			runErr = fmt.Errorf("mark sentinel: %w", err)
		} else {
			result.SentinelMarked = true
		}
	}

	doneMsg := fmt.Sprintf("Indexed %d activity row(s) over %d window(s); indexed filtering enabled", result.RowsIndexed, result.Windows)
	if runErr != nil {
		doneMsg = fmt.Sprintf("FAILED after indexing %d activity row(s); indexed filtering stays OFF: %v", result.RowsIndexed, runErr)
		_ = reporter.Log(slog.LevelError, doneMsg)
	} else {
		_ = reporter.Log(slog.LevelInfo, doneMsg)
	}
	logging.Info(ctx, doneMsg)

	if setErr := opsregistry.ReporterSetResult(reporter, result); setErr != nil && runErr == nil {
		return fmt.Errorf("activity-filter-index-backfill: persist result: %w", setErr)
	}
	if runErr != nil {
		return fmt.Errorf("activity-filter-index-backfill: %w", runErr)
	}
	return nil
}
