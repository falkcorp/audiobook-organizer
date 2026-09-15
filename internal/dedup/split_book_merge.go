// file: internal/dedup/split_book_merge.go
// version: 1.8.0
// guid: 3b5d7f9a-2e4c-6b8d-0f1a-3c5e7d9f1b3e
// last-edited: 2026-09-14

// Split-book cluster merge — portable across SQLite and Pebble.
//
// The existing `MergeChapterBooks` store method is SQLite-only
// (PebbleStore returns nil without doing anything). The existing
// `merge.Service.MergeBooks` soft-deletes losers but does NOT move
// their BookFiles to the keeper — for chapter merges that would orphan
// every chapter file.
//
// This function uses the portable `MoveBookFilesToBook` (implemented on
// both stores) and only soft-deletes after the files AND the external-ID
// mappings have been reassigned. Duration is recomputed as the sum of
// moved-file durations. Every run is recorded in the same undo journal
// merge.Service.CombineBooks writes, so merge.Service.UndoCombine reverses it.

package dedup

import (
	"fmt"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
)

var splitMergeLog = logger.New("dedup.split-merge")

// SplitBookMergeJournalOrigin marks a combine journal written by
// MergeSplitBookCluster.
const SplitBookMergeJournalOrigin = "split_book_merge"

// SplitBookMergeResult summarises a successful merge.
type SplitBookMergeResult struct {
	KeepID         string `json:"keep_id"`
	MergedSrcCount int    `json:"merged_src_count"`
	FilesMoved     int    `json:"files_moved"`
	NewDuration    int    `json:"new_duration"`
	// TitleKeptLocked reports that a suggested title was offered but the keep
	// book's title is user-locked, so the user's title stands.
	TitleKeptLocked bool `json:"title_kept_locked,omitempty"`
	// JournalID names the combine journal that merge.Service.UndoCombine
	// reverses. Empty when the journal could not be finalized (see Errors).
	JournalID string   `json:"journal_id,omitempty"`
	Errors    []string `json:"errors,omitempty"`
}

// BulkSplitBookMergeItem is the immutable candidate snapshot consumed by the
// durable batch operation. Keeping the book IDs here means a later detector
// rescan cannot alter already-reviewed queued work.
type BulkSplitBookMergeItem struct {
	CandidateID    string   `json:"candidate_id"`
	BookIDs        []string `json:"book_ids"`
	KeepID         string   `json:"keep_id"`
	SuggestedTitle string   `json:"suggested_title"`
}

// BulkSplitBookMergeParams controls one queued batch. DryRun deliberately
// defaults to true in the HTTP handler; an apply requires an explicit false.
type BulkSplitBookMergeParams struct {
	Items  []BulkSplitBookMergeItem `json:"items"`
	DryRun bool                     `json:"dry_run"`
}

// splitSrcPlan is one src as read before the first write.
type splitSrcPlan struct {
	entry       merge.CombineAbsorbed
	fileIDs     []string
	hasMappings bool // any mapping, tombstoned included: all ride ReassignExternalIDs
}

