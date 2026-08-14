// file: internal/undo/engine.go
// version: 1.0.0
// guid: 2e7a9f1c-3b4d-4e8f-a1c5-7d9e2f4b8c3a
//
// Undo engine (spec 3.2 task 3). Reverses the destructive changes
// recorded by a prior operation by walking its operation_changes
// rows in reverse order and applying the inverse transform.
//
// Supported change_type reversals:
//   - file_move / organize_rename: os.Rename(new_value → old_value)
//   - metadata_update / db_update: restore field from old_value
//   - dir_create: remove directory if empty
//   - tag_write: no-op (tags are idempotently re-derived)
//
// Each change row is marked reverted_at on success; already-reverted
// rows are skipped (idempotent for resume after crash).

package undo

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// UndoResult summarizes the outcome of an undo operation.
type UndoResult struct {
	Reverted        int      `json:"reverted"`
	SkippedConflict int      `json:"skipped_conflict"`
	SkippedReverted int      `json:"skipped_reverted"`
	Failed          int      `json:"failed"`
	Errors          []string `json:"errors,omitempty"`
}

// OnFileMovedFunc is the callback signature for handling file moves during undo.
// Called when a file_move or organize_rename change is successfully reverted.
// store: the database store
// bookID: the ID of the book being reverted
// oldFilePath: the path the file was restored to (original location before organize)
type OnFileMovedFunc func(store interface {
	database.BookReader
	database.BookVersionStore
}, bookID, oldFilePath string)

// RunUndoOperation loads the changes for targetOpID, walks them in
// reverse order, and applies the inverse of each change. Progress
// is reported via the callback (step description + percentage).
//
// onFileMoved is an optional callback invoked after a file_move or
// organize_rename change is successfully reverted. Pass nil if no
// callback is needed.
func RunUndoOperation(
	store interface {
		database.BookStore
		database.BookVersionStore
		database.OperationStore
	},
	targetOpID string,
	progress func(step string, pct int),
	onFileMoved OnFileMovedFunc,
) (*UndoResult, error) {
	if progress == nil {
		progress = func(string, int) {}
	}

	changes, err := store.GetOperationChanges(targetOpID)
	if err != nil {
		return nil, fmt.Errorf("load operation changes: %w", err)
	}
	if len(changes) == 0 {
		return &UndoResult{}, nil
	}

	// Reverse order: last change undone first.
	reversed := make([]*database.OperationChange, len(changes))
	for i, c := range changes {
		reversed[len(changes)-1-i] = c
	}

	result := &UndoResult{}
	total := len(reversed)

	for i, change := range reversed {
		pct := int(float64(i+1) / float64(total) * 100)
		progress(fmt.Sprintf("undo %s on %s", change.ChangeType, change.BookID), pct)

		if change.RevertedAt != nil {
			result.SkippedReverted++
			continue
		}

		err := revertChange(store, change, onFileMoved)
		if err != nil {
			result.Failed++
			result.Errors = append(result.Errors, fmt.Sprintf("%s/%s: %v", change.BookID, change.ChangeType, err))
			continue
		}

		now := time.Now()
		change.RevertedAt = &now
		if updateErr := store.CreateOperationChange(change); updateErr != nil {
			// Best effort — the FS/DB change already succeeded.
			result.Errors = append(result.Errors, fmt.Sprintf("mark reverted %s: %v", change.ID, updateErr))
		}
		result.Reverted++
	}

	return result, nil
}

// revertChange applies the inverse of a single operation change.
func revertChange(
	store interface {
		database.BookStore
		database.BookVersionStore
		database.OperationStore
	},
	change *database.OperationChange,
	onFileMoved OnFileMovedFunc,
) error {
	switch change.ChangeType {
	case "file_move", "organize_rename":
		if err := revertFileMove(change); err != nil {
			return err
		}
		if onFileMoved != nil {
			onFileMoved(store, change.BookID, change.OldValue)
		}
		return nil
	case "metadata_update", "db_update":
		return revertMetadataUpdate(store, change)
	case "dir_create":
		return revertDirCreate(change)
	case "tag_write":
		return nil // tags are re-derived on next metadata apply
	default:
		return fmt.Errorf("unknown change_type: %s", change.ChangeType)
	}
}

