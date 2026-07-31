// file: internal/maintenance/jobs/backfill_sync_ids.go
// version: 1.0.0
// guid: 85ae5c94-d001-49d9-9f65-97f73f32522b
// last-edited: 2026-07-30

package jobs

import (
	"context"
	"fmt"
	"log/slog"
	"sync/atomic"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

func init() { maintenance.Register(&backfillSyncIDsJob{}) }

// backfillSyncIDsJob assigns durable ABS identities to the existing library:
// a sync_item syncID per Book and a sync_file syncFileID per BookFile. See
// docs/agent-tasks/abs-sync/TASK-04-syncid-backfill.md and
// docs/specs/2026-07-29-abs-sync-api-design.md §4 / §4.2b.
//
// Both keyspaces mint on first encounter at request time anyway, so this job is
// not required for correctness — it exists so the whole library is
// identity-consistent BEFORE any client connects: no first-request latency
// spike, and no window where half the library has stable IDs and half does not.
type backfillSyncIDsJob struct{}

func (j *backfillSyncIDsJob) ID() string       { return "backfill-sync-ids" }
func (j *backfillSyncIDsJob) Name() string     { return "Backfill ABS Sync IDs" }
func (j *backfillSyncIDsJob) Category() string { return "library" }

func (j *backfillSyncIDsJob) Description() string {
	return "Mints durable ABS sync_item/sync_file identities for every book and book file that doesn't have one yet"
}

func (j *backfillSyncIDsJob) DefaultParams() any {
	return struct {
		DryRun bool `json:"dry_run"`
	}{DryRun: false}
}

// CanResume returns false deliberately, and NOT because the job is
// restart-unsafe. Every mint call (MintOrGetSyncID / MintOrGetSyncFileID) is
// independently idempotent, so re-running the whole job from book 0 after an
// interruption is both correct and cheap — an already-minted book or file is a
// single point-get skip, not re-work. There is therefore no index worth
// checkpointing, unlike backfill_file_hashes.go's resumeIndex.
func (j *backfillSyncIDsJob) CanResume() bool { return false }

func (j *backfillSyncIDsJob) Run(ctx context.Context, store database.Store, reporter maintenance.ProgressReporter, dryRun bool) error {
	syncStore := database.AsSyncIdentityStore(store)
	fileStore := database.AsSyncFileStore(store)
	if syncStore == nil || fileStore == nil {
		return fmt.Errorf("backfill-sync-ids: store does not implement the sync-identity capability interfaces")
	}

	// ListBookIDs, never GetAllBooksFrom: the latter's memdb fast path silently
	// caps a page at 2× the requested limit, so a paginated walk can miss books
	// (fixed once in #1647, still the documented hazard for new full-library
	// jobs). ListBookIDs returns the complete ID set with no pagination cap.
	ids, err := store.ListBookIDs()
	if err != nil {
		return fmt.Errorf("backfill-sync-ids: list book ids: %w", err)
	}
	reporter.SetTotal(len(ids))
	slog.Info("backfill-sync-ids: starting", "books", len(ids), "dry_run", dryRun,
		"concurrency", BackfillConcurrency())

	var booksMinted, booksAlready, filesMinted, filesAlready, failures atomic.Int64

	rep := &absBackfillReporter{ctx: ctx, inner: reporter}
	runErr := registry.RunItems(ctx, rep, ids, func(_ context.Context, bookID string) error {
		// Point-get first so a re-run's logging and counters distinguish
		// "already had one" from "minted now". MintOrGetSyncID would be correct
		// on its own — this is bookkeeping clarity, not a correctness need.
		_, hadSyncID, err := syncStore.GetSyncIDForBook(bookID)
		if err != nil {
			failures.Add(1)
			slog.Warn("backfill-sync-ids: read syncID failed", "book", bookID, "err", err)
			return nil
		}

		if !dryRun && !hadSyncID {
			if _, err := syncStore.MintOrGetSyncID(bookID); err != nil {
				failures.Add(1)
				slog.Warn("backfill-sync-ids: mint syncID failed", "book", bookID, "err", err)
				// Keep going into the file loop anyway: MintOrGetSyncFileID does
				// not depend on the book having a sync_item.
			}
		}
		if hadSyncID {
			booksAlready.Add(1)
		} else {
			booksMinted.Add(1)
		}

		files, ferr := store.GetBookFiles(bookID)
		if ferr != nil {
			failures.Add(1)
			slog.Warn("backfill-sync-ids: list book files failed", "book", bookID, "err", ferr)
			return nil
		}
		for _, f := range files {
			if f.ID == "" {
				failures.Add(1)
				slog.Warn("backfill-sync-ids: book file has no ID", "book", bookID, "path", f.FilePath)
				continue
			}
			_, hadFileID, err := fileStore.GetSyncFileID(bookID, f.ID)
			if err != nil {
				failures.Add(1)
				slog.Warn("backfill-sync-ids: read syncFileID failed", "book", bookID, "file", f.ID, "err", err)
				continue
			}
			if hadFileID {
				filesAlready.Add(1)
				continue
			}
			if !dryRun {
				if _, err := fileStore.MintOrGetSyncFileID(bookID, f.ID); err != nil {
					failures.Add(1)
					slog.Warn("backfill-sync-ids: mint syncFileID failed", "book", bookID, "file", f.ID, "err", err)
					continue
				}
			}
			filesMinted.Add(1)
		}
		return nil
	}, registry.RunItemsOptions{
		Concurrency: BackfillConcurrency(),
		// ErrModeCollect, not the ErrModeFail default: a handful of unreadable
		// books must not cancel the remaining tens of thousands. Per-item
		// errors are already absorbed above (warn + counter + return nil), so
		// this only matters for anything RunItems itself surfaces.
		ErrMode: registry.ErrModeCollect,
		Label:   func(i, total int) string { return fmt.Sprintf("book %d/%d", i+1, total) },
	})

	summary := fmt.Sprintf(
		"backfill-sync-ids complete (dry_run=%t): books %d minted / %d already had one, files %d minted / %d already had one, %d failures",
		dryRun, booksMinted.Load(), booksAlready.Load(), filesMinted.Load(), filesAlready.Load(), failures.Load())
	slog.Info(summary)
	reporter.Log("info", summary, nil)

	return runErr
}