// MergeSplitBookCluster absorbs every srcID into keepID:
//
//  0. Read every src (files, external-ID mappings) and write a Pending
//     combine journal before any mutation. A journal that cannot be written
//     refuses the merge.
//  1. For each src: MoveBookFilesToBook(ids, src, keep), then reassign its
//     external-ID mappings (iTunes PIDs etc.) to keep, then soft-delete it.
//     A src whose move or reassignment fails is left live -- deleting it would
//     strand audio or PIDs on a deleted row -- and re-running the merge
//     finishes it.
//  2. Recompute keep duration as sum of all bookfile durations.
//  3. Optionally update keep.Title to suggestedTitle (when non-empty).
//     Steps 2 and 3 go through ModifyBook and set only those two columns.
//  4. Mark the journal Applied, listing only the srcs that completed step 1.
//
// Per-src errors are collected but do not abort — the remaining sources
// still get processed so the operator doesn't end up with a half-merged
// cluster.
func MergeSplitBookCluster(store Store, keepID string, srcIDs []string, suggestedTitle string) (*SplitBookMergeResult, error) {
	if keepID == "" {
		return nil, fmt.Errorf("MergeSplitBookCluster: empty keepID")
	}
	if len(srcIDs) == 0 {
		return nil, fmt.Errorf("MergeSplitBookCluster: no srcIDs")
	}

	// This is a fourth unguarded read-modify-write over shared book rows
	// (GetBookByID -> MoveBookFilesToBook -> UpdateBook -> SoftDeleteBook), the
	// same failure class #1930 fixed for merge.Service.MergeBooks and that
	// dedup.MergeBooks (book_dedup.go) already guards with this same lock. A
	// user merging/combining a book via the dedup review UI while a split-book
	// bulk-merge op or the single-candidate handler touches the same book id
	// (as keepID or a srcID) could otherwise interleave writes and corrupt the
	// row the same way. See internal/merge/serialize.go. It is also the lock
	// merge.Service.UndoCombine takes, so an undo of this journal cannot
	// interleave with the merge that writes it.
	merge.LockMergeRMW()
	defer merge.UnlockMergeRMW()

	// Refuse before any write if keep or any src has a file under the active
	// iTunes library (merge.ErrITunesProtected).
	if err := merge.GuardITunesProtected(store, append([]string{keepID}, srcIDs...)); err != nil {
		return nil, err
	}

	keep, err := store.GetBookByID(keepID)
	if err != nil || keep == nil {
		return nil, fmt.Errorf("keep book %s not found: %w", keepID, err)
	}

	result := &SplitBookMergeResult{KeepID: keepID}
	eids := merge.AsExternalIDReassigner(store)

	// Step 0: read everything, then journal, before the first write. A src
	// that cannot be read is skipped whole (nothing is written for it).
	var plans []splitSrcPlan
	seen := map[string]bool{keepID: true}
	for _, srcID := range srcIDs {
		if seen[srcID] {
			continue
		}
		seen[srcID] = true
		plan, perr := planSplitSource(store, srcID, eids != nil)
		if perr != nil {
			result.Errors = append(result.Errors, perr.Error())
			continue
		}
		plans = append(plans, plan)
	}
	journal := merge.NewCombineJournal(keepID)
	journal.Origin = SplitBookMergeJournalOrigin
	for _, p := range plans {
		journal.Absorbed = append(journal.Absorbed, p.entry)
	}
	if err := merge.WriteCombineJournal(store, journal); err != nil {
		return nil, fmt.Errorf("split-book merge refused: undo journal could not be written: %w", err)
	}

	// Step 1: move files, reassign external IDs, soft-delete -- per src, in
	// that order. Only a src that completed all three enters the journal.
	kept := make([]merge.CombineAbsorbed, 0, len(plans))
	for _, p := range plans {
		srcID := p.entry.BookID
		if len(p.fileIDs) > 0 {
			if err := store.MoveBookFilesToBook(p.fileIDs, srcID, keepID); err != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("move files from %s: %v", srcID, err))
				continue
			}
			result.FilesMoved += len(p.fileIDs)
		}
		if p.hasMappings && eids != nil {
			// Fail closed, as merge.Service.MergeBooks does: a src soft-deleted
			// while still holding its PIDs would have them enqueued for iTunes
			// removal by the purge, for audio the keep now owns.
			if err := eids.ReassignExternalIDs(srcID, keepID); err != nil {
				msg := fmt.Sprintf("reassign external ids of %s: %v; %s left live, re-run the merge to finish it", srcID, err, srcID)
				result.Errors = append(result.Errors, msg)
				if len(p.fileIDs) > 0 {
					journal.Warnings = append(journal.Warnings, fmt.Sprintf("%d file(s) of %s moved to %s but %s was left live (external ids not reassigned)", len(p.fileIDs), srcID, keepID, srcID))
				}
				continue
			}
		}
		stamp, err := softDeleteSplitSource(store, srcID)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("soft-delete %s: %v", srcID, err))
			splitMergeLog.Warn("split-book merge soft-delete failed src=%s err=%s", logger.SanitizeLogValue(srcID), logger.SanitizeLogValue(fmt.Sprint(err)))
			continue
		}
		entry := p.entry
		entry.MarkedForDeletionAt = stamp
		kept = append(kept, entry)
		result.MergedSrcCount++
	}

	// Step 2: recompute keep duration as sum of all bookfile durations.
	total := 0
	allFiles, err := store.GetBookFiles(keepID)
	if err != nil {
		result.Errors = append(result.Errors,
			fmt.Sprintf("recount files on keep: %v", err))
	} else {
		for _, f := range allFiles {
			total += int(f.Duration)
		}
		if total > 0 {
			result.NewDuration = total
		}
	}

	// Step 3: update keep title if a non-empty suggested title was given --
	// unless the user locked the keep's title, in which case theirs stands.
	// Fail closed on an unreadable lock set: the duration recount (Step 2) is
	// derived from the files and is never lockable, so it is still written;
	// only the title change is withheld.
	applyTitle := false
	if suggestedTitle != "" && suggestedTitle != keep.Title {
		locks, lerr := database.LoadFieldLocks(store, keep.ID)
		switch {
		case lerr != nil:
			result.Errors = append(result.Errors,
				fmt.Sprintf("keep %s: title not changed, %v", keep.ID, lerr))
		case locks.Locked(database.FieldKeyTitle):
			result.TitleKeptLocked = true
			splitMergeLog.Info("split-book merge kept the user-locked title keep=%s suggested=%s", logger.SanitizeLogValue(keep.ID), logger.SanitizeLogValue(suggestedTitle))
		default:
			applyTitle = true
		}
	}
	// The keep row read above is stale by now: MoveBookFilesToBook recomputed
	// its aggregates (FileSize among them) inside the store, and any other
	// writer may have changed it too. A whole-row UpdateBook of that read
	// reverted all of it, so the write goes through ModifyBook on the fresh
	// row and sets only the two columns this merge owns.
	var titleBefore string
	titleChanged := false
	if total > 0 || applyTitle {
		_, err := store.ModifyBook(keepID, func(b *database.Book) error {
			titleChanged = false
			changed := false
			if total > 0 && (b.Duration == nil || *b.Duration != total) {
				d := total
				b.Duration = &d
				changed = true
			}
			if applyTitle && b.Title != suggestedTitle {
				titleBefore = b.Title
				b.Title = suggestedTitle
				titleChanged = true
				changed = true
			}
			if !changed {
				return database.ErrSkipBookWrite
			}
			return nil
		})
		if err != nil {
			titleChanged = false
			result.Errors = append(result.Errors, fmt.Sprintf("update keep book: %v", err))
		}
	}
	if titleChanged {
		journal.Override = &merge.CombineOverrideUndo{
			Applied:     merge.CombineOverride{Title: suggestedTitle},
			TitleBefore: titleBefore,
		}
	}

	// Step 4: finalize the journal. Duration needs no entry: UndoCombine
	// recomputes the keep's aggregates from the files it leaves there.
	journal.Absorbed = kept
	journal.Status = merge.CombineJournalApplied
	if err := merge.WriteCombineJournal(store, journal); err != nil {
		result.Errors = append(result.Errors, fmt.Sprintf("merge applied but its undo journal %s could not be finalized; this merge is NOT undoable: %v", journal.ID, err))
		splitMergeLog.Error("split-book merge applied but journal not finalized journal=%s keep=%s err=%s", journal.ID, logger.SanitizeLogValue(keepID), logger.SanitizeLogValue(fmt.Sprint(err)))
	} else {
		result.JournalID = journal.ID
	}

	return result, nil
}

