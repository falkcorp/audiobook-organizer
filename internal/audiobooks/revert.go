// file: internal/audiobooks/revert.go
// version: 1.4.0
// guid: d4e5f6a7-b8c9-d0e1-f2a3-b4c5d6e7f8a9
// last-edited: 2026-09-12

package audiobooks

import (
	"fmt"
	"log/slog"
	"os"
	"reflect"
	"slices"
	"sort"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/fileops"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

// revertServiceStore is the slice of the store this service uses, measured
// with an empty-interface compiler probe: four direct calls plus
// GetAllImportPaths, which is what satisfies importPathLister when the store is
// forwarded to isProtectedPath.
//
// It previously embedded database.BookReader, database.BookWriter and
// database.OperationStore wholesale to reach those four -- the comment above it
// called that "the narrow slice", which it was only relative to database.Store.
type revertServiceStore interface {
	GetBookByID(id string) (*database.Book, error)
	UpdateBook(id string, book *database.Book) (*database.Book, error)
	GetOperationChanges(operationID string) ([]*database.OperationChange, error)
	MarkOperationChangesReverted(operationID string, changeIDs []string) error

	// Needed by the embedded isProtectedPath call in revertTagWrite
	// (SERVER-GLOBAL-STORE-AUDIT phase 6).
	GetAllImportPaths() ([]database.ImportPath, error)
}

// RevertService handles reverting operations by undoing recorded changes.
type RevertService struct {
	db revertServiceStore
}

// NewRevertService creates a new RevertService.
func NewRevertService(db revertServiceStore) *RevertService {
	return &RevertService{db: db}
}

// RevertResult reports what RevertOperation did with each row of the
// operation's change ledger. Only the Restored rows are marked reverted in the
// store; Failed and NotRestorable rows keep a nil RevertedAt so the ledger
// never claims an undo that did not happen.
type RevertResult struct {
	OperationID string `json:"operation_id"`
	// Total is the number of change rows the operation recorded.
	Total int `json:"total"`
	// Restored rows were reversed (or were no-op markers with nothing to
	// reverse) and are now marked reverted.
	Restored int `json:"restored"`
	// Failed rows have a change type the engine can reverse, but the reversal
	// returned an error. They are not marked reverted.
	Failed int `json:"failed"`
	// NotRestorable rows have a change type the engine has no reversal for
	// (the record-only author_delete / narrator_delete rows, or a type it does
	// not recognise). They are not marked reverted.
	NotRestorable int `json:"not_restorable"`
	// NotRestorableTypes counts NotRestorable rows by change type.
	NotRestorableTypes map[string]int `json:"not_restorable_types,omitempty"`
}

// Partial reports whether any row of the operation was left un-reverted.
func (r *RevertResult) Partial() bool {
	return r.Failed > 0 || r.NotRestorable > 0
}

// Summary is a one-line human-readable account of the result, used both in the
// log and as the HTTP response message.
func (r *RevertResult) Summary() string {
	if !r.Partial() {
		return fmt.Sprintf("operation reverted: %d of %d changes restored", r.Restored, r.Total)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "operation partially reverted: %d of %d changes restored", r.Restored, r.Total)
	if r.Failed > 0 {
		fmt.Fprintf(&b, "; %d failed to restore", r.Failed)
	}
	if r.NotRestorable > 0 {
		fmt.Fprintf(&b, "; %d cannot be undone automatically (%s)", r.NotRestorable, formatTypeCounts(r.NotRestorableTypes))
	}
	return b.String()
}

// NotRestorableError is returned by RevertOperation when no row of the
// operation has a change type the engine can reverse -- e.g. every row of a
// maintenance.purge-empty-authors run is an author_delete record. Nothing is
// reversed and nothing is marked reverted.
type NotRestorableError struct {
	OperationID string
	Total       int
	Types       map[string]int
}

func (e *NotRestorableError) Error() string {
	return fmt.Sprintf("this operation's changes are a record only and cannot be undone automatically: %s",
		formatTypeCounts(e.Types))
}

// recordOnlyChangeTypes are ledger rows written as a durable record of a
// destructive step that has no automatic reversal. Restoring a deleted author
// or narrator would mean recreating the entity (and deciding whether to reuse
// its ID), which the engine deliberately does not do.
//
//   - author_delete: maintenance.purge-empty-authors, author-duplicate-merge
//     (journalAuthorMerge) and the API author merge (server/entities_ops.go).
//   - narrator_delete: maintenance.purge-empty-narrators.
//
// Listing them is documentation: any type revertChange has no case for is
// treated the same way.
var recordOnlyChangeTypes = map[string]bool{
	"author_delete":   true,
	"narrator_delete": true,
}

// isRestorable reports whether revertChange has a reversal for changeType. It
// must agree with revertChange's switch.
func isRestorable(changeType string) bool {
	if recordOnlyChangeTypes[changeType] {
		return false
	}
	switch changeType {
	case "file_move", "organize_rename", "metadata_update", "tag_write",
		"organize_failed", "organize_skipped", "organize_summary":
		return true
	}
	return false
}

// RevertOperation undoes the restorable changes of an operation in reverse
// order and marks exactly those rows reverted.
//
//   - No row restorable: returns *NotRestorableError; nothing is touched.
//   - Some rows not restorable: the restorable ones are reversed and marked;
//     the rest are left unmarked and counted in the result.
//   - A restorable row whose reversal fails is left unmarked, counted in
//     Failed, and RevertOperation returns the result with a non-nil error.
func (rs *RevertService) RevertOperation(operationID string) (*RevertResult, error) {
	changes, err := rs.db.GetOperationChanges(operationID)
	if err != nil {
		return nil, fmt.Errorf("failed to get operation changes: %w", err)
	}

	if len(changes) == 0 {
		return nil, fmt.Errorf("no changes found for operation %s", operationID)
	}

	// Check if already reverted
	for _, c := range changes {
		if c.RevertedAt != nil {
			return nil, fmt.Errorf("operation %s has already been reverted", operationID)
		}
	}

	result := &RevertResult{OperationID: operationID, Total: len(changes)}
	var restorable []*database.OperationChange
	for _, c := range changes {
		if isRestorable(c.ChangeType) {
			restorable = append(restorable, c)
			continue
		}
		result.NotRestorable++
		if result.NotRestorableTypes == nil {
			result.NotRestorableTypes = map[string]int{}
		}
		result.NotRestorableTypes[c.ChangeType]++
	}

	if len(restorable) == 0 {
		slog.Warn("revert refused: no restorable changes",
			"operation", logger.SanitizeLogValue(operationID), "not_restorable", result.NotRestorable,
			"types", formatTypeCounts(result.NotRestorableTypes))
		return nil, &NotRestorableError{OperationID: operationID, Total: result.Total, Types: result.NotRestorableTypes}
	}

	// Process in reverse order
	var errors []string
	var restoredIDs []string
	for _, c := range slices.Backward(restorable) {
		if err := rs.revertChange(c); err != nil {
			result.Failed++
			errors = append(errors, fmt.Sprintf("change %s: %v", c.ID, err))
			slog.Warn("revert failed for change", "c", c.ID, "err", err)
			continue
		}
		restoredIDs = append(restoredIDs, c.ID)
	}

	// Mark only the rows that were actually restored.
	if len(restoredIDs) > 0 {
		if err := rs.db.MarkOperationChangesReverted(operationID, restoredIDs); err != nil {
			return nil, fmt.Errorf("restored %d changes but failed to mark them reverted: %w", len(restoredIDs), err)
		}
	}
	result.Restored = len(restoredIDs)

	slog.Info("revert finished", "operation", logger.SanitizeLogValue(operationID), "total", result.Total,
		"restored", result.Restored, "failed", result.Failed,
		"not_restorable", result.NotRestorable, "types", formatTypeCounts(result.NotRestorableTypes))

	if len(errors) > 0 {
		return result, fmt.Errorf("partially reverted with %d errors: %s", len(errors), errors[0])
	}
	return result, nil
}

// formatTypeCounts renders {"author_delete": 3} as "3 author_delete rows",
// sorted by type so the output is stable.
func formatTypeCounts(counts map[string]int) string {
	types := make([]string, 0, len(counts))
	for t := range counts {
		types = append(types, t)
	}
	sort.Strings(types)
	parts := make([]string, 0, len(types))
	for _, t := range types {
		noun := "rows"
		if counts[t] == 1 {
			noun = "row"
		}
		parts = append(parts, fmt.Sprintf("%d %s %s", counts[t], t, noun))
	}
	return strings.Join(parts, ", ")
}

func (rs *RevertService) revertChange(c *database.OperationChange) error {
	switch c.ChangeType {
	case "file_move", "organize_rename":
		// organize_rename writes the same (OldValue, NewValue) shape as
		// file_move — old path → new path, Book.file_path updated. The
		// reversal is identical: move the file back and restore
		// Book.file_path.
		return rs.revertFileMove(c)
	case "metadata_update":
		return rs.revertMetadataUpdate(c)
	case "tag_write":
		return rs.revertTagWrite(c)
	case "organize_failed", "organize_skipped", "organize_summary":
		// No filesystem or DB mutation recorded; nothing to reverse.
		return nil
	default:
		// Unreachable from RevertOperation, which filters on isRestorable.
		return fmt.Errorf("unknown change type: %s", c.ChangeType)
	}
}

func (rs *RevertService) revertFileMove(c *database.OperationChange) error {
	// Check file exists at new location
	if _, err := os.Stat(c.NewValue); os.IsNotExist(err) {
		slog.Warn("file no longer at , skipping revert", "c", c.NewValue)
		return nil
	}

	// Move file back
	if err := os.Rename(c.NewValue, c.OldValue); err != nil {
		return fmt.Errorf("failed to move file back from %s to %s: %w", c.NewValue, c.OldValue, err)
	}

	// Update book record
	book, err := rs.db.GetBookByID(c.BookID)
	if err != nil {
		return fmt.Errorf("failed to get book %s: %w", c.BookID, err)
	}
	book.FilePath = c.OldValue
	if _, err := rs.db.UpdateBook(book.ID, book); err != nil {
		return fmt.Errorf("failed to update book path: %w", err)
	}

	return nil
}

// bookFieldMap maps field names to Book struct field names for reflection-based revert.
var bookFieldMap = map[string]string{
	"title":                  "Title",
	"narrator":               "Narrator",
	"edition":                "Edition",
	"language":               "Language",
	"publisher":              "Publisher",
	"isbn10":                 "ISBN10",
	"isbn13":                 "ISBN13",
	"asin":                   "ASIN",
	"cover_url":              "CoverURL",
	"library_state":          "LibraryState",
	"open_library_id":        "OpenLibraryID",
	"hardcover_id":           "HardcoverID",
	"google_books_id":        "GoogleBooksID",
	"metadata_review_status": "MetadataReviewStatus",
}

func (rs *RevertService) revertMetadataUpdate(c *database.OperationChange) error {
	book, err := rs.db.GetBookByID(c.BookID)
	if err != nil {
		return fmt.Errorf("failed to get book %s: %w", c.BookID, err)
	}

	structField, ok := bookFieldMap[c.FieldName]
	if !ok {
		return fmt.Errorf("unknown metadata field: %s", c.FieldName)
	}

	v := reflect.ValueOf(book).Elem()
	f := v.FieldByName(structField)
	if !f.IsValid() {
		return fmt.Errorf("invalid struct field: %s", structField)
	}

	// Set the field to old value
	if c.OldValue == "" {
		// Set to nil for pointer types
		if f.Kind() == reflect.Pointer {
			f.Set(reflect.Zero(f.Type()))
		} else {
			f.SetString("")
		}
	} else {
		if f.Kind() == reflect.Pointer {
			val := reflect.New(f.Type().Elem())
			val.Elem().SetString(c.OldValue)
			f.Set(val)
		} else {
			f.SetString(c.OldValue)
		}
	}

	if _, err := rs.db.UpdateBook(book.ID, book); err != nil {
		return fmt.Errorf("failed to update book: %w", err)
	}
	return nil
}

func (rs *RevertService) revertTagWrite(c *database.OperationChange) error {
	book, err := rs.db.GetBookByID(c.BookID)
	if err != nil {
		return fmt.Errorf("failed to get book %s: %w", c.BookID, err)
	}

	if _, statErr := os.Stat(book.FilePath); os.IsNotExist(statErr) {
		slog.Warn("file not found, skipping tag revert", "book", book.FilePath)
		return nil
	}

	if isProtectedPath(rs.db, book.FilePath) {
		slog.Info("skipping tag revert for protected path", "book", book.FilePath)
		return nil
	}

	tagMap := map[string]any{
		c.FieldName: c.OldValue,
	}
	opConfig := fileops.OperationConfig{VerifyChecksums: true}
	if err := metadata.WriteMetadataToFile(book.FilePath, tagMap, opConfig); err != nil {
		return fmt.Errorf("failed to write tag %s back to %s: %w", c.FieldName, book.FilePath, err)
	}

	return nil
}
