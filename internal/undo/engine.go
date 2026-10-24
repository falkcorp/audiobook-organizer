// file: internal/undo/engine.go
// version: 1.52.0
// guid: 2e7a9f1c-3b4d-4e8f-a1c5-7d9e2f4b8c3a
// last-edited: 2026-10-23
//
// Undo preflight. PreflightUndoConflicts predicts what POST
// /operations/:id/revert (audiobooks.RevertService) will do with each change
// row of an operation, so the UI can show it before the user confirms. The
// revert lives in internal/audiobooks/revert.go; the classifier both share is
// restorable.go.
//
// RunUndoOperation, a second undo walk that used to live here, was deleted on
// 2026-09-12: nothing outside tests called it, and it restored series_id with
// no existence check and no nil-on-empty.

package undo

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
)

// UndoConflictReport summarizes potential conflicts detected before
// executing an undo operation. The caller shows this to the user
// so they can decide whether to proceed.
type UndoConflictReport struct {
	TotalChanges    int                `json:"total_changes"`
	AlreadyReverted int                `json:"already_reverted"`
	ContentChanged  []UndoConflictItem `json:"content_changed,omitempty"`
	// BookDeleted holds rows whose book is soft-deleted (trashed). The
	// revert still restores those, so they count as restorable conflicts.
	BookDeleted []UndoConflictItem `json:"book_deleted,omitempty"`
	ReOrganized []UndoConflictItem `json:"re_organized,omitempty"`
	// The refused buckets hold rows CheckRestoreBook or CheckRestoreReferent
	// refuses. The revert refuses every one of them each time it runs, so
	// they are NOT restorable and must never count toward offering an Undo
	// (the web confirmation names them separately). BookMissing: the book no
	// longer exists (hard-deleted). SeriesDeleted: the series no longer
	// exists. SeriesRenamedSince: a series_rename whose series was renamed
	// again after the operation. SeriesNameTaken: a series_rename whose old
	// name now belongs to another series. CheckFailed: the book or series
	// could not be read, the row's value is malformed, or the field no longer
	// holds what the operation wrote, ReasonChangedSince (Reason says which).
	BookMissing        []UndoConflictItem `json:"book_missing,omitempty"`
	SeriesDeleted      []UndoConflictItem `json:"series_deleted,omitempty"`
	SeriesRenamedSince []UndoConflictItem `json:"series_renamed_since,omitempty"`
	SeriesNameTaken    []UndoConflictItem `json:"series_name_taken,omitempty"`
	CheckFailed        []UndoConflictItem `json:"check_failed,omitempty"`
	Safe               int                `json:"safe"`
	// NotRestorable counts rows the revert endpoint cannot reverse (see
	// NotRestorableLabel), by label in NotRestorableTypes. They are in no
	// other bucket, and AlreadyReverted counts only restorable rows, so Safe
	// plus ContentChanged, BookDeleted and ReOrganized is what the revert can
	// restore.
	NotRestorable      int            `json:"not_restorable"`
	NotRestorableTypes map[string]int `json:"not_restorable_types,omitempty"`
	// AlreadyRestored counts the Safe rows that need no write: the target
	// already holds OldValue (ErrAlreadyRestored), as the revert's
	// already_restored will.
	AlreadyRestored int `json:"already_restored,omitempty"`
}

// UndoConflictItem describes one change that may conflict.
type UndoConflictItem struct {
	ChangeID   string `json:"change_id"`
	BookID     string `json:"book_id"`
	ChangeType string `json:"change_type"`
	Reason     string `json:"reason"`
}

// ConflictChecker is the four-method slice the preflight conflict scan needs.
// Both entry points previously took database.Store — all 398 methods.
// GetSeriesByID and GetSeriesByName serve CheckRestoreReferent, the series
// checks the revert also runs.
type ConflictChecker interface {
	GetOperationChanges(operationID string) ([]*database.OperationChange, error)
	GetBookByID(id string) (*database.Book, error)
	GetSeriesByID(id int) (*database.Series, error)
	GetSeriesByName(name string, authorID *int) (*database.Series, error)
}

// addReferentConflict files a CheckRestoreBook / CheckRestoreReferent refusal
// under the bucket matching its reason. Anything that is not book missing /
// series deleted / renamed since / name taken (a lookup failure, a malformed
// row, or an error without a reason) goes to CheckFailed: the revert refuses
// it just the same.
func (r *UndoConflictReport) addReferentConflict(c *database.OperationChange, err error) {
	reason := RefusalReason(err)
	if reason == "" {
		reason = ReasonSeriesLookupFailed
	}
	item := UndoConflictItem{ChangeID: c.ID, BookID: c.BookID, ChangeType: c.ChangeType, Reason: reason}
	switch reason {
	case ReasonBookMissing:
		r.BookMissing = append(r.BookMissing, item)
	case ReasonSeriesDeleted:
		r.SeriesDeleted = append(r.SeriesDeleted, item)
	case ReasonSeriesRenamedSince:
		r.SeriesRenamedSince = append(r.SeriesRenamedSince, item)
	case ReasonSeriesNameTaken:
		r.SeriesNameTaken = append(r.SeriesNameTaken, item)
	default:
		r.CheckFailed = append(r.CheckFailed, item)
	}
}