// planSplitSource reads one src's row, files and external-ID mappings. It
// writes nothing.
func planSplitSource(store Store, srcID string, canReassign bool) (splitSrcPlan, error) {
	src, err := store.GetBookByID(srcID)
	if err != nil || src == nil {
		return splitSrcPlan{}, fmt.Errorf("load src %s: %v", srcID, err)
	}
	if src.IsSoftDeleted() {
		// Already merged away or trashed. Re-stamping it would restart its
		// retention and break the undo of whatever deleted it.
		return splitSrcPlan{}, fmt.Errorf("src %s is already soft-deleted; skipped", srcID)
	}
	files, err := store.GetBookFiles(srcID)
	if err != nil {
		return splitSrcPlan{}, fmt.Errorf("get files for %s: %v", srcID, err)
	}
	mappings, err := store.GetExternalIDsForBook(srcID)
	if err != nil {
		return splitSrcPlan{}, fmt.Errorf("read external ids of %s: %v; left untouched", srcID, err)
	}
	p := splitSrcPlan{
		entry: merge.CombineAbsorbed{
			BookID:           srcID,
			FilePath:         src.FilePath,
			IsPrimaryVersion: src.IsPrimaryVersion,
			VersionGroupID:   src.VersionGroupID,
		},
		hasMappings: len(mappings) > 0,
	}
	for _, f := range files {
		p.fileIDs = append(p.fileIDs, f.ID)
		p.entry.Files = append(p.entry.Files, merge.CombineFileMove{
			FileID: f.ID, FromBookID: srcID, FilePath: f.FilePath,
			DiscBefore: f.DiscNumber, TrackBefore: f.TrackNumber,
		})
	}
	live := 0
	for _, m := range mappings {
		// Tombstoned mappings ride along with ReassignExternalIDs but resolve
		// to no book, so undo neither checks nor moves them back.
		if !m.Tombstoned {
			live++
			p.entry.ExternalIDs = append(p.entry.ExternalIDs, merge.CombineExternalID{Source: m.Source, ExternalID: m.ExternalID})
		}
	}
	if live > 0 && !canReassign {
		return splitSrcPlan{}, fmt.Errorf("src %s holds %d external id(s) and this store cannot reassign them; left untouched", srcID, live)
	}
	return p, nil
}

// softDeleteSplitSource stamps srcID soft-deleted through ModifyBook (only the
// two deletion columns change) and returns the stamp as stored, which
// UndoCombine compares exactly.
func softDeleteSplitSource(store Store, srcID string) (*time.Time, error) {
	updated, err := store.ModifyBook(srcID, func(b *database.Book) error {
		t := true
		now := time.Now()
		b.MarkedForDeletion = &t
		b.MarkedForDeletionAt = &now
		return nil
	})
	if err != nil {
		return nil, err
	}
	if updated == nil || updated.MarkedForDeletionAt == nil {
		if updated, err = store.GetBookByID(srcID); err != nil || updated == nil || updated.MarkedForDeletionAt == nil {
			return nil, fmt.Errorf("read back soft-delete of %s: %v", srcID, err)
		}
	}
	return updated.MarkedForDeletionAt, nil
}
