// file: internal/maintenance/jobs/backfill_merge_undo_pointers.go
// version: 1.0.0
// guid: 844f88c2-fda0-43f4-bf8d-e4e35ca38b0c
// last-edited: 2026-10-18

package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
)

func init() { maintenance.Register(&backfillMergeUndoPointersJob{}) }

// The job builds a merge.Service over the store it is handed. Every
// database.Store is a merge.Store, so the runtime assertion in Run cannot start
// failing in prod because database.Store lost a method.
var _ merge.Store = database.Store(nil)

var (
	bmupLog = logger.New("backfill-merge-undo-pointers")

	errBackfillPointerStore = errors.New("backfill-merge-undo-pointers: store does not implement merge.Store; refusing to run")
)

// backfillMergeUndoPointersJob gives every combine journal and sibling-move
// journal written before the merge cores journaled their own undo pointer
// (merge/undo_pointer.go) a pointer row, so POST /operations/:id/revert can
// undo it. Until then the retired POST /merge/undo/:journal_id and
// /merge/sibling-undo/:journal_id routes were the only way back for those
// merges.
//
// Each journal gets a fresh correlation id of its own, never this job's
// operation id: reverting the backfill must never undo every historical
// combine at once. The ids are recorded as this job's operation results
// (journal -> operation_id), and the pointer is on the survivor, so GET
// /audiobooks/:id/changes lists it too.
//
// A preview writes nothing and records, per journal that would get a pointer,
// what it would write. Journals that already have a live pointer, and journals
// no undo would accept (undone, aborted, a CombineBooks journal left pending, a
// refused combine's emptied journal), are counted and skipped.
type backfillMergeUndoPointersJob struct{}

func (j *backfillMergeUndoPointersJob) ID() string       { return "backfill-merge-undo-pointers" }
func (j *backfillMergeUndoPointersJob) Name() string     { return "Backfill Merge Undo Pointers" }
func (j *backfillMergeUndoPointersJob) Category() string { return "library" }
func (j *backfillMergeUndoPointersJob) Description() string {
	return "Gives every older combine and sibling-move journal an undo pointer so Operations > Revert can undo it (preview first)"
}

// DefaultParams advertises dry_run:true, which the dispatcher and the op
// bridge apply when dry_run is omitted, so only an explicit "dry_run": false
// writes pointers.
func (j *backfillMergeUndoPointersJob) DefaultParams() any {
	return struct {
		DryRun bool `json:"dry_run"`
	}{DryRun: true}
}

// CanResume is false: a re-run skips every journal that already has a live
// pointer, so it picks up where an interrupted run stopped with no checkpoint.
func (j *backfillMergeUndoPointersJob) CanResume() bool { return false }

// Policy declares the bridge's existing behaviour verbatim: see DefaultPolicy.
func (j *backfillMergeUndoPointersJob) Policy() maintenance.ExecutionPolicy {
	return maintenance.DefaultPolicy()
}

// backfillPointerCounts tallies the run's verdicts.
type backfillPointerCounts struct {
	Journals    int `json:"journals"`
	Written     int `json:"written"`
	WouldWrite  int `json:"would_write"`
	HasPointer  int `json:"has_pointer"`
	NotUndoable int `json:"not_undoable"`
	Failed      int `json:"failed"`
}

// Run walks the journals one at a time, and that is deliberate rather than a
// missing worker pool. Each journal is decided and written under the
// process-wide merge lock (merge.Service.BackfillJournalPointer), which is what
// keeps the backfill from giving a merge in flight a second pointer; parallel
// workers would only queue on that lock. The per-journal work is one journal
// read, one ledger read and at most one row write, so the lock is held only
// briefly and merges interleave between journals.
func (j *backfillMergeUndoPointersJob) Run(ctx context.Context, store maintenance.JobStore, reporter maintenance.ProgressReporter, dryRun bool) error {
	ms, ok := store.(merge.Store)
	if !ok {
		return errBackfillPointerStore
	}
	svc := merge.NewService(ms)
	items, err := svc.ListPointerBackfillCandidates()
	if err != nil {
		return fmt.Errorf("backfill-merge-undo-pointers: list journals: %w", err)
	}
	opID := maintenance.OperationIDFromCtx(ctx)
	reporter.SetTotal(len(items))
	bmupLog.Info("starting journals=%d dry_run=%t", len(items), dryRun)

	counts := backfillPointerCounts{Journals: len(items)}
	var failures []error
	for _, item := range items {
		if err := ctx.Err(); err != nil {
			return err
		}
		out, berr := svc.BackfillJournalPointer(item, dryRun)
		reporter.Increment()
		if berr != nil {
			counts.Failed++
			failures = append(failures, fmt.Errorf("journal %s: %w", item.JournalID, berr))
			bmupLog.Warn("journal failed journal=%s type=%s err=%s",
				logger.SanitizeLogValue(item.JournalID), item.ChangeType, logger.SanitizeLogValue(berr.Error()))
			j.recordResult(store, opID, out, "failed", berr.Error())
			continue
		}
		switch out.Verdict {
		case merge.PointerBackfillWritten:
			counts.Written++
			j.recordResult(store, opID, out, out.Verdict, "")
		case merge.PointerBackfillWouldWrite:
			counts.WouldWrite++
			j.recordResult(store, opID, out, out.Verdict, "")
		case merge.PointerBackfillHasPointer:
			counts.HasPointer++
		case merge.PointerBackfillNotUndoable:
			counts.NotUndoable++
		}
	}

	summary, _ := json.Marshal(counts)
	msg := fmt.Sprintf("backfill-merge-undo-pointers complete (dry_run=%t): %s", dryRun, summary)
	bmupLog.Info("%s", msg)
	reporter.Log("info", msg, nil)
	if len(failures) > 0 {
		return fmt.Errorf("backfill-merge-undo-pointers: %d of %d journals failed: %w", len(failures), len(items), errors.Join(failures...))
	}
	return nil
}

// recordResult writes one operation result: the journal, its survivor and,
// for an applied pointer, the operation id that reverts the merge. A run with
// no operation id (a direct call outside the registry) records nothing.
func (j *backfillMergeUndoPointersJob) recordResult(store maintenance.JobStore, opID string, item merge.PointerBackfillItem, status, errMsg string) {
	if opID == "" {
		return
	}
	rec := struct {
		merge.PointerBackfillItem
		Error string `json:"error,omitempty"`
	}{item, errMsg}
	raw, err := json.Marshal(rec)
	if err != nil {
		bmupLog.Error("result not encoded journal=%s err=%s", logger.SanitizeLogValue(item.JournalID), logger.SanitizeLogValue(err.Error()))
		return
	}
	if err := store.CreateOperationResult(&database.OperationResult{
		OperationID: opID,
		BookID:      item.SurvivorID,
		ResultJSON:  string(raw),
		Status:      status,
	}); err != nil {
		bmupLog.Error("result not written journal=%s err=%s", logger.SanitizeLogValue(item.JournalID), logger.SanitizeLogValue(err.Error()))
	}
}
