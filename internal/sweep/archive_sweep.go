// file: internal/sweep/archive_sweep.go
// version: 1.2.0
// guid: a9f8e7d6-c5b4-3a21-9087-654321fedcba
// last-edited: 2026-08-18
//
// Archive sweep for soft-deleted books (backlog 7.10).
//
// Books marked_for_deletion with a deletion date older than the
// retention window are physically cleaned up: files removed from
// disk, book_files rows deleted, and the book row hard-deleted.
// Runs as a maintenance task.

package sweep

import (
	"log/slog"
	"os"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

const archiveRetentionDays = 30

// archiveSweepStore is the three-method slice the archive sweep needs. It
// previously took an inline interface embedding database.BookStore and
// database.BookFileStore — 78 methods.
type archiveSweepStore interface {
	GetAllBooksCore(limit, offset int) ([]database.BookCore, error)
	GetBookFiles(bookID string) ([]database.BookFile, error)
	DeleteBook(id string) error
}

// SweepArchivedBooks removes soft-deleted books past the retention
// window. Returns the count of books cleaned up.
func SweepArchivedBooks(store archiveSweepStore) int {
	books, err := store.GetAllBooksCore(0, 0)
	if err != nil {
		slog.Warn("archive sweep list books", "err", err)
		return 0
	}

	cutoff := time.Now().Add(-time.Duration(archiveRetentionDays) * 24 * time.Hour)
	cleaned := 0

	for _, book := range books {
		if book.MarkedForDeletion == nil || !*book.MarkedForDeletion {
			continue
		}
		if book.MarkedForDeletionAt == nil || book.MarkedForDeletionAt.After(cutoff) {
			continue
		}

		// Remove files from disk.
		files, _ := store.GetBookFiles(book.ID)
		for _, f := range files {
			if f.FilePath != "" {
				if err := os.Remove(f.FilePath); err != nil && !os.IsNotExist(err) {
					slog.Warn("archive sweep remove", "f", f.FilePath, "err", err)
				}
			}
		}

		// Hard-delete the book record.
		if err := store.DeleteBook(book.ID); err != nil {
			slog.Warn("archive sweep delete", "book", book.ID, "err", err)
			continue
		}
		cleaned++
	}

	return cleaned
}
