// file: internal/dedup/split_book_merge.go
// version: 1.15.0
// guid: 3b5d7f9a-2e4c-6b8d-0f1a-3c5e7d9f1b3e
// last-edited: 2026-09-19

// Split-book cluster merge — portable across SQLite and Pebble.
//
// The merge-chapter-groups maintenance job also merges through this function
// (2026-09-19); the journal-less `MergeChapterBooks` store method it used was
// removed. The existing
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
	"errors"
	"fmt"
	"sort"
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
	files       []database.BookFile
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
	return MergeSplitBookClusterWithOptions(store, keepID, srcIDs, suggestedTitle, SplitMergeOptions{})
}

// SplitMergeOptions tunes MergeSplitBookClusterWithOptions.
type SplitMergeOptions struct {
	// FileOrder, when set, is the keep's play order after the merge: every
	// listed file is stamped DiscNumber=0, TrackNumber=1..N in this order. The
	// previous numbers are journaled (keep's own files and absorbed files), so
	// UndoCombine restores them.
	FileOrder []string
	// FillEmpty copies ASIN, narrator, series and author from the sources
	// onto the keep where the keep's field is EMPTY (see PlanFillEmpty).
	FillEmpty bool
	// OrderByBooks computes the play order under the merge lock: the keep's
	// files, then each src's in srcIDs order, each book's files by (disc,
	// track, path). It is stamped like FileOrder and also gives each src its
	// offset in the merged timeline for progress mapping. FileOrder, when set,
	// takes precedence.
	OrderByBooks bool
	// Precheck, when set, runs under the merge lock before anything is read
	// for the merge; an error refuses the merge with nothing written. The
	// chapter job re-verifies its reviewed group here.
	Precheck func() error
}

