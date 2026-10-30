// file: internal/sweep/sweeper.go
// version: 1.3.0
// guid: a1b2c3d4-e5f6-4789-abcd-ef2345678901
// last-edited: 2026-10-30

package sweep

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

// sweepLog carries the refusals: they name paths and reasons built from library
// data, which must pass the log-injection barrier (logger.SanitizeLogValue).
var sweepLog = logger.New("sweep")

// SweeperResult captures the outcome of a sweep operation.
type SweeperResult struct {
	TombstonesCleaned int `json:"tombstones_cleaned"`
	// RefusedFiles counts the tombstones whose file the sweep declined to
	// remove (and whose tombstone it therefore kept), and Refusals says why for
	// each, so the operation report shows them instead of a quiet success.
	RefusedFiles  int      `json:"refused_files"`
	Refusals      []string `json:"refusals,omitempty"`
	OrphanedFiles []string `json:"orphaned_files,omitempty"`
	MissingFiles  []string `json:"missing_files,omitempty"`
	Errors        []string `json:"errors,omitempty"`
}

// tombstoneSweeper is the three-method slice SweepTombstones needs.
// Was database.BookStore (51 methods).
type tombstoneSweeper interface {
	ListBookTombstones(limit int) ([]database.Book, error)
	GetBookByID(id string) (*database.Book, error)
	DeleteBookTombstone(id string) error
}

// FileGuard decides whether the sweep may unlink the file a tombstone names. The
// sweep holds none of the rules itself: they are the book purge's, and live in
// package audiobooks (AudiobookService.TombstoneSweepGuard), which this package
// does not import, so the server injects it. It uses builtin and database types
// only so that the two packages can stay apart.
type FileGuard interface {
	// RootReady reports whether the library is there to be judged against: an
	// unmounted library makes every file look already gone. The sweep asks it
	// once, before its first removal, and keeps every tombstone that names a
	// file when it fails.
	RootReady() error
	// Check says what to do about path, named by a tombstone that carries stamp
	// (nil when it carries none). remove true: unlink it. remove false with a
	// nil error: nothing is there, so only the tombstone is cleaned. A non-nil
	// error: leave both the file and the tombstone, and say why.
	Check(path string, stamp *database.TombstoneFileStamp) (remove bool, err error)
}

// SweepTombstones cleans up tombstone records where:
// - The original book record is gone (deleted successfully)
// - The file no longer exists on disk (deleted or moved)
//
// The file a tombstone names is unlinked only when guard allows it: inside the
// library root, not a protected path, named by no book_file row and no book row
// (live, trashed or retired), and the very file the purge recorded in the
// tombstone's stamp. Any refusal is logged at Warn, counted in the result's
// RefusedFiles and Refusals, and leaves the tombstone in place. A nil guard
// refuses every file removal: there is no unguarded path.
//
// The loop is sequential on purpose. It is capped at 1000 tombstones, which are
// leftovers of purges that did not finish and so are normally none; and two
// tombstones can name the same path (duplicate book rows over one file), which a
// worker pool would have to partition by path to keep from racing on one
// unlink.
func SweepTombstones(store tombstoneSweeper, guard FileGuard) (*SweeperResult, error) {
	result := &SweeperResult{
		Errors: []string{},
	}

	tombstones, err := store.ListBookTombstones(1000)
	if err != nil {
		return nil, fmt.Errorf("failed to list tombstones: %w", err)
	}

	stamps, _ := database.AsCapability[database.TombstoneStampReader](store)
	rootChecked := false
	var rootErr error

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
			if guard != nil && !rootChecked {
				rootChecked, rootErr = true, guard.RootReady()
			}
			remove, refusal := decideFile(guard, rootErr, stamps, tomb)
			if refusal != nil {
				sweepLog.Warn("sweeper keeping tombstone and file: removal refused: tombstone=%s path=%s reason=%s",
					logger.SanitizeLogValue(tomb.ID), logger.SanitizeLogValue(tomb.FilePath), logger.SanitizeLogValue(refusal.Error()))
				result.RefusedFiles++
				result.Refusals = append(result.Refusals, fmt.Sprintf("tombstone %s: %s kept: %v", tomb.ID, tomb.FilePath, refusal))
				continue
			}
			if remove {
				if err := os.Remove(tomb.FilePath); err != nil && !errors.Is(err, fs.ErrNotExist) {
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

// decideFile is the guard's answer for one tombstone's file, with every way the
// question can fail turned into a refusal: no guard, a library that is not
// ready, a stamp that cannot be read.
func decideFile(guard FileGuard, rootErr error, stamps database.TombstoneStampReader, tomb database.Book) (remove bool, refusal error) {
	if guard == nil {
		return false, errors.New("no removal guard is configured")
	}
	if rootErr != nil {
		return false, fmt.Errorf("library root not ready: %w", rootErr)
	}
	var stamp *database.TombstoneFileStamp
	if stamps != nil {
		var err error
		if stamp, err = stamps.GetBookTombstoneStamp(tomb.ID); err != nil {
			return false, fmt.Errorf("cannot read the tombstone's file stamp: %w", err)
		}
	}
	return guard.Check(tomb.FilePath, stamp)
}

// fileAuditor is the one method AuditFileConsistency calls. It previously
// declared database.BookStore — 51 methods for a single call.
type fileAuditor interface {
	GetAllBooksCore(limit, offset int) ([]database.BookCore, error)
}

// AuditFileConsistency checks all books in the database and reports:
// - Books pointing to files that don't exist
// It does NOT fix anything — just reports.
func AuditFileConsistency(store fileAuditor) (*SweeperResult, error) {
	result := &SweeperResult{
		MissingFiles: []string{},
		Errors:       []string{},
	}

	books, err := store.GetAllBooksCore(0, 0)
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
