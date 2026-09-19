// file: internal/sweep/archive_sweep.go
// version: 1.4.0
// guid: a9f8e7d6-c5b4-3a21-9087-654321fedcba
// last-edited: 2026-09-19
//
// Archive sweep for soft-deleted books (backlog 7.10).
//
// Books marked_for_deletion with a deletion date older than the
// retention window are hard-deleted — but only books that own NO
// book_file rows. This sweep never removes files from disk and never
// deletes book_file rows (see SweepArchivedBooks).
// Runs as a maintenance task.

package sweep

import (
	"log/slog"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

const archiveRetentionDays = 30

// ArchiveSweepStore is the three-method slice the archive sweep needs. Exported
// so a caller that forwards into SweepArchivedBooks can name it. It
// previously took an inline interface embedding database.BookStore and
// database.BookFileStore — 78 methods.
type ArchiveSweepStore interface {
	GetAllBooksCore(limit, offset int) ([]database.BookCore, error)
	GetBookFiles(bookID string) ([]database.BookFile, error)
	DeleteBook(id string) error
}

// SweepArchivedBooks removes soft-deleted books past the retention
// window that own no book_file rows. Returns the count of books cleaned up.
//
// KNOWN INERT (2026-09-19): the enumeration below is GetAllBooksCore, which
// excludes soft-deleted books, so in practice this finds nothing to sweep.
// That is left as is on purpose: pointing it at ListSoftDeletedBooks would
// start hard-deleting books on a hardcoded 30-day clock that bypasses
// purge_soft_deleted_after_days — the owner's switch for exactly this, which
// is off in production. Whether to revive it (or retire it in favour of the
// purge) is an owner decision. The guard below makes it safe either way.
func SweepArchivedBooks(store ArchiveSweepStore) int {
	books, err := store.GetAllBooksCore(0, 0)
	if err != nil {
		slog.Warn("archive sweep list books", "err", err)
		return 0
	}

	cutoff := time.Now().Add(-time.Duration(archiveRetentionDays) * 24 * time.Hour)
	cleaned := 0
	ownsFiles := 0
	defer func() {
		if ownsFiles > 0 {
			slog.Info("archive sweep: left soft-deleted books that still own book_file rows",
				"count", ownsFiles)
		}
	}()

	for _, book := range books {
		if book.MarkedForDeletion == nil || !*book.MarkedForDeletion {
			continue
		}
		if book.MarkedForDeletionAt == nil || book.MarkedForDeletionAt.After(cutoff) {
			continue
		}

		// Never sweep a book that still owns book_file rows, and never touch
		// its files on disk.
		//
		// This used to os.Remove() every file the book owned and then
		// DeleteBook it. DeleteBook does not delete book_file rows (and must
		// not — database.ErrBookOwnsFiles), so every swept book left its rows
		// naming a book with no row. Worse, the removal had no protected-path
		// check, no RootDir confinement and no config opt-in, unlike
		// PurgeSoftDeletedBooks: a dedup-merge loser keeps its own files by
		// design, so 30 days after a merge it deleted the loser's audio, and
		// an iTunes-library path would have been deleted just the same.
		// Such a book stays soft-deleted (hidden, restorable). Fail closed on
		// a read error: an unreadable file list is not proof there are none.
		files, ferr := store.GetBookFiles(book.ID)
		if ferr != nil {
			slog.Warn("archive sweep: cannot read book_file rows; not sweeping", "book", book.ID, "err", ferr)
			continue
		}
		if len(files) > 0 {
			ownsFiles++
			continue
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
