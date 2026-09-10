// file: internal/maintenance/jobs/backfill_file_hashes.go
// version: 1.7.0
// guid: a1000014-0000-0000-0000-000000000014
// last-edited: 2026-09-10

package jobs

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"log/slog"

	"golang.org/x/sync/errgroup"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/filehash"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
	"github.com/falkcorp/audiobook-organizer/internal/operations"
)

func init() { maintenance.Register(&backfillFileHashesJob{}) }

type backfillFileHashesJob struct{}

func (j *backfillFileHashesJob) ID() string       { return "backfill-file-hashes" }
func (j *backfillFileHashesJob) Name() string     { return "Backfill File Hashes" }
func (j *backfillFileHashesJob) Category() string { return "files" }
func (j *backfillFileHashesJob) DefaultParams() any {
	return struct {
		DryRun bool `json:"dry_run"`
	}{DryRun: false}
}
func (j *backfillFileHashesJob) Description() string {
	return "Compute and store file hashes for book_files missing them"
}

// hashBackfillWorkers bounds the parallel hashing pool. Hashing reads whole
// files (<=100MB) or ~20MB of head+tail chunks each, off what in production is
// a network-attached volume, so this work is IO-bound: a handful of workers
// hides mount latency without thrashing the filesystem or exhausting file
// descriptors. Override with ABK_HASH_BACKFILL_WORKERS.
func hashBackfillWorkers() int {
	const def = 8
	if v := os.Getenv("ABK_HASH_BACKFILL_WORKERS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}

// hashBackfillChunkSize is how many files are dispatched to the worker pool
// before a checkpoint is written. See the checkpoint invariant in Run.
const hashBackfillChunkSize = 512

// Job supports checkpoint-based resume after restart.
func (j *backfillFileHashesJob) CanResume() bool { return true }
func (j *backfillFileHashesJob) Run(ctx context.Context, store maintenance.JobStore, reporter maintenance.ProgressReporter, dryRun bool) error {
	files, err := store.GetAllBookFilesCore()
	if err != nil {
		return err
	}
	reporter.SetTotal(len(files))

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

	var hashed atomic.Int64
	// repMu serializes access to reporter, whose maintenance.ProgressAdapter
	// increments a plain int (progress.go) and is NOT concurrency-safe. The
	// guarded calls are microseconds; file hashing dominates, so this does not
	// meaningfully serialize the pool.
	var repMu sync.Mutex
	workers := hashBackfillWorkers()

	// Process in ORDERED chunks. Each chunk runs through a bounded worker pool,
	// and the checkpoint is written only AFTER the whole chunk completes. That
	// preserves the invariant the old serial loop had for free -- every index
	// below the checkpoint has been processed -- which a naive fan-out would
	// break: a run killed mid-flight could otherwise checkpoint an index past
	// rows still being hashed, and resume would skip them forever. Distinct
	// rows are hashed concurrently; SetBookFileHash round-trips the full record
	// (no field wipe) and writes one distinct key per row, so parallel writes
	// across different rows do not race.
	for start := resumeIndex; start < len(files); start += hashBackfillChunkSize {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		end := start + hashBackfillChunkSize
		if end > len(files) {
			end = len(files)
		}

		g, gctx := errgroup.WithContext(ctx)
		g.SetLimit(workers)
		for i := start; i < end; i++ {
			bf := files[i]
			g.Go(func() error {
				if gctx.Err() != nil {
					return gctx.Err()
				}
				repMu.Lock()
				reporter.Increment()
				repMu.Unlock()
				if bf.FileHash != "" {
					return nil
				}
				// filehash.BookFileHash directly, not scanner.ComputeFileHash:
				// the latter is a thin wrapper whose only remaining job is the
				// activeScanner test seam, so a package-global set by a test
				// could swap the algorithm out from under the very job that
				// repairs this column. Same digest, one fewer place it can be
				// substituted.
				hash, herr := filehash.BookFileHash(bf.FilePath)
				if herr != nil {
					msg := herr.Error()
					slog.Warn("backfill-file-hashes hash failed for"+bf.FilePath, "details", msg)
					repMu.Lock()
					reporter.Log("warn", "Hash computation failed for "+bf.FilePath, &msg)
					repMu.Unlock()
					return nil // one unreadable file must not abort the run
				}
				if !dryRun {
					if serr := store.SetBookFileHash(bf.ID, hash); serr != nil {
						msg := serr.Error()
						slog.Error("backfill-file-hashes SetBookFileHash failed", "details", msg)
						repMu.Lock()
						reporter.Log("error", "Failed to save file hash for "+bf.FilePath, &msg)
						repMu.Unlock()
						return nil
					}
				}
				hashed.Add(1)
				return nil
			})
		}
		// Per-file failures are swallowed above, so a non-nil result here means
		// the context was cancelled. Return it: the checkpoint from the last
		// fully-completed chunk stands, and resume restarts at that boundary.
		if gerr := g.Wait(); gerr != nil {
			return gerr
		}

		// Periodic checkpoint so long runs can resume after restart.
		if opID != "" {
			_ = operations.SaveCheckpoint(store, opID, "maintenance:backfill-file-hashes", "scanning", end, len(files))
		}
	}

	// Clear any saved state on clean completion.
	if opID != "" {
		_ = operations.ClearState(store, opID)
	}

	// Save a lightweight operation summary for the UI and activity feed.
	res := fmt.Sprintf("Backfilled hashes for %d files", hashed.Load())
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

// Policy: ResumeRestart because this job checkpoints via
// operations.SaveCheckpoint, so a resumed run has real progress to reload.
// PR-2 moves it to reporter.Checkpoint; the policy is unchanged by that move.
func (j *backfillFileHashesJob) Policy() maintenance.ExecutionPolicy {
	return maintenance.RestartPolicy()
}
