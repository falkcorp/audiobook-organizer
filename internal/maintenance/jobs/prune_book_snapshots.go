// file: internal/maintenance/jobs/prune_book_snapshots.go
// version: 1.0.0
// guid: f3714dde-e788-468a-958a-5514eebcd952
// last-edited: 2026-08-29

package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"sync/atomic"

	"golang.org/x/sync/errgroup"

	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
)

func init() { maintenance.Register(&pruneBookSnapshotsJob{}) }

type pruneBookSnapshotsJob struct{}

// pbs_params carries the retention depth. It arrives the same way
// revert_metadata_fetch's fetch_op_ids does: MaintenanceJob.Run is handed only
// dryRun, so anything else has to be read back off the operation row via
// GetOperationParams(OperationIDFromCtx(ctx)).
type pbs_params struct {
	DryRun    bool `json:"dry_run"`
	KeepCount int  `json:"keep_count"`
}

// defaultKeepCount retains enough history for booksig_recovery_audit to find a
// pre-wipe signature while discarding the long tail. Measured 2026-08-29: the
// mean book_ver: blob is ~15 KB and 85.4% of it is an unchanged book_sig_v1
// copy, so the tail is almost entirely duplicated signature bytes.
const defaultKeepCount = 10

func (j *pruneBookSnapshotsJob) ID() string       { return "prune-book-snapshots" }
func (j *pruneBookSnapshotsJob) Name() string     { return "Prune Book Version Snapshots" }
func (j *pruneBookSnapshotsJob) Category() string { return "database" }
func (j *pruneBookSnapshotsJob) DefaultParams() any {
	return &pbs_params{DryRun: true, KeepCount: defaultKeepCount}
}
func (j *pruneBookSnapshotsJob) Description() string {
	return "Delete old copy-on-write book version snapshots library-wide, keeping the newest keep_count per book"
}

// Policy is RestartPolicy, not DefaultPolicy, for a reason specific to this job:
// keep_count is read back off the operation row via GetOperationParams. ResumeDrop
// would abandon a library-wide run on any restart, and ResumeRequeue "mints a fresh
// ULID and moves all of that" — including the params this job needs. ResumeRestart
// re-dispatches the SAME row, so the retention depth survives the restart.
//
// ConcurrencyKey is set because two concurrent runs would each count the same
// book_ver: prefix and issue overlapping deletes; the job is idempotent across
// sequential runs, not across simultaneous ones.
func (j *pruneBookSnapshotsJob) Policy() maintenance.ExecutionPolicy {
	p := maintenance.RestartPolicy()
	p.ConcurrencyKey = "prune-book-snapshots"
	return p
}

// Not resumable: a re-run is idempotent (a book already at keep_count prunes
// zero), so restarting from the beginning costs a keys-only count per book and
// never over-deletes.
func (j *pruneBookSnapshotsJob) CanResume() bool { return false }

func (j *pruneBookSnapshotsJob) Run(ctx context.Context, store maintenance.JobStore, reporter maintenance.ProgressReporter, dryRun bool) error {
	keepCount := defaultKeepCount
	if opID := maintenance.OperationIDFromCtx(ctx); opID != "" {
		if raw, err := store.GetOperationParams(opID); err == nil && len(raw) > 0 {
			var p pbs_params
			if jerr := json.Unmarshal(raw, &p); jerr == nil && p.KeepCount > 0 {
				keepCount = p.KeepCount
			}
		}
	}

	ids, err := store.ListBookIDs()
	if err != nil {
		return fmt.Errorf("list book ids: %w", err)
	}
	reporter.SetTotal(len(ids))
	reporter.Log("info", fmt.Sprintf("Pruning snapshots for %d books, keeping %d per book (dry_run=%t)",
		len(ids), keepCount, dryRun), nil)

	var scanned, booksPruned, deleted, failed atomic.Int64

	// Each task owns exactly one book ID and every ID in the list is distinct,
	// so the worker set is partitioned by row: two workers can never prune the
	// same book_ver: prefix concurrently. Bounded at NumCPU because the work is
	// a keys-only iteration plus a batched delete, not network-bound.
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(runtime.NumCPU())

	for _, id := range ids {
		select {
		case <-gctx.Done():
			return gctx.Err()
		default:
		}
		g.Go(func() error {
			defer reporter.Increment()
			if gctx.Err() != nil {
				return nil
			}
			scanned.Add(1)

			n, cerr := store.CountBookSnapshots(id)
			if cerr != nil {
				failed.Add(1)
				msg := cerr.Error()
				reporter.Log("warn", "Count snapshots failed for book "+id, &msg)
				return nil
			}
			if n <= keepCount {
				return nil
			}

			if dryRun {
				booksPruned.Add(1)
				deleted.Add(int64(n - keepCount))
				return nil
			}

			removed, perr := store.PruneBookSnapshots(id, keepCount)
			if perr != nil {
				failed.Add(1)
				msg := perr.Error()
				reporter.Log("warn", "Prune failed for book "+id, &msg)
				return nil
			}
			if removed > 0 {
				booksPruned.Add(1)
				deleted.Add(int64(removed))
			}
			return nil
		})
	}
	if werr := g.Wait(); werr != nil {
		return werr
	}

	verb := "Deleted"
	if dryRun {
		verb = "Would delete"
	}
	reporter.Log("info", fmt.Sprintf("%s %d snapshots across %d books (scanned %d, failed %d)",
		verb, deleted.Load(), booksPruned.Load(), scanned.Load(), failed.Load()), nil)
	if failed.Load() > 0 {
		return fmt.Errorf("prune completed with %d failures", failed.Load())
	}
	return nil
}