// PreflightUndoConflicts scans the operation's changes and reports
// which ones can be safely undone vs which have conflicts. It predicts what
// POST /operations/:id/revert (audiobooks.RevertService) will do, so it
// classifies rows with NotRestorableLabel, the classifier that endpoint uses,
// and walks them in the revert's order (PlanRevert): a retired book's rows
// that the revert will refuse because a row that moved or repointed its file
// is refused are reported refused (ReasonDependentNotReverted).
func PreflightUndoConflicts(store ConflictChecker, operationID string) (*UndoConflictReport, error) {
	changes, err := store.GetOperationChanges(operationID)
	if err != nil {
		return nil, fmt.Errorf("load changes: %w", err)
	}

	report := &UndoConflictReport{TotalChanges: len(changes)}

	var restorable []*database.OperationChange
	for _, c := range changes {
		if c.Voided {
			continue // the write it describes never happened
		}
		if IsLedgerOnly(c) {
			// Restores nothing and counts in no total: the revert skips it too.
			report.TotalChanges--
			continue
		}
		// Classify before the reverted check, as the revert does: a
		// record-only row is never counted as already reverted.
		if label := NotRestorableLabel(c); label != "" {
			report.NotRestorable++
			if report.NotRestorableTypes == nil {
				report.NotRestorableTypes = map[string]int{}
			}
			report.NotRestorableTypes[label]++
			continue
		}
		if c.RevertedAt != nil {
			report.AlreadyReverted++
			continue
		}
		restorable = append(restorable, c)
	}

	var files func(string) ([]database.BookFile, error)
	if r, ok := store.(bookFilesReader); ok {
		files = r.GetBookFiles
	}
	plan, err := PlanRevert(restorable, files)
	if err != nil {
		return nil, err
	}
	plan.NoteHandOffs(changes)
	// restoresBook: a soft-delete row earlier in the order is predicted to
	// restore the book, so the revert finds it live at any later soft-delete
	// row of the same book (a resumed retire's second stamp) and counts that
	// row already restored.
	//
	// The follow-on refusals come from the same plan: a soft-delete row
	// predicted refused is Recorded refused, and Gate then refuses every
	// later row of its book, as the revert does. What the preflight cannot
	// predict is a soft-delete revert that fails for a transient reason (a
	// store read error, the version-group read the revert makes under the
	// group lock): it has no way to know that will happen, so it counts
	// those rows safe and the revert refuses them when it does.
	restoresBook := map[string]bool{}
	recreatable := recreatableShells(store, restorable)
	for _, c := range plan.Order {
		if refusal := plan.Gate(c); refusal != nil {
			plan.Record(c, refusal)
			report.addReferentConflict(c, refusal)
			continue
		}
		var v rowVerdict
		switch {
		case c.ChangeType == ChangeTypeBookSoftDelete && restoresBook[c.BookID] && !recreatable[c.BookID]:
			v = rowVerdict{already: true}
		case recreatable[c.BookID] && shellRowType(c.ChangeType):
			v = recreatableShellVerdict(store, c)
		default:
			v = preflightRow(store, c, plan.Stamps, plan)
		}
		if c.ChangeType == ChangeTypeBookSoftDelete && v.refusal == nil && v.conflict == nil {
			restoresBook[c.BookID] = true
		}
		plan.Record(c, v.refusal)
		switch {
		case v.refusal != nil:
			report.addReferentConflict(c, v.refusal)
		case v.conflict == nil:
			report.Safe++
			if v.already {
				report.AlreadyRestored++
			}
		case v.conflict.Reason == "book deleted":
			report.BookDeleted = append(report.BookDeleted, *v.conflict)
		case v.conflict.Reason == "re-organized":
			report.ReOrganized = append(report.ReOrganized, *v.conflict)
		default:
			report.ContentChanged = append(report.ContentChanged, *v.conflict)
		}
	}

	return report, nil
}

// siblingJournalReader is what merge.SiblingUndoKeptFollows reads: the raw
// journal and the books it names.
type siblingJournalReader interface {
	GetRaw(key string) ([]byte, error)
	GetBookByID(id string) (*database.Book, error)
}

// rowVerdict is the preflight's prediction for one row: refused (refusal), a
// restorable conflict (conflict), or safe, already restored or not.
type rowVerdict struct {
	refusal  error
	conflict *UndoConflictItem
	already  bool
}

// ListJournalReader reads the journal of one collection or playlist: every row
// any retire into the list wrote (database GetListChanges). The membership
// graph and the roles are decided from it alone. Resolve it with
// database.AsCapability; it is not on Store.
type ListJournalReader interface {
	GetListChanges(kind, listID string) ([]*database.OperationChange, error)
}

// membershipVerdict predicts the revert of a collection or playlist row of a
// retire's carry (CheckMembershipFollow) from the list as it is now and the
// list's journal the revert reads. A row the revert restores by the survivor's
// standing, the retired book's, or a survivor retired out of the list since is
// therefore not filed under CheckFailed (an unrestorable row never counts toward
// offering Undo, so a wrong refusal here would hide an Undo the revert performs
// exactly). A store that cannot read the journal refuses the row
// (ReasonFieldUnreadable), as the revert does (revertListFollow): the revert
// decides every list row from the list's journal and has no answer without it,
// so predicting one from the row alone would offer an Undo the revert refuses.
func membershipVerdict(store ConflictChecker, cur []string, c *database.OperationChange, kind, listID string) rowVerdict {
	j, ok := database.AsCapability[ListJournalReader](store)
	if !ok {
		return rowVerdict{refusal: refuse(ReasonFieldUnreadable, "store cannot read the journal of %s %s", kind, listID)}
	}
	rows, err := j.GetListChanges(kind, listID)
	if err != nil {
		return rowVerdict{refusal: refuse(ReasonFieldUnreadable, "read the journal of %s %s: %v", kind, listID, err)}
	}
	return verdictOf(CheckMembershipFollow(cur, c, kind, listID, rows))
}

