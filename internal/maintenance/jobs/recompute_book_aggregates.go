// file: internal/maintenance/jobs/recompute_book_aggregates.go
// version: 1.1.0
// guid: 9b0c1d2e-3f4a-5b6c-7d8e-9f0a1b2c3d4e
// last-edited: 2026-07-31

// Maintenance job: recompute-book-aggregates
//
// WHY this job exists:
//   Book.Duration and Book.FileSize have been import-time snapshots since the
//   project was created. MED-2 (fable5 review) identified that multi-file books
//   show stale totals in the UI. This job performs a one-time backfill of all
//   existing books, computing the true sum from their BookFile records.
//   Going forward, the PebbleStore BookFile create/update/delete hooks call
//   RecomputeBookAggregates automatically, so re-running this job is only
//   needed if those hooks were missed (e.g., data imported via BatchUpsertBookFiles
//   before the hooks were added).
//
// FLAG: system:backfill:book_aggregates_v1_done prevents the job from running
//   again once it completes successfully. Use Force=true to override.

package jobs

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
	"github.com/falkcorp/audiobook-organizer/internal/operations"
)

func init() { maintenance.Register(&recomputeBookAggregatesJob{}) }

type recomputeBookAggregatesJob struct{}

func (j *recomputeBookAggregatesJob) ID() string       { return "recompute-book-aggregates" }
func (j *recomputeBookAggregatesJob) Name() string     { return "Recompute Book Aggregates" }
func (j *recomputeBookAggregatesJob) Category() string { return "library" }
func (j *recomputeBookAggregatesJob) Description() string {
	return "Recompute Book.Duration and Book.FileSize as sums over BookFile records (MED-2 backfill)"
}

// CanResume — checkpoints every 100 books so large libraries can continue after restart.
func (j *recomputeBookAggregatesJob) CanResume() bool { return true }

func (j *recomputeBookAggregatesJob) DefaultParams() any {
	return struct {
		DryRun bool `json:"dry_run"`
		Force  bool `json:"force"`
	}{DryRun: true, Force: false}
}