// MergeSplitBookClusterWithOptions is MergeSplitBookCluster with options.
//
// CRASH SAFETY. The journal is written Pending before the first write and
// re-written after every step that changes something (each src's file move,
// external-id reassignment, progress follow and soft-delete; the title and
// fill-empty writes), so at any instant it names everything already done.
// UndoCombine accepts a Pending journal and replays whatever it records. A src
// whose step fails after its files moved is KEPT in the journal, marked
// LeftLive, so undo still moves its files back; only a src for which nothing
// was written is dropped from it.
func MergeSplitBookClusterWithOptions(store Store, keepID string, srcIDs []string, suggestedTitle string, opts SplitMergeOptions) (*SplitBookMergeResult, error) {
	if keepID == "" {
		return nil, fmt.Errorf("MergeSplitBookCluster: empty keepID")
	}
	if len(srcIDs) == 0 {
		return nil, fmt.Errorf("MergeSplitBookCluster: no srcIDs")
	}

	// This is a fourth unguarded read-modify-write over shared book rows
	// (GetBookByID -> MoveBookFilesToBook -> UpdateBook -> SoftDeleteBook), the
	// same failure class #1930 fixed for merge.Service.MergeBooks and that
	// dedup.MergeBooks (book_dedup.go) already guards with this same lock. It
	// is also the lock merge.Service.UndoCombine takes, so an undo of this
	// journal cannot interleave with the merge that writes it.
	merge.LockMergeRMW()
	defer merge.UnlockMergeRMW()

	if opts.Precheck != nil {
		if err := opts.Precheck(); err != nil {
			return nil, fmt.Errorf("split-book merge refused: %w", err)
		}
	}

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
	srcBook := map[string]*database.Book{}
	if opts.FillEmpty {
		for _, p := range plans {
			b, berr := store.GetBookByID(p.entry.BookID)
			if berr != nil || b == nil {
				return nil, fmt.Errorf("split-book merge refused: re-read src %s: %v", p.entry.BookID, berr)
			}
			srcBook[p.entry.BookID] = b
		}
		// A conflict (sources disagreeing on a value the keep lacks) refuses
		// the merge before any write. The fill itself is re-planned over the
		// srcs actually absorbed (below).
		all := make([]*database.Book, 0, len(plans))
		for _, p := range plans {
			all = append(all, srcBook[p.entry.BookID])
		}
		fp, perr := PlanFillEmpty(store, keep, all)
		if perr != nil {
			return nil, fmt.Errorf("split-book merge refused: %w", perr)
		}
		if len(fp.Conflicts) > 0 {
			return nil, fmt.Errorf("split-book merge refused: metadata conflict: %v", fp.Conflicts)
		}
	}

	// A src whose listening progress cannot be carried (a store without a
	// sync follower) is left out whole: retiring it would lose that progress
	// at the purge.
	canFollow := database.AsSyncIdentityStore(store) != nil
	if !canFollow {
		carriable := plans[:0]
		for _, p := range plans {
			has, herr := merge.BookHasUserProgress(store, p.entry.BookID)
			if herr != nil || has {
				result.Errors = append(result.Errors, fmt.Sprintf("src %s has listening progress this store cannot carry (%v); left untouched", p.entry.BookID, herr))
				continue
			}
			carriable = append(carriable, p)
		}
		plans = carriable
	}

	ownFiles, oerr := store.GetBookFiles(keepID)
	if oerr != nil {
		return nil, fmt.Errorf("split-book merge refused: read keep files: %w", oerr)
	}
	order, slices := splitPlayOrder(keepID, ownFiles, plans, opts)

	journal := merge.NewCombineJournal(keepID)
	journal.Origin = SplitBookMergeJournalOrigin
	journal.StepJournaled = true
	for _, p := range plans {
		journal.Absorbed = append(journal.Absorbed, p.entry)
	}
	// The keep's own files, with their numbers, so UndoCombine can restore a
	// play-order stamp on them too.
	for _, f := range ownFiles {
		journal.SurvivorOwnFiles = append(journal.SurvivorOwnFiles, merge.CombineFileMove{
			FileID: f.ID, FromBookID: keepID, FilePath: f.FilePath,
			DiscBefore: f.DiscNumber, TrackBefore: f.TrackNumber,
		})
	}
	if err := merge.WriteCombineJournal(store, journal); err != nil {
		return nil, fmt.Errorf("split-book merge refused: undo journal could not be written: %w", err)
	}
	// persist re-writes the journal after a step. A failure is recorded: the
	// merge goes on (its writes are real), but the result says the journal may
	// be behind.
	persist := func() {
		if err := merge.WriteCombineJournal(store, journal); err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("undo journal %s not updated: %v", journal.ID, err))
		}
	}

	// Step 1: per src -- move files, reassign external IDs, follow progress,
	// soft-delete -- persisting the journal after each.
	kept := make([]string, 0, len(plans))
	for i := range journal.Absorbed {
		entry := &journal.Absorbed[i]
		p := plans[i]
		srcID := entry.BookID
		leaveLive := func(msg string) {
			entry.LeftLive = true
			result.Errors = append(result.Errors, msg)
			journal.Warnings = append(journal.Warnings, msg)
			persist()
		}
		if len(p.fileIDs) > 0 {
			mvErr := store.MoveBookFilesToBook(p.fileIDs, srcID, keepID)
			if errors.Is(mvErr, database.ErrBookFileDurabilityUnknown) {
				// Moved (visible); only the fsync failed. Recording "nothing
				// moved" would make the journal's undo wrong.
				msg := fmt.Sprintf("move files from %s was written but its fsync failed (%v): durability unknown; treated as applied", srcID, mvErr)
				result.Errors = append(result.Errors, msg)
				journal.Warnings = append(journal.Warnings, msg)
				mvErr = nil
			}
			if err := mvErr; err != nil {
				// Atomic: nothing moved. The entry stays (with nothing to undo
				// beyond a no-op) marked LeftLive.
				leaveLive(fmt.Sprintf("move files from %s: %v; %s left live", srcID, err, srcID))
				continue
			}
			result.FilesMoved += len(p.fileIDs)
			merge.FollowFileMove(store, srcID, keepID, p.fileIDs)
		}
		if p.hasMappings && eids != nil {
			// Fail closed, as merge.Service.MergeBooks does: a src soft-deleted
			// while still holding its PIDs would have them enqueued for iTunes
			// removal by the purge, for audio the keep now owns.
			if err := eids.ReassignExternalIDs(srcID, keepID); err != nil {
				entry.ExternalIDs = nil // nothing moved, nothing to move back
				leaveLive(fmt.Sprintf("reassign external ids of %s: %v; %s left live, undo moves its files back", srcID, err, srcID))
				continue
			}
		}
		// Carry every user's listening progress and the src's sync identity
		// onto the keep. A split part / chapter is a SLICE of the keep, so its
		// position is mapped into the keep's timeline and its Finished state
		// is never carried (merge.SliceMapping). The before-snapshot is
		// journaled before the follow drains anything.
		if canFollow {
			sm := slices[srcID]
			progress, redirected, ferr := merge.FollowAbsorbedJournaled(store, keepID, srcID, &sm, func(before []merge.CombineUserProgress) error {
				entry.Progress = before
				entry.SyncRedirected = true
				return merge.WriteCombineJournal(store, journal)
			})
			entry.Progress = progress
			entry.SyncRedirected = redirected
			if ferr != nil {
				leaveLive(fmt.Sprintf("follow progress of %s: %v; %s left live, undo moves its files back", srcID, ferr, srcID))
				continue
			}
		}
		stamp, err := softDeleteSplitSource(store, srcID)
		if err != nil {
			splitMergeLog.Warn("split-book merge soft-delete failed src=%s err=%s", logger.SanitizeLogValue(srcID), logger.SanitizeLogValue(fmt.Sprint(err)))
			leaveLive(fmt.Sprintf("soft-delete %s: %v; %s left live, undo moves its files back", srcID, err, srcID))
			continue
		}
		entry.MarkedForDeletionAt = stamp
		kept = append(kept, srcID)
		result.MergedSrcCount++
		persist()
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

	// Step 2b: stamp the play order. Only files the keep now owns are
	// touched; their previous numbers are already in the journal
	// (SurvivorOwnFiles and Absorbed[].Files), so UndoCombine restores them.
	for i, fid := range order {
		f, ferr := store.GetBookFileByID(keepID, fid)
		if ferr != nil || f == nil {
			continue // not on the keep (its src was left live)
		}
		if f.DiscNumber == 0 && f.TrackNumber == i+1 {
			continue
		}
		f.DiscNumber, f.TrackNumber = 0, i+1
		if uerr := store.UpdateBookFile(f.ID, f); uerr != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("set track order on %s: %v", f.ID, uerr))
		}
	}

	// Step 3: update keep title if a non-empty suggested title was given --
	// unless the user locked the keep's title, in which case theirs stands.
	// Fail closed on an unreadable lock set.
	applyTitle := false
	if suggestedTitle != "" && suggestedTitle != keep.Title && len(kept) > 0 {
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
			// Journal the title change BEFORE writing it.
			journal.Override = &merge.CombineOverrideUndo{
				Applied:     merge.CombineOverride{Title: suggestedTitle},
				TitleBefore: keep.Title,
			}
			persist()
		}
	}
	// ModifyBook on the fresh row, setting only the two columns this merge
	// owns (a whole-row write of the stale keep read would revert others).
	if total > 0 || applyTitle {
		titleWritten := false
		_, err := store.ModifyBook(keepID, func(b *database.Book) error {
			titleWritten = false
			changed := false
			if total > 0 && (b.Duration == nil || *b.Duration != total) {
				d := total
				b.Duration = &d
				changed = true
			}
			if applyTitle && b.Title != suggestedTitle {
				journal.Override.TitleBefore = b.Title
				b.Title = suggestedTitle
				titleWritten = true
				changed = true
			}
			if !changed {
				return database.ErrSkipBookWrite
			}
			return nil
		})
		if err != nil {
			titleWritten = false
			result.Errors = append(result.Errors, fmt.Sprintf("update keep book: %v", err))
		}
		if applyTitle && !titleWritten {
			journal.Override = nil
		}
		if applyTitle {
			persist()
		}
	}

	// Step 3b: fill-empty metadata onto the keep, planned over the srcs
	// actually absorbed, journaled BEFORE the write and again after.
	if opts.FillEmpty && len(kept) > 0 {
		absorbed := make([]*database.Book, 0, len(kept))
		for _, id := range kept {
			absorbed = append(absorbed, srcBook[id])
		}
		fp, perr := PlanFillEmpty(store, keep, absorbed)
		switch {
		case perr != nil:
			result.Errors = append(result.Errors, fmt.Sprintf("fill-empty plan for keep %s: %v", keepID, perr))
		case len(fp.Conflicts) > 0:
			result.Errors = append(result.Errors, fmt.Sprintf("fill-empty skipped, conflict: %v", fp.Conflicts))
		case !fp.Values.Empty():
			planned := fp.Values
			journal.FilledEmpty = &planned
			persist()
			filled, ferr := applyFillEmpty(store, keepID, fp)
			if ferr != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("fill-empty metadata on keep %s: %v", keepID, ferr))
			}
			if filled == nil && ferr == nil {
				journal.FilledEmpty = nil
			} else if filled != nil {
				journal.FilledEmpty = filled
			}
			persist()
		}
	}

	// Step 4: finalize the journal. Srcs for which nothing at all was written
	// (a failed atomic move) are dropped; everything else stays so undo can
	// reverse it. Duration needs no entry: UndoCombine recomputes aggregates.
	final := journal.Absorbed[:0]
	for i, a := range journal.Absorbed {
		if a.LeftLive && len(plans[i].fileIDs) > 0 && !movedAny(store, keepID, plans[i].fileIDs) && len(a.ExternalIDs) == 0 && len(a.Progress) == 0 {
			continue
		}
		final = append(final, a)
	}
	journal.Absorbed = final
	journal.Status = merge.CombineJournalApplied
	if err := merge.WriteCombineJournal(store, journal); err != nil {
		result.Errors = append(result.Errors, fmt.Sprintf("merge applied but its undo journal %s could not be finalized; it is still %s and undo replays what it recorded: %v", journal.ID, merge.CombineJournalPending, err))
		splitMergeLog.Error("split-book merge applied but journal not finalized journal=%s keep=%s err=%s", journal.ID, logger.SanitizeLogValue(keepID), logger.SanitizeLogValue(fmt.Sprint(err)))
	}
	result.JournalID = journal.ID
	return result, nil
}

