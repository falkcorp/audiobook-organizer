// file: internal/maintenance/jobs/prune_book_snapshots.go
// version: 1.2.0
// guid: f3714dde-e788-468a-958a-5514eebcd952
// last-edited: 2026-11-17

package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"sync/atomic"

	"golang.org/x/sync/errgroup"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
)

func init() { maintenance.Register(&pruneBookSnapshotsJob{}) }

type pruneBookSnapshotsJob struct{}

// pbs_params carries the retention depth. MaintenanceJob.Run is handed only
// dryRun, so anything else rides the context.
//
// This originally copied revert_metadata_fetch's GetOperationParams(opID) call.
// That path is DEAD for maintenance jobs: GetOperationParams reads the Pebble key
// opstate:<opID>:params, written only by operations.SaveParams, whose sole
// production callers are internal/organizer/service.go and
// internal/itunes/service/importer.go -- the maintenance dispatcher's call went
// away with the v1 op minter (#2784). So keep_count silently never arrived and
// every run used the default. The package test passed because it stubbed
// GetOperationParamsFunc, a hook production never populates.
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

// Policy is DefaultPolicy. Two earlier justifications for deviating from it did
// not survive checking:
//
//   - ResumeRestart "so keep_count survives a restart" — but ResumeDrop never
//     resumes, so it never needs to reconstruct params at all. The argument was
//     against ResumeRequeue (which mints a fresh operation id and would lose the
//     params), not in favour of Restart over Drop. Dropping an in-flight prune is
//     acceptable: the job is idempotent and cheap to re-trigger, and CanResume()
//     is false because it genuinely does not checkpoint.
//   - A ConcurrencyKey "because concurrent runs issue overlapping deletes" — they
//     do, but a Pebble delete of an already-deleted key is a no-op, so two
//     simultaneous runs waste work without corrupting anything. Wasteful is not
//     the bar the bridge's empty key is measured against.
func (j *pruneBookSnapshotsJob) Policy() maintenance.ExecutionPolicy {
	return maintenance.DefaultPolicy()
}

// Not resumable: a re-run is idempotent (a book already at keep_count prunes
// zero), so restarting from the beginning costs a keys-only count per book and
// never over-deletes.
func (j *pruneBookSnapshotsJob) CanResume() bool { return false }

// keepCountFromCtx reads keep_count off the live params channel. A missing,
// unparseable, or non-positive value falls back to the default: a zero would
// otherwise mean "keep nothing", turning a retention tweak into a full history
// wipe, so zero is deliberately NOT load-bearing here.
func keepCountFromCtx(ctx context.Context) int {
	raw := maintenance.RawParamsFromCtx(ctx)
	if len(raw) == 0 {
		return defaultKeepCount
	}
	var p pbs_params
	if err := json.Unmarshal(raw, &p); err != nil {
		slog.Warn("prune-book-snapshots: could not decode run params; using default keep_count",
			"error", err, "default", defaultKeepCount)
		return defaultKeepCount
	}
	if p.KeepCount <= 0 {
		return defaultKeepCount
	}
	return p.KeepCount
}

// PruneBookSnapshotsResult is the run's structured result, served by
// GET /operations/:id/result. A dry run is the only way to see what a real run
// would do, so its numbers come from the same planner the delete uses.
type PruneBookSnapshotsResult struct {
	DryRun    bool `json:"dry_run"`
	KeepCount int  `json:"keep_count"`
	// BooksScanned is every book visited.
	BooksScanned int `json:"books_scanned"`
	// BooksPruned is the books that had something to delete (dry run: would).
	BooksPruned int `json:"books_pruned"`
	// SnapshotsDeleted is the snapshots removed (dry run: that would be).
	SnapshotsDeleted int `json:"snapshots_deleted"`
	// PinnedByJournal is the snapshots a prune would have removed but kept, because
	// a live merge journal row points at them: the undo of an automatic merge
	// restores from those.
	PinnedByJournal int `json:"pinned_by_journal"`
	// Failed is the books whose plan or delete errored.
	Failed int `json:"failed"`

	// The fields below are set on a dry run only. They report, read-only, whether
	// a merge's undo snapshots have ALREADY been lost (to a prune that ran before
	// the pin existed, or a manual delete). Nothing is repaired.
	//
	// LiveMergeJournals is the live auto-merge journal entries that name a snapshot.
	LiveMergeJournals int `json:"live_merge_journals"`
	// MergeJournalsMissingSnapshots is how many of those name a snapshot that is
	// gone, so UnmergeAuto would refuse them; MergeJournalsMissingIDs lists their
	// journal ids (ids only) up to mergeJournalIDCap, and
	// MergeJournalsMissingTruncated counts the rest.
	MergeJournalsMissingSnapshots int      `json:"merge_journals_missing_snapshots"`
	MergeJournalsMissingIDs       []string `json:"merge_journals_missing_ids,omitempty"`
	MergeJournalsMissingTruncated int      `json:"merge_journals_missing_truncated,omitempty"`

	// StaleProvisionalMergeJournals counts auto-merge entries that have been
	// provisional for over an hour: the merge failed, or its finalizing write
	// never landed, so the entry cannot be undone. Each keeps pinning the later
	// snapshots of its two books for as long as the row exists; this count is the
	// bug report, and StaleProvisionalMergeJournalIDs names them (ids only,
	// capped; the _truncated count is the rest).
	StaleProvisionalMergeJournals          int      `json:"stale_provisional_merge_journals"`
	StaleProvisionalMergeJournalIDs        []string `json:"stale_provisional_merge_journal_ids,omitempty"`
	StaleProvisionalMergeJournalsTruncated int      `json:"stale_provisional_merge_journals_truncated,omitempty"`
}