// verdictOf reads a compare-and-set answer: ErrAlreadyRestored is safe (the
// revert counts that row restored without writing), any other error refuses.
func verdictOf(err error) rowVerdict {
	if errors.Is(err, ErrAlreadyRestored) {
		return rowVerdict{already: true}
	}
	return rowVerdict{refusal: err}
}

func preflightRow(store ConflictChecker, c *database.OperationChange, stamps SoftDeleteStamps, plan *RevertPlan) rowVerdict {
	switch c.ChangeType {
	case "file_move", "organize_rename":
		conflict, refusal := checkFileMoveConflict(store, c)
		return rowVerdict{refusal: refusal, conflict: conflict}
	case "metadata_update":
		// The revert reads the book first (RevertService.loadBook) and
		// then runs CheckRestoreReferent. Either refusal fails the row
		// every time, so neither may be counted restorable here.
		book, refusal := CheckRestoreBook(store, c.BookID)
		if refusal == nil {
			refusal = CheckRestoreReferent(store, c)
		}
		switch {
		case refusal != nil:
			return rowVerdict{refusal: refusal}
		case book.IsSoftDeleted():
			return rowVerdict{conflict: &UndoConflictItem{
				ChangeID: c.ID, BookID: c.BookID, ChangeType: c.ChangeType,
				Reason: "book deleted",
			}}
		case !IsRevertableBookField(c.FieldName):
			// Fields the revert cannot restore at all keep their earlier
			// classification.
			return rowVerdict{}
		}
		// A value whose paired repair lock a person took over is theirs
		// (CheckPairedFieldLock), when the store can read both.
		if locks := plan.FieldLocksOf(c); len(locks) > 0 {
			if r, ok := store.(fieldStateReader); ok {
				states, serr := r.GetMetadataFieldStates(c.BookID)
				if serr != nil {
					return rowVerdict{refusal: refuse(ReasonFieldUnreadable, "read field states of %s: %v", c.BookID, serr)}
				}
				if err := CheckPairedFieldLock(locks, states, c); err != nil {
					return rowVerdict{refusal: err}
				}
			}
		}
		// A position goes back only into the series it was numbered in,
		// with its link when the link is still the operation's
		// (CheckSequencePaired); the revert runs the same rule.
		if link := plan.SeriesLinkOf(c); link != nil {
			withLink, err := CheckSequencePaired(book, c, link)
			if err != nil {
				return rowVerdict{refusal: err}
			}
			if withLink {
				if err := CheckRestoreReferent(store, link); err != nil {
					return rowVerdict{refusal: err}
				}
			}
		}
		// The revert is compare-and-set (CheckBookFieldCurrent): a field
		// edited since the operation is refused, not overwritten.
		return verdictOf(CheckBookFieldCurrent(book, c))
	case "tag_write":
		// The revert reads the book before it touches the file, and refuses a
		// file row that was retired into another book's version since.
		_, refusal := CheckRestoreBook(store, c.BookID)
		if refusal == nil {
			refusal = tagWriteRowRetired(store, c)
		}
		return rowVerdict{refusal: refusal}
	case ChangeTypeTitleRelinkCredits, ChangeTypeJunkAuthorCredits,
		ChangeTypeAuthorMergeCredits, ChangeTypeAuthorReclassifyCredits:
		// The preflight only proves the book is restorable: the credit
		// compare-and-set needs the junction, which the preflight store
		// does not read, so a row counted Safe here can still be refused
		// by the revert. What the revert refuses differs by type:
		// junk_author_credits requires the EXACT junction and primary the
		// repair wrote (any later credit change is refused, nothing
		// written); title_relink_credits checks only that its source
		// credit is absent and its target present (revertTitleRelinkCredits).
		// author_merge_credits / author_reclassify_credits are exact like
		// junk_author_credits, and also need their author restored first.
		_, refusal := CheckRestoreBook(store, c.BookID)
		if refusal != nil && c.ChangeType == ChangeTypeAuthorMergeCredits && isBookMissing(refusal) {
			// A ghost book's credit move (no book row, no primary changed) is
			// reverted junction-only (revertGhostJunction).
			if _, move, err := DecodeAuthorMergeCredits(c); err == nil && !move.PrimaryChanged {
				return rowVerdict{}
			}
		}
		return rowVerdict{refusal: refusal}
	case ChangeTypeSeriesRename:
		return verdictOf(CheckRestoreReferent(store, c))
	case ChangeTypeAuthorRename:
		// The revert renames the author back only while it is live under its
		// own id, still carries the name the rename wrote, and no other live
		// author holds the name it goes back to. The holder is read as the
		// revert reads it (AuthorNameHolder); a store that cannot read it
		// fails closed, as the revert does.
		id, from, to, err := DecodeAuthorRename(c)
		if err != nil {
			return rowVerdict{refusal: err}
		}
		r, ok := database.AsCapability[authorRestoreReader](store)
		if !ok {
			return rowVerdict{refusal: refuse(ReasonFieldUnreadable, "this store cannot read author %d", id)}
		}
		cur, err := r.GetAuthorByID(id)
		if err != nil {
			return rowVerdict{refusal: refuse(ReasonFieldUnreadable, "look up author %d: %v", id, err)}
		}
		holder, err := r.AuthorNameHolder(from)
		if err != nil {
			return rowVerdict{refusal: refuse(ReasonFieldUnreadable, "look up author name %q: %v", from, err)}
		}
		return verdictOf(CheckAuthorRename(cur, holder, id, from, to))
	case ChangeTypeAuthorIDRepoint:
		// The revert puts the old primary back only while the book still
		// carries the one the repair wrote. A newer row of this operation
		// that repointed the same book (phase b's merge moving the primary
		// off the author phase a repointed it to) is reverted first, so the
		// check is against the primary that row puts back.
		old, to, err := DecodeAuthorIDRepoint(c)
		if err != nil {
			return rowVerdict{refusal: err}
		}
		book, refusal := CheckRestoreBook(store, c.BookID)
		if refusal != nil {
			return rowVerdict{refusal: refusal}
		}
		current := book.AuthorID
		if predicted, ok := plan.PredictedPrimary(c.BookID); ok {
			current = predicted
		}
		return verdictOf(CheckAuthorIDRepoint(c.BookID, current, old.AuthorID, to))
	case ChangeTypeAuthorMergeDelete, ChangeTypeAuthorReclassifyDelete:
		// The revert recreates the author under its id (RestoreAuthor), refused
		// only when another author took the name entry it held
		// (database.AuthorNameIndexOnRestore). Its credit rows, reverted after it,
		// depend on it: RevertPlan.Gate refuses them when this row is refused.
		return preflightAuthorDelete(store, c)
	case ChangeTypeFieldLock:
		// The revert reads the book, then lifts the lock only while the
		// field still carries exactly it (CheckFieldLockCurrent). A store
		// that cannot read field states gets the book check alone; the
		// revert still runs the compare-and-set.
		if _, refusal := CheckRestoreBook(store, c.BookID); refusal != nil {
			return rowVerdict{refusal: refusal}
		}
		r, ok := store.(fieldStateReader)
		if !ok {
			return rowVerdict{}
		}
		states, err := r.GetMetadataFieldStates(c.BookID)
		if err != nil {
			return rowVerdict{refusal: refuse(ReasonFieldUnreadable, "read field states of %s: %v", c.BookID, err)}
		}
		return verdictOf(CheckFieldLockCurrent(states, c))
	case ChangeTypeBookTagAdd:
		// The revert reads the book, then removes the tag only while it
		// still carries the row's source (CheckBookTagAdd). A store that
		// cannot read tags gets the book check alone; the revert still
		// runs the compare-and-set.
		if _, refusal := CheckRestoreBook(store, c.BookID); refusal != nil {
			return rowVerdict{refusal: refusal}
		}
		r, ok := store.(bookTagReader)
		if !ok {
			return rowVerdict{}
		}
		tags, err := r.GetBookTagsDetailed(c.BookID)
		if err != nil {
			return rowVerdict{refusal: refuse(ReasonFieldUnreadable, "read tags of %s: %v", c.BookID, err)}
		}
		return verdictOf(CheckBookTagAdd(tags, c))
	case ChangeTypeUserContentFollow:
		// Every kind is checked as the revert will check it: a rating row
		// against the survivor's ratings (RestoreRating), a collection or
		// playlist row against the list as it is now and the journals of the
		// survivor and the retired book (membershipVerdict; a list deleted or
		// no longer static counts restored, as in the revert). A store that
		// cannot read lists or journals gets no prediction; the revert still
		// runs the compare-and-set under the list's lock. The
		// survivor-side markers restore nothing and always pass.
		kind, id, _ := UserContentFromField(c.FieldName)
		switch kind {
		case UserContentRating:
			survivor, refusal := CheckRestoreBook(store, id)
			if refusal != nil {
				return rowVerdict{refusal: refusal}
			}
			return verdictOf(CheckRatingFollowCurrent(survivor, c))
		case UserContentCollection:
			cols, ok := database.AsCapability[database.CollectionStore](store)
			if !ok {
				return rowVerdict{}
			}
			col, err := cols.GetCollection(id)
			if err != nil {
				return rowVerdict{refusal: refuse(ReasonFieldUnreadable, "read collection %s: %v", id, err)}
			}
			if col == nil || col.Type != database.CollectionTypeStatic {
				return rowVerdict{already: true}
			}
			return membershipVerdict(store, col.BookIDs, c, UserContentCollection, id)
		case UserContentPlaylist:
			pls, ok := database.AsCapability[database.UserPlaylistReader](store)
			if !ok {
				return rowVerdict{}
			}
			pl, err := pls.GetUserPlaylist(id)
			if err != nil {
				return rowVerdict{refusal: refuse(ReasonFieldUnreadable, "read playlist %s: %v", id, err)}
			}
			if pl == nil || pl.Type != database.UserPlaylistTypeStatic {
				return rowVerdict{already: true}
			}
			return membershipVerdict(store, pl.BookIDs, c, UserContentPlaylist, id)
		}
		return rowVerdict{}
	case ChangeTypeUserBookStateSet:
		// The revert puts the user's state and positions back only while
		// they still hold what the import wrote (CheckUserBookStateSet). It
		// does not need the book. A store that cannot read user state gets
		// no prediction; the revert still runs the compare-and-set.
		r, ok := store.(UserStateReader)
		if !ok {
			return rowVerdict{}
		}
		user, rc, rerr := ResolveUserStateChange(store, c)
		if rerr != nil {
			return rowVerdict{refusal: refuse(ReasonOldValueUnparsable, "%v", rerr)}
		}
		cur, err := ReadUserStateSnapshot(r, user, c.BookID)
		if err != nil {
			return rowVerdict{refusal: refuse(ReasonFieldUnreadable, "%v", err)}
		}
		_, cerr := CheckUserBookStateSet(cur, rc)
		return verdictOf(cerr)
	case ChangeTypeMetadataApply:
		// The revert undoes the apply's history batch field by field
		// (metafetch.Service.UndoApplyBatch). The preflight store does not
		// read change history, so it proves only that the book is there; a
		// field edited since is still left by the revert, never overwritten.
		_, refusal := CheckRestoreBook(store, c.BookID)
		return rowVerdict{refusal: refusal}
	case ChangeTypeMergeCombineJournal:
		// The revert undoes the merge's combine journal
		// (merge.Service.UndoCombine), which runs its own whole-journal
		// preconditions and refuses as a whole. The preflight store does not
		// read combine journals, so it proves only that the keep is there.
		_, refusal := CheckRestoreBook(store, c.BookID)
		return rowVerdict{refusal: refusal}
	case ChangeTypeMergeSiblingJournal:
		// The revert undoes the merge's sibling-move journal
		// (merge.Service.UndoSiblingMove), which checks every sibling's
		// current group itself and refuses as a whole when a newer merge
		// moved one again. It needs only the journal, not the survivor row
		// (a survivor purged since does not stop it). What it predicts here
		// is the listening state it would leave on a flag holder for want of
		// a live book to put it on (merge.SiblingUndoKeptFollows): a
		// restorable conflict, not Safe. A store that cannot read the
		// journal gets no prediction; the revert still decides.
		r, ok := database.AsCapability[siblingJournalReader](store)
		if !ok {
			return rowVerdict{}
		}
		kept, err := merge.SiblingUndoKeptFollows(r, c.NewValue)
		switch {
		case errors.Is(err, merge.ErrSiblingJournalNotFound):
			return rowVerdict{refusal: refuse(ReasonFieldUnreadable, "book %s: sibling-move journal %s not found", c.BookID, c.NewValue)}
		case err != nil:
			return rowVerdict{refusal: refuse(ReasonFieldUnreadable, "book %s: read sibling-move journal %s: %v", c.BookID, c.NewValue, err)}
		case len(kept) > 0:
			return rowVerdict{conflict: &UndoConflictItem{ChangeID: c.ID, BookID: c.BookID, ChangeType: c.ChangeType,
				Reason: "listening state kept: " + strings.Join(kept, "; ")}}
		}
		return rowVerdict{}
	case ChangeTypeMetadataCacheCopy:
		// The revert deletes or restores the cache row only while it still
		// carries the copy's stamp (CheckMetadataCacheCopy). It fails closed
		// on a store that cannot read cache rows, and so does this.
		if _, refusal := CheckRestoreBook(store, c.BookID); refusal != nil {
			return rowVerdict{refusal: refusal}
		}
		r, ok := store.(MetadataCacheReader)
		if !ok {
			return rowVerdict{refusal: refuse(ReasonFieldUnreadable, "this store cannot read book %s's candidate cache", c.BookID)}
		}
		cur, err := r.GetMetadataCache(c.BookID)
		if err != nil {
			return rowVerdict{refusal: refuse(ReasonFieldUnreadable, "read the candidate cache of %s: %v", c.BookID, err)}
		}
		return verdictOf(CheckMetadataCacheCopy(cur, c))
	case ChangeTypeRepairBookCreate:
		// The revert soft-deletes the created book; one that is absent or
		// already soft-deleted counts restored, one whose rows moved or that
		// joined a version group is refused.
		rbs, ok := store.(RepairBookCreateStore)
		if !ok {
			return rowVerdict{refusal: &ReferentError{Reason: ReasonBookLookupFailed,
				Detail: "this store cannot read the created book's rows"}}
		}
		return verdictOf(CheckRepairBookCreate(rbs, c))
	case ChangeTypeBookFileRetire, ChangeTypeBookVersionCreate, ChangeTypeRetiredVersionRehome:
		return verdictOf(checkRetireRow(store, c))
	case ChangeTypeBookFileReassign, ChangeTypeBookFileTrack, ChangeTypeBookPathUpdate,
		ChangeTypeBookSoftDelete, ChangeTypeBookPrimaryDemote, ChangeTypeExternalIDReassign,
		ChangeTypeBookFileMove, database.ChangeTypeBookFileRepoint, ChangeTypeBookMergedInto, ChangeTypeUserStateFollow,
		ChangeTypeITunesPathClear:
		// A journaled step whose write never happened, or one already put
		// back, is already restored.
		return verdictOf(checkFsRegroupRow(store, c, stamps))
	}
	return rowVerdict{}
}