// movedAny reports whether any of fileIDs is now on keepID.
func movedAny(store Store, keepID string, fileIDs []string) bool {
	for _, id := range fileIDs {
		if f, err := store.GetBookFileByID(keepID, id); err == nil && f != nil {
			return true
		}
	}
	return false
}

// splitPlayOrder returns the play order to stamp (nil = none) and each src's
// slice mapping into the merged timeline. The order used for mapping is always
// computed (keep files, then srcs in order, each by disc/track/path) unless the
// caller gave an explicit FileOrder, which then defines both.
func splitPlayOrder(keepID string, ownFiles []database.BookFile, plans []splitSrcPlan, opts SplitMergeOptions) ([]string, map[string]merge.SliceMapping) {
	dur := map[string]float64{}
	owner := map[string]string{}
	sorted := func(files []database.BookFile) []database.BookFile {
		out := append([]database.BookFile(nil), files...)
		sort.SliceStable(out, func(i, j int) bool {
			if out[i].DiscNumber != out[j].DiscNumber {
				return out[i].DiscNumber < out[j].DiscNumber
			}
			if out[i].TrackNumber != out[j].TrackNumber {
				return out[i].TrackNumber < out[j].TrackNumber
			}
			return out[i].FilePath < out[j].FilePath
		})
		return out
	}
	var byBooks []string
	for _, f := range sorted(ownFiles) {
		byBooks = append(byBooks, f.ID)
		dur[f.ID], owner[f.ID] = float64(f.Duration), keepID
	}
	for _, p := range plans {
		for _, f := range sorted(p.files) {
			byBooks = append(byBooks, f.ID)
			dur[f.ID], owner[f.ID] = float64(f.Duration), p.entry.BookID
		}
	}
	order := byBooks
	stamp := opts.OrderByBooks
	if len(opts.FileOrder) > 0 {
		order, stamp = opts.FileOrder, true
	}
	slices := map[string]merge.SliceMapping{}
	offset, known := 0.0, true
	for _, fid := range order {
		src := owner[fid]
		if _, done := slices[src]; !done && src != keepID && src != "" {
			slices[src] = merge.SliceMapping{OffsetSeconds: offset, Mappable: known}
		}
		d, ok := dur[fid]
		if !ok || d <= 0 {
			known = false
		}
		offset += d
	}
	for _, p := range plans {
		if _, ok := slices[p.entry.BookID]; !ok {
			slices[p.entry.BookID] = merge.SliceMapping{} // not in the order: unmappable
		}
	}
	if !stamp {
		return nil, slices
	}
	return order, slices
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
	p.files = files
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
		// Clear FilePath, as CombineBooks' softDeleteAbsorbed does: the path
		// now belongs to a file row the keeper owns, and a purge with
		// delete-files on os.Remove()s a purged book's FilePath, which would
		// delete the keeper's audio. The combine journal keeps the original,
		// and UndoCombine restores it.
		b.FilePath = ""
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
