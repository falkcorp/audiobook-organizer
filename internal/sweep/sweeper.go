// file: internal/sweep/sweeper.go
// version: 1.1.0
// guid: a1b2c3d4-e5f6-4789-abcd-ef2345678901
// last-edited: 2026-07-07

package sweep

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// SweeperResult captures the outcome of a sweep operation.
type SweeperResult struct {
	TombstonesCleaned int      `json:"tombstones_cleaned"`
	OrphanedFiles     []string `json:"orphaned_files,omitempty"`
	MissingFiles      []string `json:"missing_files,omitempty"`
	Errors            []string `json:"errors,omitempty"`
}

// SweepTombstones cleans up tombstone records where:
// - The original book record is gone (deleted successfully)
// - The file no longer exists on disk (deleted or moved)
func SweepTombstones(store database.BookStore) (*SweeperResult, error) {
	result := &SweeperResult{
		Errors: []string{},
	}

	tombstones, err := store.ListBookTombstones(1000)
	if err != nil {
		return nil, fmt.Errorf("failed to list tombstones: %w", err)
	}

	for _, tomb := range tombstones {
		// Check if the original book still exists (shouldn't if purge completed)
		existing, err := store.GetBookByID(tomb.ID)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("tombstone %s: failed to check book: %v", tomb.ID, err))
			continue
		}

		if existing != nil {
			// Book still exists — purge didn't complete. Delete the orphan tombstone.
			slog.Info("sweeper tombstone has live book record, removing tombstone", "tombID", tomb.ID)
			_ = store.DeleteBookTombstone(tomb.ID)
			result.TombstonesCleaned++
			continue
		}

		// Book is gone. Check if file still exists.
		if tomb.FilePath != "" {
			if _, err := os.Stat(tomb.FilePath); err == nil {
				// File exists but book record is gone — try to delete file
				if err := os.Remove(tomb.FilePath); err != nil {
					result.Errors = append(result.Errors, fmt.Sprintf("tombstone %s: failed to delete orphaned file %s: %v", tomb.ID, tomb.FilePath, err))
					continue
				}
				slog.Info("sweeper deleted orphaned file (tombstone)", "path", tomb.FilePath, "tombstoneID", tomb.ID)
			}
			// File gone or just deleted — clean up tombstone
		}

		if err := store.DeleteBookTombstone(tomb.ID); err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("tombstone %s: failed to delete: %v", tomb.ID, err))
		} else {
			result.TombstonesCleaned++
		}
	}

	return result, nil
}

// AuditFileConsistency checks all books in the database and reports:
// - Books pointing to files that don't exist
// It does NOT fix anything — just reports.
func AuditFileConsistency(store database.BookStore) (*SweeperResult, error) {
	result := &SweeperResult{
		MissingFiles: []string{},
		Errors:       []string{},
	}

	books, err := store.GetAllBooksCore(100000, 0)
	if err != nil {
		return nil, fmt.Errorf("failed to list books: %w", err)
	}

	for _, book := range books {
		if book.FilePath == "" {
			continue
		}
		if _, err := os.Stat(book.FilePath); err != nil {
			result.MissingFiles = append(result.MissingFiles, fmt.Sprintf("%s: %s (%s)", book.ID, book.FilePath, book.Title))
		}
	}

	slog.Info("sweeper audit complete — books checked, missing files", "books_count", len(books), "missing_count", len(result.MissingFiles))
	return result, nil
}