// checkFileMoveConflict mirrors RevertService.revertFileMove. A file no longer
// at its new location is skipped by the revert without reading the book, so
// it is reported as content changed (the revert still counts it restored).
// Otherwise the revert reads the book before moving the file back and refuses
// the row when the book is missing or unreadable: that comes back as the
// refusal, and the row is never restorable.
func checkFileMoveConflict(store ConflictChecker, c *database.OperationChange) (*UndoConflictItem, error) {
	if c.NewValue == "" {
		return nil, nil
	}

	info, err := os.Stat(c.NewValue)
	if os.IsNotExist(err) {
		return &UndoConflictItem{
			ChangeID: c.ID, BookID: c.BookID, ChangeType: c.ChangeType,
			Reason: "content changed",
		}, nil
	}

	book, refusal := CheckRestoreBook(store, c.BookID)
	if refusal != nil {
		return nil, refusal
	}

	// The file at the new location was modified after the op.
	if err == nil && info.ModTime().After(c.CreatedAt) {
		return &UndoConflictItem{
			ChangeID: c.ID, BookID: c.BookID, ChangeType: c.ChangeType,
			Reason: "content changed",
		}, nil
	}
	if book.IsSoftDeleted() {
		return &UndoConflictItem{
			ChangeID: c.ID, BookID: c.BookID, ChangeType: c.ChangeType,
			Reason: "book deleted",
		}, nil
	}
	if book.LastOrganizedAt != nil && book.LastOrganizedAt.After(c.CreatedAt) {
		return &UndoConflictItem{
			ChangeID: c.ID, BookID: c.BookID, ChangeType: c.ChangeType,
			Reason: "re-organized",
		}, nil
	}
	return nil, nil
}

