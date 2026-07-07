// file: internal/maintenance/jobs/backfill_file_hashes.go
// version: 1.3.0
// guid: a1000014-0000-0000-0000-000000000014
// last-edited: 2026-07-06

package jobs

import (
	"context"
	"fmt"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
	"github.com/falkcorp/audiobook-organizer/internal/operations"
	"github.com/falkcorp/audiobook-organizer/internal/scanner"
	"log/slog")

func init() { maintenance.Register(&backfillFileHashesJob{}) }

type backfillFileHashesJob struct{}

func (j *backfillFileHashesJob) ID() string          { return "backfill-file-hashes" }
func (j *backfillFileHashesJob) Name() string        { return "Backfill File Hashes" }
func (j *backfillFileHashesJob) Category() string    { return "files" }
func (j *backfillFileHashesJob) DefaultParams() any  { return struct{ DryRun bool `json:"dry_run"` }{DryRun: false} }
func (j *backfillFileHashesJob) Description() string { return "Compute and store file hashes for book_files missing them" }
// Job supports checkpoint-based resume after restart.
func (j *backfillFileHashesJob) CanResume() bool { return true }
func (j *backfillFileHashesJob) Run(ctx context.Context, store database.Store, reporter maintenance.ProgressReporter, dryRun bool) error {
	files, err := store.GetAllBookFilesCore()
	if err != nil {
		return err
	}
	reporter.SetTotal(len(files))
	hashed := 0

	// Resume support: load checkpoint if present.
	opID := maintenance.OperationIDFromCtx(ctx)
	resumeIndex := 0
	if opID != "" {
		if cp, _ := operations.LoadCheckpoint(store, opID); cp != nil {
			// resume phase 'scanning'
			if cp.Phase == "scanning" {
				resumeIndex = cp.PhaseIndex
			}
		}
	}

	for i := resumeIndex; i < len(files); i++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		reporter.Increment()
		bf := files[i]
		if bf.FileHash != "" {
			continue
		}
		hash, herr := scanner.ComputeFileHash(bf.FilePath)
		if herr != nil {
			msg := herr.Error()
			slog.Warn("backfill-file-hashes hash failed for"+bf.FilePath, "details", msg)
			reporter.Log("warn", "Hash computation failed for "+bf.FilePath, &msg)
			continue
		}
		if !dryRun {
			if serr := store.SetBookFileHash(bf.ID, hash); serr != nil {
				msg := serr.Error()
				slog.Error("backfill-file-hashes SetBookFileHash failed", "details", msg)
				reporter.Log("error", "Failed to save file hash for "+bf.FilePath, &msg)
				continue
			}
		}
		hashed++

		// Periodic checkpoint so long runs can resume after restart.
		if opID != "" && i%50 == 0 {
			_ = operations.SaveCheckpoint(store, opID, "maintenance:backfill-file-hashes", "scanning", i, len(files))
		}
	}

	// Clear any saved state on clean completion.
	if opID != "" {
		_ = operations.ClearState(store, opID)
	}

	// Save a lightweight operation summary for the UI and activity feed.
	res := fmt.Sprintf("Backfilled hashes for %d files", hashed)
	now := time.Now()
	opLog := &database.OperationSummaryLog{
		ID:          opID,
		Type:        "backfill-file-hashes",
		Status:      "completed",
		Progress:    1.0,
		Result:      &res,
		CreatedAt:   now,
		UpdatedAt:   now,
		CompletedAt: &now,
	}
	_ = store.SaveOperationSummaryLog(opLog)

	slog.Info("backfill-file-hashes complete")
	return nil
}