// mergeJournalIDCap bounds the journal ids listed in a dry run's result.
const mergeJournalIDCap = 200

func (j *pruneBookSnapshotsJob) Run(ctx context.Context, store maintenance.JobStore, reporter maintenance.ProgressReporter, dryRun bool) error {
	keepCount := keepCountFromCtx(ctx)

	ids, err := store.ListBookIDs()
	if err != nil {
		return fmt.Errorf("list book ids: %w", err)
	}
	reporter.SetTotal(len(ids))
	reporter.Log("info", fmt.Sprintf("Pruning snapshots for %d books, keeping %d per book (dry_run=%t)",
		len(ids), keepCount, dryRun), nil)

	// One pass over the merge journal, before the per-book pool starts: it is a
	// property of the journal, not of any book. A failure fails the run rather
	// than reporting "none lost" for a journal that could not be read.
	var audit database.MergeJournalSnapshotAudit
	if dryRun {
		var aerr error
		if audit, aerr = store.AuditMergeJournalSnapshots(mergeJournalIDCap); aerr != nil {
			return fmt.Errorf("audit merge journal snapshots: %w", aerr)
		}
	}

	var scanned, booksPruned, deleted, pinned, failed atomic.Int64

	// Each task owns exactly one book ID and every ID in the list is distinct,
	// so the worker set is partitioned by row: two workers can never prune the
	// same book_ver: prefix concurrently. Bounded at NumCPU because the work is
	// a keys-only iteration plus a batched delete, not network-bound.
	//
	// The set of snapshots a merge journal pins is built once by the store and
	// shared by every worker (store.PlanBookSnapshotPrune / PruneBookSnapshots),
	// not rebuilt per book.
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

			plan, cerr := store.PlanBookSnapshotPrune(id, keepCount)
			if cerr != nil {
				failed.Add(1)
				msg := cerr.Error()
				reporter.Log("warn", "Plan snapshot prune failed for book "+id, &msg)
				return nil
			}
			pinned.Add(int64(plan.PinnedByJournal))
			if plan.Delete == 0 {
				return nil
			}

			if dryRun {
				booksPruned.Add(1)
				deleted.Add(int64(plan.Delete))
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
	reporter.Log("info", fmt.Sprintf("%s %d snapshots across %d books (scanned %d, kept %d pinned by a merge journal, failed %d)",
		verb, deleted.Load(), booksPruned.Load(), scanned.Load(), pinned.Load(), failed.Load()), nil)

	res := PruneBookSnapshotsResult{
		DryRun: dryRun, KeepCount: keepCount,
		BooksScanned: int(scanned.Load()), BooksPruned: int(booksPruned.Load()),
		SnapshotsDeleted: int(deleted.Load()), PinnedByJournal: int(pinned.Load()), Failed: int(failed.Load()),
		LiveMergeJournals:             audit.LiveEntries,
		MergeJournalsMissingSnapshots: audit.MissingSnapshotEntries,
		MergeJournalsMissingIDs:       audit.MissingJournalIDs,
		MergeJournalsMissingTruncated: audit.Truncated,

		StaleProvisionalMergeJournals:          audit.StaleProvisional,
		StaleProvisionalMergeJournalIDs:        audit.StaleProvisionalIDs,
		StaleProvisionalMergeJournalsTruncated: audit.StaleProvisionalTruncated,
	}
	if dryRun {
		reporter.Log("info", fmt.Sprintf("Merge journal audit: %d live entries, %d with undo snapshots already missing, %d stale provisional entries",
			audit.LiveEntries, audit.MissingSnapshotEntries, audit.StaleProvisional), nil)
	}
	if rerr := maintenance.SetResult(ctx, res); rerr != nil {
		// A dry run IS its result, so losing it fails the run. A real run's
		// deletes already happened and are in the log line above.
		if dryRun || !errors.Is(rerr, maintenance.ErrNoResultSetter) {
			return fmt.Errorf("persist prune result: %w", rerr)
		}
	}
	if failed.Load() > 0 {
		return fmt.Errorf("prune completed with %d failures", failed.Load())
	}
	return nil
}