// Reads the fs-regroup-xml drift checks use when the store has them. They are
// not on ConflictChecker, so its implementers need not grow: a store without
// them gets the book checks only, and the revert still refuses drift under its
// own compare-and-set.
type bookFilesReader interface {
	GetBookFiles(bookID string) ([]database.BookFile, error)
}

type fieldStateReader interface {
	GetMetadataFieldStates(bookID string) ([]database.MetadataFieldState, error)
}

type bookFileByIDReader interface {
	GetBookFileByID(bookID, fileID string) (*database.BookFile, error)
}

type externalIDOwnerReader interface {
	GetBookByExternalID(source, externalID string) (string, error)
}

// checkFsRegroupRow predicts the revert of one maintenance.fs-regroup-xml row:
// the books it reads must exist, and the field it restores must still hold the
// value the operation wrote (ReasonChangedSince otherwise), as the revert's
// compare-and-set requires.
func checkFsRegroupRow(store ConflictChecker, c *database.OperationChange, stamps SoftDeleteStamps) error {
	book, refusal := CheckRestoreBook(store, c.BookID)
	if refusal != nil {
		return refusal
	}
	switch c.ChangeType {
	case ChangeTypeBookFileReassign:
		if _, refusal := CheckRestoreBook(store, c.OldValue); refusal != nil {
			return refusal
		}
		r, ok := store.(bookFileByIDReader)
		if !ok {
			return nil
		}
		id, ok := BookFileIDFromField(c.FieldName)
		if !ok {
			return refuse(ReasonOldValueUnparsable, "no book_file id in %q", c.FieldName)
		}
		// Not found is (nil, nil); an error is a failed read, never "absent".
		onTarget, err := r.GetBookFileByID(c.BookID, id)
		if err != nil {
			return refuse(ReasonFieldUnreadable, "read book_file %s on %s: %v", id, c.BookID, err)
		}
		onSource, err := r.GetBookFileByID(c.OldValue, id)
		if err != nil {
			return refuse(ReasonFieldUnreadable, "read book_file %s on %s: %v", id, c.OldValue, err)
		}
		// A row retired since is neither "still on the target" nor "back on
		// the source": a later retire owns it.
		for _, f := range []*database.BookFile{onTarget, onSource} {
			if f != nil && f.Retired {
				return RetiredRowRefusal(f)
			}
		}
		return CheckReassignCurrent(onTarget != nil, onSource != nil, c)
	case ChangeTypeBookFileTrack:
		return checkFsRegroupRowCurrent(store, c, func(f *database.BookFile) error {
			return CheckTrackCurrent(f.TrackNumber, c)
		})
	case ChangeTypeBookFileMove:
		// The revert moves the file back only while the row still names
		// NewValue, the file is there and OldValue is free.
		if err := checkFsRegroupRowOn(store, c, func(f *database.BookFile) bool {
			return f.FilePath == c.NewValue
		}); err != nil {
			return err
		}
		if _, err := os.Lstat(c.NewValue); err != nil {
			return refuse(ReasonChangedSince, "file is no longer at %s", c.NewValue)
		}
		if _, err := os.Lstat(c.OldValue); err == nil {
			return refuse(ReasonChangedSince, "%s is occupied again", c.OldValue)
		}
	case database.ChangeTypeBookFileRepoint:
		// The revert puts the row back only while it still names the path
		// (and Missing flag) the repoint wrote (database.SameFileLocation).
		return checkFsRegroupRowCurrent(store, c, func(f *database.BookFile) error {
			return CheckRepointCurrent(f, c)
		})
	case ChangeTypeBookMergedInto:
		return CheckMergedIntoCurrent(book, c)
	case ChangeTypeBookSoftDelete:
		return CheckSoftDeleteCurrent(book, c, stamps)
	case ChangeTypeUserStateFollow:
		// The revert writes progress on both books: both must exist.
		survivor, _ := SurvivorFromField(c.FieldName)
		if _, refusal := CheckRestoreBook(store, survivor); refusal != nil {
			return refusal
		}
	case ChangeTypeBookPathUpdate:
		return CheckPathUpdateCurrent(book, c)
	case ChangeTypeITunesPathClear:
		// The revert puts the path back only while the field is still
		// empty (CheckITunesPathClearCurrent): on the book, or on the row,
		// which must still be on BookID.
		if c.FieldName == ITunesPathBookField {
			//lint:ignore SA1019 preflight checks the legacy book.itunes_path column the stale-path repair cleared, matching the revert
			return CheckITunesPathClearCurrent(derefStr(book.ITunesPath), c)
		}
		return checkFsRegroupRowCurrent(store, c, func(f *database.BookFile) error {
			return CheckITunesPathClearCurrent(f.ITunesPath, c)
		})
	case ChangeTypeBookPrimaryDemote:
		return CheckPrimaryDemoteCurrent(book, c)
	case ChangeTypeExternalIDReassign:
		source, id, ok := ExternalIDFromField(c.FieldName)
		if !ok {
			return refuse(ReasonOldValueUnparsable, "no external id in %q", c.FieldName)
		}
		if r, ok := store.(externalIDOwnerReader); ok {
			owner, err := r.GetBookByExternalID(source, id)
			if err != nil {
				return refuse(ReasonBookLookupFailed, "look up external id %s/%s: %v", source, id, err)
			}
			return CheckExternalIDOwnerCurrent(owner, c)
		}
	}
	return nil
}

