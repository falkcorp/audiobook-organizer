// file: internal/plugins/maintenance/review_status_index_repair.go
// version: 1.1.0
// guid: 9e2b7c41-3f8d-4a65-b1c9-7d0e5f2a8b36
// last-edited: 2026-11-19

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/falkcorp/mediacore/tasks"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// Repairing the review-item status index.
//
// Review items are stored twice: the record under review_item:r:<id> and a
// secondary row under review_item:status:<status>:<id> that status-filtered
// listing and counting read instead of scanning every record. Until 2026-09-02
// SetReviewItemDecision moved that row without the mutex its sibling writers
// hold, so two concurrent decisions on one item could leave it indexed under
// two statuses — the review queue then listed and counted an item under a
// status it no longer had. The writer is fixed; this op is the route back for
// rows the unlocked writer already damaged. It is safe to run at any time and
// is a no-op on a healthy index.

// reviewStatusIndexRepairParams configures the repair.
type reviewStatusIndexRepairParams struct {
	// Apply writes the fix. Default false: scan and report only.
	Apply bool `json:"apply"`
}

// reviewStatusIndexRepairResult is what the op reports back. The *_found
// counts are what the scan saw; the *_removed / *_added counts are what was
// written, and are zero on a dry run so a reader can never mistake "would
// fix 40" for "fixed 40".
type reviewStatusIndexRepairResult struct {
	DryRun                   bool `json:"dry_run"`
	ItemsScanned             int  `json:"items_scanned"`
	IndexEntriesScanned      int  `json:"index_entries_scanned"`
	StaleIndexEntriesFound   int  `json:"stale_index_entries_found"`
	MissingIndexEntriesFound int  `json:"missing_index_entries_found"`
	StaleIndexEntriesRemoved int  `json:"stale_index_entries_removed"`
	MissingIndexEntriesAdded int  `json:"missing_index_entries_added"`
}

// reviewStatusIndexRepairHelp is the def's Description, which the native kind
// carries as its Help.
const reviewStatusIndexRepairHelp = "Rebuilds the review_item:status:* secondary index from the review " +
	"item records: removes index rows that name an item under a status it no " +
	"longer has (or that no longer exists) and adds the row for any item missing " +
	"from the index. Default dry-run reports the counts; pass {\"apply\": true} " +
	"to write the fix."

func (p *Plugin) reviewStatusIndexRepairDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:              "maintenance.review-status-index-repair",
		Preview:         sdk.PreviewSpec{Params: reviewStatusIndexRepairParams{}},
		DisplayName:     "Repair review-queue status index",
		Description:     reviewStatusIndexRepairHelp,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.review-status-index-repair",
		// ResumeDrop: the whole scan-and-write is one synced batch under the
		// store's review mutex, so a dropped run left nothing half-written and
		// re-running from scratch is the correct resume.
		ResumePolicy: sdk.ResumeDrop,
		Liveness:     sdk.LivenessManual,
		Cancellable:  true,
		Timeout:      30 * time.Minute,
		// CapLibraryWrite: the index is not a library row, but with apply this
		// writes durable store state and a writing op must not claim read-only.
		Capabilities: []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
		Run: func(ctx context.Context, raw json.RawMessage, reporter sdk.Reporter) error {
			return p.runReviewStatusIndexRepair(ctx, raw, reporter)
		},
	}
}

func (p *Plugin) runReviewStatusIndexRepair(ctx context.Context, rawParams json.RawMessage, reporter sdk.Reporter) error {
	var params reviewStatusIndexRepairParams
	if len(rawParams) > 0 {
		if err := json.Unmarshal(rawParams, &params); err != nil {
			return fmt.Errorf("review-status-index-repair: decode params: %w", err)
		}
	}
	env := p.reviewStatusIndexRepairHeadEnv(reporter, params.Apply)
	result, err := reviewStatusIndexRepairCore(ctx, params.Apply, env)
	if err != nil {
		return err
	}
	return reviewStatusIndexRepairReport(env.t6g7Env, result)
}

// reviewStatusIndexRepairKind is rule R3's kind for the def above.
const reviewStatusIndexRepairKind = tasks.Kind("books.maintenance.review-status-index-repair")

// reviewStatusIndexRepairStore is the read the scan makes: the report of a
// rebuild that writes nothing.
type reviewStatusIndexRepairStore interface {
	// ReviewStatusIndexReport scans the index against the records and reports
	// what a rebuild would change, without writing (RebuildReviewStatusIndex
	// with apply false).
	ReviewStatusIndexReport() (database.ReviewStatusIndexRepair, error)
}

// reviewStatusIndexRepairWriter is the one write: the rebuild, one synced batch
// under the store's review mutex.
type reviewStatusIndexRepairWriter interface {
	RebuildReviewStatusIndex(apply bool) (database.ReviewStatusIndexRepair, error)
}

// reviewStatusIndexRepairEnv is what the core takes from its shell.
type reviewStatusIndexRepairEnv struct {
	t6g7Env
	Store  reviewStatusIndexRepairStore
	Writer reviewStatusIndexRepairWriter
}

// reviewStatusIndexHeadStore is HEAD's read: the repairer with apply false.
type reviewStatusIndexHeadStore struct {
	r database.ReviewStatusIndexRepairer
}

func (s reviewStatusIndexHeadStore) ReviewStatusIndexReport() (database.ReviewStatusIndexRepair, error) {
	return s.r.RebuildReviewStatusIndex(false)
}

