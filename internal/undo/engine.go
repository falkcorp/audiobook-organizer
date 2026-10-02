// file: internal/undo/engine.go
// version: 1.25.0
// guid: 2e7a9f1c-3b4d-4e8f-a1c5-7d9e2f4b8c3a
// last-edited: 2026-10-02
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

	"github.com/falkcorp/audiobook-organizer/internal/database"
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
	for _, c := range plan.Order {
		if refusal := plan.Gate(c); refusal != nil {
			plan.Record(c, refusal)
			report.addReferentConflict(c, refusal)
			continue
		}
		var v rowVerdict
		if c.ChangeType == ChangeTypeBookSoftDelete && restoresBook[c.BookID] {
			v = rowVerdict{already: true}
		} else {
			v = preflightRow(store, c, plan.Stamps)
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

// rowVerdict is the preflight's prediction for one row: refused (refusal), a
// restorable conflict (conflict), or safe, already restored or not.
type rowVerdict struct {
	refusal  error
	conflict *UndoConflictItem
	already  bool
}

// verdictOf reads a compare-and-set answer: ErrAlreadyRestored is safe (the
// revert counts that row restored without writing), any other error refuses.
func verdictOf(err error) rowVerdict {
	if errors.Is(err, ErrAlreadyRestored) {
		return rowVerdict{already: true}
	}
	return rowVerdict{refusal: err}
}

func preflightRow(store ConflictChecker, c *database.OperationChange, stamps SoftDeleteStamps) rowVerdict {
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
		// The revert is compare-and-set (CheckBookFieldCurrent): a field
		// edited since the operation is refused, not overwritten.
		return verdictOf(CheckBookFieldCurrent(book, c))
	case "tag_write":
		// The revert reads the book before it touches the file.
		_, refusal := CheckRestoreBook(store, c.BookID)
		return rowVerdict{refusal: refusal}
	case ChangeTypeTitleRelinkCredits, ChangeTypeJunkAuthorCredits:
		// The preflight only proves the book is restorable: the credit
		// compare-and-set needs the junction, which the preflight store
		// does not read, so a row counted Safe here can still be refused
		// by the revert. What the revert refuses differs by type:
		// junk_author_credits requires the EXACT junction and primary the
		// repair wrote (any later credit change is refused, nothing
		// written); title_relink_credits checks only that its source
		// credit is absent and its target present (revertTitleRelinkCredits).
		_, refusal := CheckRestoreBook(store, c.BookID)
		return rowVerdict{refusal: refusal}
	case ChangeTypeSeriesRename:
		return verdictOf(CheckRestoreReferent(store, c))
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
	case ChangeTypeBookFileReassign, ChangeTypeBookFileTrack, ChangeTypeBookPathUpdate,
		ChangeTypeBookSoftDelete, ChangeTypeBookPrimaryDemote, ChangeTypeExternalIDReassign,
		ChangeTypeBookFileMove, ChangeTypeBookFileRepoint, ChangeTypeBookMergedInto, ChangeTypeUserStateFollow:
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
	case ChangeTypeBookFileRepoint:
		// The revert puts the row back only while it still names the path
		// (and Missing flag) the repoint wrote (SameFileLocation).
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
	if holds != nil && !holds(f) {
		return refuse(ReasonChangedSince, "book_file %s changed since the operation", id)
	}
	return nil
}
