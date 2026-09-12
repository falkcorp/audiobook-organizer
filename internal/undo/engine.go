// file: internal/undo/engine.go
// version: 1.14.0
// guid: 2e7a9f1c-3b4d-4e8f-a1c5-7d9e2f4b8c3a
// last-edited: 2026-09-12
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
	"strconv"

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
// classifies rows with NotRestorableLabel, the classifier that endpoint uses.
func PreflightUndoConflicts(store ConflictChecker, operationID string) (*UndoConflictReport, error) {
	changes, err := store.GetOperationChanges(operationID)
	if err != nil {
		return nil, fmt.Errorf("load changes: %w", err)
	}

	report := &UndoConflictReport{TotalChanges: len(changes)}

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

		switch c.ChangeType {
		case "file_move", "organize_rename":
			conflict, refusal := checkFileMoveConflict(store, c)
			switch {
			case refusal != nil:
				report.addReferentConflict(c, refusal)
			case conflict == nil:
				report.Safe++
			case conflict.Reason == "book deleted":
				report.BookDeleted = append(report.BookDeleted, *conflict)
			case conflict.Reason == "re-organized":
				report.ReOrganized = append(report.ReOrganized, *conflict)
			default:
				report.ContentChanged = append(report.ContentChanged, *conflict)
			}
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
				report.addReferentConflict(c, refusal)
			case book.IsSoftDeleted():
				report.BookDeleted = append(report.BookDeleted, UndoConflictItem{
					ChangeID: c.ID, BookID: c.BookID, ChangeType: c.ChangeType,
					Reason: "book deleted",
				})
			default:
				// The revert is compare-and-set: a field edited since the
				// operation is refused, not overwritten. One that already
				// holds OldValue is already restored and needs no write.
				// Fields the revert cannot restore at all keep their earlier
				// classification.
				if !IsRevertableBookField(c.FieldName) {
					report.Safe++
				} else if current, err := CurrentBookField(book, c.FieldName); err != nil {
					report.addReferentConflict(c, refuse(ReasonOldValueUnparsable, "%v", err))
				} else if current != c.NewValue && current != c.OldValue {
					report.addReferentConflict(c, refuse(ReasonChangedSince,
						"book %s %s changed since the operation", c.BookID, c.FieldName))
				} else {
					report.Safe++
				}
			}
		case "tag_write":
			// The revert reads the book before it touches the file.
			if _, refusal := CheckRestoreBook(store, c.BookID); refusal != nil {
				report.addReferentConflict(c, refusal)
			} else {
				report.Safe++
			}
		case ChangeTypeSeriesRename:
			// ErrAlreadyRestored is not a conflict: the revert counts that row
			// Restored without writing.
			if err := CheckRestoreReferent(store, c); err != nil && !errors.Is(err, ErrAlreadyRestored) {
				report.addReferentConflict(c, err)
			} else {
				report.Safe++
			}
		case ChangeTypeBookFileReassign, ChangeTypeBookFileTrack, ChangeTypeBookPathUpdate,
			ChangeTypeBookSoftDelete, ChangeTypeBookPrimaryDemote, ChangeTypeExternalIDReassign:
			if refusal := checkFsRegroupRow(store, c); refusal != nil {
				report.addReferentConflict(c, refusal)
			} else {
				report.Safe++
			}
		default:
			report.Safe++
		}
	}

	return report, nil
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
func checkFsRegroupRow(store ConflictChecker, c *database.OperationChange) error {
	book, refusal := CheckRestoreBook(store, c.BookID)
	if refusal != nil {
		return refusal
	}
	switch c.ChangeType {
	case ChangeTypeBookFileReassign:
		if _, refusal := CheckRestoreBook(store, c.OldValue); refusal != nil {
			return refusal
		}
		return checkFsRegroupRowOn(store, c, nil)
	case ChangeTypeBookFileTrack:
		return checkFsRegroupRowOn(store, c, func(f *database.BookFile) bool {
			return strconv.Itoa(f.TrackNumber) == c.NewValue
		})
	case ChangeTypeBookPathUpdate:
		if book.FilePath != c.NewValue {
			return refuse(ReasonChangedSince, "book %s path changed since the operation", c.BookID)
		}
	case ChangeTypeBookPrimaryDemote:
		if book.IsPrimaryVersion == nil || *book.IsPrimaryVersion {
			return refuse(ReasonChangedSince, "book %s was made primary again since the operation", c.BookID)
		}
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
			if owner != c.NewValue {
				return refuse(ReasonChangedSince, "external id %s/%s now names %q", source, id, owner)
			}
		}
	}
	return nil
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