// checkFsRegroupRowCurrent runs check on the row a book_file change names,
// refusing when the row is no longer on BookID.
func checkFsRegroupRowCurrent(store ConflictChecker, c *database.OperationChange, check func(*database.BookFile) error) error {
	r, ok := store.(bookFileByIDReader)
	if !ok {
		return nil
	}
	id, ok := BookFileIDFromField(c.FieldName)
	if !ok {
		return refuse(ReasonOldValueUnparsable, "no book_file id in %q", c.FieldName)
	}
	f, err := r.GetBookFileByID(c.BookID, id)
	if err != nil {
		return refuse(ReasonFieldUnreadable, "read book_file %s on %s: %v", id, c.BookID, err)
	}
	if f == nil {
		return refuse(ReasonChangedSince, "book_file %s is no longer on book %s", id, c.BookID)
	}
	if f.Retired {
		return RetiredRowRefusal(f)
	}
	return check(f)
}

// checkFsRegroupRowOn checks that the row a book_file change names is still on
// BookID and, when holds is given, still holds the value the operation set.
func checkFsRegroupRowOn(store ConflictChecker, c *database.OperationChange, holds func(*database.BookFile) bool) error {
	r, ok := store.(bookFileByIDReader)
	if !ok {
		return nil
	}
	id, ok := BookFileIDFromField(c.FieldName)
	if !ok {
		return refuse(ReasonOldValueUnparsable, "no book_file id in %q", c.FieldName)
	}
	f, err := r.GetBookFileByID(c.BookID, id)
	if err != nil || f == nil {
		return refuse(ReasonChangedSince, "book_file %s is no longer on book %s", id, c.BookID)
	}
	if f.Retired {
		return RetiredRowRefusal(f)
	}
	if holds != nil && !holds(f) {
		return refuse(ReasonChangedSince, "book_file %s changed since the operation", id)
	}
	return nil
}

