// file: internal/audiobooks/revert.go
// version: 1.3.1
// guid: d4e5f6a7-b8c9-d0e1-f2a3-b4c5d6e7f8a9
// last-edited: 2026-09-02

package audiobooks

import (
	"fmt"
	"log/slog"
	"os"
	"reflect"
	"slices"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/fileops"
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
	RevertOperationChanges(operationID string) error

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

// RevertOperation undoes all changes from a given operation in reverse order.
func (rs *RevertService) RevertOperation(operationID string) error {
	changes, err := rs.db.GetOperationChanges(operationID)
	if err != nil {
		return fmt.Errorf("failed to get operation changes: %w", err)
	}

	if len(changes) == 0 {
		return fmt.Errorf("no changes found for operation %s", operationID)
	}

	// Check if already reverted
	for _, c := range changes {
		if c.RevertedAt != nil {
			return fmt.Errorf("operation %s has already been reverted", operationID)
		}
	}

	// Process in reverse order
	var errors []string
	for _, c := range slices.Backward(changes) {

		if err := rs.revertChange(c); err != nil {
			errors = append(errors, fmt.Sprintf("change %s: %v", c.ID, err))
			slog.Warn("revert failed for change", "c", c.ID, "err", err)
		}
	}

	// Mark all changes as reverted regardless of individual failures
	if err := rs.db.RevertOperationChanges(operationID); err != nil {
		return fmt.Errorf("failed to mark changes as reverted: %w", err)
	}

	if len(errors) > 0 {
		return fmt.Errorf("partially reverted with %d errors: %s", len(errors), errors[0])
	}
	return nil
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
