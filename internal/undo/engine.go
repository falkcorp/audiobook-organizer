// file: internal/undo/engine.go
// version: 1.11.0
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
	// could not be read, or the row's value is malformed (Reason says which).
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
				report.Safe++
			}
		case "tag_write":
			// The revert reads the book before it touches the file.
			if _, refusal := CheckRestoreBook(store, c.BookID); refusal != nil {
				report.addReferentConflict(c, refusal)
			} else {
				report.Safe++
			}
		case ChangeTypeSeriesRename:
			if err := CheckRestoreReferent(store, c); err != nil {
				report.addReferentConflict(c, err)
			} else {
				report.Safe++
			}
		case ChangeTypeBookFileReassign:
			// The revert moves the row from BookID back to OldValue and reads
			// both books first; either one missing fails the row.
			_, refusal := CheckRestoreBook(store, c.BookID)
			if refusal == nil {
				_, refusal = CheckRestoreBook(store, c.OldValue)
			}
			if refusal != nil {
				report.addReferentConflict(c, refusal)
			} else {
				report.Safe++
			}
		case ChangeTypeBookFileTrack, ChangeTypeBookPathUpdate, ChangeTypeBookSoftDelete:
			// Each reads the book before writing (a soft-deleted book is still
			// there, and restoring it is the point of book_soft_delete).
			if _, refusal := CheckRestoreBook(store, c.BookID); refusal != nil {
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