// tagWriteRowRetired is the refusal for a tag_write row whose file row has
// been retired since (RetiredRowRefusal), nil when it is not, cannot be read
// here (the revert reads it itself and refuses on its own) or the field names
// no file.
func tagWriteRowRetired(store ConflictChecker, c *database.OperationChange) error {
	r, ok := store.(bookFileByIDReader)
	if !ok {
		return nil
	}
	_, fileID, ok := TagWriteFromField(c.FieldName)
	if !ok {
		return nil
	}
	if f, err := r.GetBookFileByID(c.BookID, fileID); err == nil && f != nil && f.Retired {
		return RetiredRowRefusal(f)
	}
	return nil
}

// isBookMissing reports a CheckRestoreBook refusal for a book with no row.
func isBookMissing(err error) bool {
	var re *ReferentError
	return errors.As(err, &re) && re.Reason == ReasonBookMissing
}

// authorRestoreReader is what the preflight of an author delete or author
// rename row reads. The
// name holder is read as the restore reads it (database.AuthorNameHolderReader),
// not through GetAuthorByName, which follows tombstones.
type authorRestoreReader interface {
	GetAuthorByID(id int) (*database.Author, error)
	database.AuthorNameHolderReader
}

// preflightAuthorDelete predicts revertAuthorMergeDelete /
// revertAuthorReclassifyDelete. It fails closed on a store that cannot read
// authors. The name holder is read through GetAuthorByName, which follows a
// redirect: the restore reads the raw entry, and they differ only for an entry
// left naming a deleted id, which the restore treats as free.
func preflightAuthorDelete(store ConflictChecker, c *database.OperationChange) rowVerdict {
	var snap AuthorSnapshot
	into := 0
	if c.ChangeType == ChangeTypeAuthorMergeDelete {
		s, v, err := DecodeAuthorMergeDelete(c)
		if err != nil {
			return rowVerdict{refusal: err}
		}
		snap, into = s, v.IntoAuthorID
	} else {
		s, _, err := DecodeAuthorReclassifyDelete(c)
		if err != nil {
			return rowVerdict{refusal: err}
		}
		snap = s
	}
	r, ok := database.AsCapability[authorRestoreReader](store)
	if !ok {
		return rowVerdict{refusal: refuse(ReasonFieldUnreadable, "this store cannot read author %d", snap.ID)}
	}
	cur, err := r.GetAuthorByID(snap.ID)
	if err != nil {
		return rowVerdict{refusal: refuse(ReasonFieldUnreadable, "look up author %d: %v", snap.ID, err)}
	}
	holder, herr := r.AuthorNameHolder(snap.Name)
	if herr != nil {
		return rowVerdict{refusal: refuse(ReasonFieldUnreadable, "look up author name %q: %v", snap.Name, herr)}
	}
	already, refusal := AuthorDeleteRestoreRefusal(snap, cur != nil && cur.ID == snap.ID, holder, into)
	return rowVerdict{refusal: refusal, already: already}
}

