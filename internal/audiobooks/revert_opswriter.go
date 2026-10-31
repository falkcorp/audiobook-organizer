// file: internal/audiobooks/revert_opswriter.go
// version: 1.0.0
// guid: fabc7c66-a593-4d41-92c3-0280d8b95b61
// last-edited: 2026-10-30

package audiobooks

import (
	"errors"
	"fmt"
	"slices"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// revertBookFileModify puts one book_file field back to the value a v3
// ops.Writer's ModifyBookFile replaced (database.ChangeTypeBookFileModify), only
// while the field still holds what the Writer wrote.
//
// The write goes through the store's ModifyBookFile, the compare-and-swap the
// Writer used forward: the check runs on the row as read under the row's
// stripe, and the store re-runs it on a fresh copy when a concurrent writer
// wins the swap, so the revert cannot clobber a write that landed in between.
// Only the one struct field is written (undo.ApplyBookFileModifyOld). A field
// already back at its old value counts as restored; one that holds anything
// else, a retired row, or a row that left the book is refused as drift and the
// operation's other rows are still reverted. One row of a write is one field,
// so a write that changed several fields is several independent rows.
//
// A file_path restored here does not hand the vacated path's single-owner key
// back (reindexVacatedPath); that is the repoint row's job
// (revertBookFileRepoint), and the Writer's ModifyBookFile did not claim one
// forward either.
func (rs *RevertService) revertBookFileModify(c *database.OperationChange) error {
	m, err := undo.DecodeBookFileModify(c)
	if err != nil {
		return err
	}
	// checkErr is the check's own answer, reset on every run of the callback
	// (the store may run it more than once). It is returned in preference to
	// the store's error so a wrap cannot lose the typed refusal.
	var checkErr error
	row, err := rs.db.ModifyBookFile(c.BookID, m.FileID, func(f *database.BookFile) error {
		checkErr = nil
		if f.Retired {
			checkErr = undo.RetiredRowRefusal(f)
			return checkErr
		}
		if checkErr = undo.CheckBookFileModifyCurrent(f, m); checkErr != nil {
			if errors.Is(checkErr, undo.ErrAlreadyRestored) {
				return database.ErrSkipBookFileWrite
			}
			return checkErr
		}
		return undo.ApplyBookFileModifyOld(f, m)
	})
	switch {
	case checkErr != nil:
		return checkErr
	case err != nil:
		return fmt.Errorf("restore field %q of book_file %s: %w", m.Key, m.FileID, err)
	case row == nil:
		return driftRefusal("book_file %s is no longer on book %s", m.FileID, c.BookID)
	}
	return nil
}

// revertBookAuthorsModify puts a book's whole credit list back to the one a v3
// ops.Writer's ModifyBookAuthors replaced (database.ChangeTypeBookAuthorsModify),
// order included, only while the book still holds the list the Writer wrote.
//
// The write is the store's ModifyBookAuthors, the guarded read-modify-write the
// Writer used forward: the comparison runs on the list as read under the book's
// credits stripe. The journaled "after" is the callback's result, which the
// store normalised on write, so it is compared in that form
// (undo.CheckBookAuthorsModifyCurrent). Every author the revert hands a credit
// back to must still be a live row (undo.CheckAuthorLive), checked before the
// write; one that was merged away or deleted since refuses the row.
//
// No author row is deleted. The Writer creates none (an author a callback
// credits already existed), so an author the forward change added and the
// revert uncredits is just an author with no credits, which
// maintenance.purge-empty-authors removes; an author the same operation did
// create has its own journaled create row, with its own revert.
func (rs *RevertService) revertBookAuthorsModify(c *database.OperationChange) error {
	m, err := undo.DecodeBookAuthorsModify(c)
	if err != nil {
		return err
	}
	book, err := rs.loadBook(c.BookID)
	if err != nil {
		return err
	}
	for _, id := range undo.BookAuthorsModifyReturned(m) {
		if err := undo.CheckAuthorLive(rs.db.GetAuthorByID, id, "book credits change"); err != nil {
			return err
		}
	}
	var checkErr error
	_, err = rs.db.ModifyBookAuthors(c.BookID, func(cur []database.BookAuthor) ([]database.BookAuthor, error) {
		if checkErr = undo.CheckBookAuthorsModifyCurrent(c.BookID, cur, book.AuthorID, m); checkErr != nil {
			if errors.Is(checkErr, undo.ErrAlreadyRestored) {
				return nil, database.ErrSkipBookAuthorsWrite
			}
			return nil, checkErr
		}
		return slices.Clone(m.Before), nil
	})
	switch {
	case checkErr != nil:
		return checkErr
	case err != nil:
		return fmt.Errorf("restore credits of book %s: %w", c.BookID, err)
	}
	return nil
}
