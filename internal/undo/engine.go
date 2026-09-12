// file: internal/undo/engine.go
// version: 1.9.0
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
	BookDeleted     []UndoConflictItem `json:"book_deleted,omitempty"`
	ReOrganized     []UndoConflictItem `json:"re_organized,omitempty"`
	// The four series buckets hold rows CheckRestoreReferent refuses. The
	// revert refuses every one of them each time it runs, so they are NOT
	// restorable and must never count toward offering an Undo (the web
	// confirmation names them separately). SeriesDeleted: the series no longer
	// exists. SeriesRenamedSince: a series_rename whose series was renamed
	// again after the operation. SeriesNameTaken: a series_rename whose old
	// name now belongs to another series. SeriesCheckFailed: the series could
	// not be read, or the row's value is malformed (Reason says which).
	SeriesDeleted      []UndoConflictItem `json:"series_deleted,omitempty"`
	SeriesRenamedSince []UndoConflictItem `json:"series_renamed_since,omitempty"`
	SeriesNameTaken    []UndoConflictItem `json:"series_name_taken,omitempty"`
	SeriesCheckFailed  []UndoConflictItem `json:"series_check_failed,omitempty"`
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

// addReferentConflict files a CheckRestoreReferent refusal under the bucket
// matching its reason. Anything that is not deleted / renamed since / name
// taken (a lookup failure, a malformed row, or an error without a reason) goes
// to SeriesCheckFailed: the revert refuses it just the same.
func (r *UndoConflictReport) addReferentConflict(c *database.OperationChange, err error) {
	reason := RefusalReason(err)
	if reason == "" {
		reason = ReasonSeriesLookupFailed
	}
	item := UndoConflictItem{ChangeID: c.ID, BookID: c.BookID, ChangeType: c.ChangeType, Reason: reason}
	switch reason {
	case ReasonSeriesDeleted:
		r.SeriesDeleted = append(r.SeriesDeleted, item)
	case ReasonSeriesRenamedSince:
		r.SeriesRenamedSince = append(r.SeriesRenamedSince, item)
	case ReasonSeriesNameTaken:
		r.SeriesNameTaken = append(r.SeriesNameTaken, item)
	default:
		r.SeriesCheckFailed = append(r.SeriesCheckFailed, item)
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
			if conflict := checkFileMoveConflict(store, c); conflict != nil {
				switch conflict.Reason {
				case "content changed":
					report.ContentChanged = append(report.ContentChanged, *conflict)
				case "book deleted":
					report.BookDeleted = append(report.BookDeleted, *conflict)
				case "re-organized":
					report.ReOrganized = append(report.ReOrganized, *conflict)
				default:
					report.ContentChanged = append(report.ContentChanged, *conflict)
				}
			} else {
				report.Safe++
			}
		case "metadata_update":
			if c.BookID != "" {
				book, _ := store.GetBookByID(c.BookID)
				if book == nil {
					report.BookDeleted = append(report.BookDeleted, UndoConflictItem{
						ChangeID: c.ID, BookID: c.BookID, ChangeType: c.ChangeType,
						Reason: "book deleted",
					})
				} else if book.IsSoftDeleted() {
					report.BookDeleted = append(report.BookDeleted, UndoConflictItem{
						ChangeID: c.ID, BookID: c.BookID, ChangeType: c.ChangeType,
						Reason: "book deleted",
					})
				} else if err := CheckRestoreReferent(store, c); err != nil {
					report.addReferentConflict(c, err)
				} else {
					report.Safe++
				}
			} else {
				report.Safe++
			}
		case ChangeTypeSeriesRename:
			if err := CheckRestoreReferent(store, c); err != nil {
				report.addReferentConflict(c, err)
			} else {
				report.Safe++
			}
		default:
			report.Safe++
		}
	}

	return report, nil
}

func checkFileMoveConflict(store ConflictChecker, c *database.OperationChange) *UndoConflictItem {
	if c.NewValue == "" {
		return nil
	}

	// Check if the file at new location was modified after the op.
	info, err := os.Stat(c.NewValue)
	if os.IsNotExist(err) {
		return &UndoConflictItem{
			ChangeID: c.ID, BookID: c.BookID, ChangeType: c.ChangeType,
			Reason: "content changed",
		}
	}
	if err == nil && info.ModTime().After(c.CreatedAt) {
		return &UndoConflictItem{
			ChangeID: c.ID, BookID: c.BookID, ChangeType: c.ChangeType,
			Reason: "content changed",
		}
	}

	// Check if book was deleted or re-organized.
	if c.BookID != "" {
		book, _ := store.GetBookByID(c.BookID)
		if book == nil || book.IsSoftDeleted() {
			return &UndoConflictItem{
				ChangeID: c.ID, BookID: c.BookID, ChangeType: c.ChangeType,
				Reason: "book deleted",
			}
		}
		if book.LastOrganizedAt != nil && book.LastOrganizedAt.After(c.CreatedAt) {
			return &UndoConflictItem{
				ChangeID: c.ID, BookID: c.BookID, ChangeType: c.ChangeType,
				Reason: "re-organized",
			}
		}
	}

	return nil
}