// bookVersionReader is the version read the preflight of a retire row makes.
// Resolved with database.AsCapability; a store without it refuses the row.
type bookVersionReader interface {
	GetBookVersion(id string) (*database.BookVersion, error)
}

// recreatableShells returns the shells a purge deleted that an un-reverted
// retire row of rows names and a purge snapshot can bring back (ShellCheck).
// The revert recreates them before it reverts any row (the revert's own
// pre-pass), so every row of such a shell is judged against a shell that will
// be there, never refused book_missing.
func recreatableShells(store ConflictChecker, rows []*database.OperationChange) map[string]bool {
	out := map[string]bool{}
	for _, c := range rows {
		if c.ChangeType != ChangeTypeBookFileRetire {
			continue
		}
		r, err := DecodeBookFileRetire(c)
		if err != nil || out[r.Shell] {
			continue
		}
		if state, err := ShellCheck(store, r.Shell); err == nil && state == ShellRecreatable {
			out[r.Shell] = true
		}
	}
	return out
}

// shellRowType reports whether a row of this type acts on the shell book row
// itself (its soft-delete, pointers and external ids), the rows a recreated
// shell has to be judged for.
func shellRowType(changeType string) bool {
	switch changeType {
	case ChangeTypeBookSoftDelete, ChangeTypeBookMergedInto, ChangeTypeBookPathUpdate,
		ChangeTypeBookPrimaryDemote, ChangeTypeUserStateFollow, ChangeTypeExternalIDReassign:
		return true
	}
	return false
}

// recreatableShellVerdict predicts a row on a shell the revert will recreate
// from its purge snapshot. The snapshot is the shell as the purge found it,
// after every write of the retire, and a purge deletes the book, so nothing
// could have changed it since: the book-row compare-and-sets (soft-delete,
// merged-into, path, primary flag, followed progress) hold by construction and
// the row is safe. An external id is not on the book row, so its owner is
// still compared.
func recreatableShellVerdict(store ConflictChecker, c *database.OperationChange) rowVerdict {
	if c.ChangeType != ChangeTypeExternalIDReassign {
		return rowVerdict{}
	}
	source, id, ok := ExternalIDFromField(c.FieldName)
	if !ok {
		return rowVerdict{refusal: refuse(ReasonOldValueUnparsable, "no external id in %q", c.FieldName)}
	}
	r, ok := store.(externalIDOwnerReader)
	if !ok {
		return rowVerdict{}
	}
	owner, err := r.GetBookByExternalID(source, id)
	if err != nil {
		return rowVerdict{refusal: refuse(ReasonBookLookupFailed, "look up external id %s/%s: %v", source, id, err)}
	}
	return verdictOf(CheckExternalIDOwnerCurrent(owner, c))
}

// checkRetireRow predicts the revert of one retire row (RevertService's
// revertBookFileRetire, revertBookVersionCreate, revertRetiredVersionRehome):
// the same compare-and-set (CheckRetireCurrent, CheckVersionCreateCurrent,
// CheckRehomeCurrent), and for a file row whose shell was purged the same
// shell check (ShellCheck), so "purged but recreatable" is restorable.
func checkRetireRow(store ConflictChecker, c *database.OperationChange) error {
	switch c.ChangeType {
	case ChangeTypeBookFileRetire:
		r, err := DecodeBookFileRetire(c)
		if err != nil {
			return refuse(ReasonOldValueUnparsable, "%v", err)
		}
		rd, ok := store.(bookFileByIDReader)
		if !ok {
			return refuse(ReasonFieldUnreadable, "this store cannot read book_file %s", r.FileID)
		}
		onHolder, err := rd.GetBookFileByID(r.Holder, r.FileID)
		if err != nil {
			return refuse(ReasonFieldUnreadable, "read book_file %s on %s: %v", r.FileID, r.Holder, err)
		}
		onShell, err := rd.GetBookFileByID(r.Shell, r.FileID)
		if err != nil {
			return refuse(ReasonFieldUnreadable, "read book_file %s on %s: %v", r.FileID, r.Shell, err)
		}
		if err := CheckRetireCurrent(onHolder, onShell, r); err != nil {
			return err
		}
		_, err = ShellCheck(store, r.Shell)
		return err
	case ChangeTypeBookVersionCreate, ChangeTypeRetiredVersionRehome:
		id, ok := BookVersionIDFromField(c.FieldName)
		if !ok {
			return refuse(ReasonOldValueUnparsable, "no book_version id in %q", c.FieldName)
		}
		rd, ok := database.AsCapability[bookVersionReader](store)
		if !ok {
			return refuse(ReasonFieldUnreadable, "this store cannot read book version %s", id)
		}
		cur, err := rd.GetBookVersion(id)
		if err != nil {
			return refuse(ReasonFieldUnreadable, "read book version %s: %v", id, err)
		}
		if c.ChangeType == ChangeTypeBookVersionCreate {
			return CheckVersionCreateCurrent(cur, c)
		}
		return CheckRehomeCurrent(cur, c)
	}
	return nil
}