// reviewStatusIndexRepairHeadEnv is the env of HEAD's shell. A nil reporter (the
// HEAD tests' direct call) leaves the hooks unset: the core does not use them.
func (p *Plugin) reviewStatusIndexRepairHeadEnv(reporter sdk.Reporter, live bool) reviewStatusIndexRepairEnv {
	env := reviewStatusIndexRepairEnv{}
	if reporter != nil {
		env.t6g7Env = t6g7HeadEnv(reporter, live)
	}
	if r := p.deps.ReviewStatusIndexStore(); r != nil {
		env.Store = reviewStatusIndexHeadStore{r: r}
		env.Writer = r
	}
	return env
}

// reviewStatusIndexRepairNative is the native kind. Preview is the dry run
// HEAD's default was; Live applies the rebuild.
func (p *Plugin) reviewStatusIndexRepairNative(deps t6g7Deps, sched func(tasks.Kind) *tasks.Schedule) tasks.Definition {
	c := t6g7Common(reviewStatusIndexRepairKind, sched)
	c.Title = "Repair review-queue status index"
	c.Help = reviewStatusIndexRepairHelp
	c.Effects = tasks.Writes(tasks.Res("books", string(ops.ResReviewItems)))
	c.Resume = tasks.ResumeDrop
	c.Priority = tasks.PriorityLow
	c.ConcurrencyKey = tasks.Key("books.maintenance.review-status-index-repair")
	c.Timeout = 30 * time.Minute
	return tasks.Task(reviewStatusIndexRepairKind, tasks.TaskSpec[json.RawMessage, reviewStatusIndexRepairResult]{
		Common: c,
		Run: func(rc *tasks.RC, raw json.RawMessage) (reviewStatusIndexRepairResult, error) {
			// The mode is the run's, never a param: apply is HEAD's selector for it.
			if err := t6g7RefuseModeKeys(reviewStatusIndexRepairKind, raw); err != nil {
				return reviewStatusIndexRepairResult{}, err
			}
			live := rc.Mode() == tasks.ModeLive
			env := reviewStatusIndexRepairEnv{t6g7Env: t6g7NativeEnv(rc)}
			if deps.Store != nil {
				env.Store = deps.Store
			}
			if live {
				w, err := t6g7NewWriter(reviewStatusIndexRepairKind, deps, rc)
				if err != nil {
					return reviewStatusIndexRepairResult{}, err
				}
				env.Writer = w
			}
			res, err := reviewStatusIndexRepairCore(rc.Context(), live, env)
			if err != nil {
				return reviewStatusIndexRepairResult{}, err
			}
			return res, reviewStatusIndexRepairReport(env.t6g7Env, res)
		},
	})
}

// reviewStatusIndexRepairReport logs the tally and closes the progress bar: the
// part of HEAD's shell both shells share.
func reviewStatusIndexRepairReport(env t6g7Env, result reviewStatusIndexRepairResult) error {
	summary := fmt.Sprintf("review-status-index-repair: %d items, %d index rows; stale found=%d removed=%d, missing found=%d added=%d (dry_run=%t)",
		result.ItemsScanned, result.IndexEntriesScanned,
		result.StaleIndexEntriesFound, result.StaleIndexEntriesRemoved,
		result.MissingIndexEntriesFound, result.MissingIndexEntriesAdded,
		result.DryRun)
	if b, mErr := json.Marshal(result); mErr == nil {
		env.Log(slog.LevelInfo, summary, slog.String("report", string(b)))
	} else {
		env.Log(slog.LevelInfo, summary)
	}
	if result.DryRun && (result.StaleIndexEntriesFound > 0 || result.MissingIndexEntriesFound > 0) {
		env.Log(slog.LevelWarn,
			"review-status-index-repair: inconsistencies found and NOT fixed — re-run with {\"apply\": true}",
			slog.Int("stale", result.StaleIndexEntriesFound),
			slog.Int("missing", result.MissingIndexEntriesFound))
	}
	env.Progress(1, 1, summary)
	return nil
}

// reviewStatusIndexRepairCore does the work and returns the tally; apply false
// scans through the store and writes nothing, true rebuilds through the writer.
func reviewStatusIndexRepairCore(ctx context.Context, apply bool, env reviewStatusIndexRepairEnv) (reviewStatusIndexRepairResult, error) {
	if err := ctx.Err(); err != nil {
		return reviewStatusIndexRepairResult{}, err
	}
	var (
		rep database.ReviewStatusIndexRepair
		err error
	)
	switch {
	case apply && env.Writer != nil:
		rep, err = env.Writer.RebuildReviewStatusIndex(true)
	case !apply && env.Store != nil:
		rep, err = env.Store.ReviewStatusIndexReport()
	default:
		return reviewStatusIndexRepairResult{}, fmt.Errorf("review-status-index-repair: store does not support a status-index rebuild")
	}
	if err != nil {
		return reviewStatusIndexRepairResult{}, fmt.Errorf("review-status-index-repair: %w", err)
	}

	result := reviewStatusIndexRepairResult{
		DryRun:                   !apply,
		ItemsScanned:             rep.ItemsScanned,
		IndexEntriesScanned:      rep.IndexEntriesScanned,
		StaleIndexEntriesFound:   rep.StaleIndexEntries,
		MissingIndexEntriesFound: rep.MissingIndexEntries,
	}
	if rep.Applied {
		result.StaleIndexEntriesRemoved = rep.StaleIndexEntries
		result.MissingIndexEntriesAdded = rep.MissingIndexEntries
	}
	return result, nil
}
