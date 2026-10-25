// file: internal/maintenance/jobs/backfill_file_hashes.go
// version: 1.10.0
// guid: a1000014-0000-0000-0000-000000000014
// last-edited: 2026-10-25

package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
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
		DryRun  bool     `json:"dry_run"`
		BookIDs []string `json:"book_ids,omitempty"`
	}{DryRun: true} // preview by default (owner 2026-09-25); send dry_run=false to apply
}

// hashBackfillScope is the part of the params the job reads itself. dry_run
// is NOT here on purpose: the dispatcher resolves it (omitted = preview) and
// passes it as Run's argument, and the opmode guard refuses a plain-bool
// dry_run field on any named params struct. BookIDs, when set, limits the
// run to those books' rows: a repair that needs hashes for one set of books
// (the fragment fixer proves a duplicate copy by hash) must not have to hash
// the whole library first. Empty means every row, as before.
type hashBackfillScope struct {
	BookIDs []string `json:"book_ids,omitempty"`
}

// scopeToBooks keeps the rows of the given books, in their original order.
func scopeToBooks(files []database.BookFileCore, bookIDs []string) []database.BookFileCore {
	if len(bookIDs) == 0 {
		return files
	}
	want := make(map[string]bool, len(bookIDs))
	for _, id := range bookIDs {
		want[id] = true
	}
	out := files[:0:0]
	for _, f := range files {
		if want[f.BookID] {
			out = append(out, f)
		}
	}
	return out
}
func (j *backfillFileHashesJob) Description() string {
	return "Compute and store file hashes for book_files missing them; rows already marked missing are skipped (re-checked by stat) and a file that is not found is marked missing instead of retried every run"
}

// hashBackfillMissingLogCap bounds how many newly-missing paths one run writes
// to the operation log. The first run after this change can find hundreds of
// thousands of absent files; they are all COUNTED (newly_missing) and flagged,
// but one log row each would bury everything else in the op log.
const hashBackfillMissingLogCap = 50

// fileAbsent reports whether err means the path is definitively not there:
// not found (ENOENT) or a path component that is not a directory (ENOTDIR).
// Anything else -- permission denied, an I/O error, a stale network handle --
// leaves the file's state unknown and is NOT an answer, so it is never used to
// mark a row missing.
func fileAbsent(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR)
}

// hashBackfillCounts is one run's outcome tally. Every un-hashed row lands in
// exactly one of hashed, skippedKnownMissing, newlyMissing, readErrors or
// writeErrors (a row changed under the run by another writer lands in none).
// Atomics: the worker pool updates them concurrently.
type hashBackfillCounts struct {
	hashed              atomic.Int64 // hash computed (and, unless dry-run, stored)
	skippedKnownMissing atomic.Int64 // row already marked missing and still not on disk
	newlyMissing        atomic.Int64 // hash attempt found the file absent; row marked missing now
	readErrors          atomic.Int64 // hash failed for a reason other than not-found; retried next run
	writeErrors         atomic.Int64 // the hash or the missing marker could not be saved
}

// summary is the one-line result stored on the operation log. In a dry run the
// same counts are what an apply WOULD do: hashed is would-hash, newly_missing
// is would-mark-missing, and nothing is written.
func (c *hashBackfillCounts) summary(dryRun bool) string {
	prefix := "Backfilled hashes"
	if dryRun {
		prefix = "Dry run, nothing written (counts are what an apply would do)"
	}
	return fmt.Sprintf("%s: hashed=%d skipped_known_missing=%d newly_missing=%d read_errors=%d write_errors=%d",
		prefix, c.hashed.Load(), c.skippedKnownMissing.Load(), c.newlyMissing.Load(),
		c.readErrors.Load(), c.writeErrors.Load())
}

// markBookFileMissing records, on the row itself, that its file is not on disk,
// using the same book_file.Missing flag the scanner clears and mark-missing
// sets, so a later run (and the Broken Files counter) sees one fact in one
// place. It writes only the flag, under the row's lock, and only if the row is
// still the one this run read: same path (a repoint since then must not be
// flagged for the old path's absence) and still un-hashed. The row is never
// deleted. flipped is true only when this call changed the flag.
func markBookFileMissing(store maintenance.JobStore, bf database.BookFileCore, dryRun bool) (flipped bool, err error) {
	if dryRun {
		return !bf.Missing, nil
	}
	_, err = store.ModifyBookFile(bf.BookID, bf.ID, func(row *database.BookFile) error {
		if row.FilePath != bf.FilePath || row.FileHash != "" || row.Missing {
			return database.ErrSkipBookFileWrite
		}
		row.Missing = true
		flipped = true
		return nil
	})
	return flipped, err
}

