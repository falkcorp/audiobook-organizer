// file: internal/plugins/maintenance/review_status_index_repair.go
// version: 1.0.0
// guid: 9e2b7c41-3f8d-4a65-b1c9-7d0e5f2a8b36
// last-edited: 2026-09-02

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

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

func (p *Plugin) reviewStatusIndexRepairDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:          "maintenance.review-status-index-repair",
		DisplayName: "Repair review-queue status index",
		Description: "Rebuilds the review_item:status:* secondary index from the review " +
			"item records: removes index rows that name an item under a status it no " +
			"longer has (or that no longer exists) and adds the row for any item missing " +
			"from the index. Default dry-run reports the counts; pass {\"apply\": true} " +
			"to write the fix.",
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
	result, err := p.repairReviewStatusIndex(ctx, rawParams)
	if err != nil {
		return err
	}

	summary := fmt.Sprintf("review-status-index-repair: %d items, %d index rows; stale found=%d removed=%d, missing found=%d added=%d (dry_run=%t)",
		result.ItemsScanned, result.IndexEntriesScanned,
		result.StaleIndexEntriesFound, result.StaleIndexEntriesRemoved,
		result.MissingIndexEntriesFound, result.MissingIndexEntriesAdded,
		result.DryRun)
	if b, mErr := json.Marshal(result); mErr == nil {
		_ = reporter.Log(slog.LevelInfo, summary, slog.String("report", string(b)))
	} else {
		_ = reporter.Log(slog.LevelInfo, summary)
	}
	if result.DryRun && (result.StaleIndexEntriesFound > 0 || result.MissingIndexEntriesFound > 0) {
		_ = reporter.Log(slog.LevelWarn,
			"review-status-index-repair: inconsistencies found and NOT fixed — re-run with {\"apply\": true}",
			slog.Int("stale", result.StaleIndexEntriesFound),
			slog.Int("missing", result.MissingIndexEntriesFound))
	}
	return reporter.UpdateProgress(1, 1, summary)
}

// repairReviewStatusIndex does the work and returns the tally.
//
// Split from the reporter plumbing so the behaviour can be asserted directly
// rather than by parsing a log line back out of a fake reporter.
func (p *Plugin) repairReviewStatusIndex(ctx context.Context, rawParams json.RawMessage) (reviewStatusIndexRepairResult, error) {
	var params reviewStatusIndexRepairParams
	if len(rawParams) > 0 {
		if err := json.Unmarshal(rawParams, &params); err != nil {
			return reviewStatusIndexRepairResult{}, fmt.Errorf("review-status-index-repair: decode params: %w", err)
		}
	}
	if err := ctx.Err(); err != nil {
		return reviewStatusIndexRepairResult{}, err
	}

	store := p.deps.ReviewStatusIndexStore()
	if store == nil {
		return reviewStatusIndexRepairResult{}, fmt.Errorf("review-status-index-repair: store does not support a status-index rebuild")
	}

	rep, err := store.RebuildReviewStatusIndex(params.Apply)
	if err != nil {
		return reviewStatusIndexRepairResult{}, fmt.Errorf("review-status-index-repair: %w", err)
	}

	result := reviewStatusIndexRepairResult{
		DryRun:                   !params.Apply,
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