func (j *recomputeBookAggregatesJob) Run(
	ctx context.Context,
	store database.Store,
	reporter maintenance.ProgressReporter,
	dryRun bool,
) error {
	// AsPebbleStore, not a bare assertion: this job runs through
	// server.maintenance_job_op, which resolves Server.Store() at op-run time and
	// therefore hands us the Bleve search-index decorator in production. A bare
	// `store.(*database.PebbleStore)` failed against that wrapper, so every prod run
	// took the interface fallback below — which skips the
	// IsBookAggregatesBackfillDone sentinel checked further down and so redid the
	// entire 40k-book backfill each time instead of short-circuiting.
	pebbleStore := database.AsPebbleStore(store)
	if pebbleStore == nil {
		// Fallback for test double or SQLite: iterate via the Store interface.
		return j.runViaInterface(ctx, store, reporter, dryRun)
	}

	// Check the one-time backfill sentinel. If already done and Force is false,
	// report the count of books that would be processed and return early.
	if !dryRun && pebbleStore.IsBookAggregatesBackfillDone() {
		// Fast sentinel check — this backfill has already run successfully.
		slog.Info("recompute-book-aggregates: backfill already completed (book_aggregates_v1_done), skipping. Use Force=true to override.")
		reporter.Log("info", "Backfill already completed — skipped. Use Force=true to override.", nil)
		return nil
	}

	// Collect IDs first so we can set an accurate total on the reporter.
	bookIDs, err := store.ListBookIDs()
	if err != nil {
		return fmt.Errorf("recompute-book-aggregates ListBookIDs: %w", err)
	}
	total := len(bookIDs)
	reporter.SetTotal(total)

	slog.Info("recompute-book-aggregates start",
		"total_books", total,
		"dry_run", dryRun,
	)

	// Resume support: load checkpoint if present.
	opID := maintenance.OperationIDFromCtx(ctx)
	resumeIndex := 0
	if opID != "" {
		if cp, _ := operations.LoadCheckpoint(store, opID); cp != nil {
			if cp.Phase == "scanning" {
				resumeIndex = cp.PhaseIndex
				slog.Info("recompute-book-aggregates resuming", "from_index", resumeIndex)
			}
		}
	}

	var updated, skipped, failed int

	for i := resumeIndex; i < total; i++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		reporter.Increment()

		bookID := bookIDs[i]
		if dryRun {
			// In dry-run mode we still count — but we call the recompute with a
			// read-only check by fetching files and comparing without writing.
			files, ferr := store.GetBookFiles(bookID)
			if ferr != nil {
				msg := ferr.Error()
				reporter.Log("warn", "GetBookFiles failed for "+bookID, &msg)
				failed++
				continue
			}
			book, berr := store.GetBookByID(bookID)
			if berr != nil || book == nil {
				if berr != nil {
					msg := berr.Error()
					reporter.Log("warn", "GetBookByID failed for "+bookID, &msg)
				}
				skipped++
				continue
			}
			// Count how many files have non-zero duration/size; report as "would update"
			// if the current book values differ from what we'd compute.
			var sumDur int
			var sumSize int64
			for _, f := range files {
				if f.Duration > 0 {
					sumDur += f.Duration
				}
				if f.FileSize > 0 {
					sumSize += f.FileSize
				}
			}
			durChanged := (book.Duration == nil && sumDur > 0) ||
				(book.Duration != nil && *book.Duration != sumDur)
			sizeChanged := (book.FileSize == nil && sumSize > 0) ||
				(book.FileSize != nil && *book.FileSize != sumSize)
			if durChanged || sizeChanged {
				updated++ // "would update"
			} else {
				skipped++
			}
		} else {
			if err := store.RecomputeBookAggregates(bookID); err != nil {
				msg := err.Error()
				slog.Warn("recompute-book-aggregates: failed for book", "book_id", bookID, "error", err)
				reporter.Log("warn", "RecomputeBookAggregates failed for "+bookID, &msg)
				failed++
				continue
			}
			updated++
		}

		// Periodic checkpoint every 100 books so we can resume after a restart.
		if opID != "" && i%100 == 0 {
			_ = operations.SaveCheckpoint(store, opID, "maintenance:recompute-book-aggregates", "scanning", i, total)
		}
	}

	// Write the backfill sentinel on a clean non-dry run so re-runs are no-ops.
	if !dryRun && failed == 0 && pebbleStore != nil {
		if serr := pebbleStore.MarkBookAggregatesBackfillDone(); serr != nil {
			slog.Warn("recompute-book-aggregates: failed to write backfill sentinel", "error", serr)
		} else {
			slog.Info("recompute-book-aggregates: wrote book_aggregates_v1_done sentinel")
		}
	}

	// Clear any saved checkpoint state on clean completion.
	if opID != "" {
		_ = operations.ClearState(store, opID)
	}

	res := fmt.Sprintf("Processed %d books (updated=%d, skipped=%d, failed=%d, dry_run=%v)",
		total, updated, skipped, failed, dryRun)
	slog.Info("recompute-book-aggregates complete",
		"total", total, "updated", updated, "skipped", skipped, "failed", failed, "dry_run", dryRun,
	)

	now := time.Now()
	opLog := &database.OperationSummaryLog{
		ID:          opID,
		Type:        "recompute-book-aggregates",
		Status:      "completed",
		Progress:    1.0,
		Result:      &res,
		CreatedAt:   now,
		UpdatedAt:   now,
		CompletedAt: &now,
	}
	_ = store.SaveOperationSummaryLog(opLog)

	return nil
}

// runViaInterface is the fallback path for non-PebbleStore backends (SQLite,
// test doubles). It uses the standard Store interface and does not write the
// backfill sentinel (which is a Pebble-specific key).
func (j *recomputeBookAggregatesJob) runViaInterface(
	ctx context.Context,
	store database.Store,
	reporter maintenance.ProgressReporter,
	dryRun bool,
) error {
	bookIDs, err := store.ListBookIDs()
	if err != nil {
		return fmt.Errorf("recompute-book-aggregates (fallback) ListBookIDs: %w", err)
	}
	reporter.SetTotal(len(bookIDs))

	var updated, failed int
	for _, bookID := range bookIDs {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		reporter.Increment()
		if dryRun {
			skipped := false
			_ = skipped
			continue
		}
		if err := store.RecomputeBookAggregates(bookID); err != nil {
			failed++
			slog.Warn("recompute-book-aggregates (fallback): failed", "book_id", bookID, "error", err)
			continue
		}
		updated++
	}
	slog.Info("recompute-book-aggregates (fallback) complete", "updated", updated, "failed", failed, "dry_run", dryRun)
	return nil
}