// clearBookFileMissing is the reappear half: a row that was marked missing and
// whose file has now been hashed is present, so the flag is cleared (the
// scanner or mark-missing would do the same on their next pass; doing it here
// keeps the Broken Files counter from reading a hashed file as broken).
func clearBookFileMissing(store maintenance.JobStore, bf database.BookFileCore) error {
	_, err := store.ModifyBookFile(bf.BookID, bf.ID, func(row *database.BookFile) error {
		if row.FilePath != bf.FilePath || !row.Missing {
			return database.ErrSkipBookFileWrite
		}
		row.Missing = false
		return nil
	})
	return err
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
	if raw := maintenance.RawParamsFromCtx(ctx); len(raw) > 0 {
		var p hashBackfillScope
		if jerr := json.Unmarshal(raw, &p); jerr != nil {
			return fmt.Errorf("backfill-file-hashes: params: %w", jerr)
		}
		files = scopeToBooks(files, p.BookIDs)
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

	var counts hashBackfillCounts
	var missingLogged atomic.Int64
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
		end := min(start+hashBackfillChunkSize, len(files))

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
				if bf.Missing {
					// Known missing: a cheap stat decides whether to hash at all, so
					// the ~515k rows whose files are gone cost one stat each instead
					// of an open + failed hash + warning every run. Only a definitive
					// not-found skips; a successful stat (the file reappeared) or an
					// inconclusive one falls through to the hash attempt, which either
					// succeeds or reports the real error.
					if _, serr := os.Stat(bf.FilePath); serr != nil && fileAbsent(serr) {
						counts.skippedKnownMissing.Add(1)
						return nil
					}
				}
				// filehash.BookFileHash directly: scanner.ComputeFileHash is only
				// a thin wrapper over it, and filehash is the single entry point
				// for the digest.
				hash, herr := filehash.BookFileHash(bf.FilePath)
				if herr != nil {
					msg := herr.Error()
					if fileAbsent(herr) {
						flipped, merr := markBookFileMissing(store, bf, dryRun)
						switch {
						case merr != nil:
							counts.writeErrors.Add(1)
							emsg := merr.Error()
							repMu.Lock()
							reporter.Log("error", "Failed to mark missing file "+bf.FilePath, &emsg)
							repMu.Unlock()
						case flipped:
							counts.newlyMissing.Add(1)
							if missingLogged.Add(1) <= hashBackfillMissingLogCap {
								repMu.Lock()
								reporter.Log("warn", "File not found, marked missing: "+bf.FilePath, &msg)
								repMu.Unlock()
							}
						case bf.Missing:
							// Stat was inconclusive but the row was already flagged.
							counts.skippedKnownMissing.Add(1)
						}
						return nil
					}
					counts.readErrors.Add(1)
					slog.Warn("backfill-file-hashes hash failed", "file", bf.FilePath, "details", msg)
					repMu.Lock()
					reporter.Log("warn", "Hash computation failed for "+bf.FilePath, &msg)
					repMu.Unlock()
					return nil // one unreadable file must not abort the run
				}
				if !dryRun {
					if serr := store.SetBookFileHash(bf.ID, hash); serr != nil {
						counts.writeErrors.Add(1)
						msg := serr.Error()
						slog.Error("backfill-file-hashes SetBookFileHash failed", "details", msg)
						repMu.Lock()
						reporter.Log("error", "Failed to save file hash for "+bf.FilePath, &msg)
						repMu.Unlock()
						return nil
					}
					if bf.Missing {
						if cerr := clearBookFileMissing(store, bf); cerr != nil {
							counts.writeErrors.Add(1)
							msg := cerr.Error()
							repMu.Lock()
							reporter.Log("error", "Hashed reappeared file but could not clear its missing flag: "+bf.FilePath, &msg)
							repMu.Unlock()
						}
					}
				}
				counts.hashed.Add(1)
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
	res := counts.summary(dryRun)
	repMu.Lock()
	reporter.Log("info", res, nil)
	repMu.Unlock()
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