// revertFileMove renames new_value back to old_value. If old_value
// already exists (conflict), returns an error rather than clobbering.
func revertFileMove(change *database.OperationChange) error {
	if change.OldValue == "" || change.NewValue == "" {
		return fmt.Errorf("missing path in change %s", change.ID)
	}

	// Check that new_value (the current location) exists.
	if _, err := os.Stat(change.NewValue); os.IsNotExist(err) {
		// Already moved back or deleted — idempotent no-op.
		return nil
	}

	// Check that old_value (restore target) doesn't already exist.
	if _, err := os.Stat(change.OldValue); err == nil {
		return fmt.Errorf("conflict: restore target already exists: %s", change.OldValue)
	}

	// Ensure parent directory of old_value exists.
	if err := os.MkdirAll(parentDir(change.OldValue), 0o775); err != nil {
		return fmt.Errorf("mkdir for restore: %w", err)
	}

	return os.Rename(change.NewValue, change.OldValue)
}

// revertMetadataUpdate restores a book field from the change's
// OldValue. OldValue is either a plain string (for single-field
// changes) or a JSON object (for multi-field snapshots).
func revertMetadataUpdate(store database.BookStore, change *database.OperationChange) error {
	if change.BookID == "" {
		return fmt.Errorf("no book_id on metadata change %s", change.ID)
	}

	book, err := store.GetBookByID(change.BookID)
	if err != nil || book == nil {
		return fmt.Errorf("book %s not found", change.BookID)
	}

	if change.FieldName != "" && change.OldValue != "" {
		applyFieldRestore(book, change.FieldName, change.OldValue)
	} else if change.OldValue != "" {
		// Multi-field JSON snapshot.
		var snapshot map[string]interface{}
		if err := json.Unmarshal([]byte(change.OldValue), &snapshot); err == nil {
			for field, val := range snapshot {
				if s, ok := val.(string); ok {
					applyFieldRestore(book, field, s)
				}
			}
		}
	}

	_, err = store.UpdateBook(book.ID, book)
	return err
}

// applyFieldRestore sets a single field on a Book from a string value.
func applyFieldRestore(book *database.Book, field, value string) {
	switch field {
	case "title":
		book.Title = value
	case "file_path":
		book.FilePath = value
	case "format":
		book.Format = value
	case "narrator":
		book.Narrator = &value
	case "edition":
		book.Edition = &value
	case "description":
		book.Description = &value
	case "language":
		book.Language = &value
	case "publisher":
		book.Publisher = &value
	case "genre":
		book.Genre = &value
	case "library_state":
		book.LibraryState = &value
	}
}

// revertDirCreate removes a directory if it's empty. Non-empty
// directories are left alone (the files inside may still be needed).
func revertDirCreate(change *database.OperationChange) error {
	if change.NewValue == "" {
		return nil
	}
	entries, err := os.ReadDir(change.NewValue)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // already gone
		}
		return err
	}
	if len(entries) > 0 {
		return nil // not empty, leave it
	}
	return os.Remove(change.NewValue)
}

func parentDir(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			return path[:i]
		}
	}
	return "."
}

// UndoConflictReport summarizes potential conflicts detected before
// executing an undo operation. The caller shows this to the user
// so they can decide whether to proceed.
type UndoConflictReport struct {
	TotalChanges    int                `json:"total_changes"`
	AlreadyReverted int                `json:"already_reverted"`
	ContentChanged  []UndoConflictItem `json:"content_changed,omitempty"`
	BookDeleted     []UndoConflictItem `json:"book_deleted,omitempty"`
	ReOrganized     []UndoConflictItem `json:"re_organized,omitempty"`
	Safe            int                `json:"safe"`
}

// UndoConflictItem describes one change that may conflict.
type UndoConflictItem struct {
	ChangeID   string `json:"change_id"`
	BookID     string `json:"book_id"`
	ChangeType string `json:"change_type"`
	Reason     string `json:"reason"`
}

// PreflightUndoConflicts scans the operation's changes and reports
// which ones can be safely undone vs which have conflicts.
func PreflightUndoConflicts(store database.Store, operationID string) (*UndoConflictReport, error) {
	changes, err := store.GetOperationChanges(operationID)
	if err != nil {
		return nil, fmt.Errorf("load changes: %w", err)
	}

	report := &UndoConflictReport{TotalChanges: len(changes)}

	for _, c := range changes {
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
		case "metadata_update", "db_update":
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
				} else {
					report.Safe++
				}
			} else {
				report.Safe++
			}
		default:
			report.Safe++
		}
	}

	return report, nil
}

func checkFileMoveConflict(store database.Store, c *database.OperationChange) *UndoConflictItem {
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
