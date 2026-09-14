// file: internal/database/pebble_store_bookfile_modify.go
// version: 1.0.0
// guid: 6d3a9e27-b4c1-4f58-8a02-e7c5d1b9f463
// last-edited: 2026-09-14

package database

import (
	"errors"
	"fmt"
)

// ErrSkipBookFileWrite, returned by a ModifyBookFile callback, means "nothing
// to change": ModifyBookFile writes nothing and returns the row as read, with
// a nil error.
var ErrSkipBookFileWrite = errors.New("skip book file write")

// ModifyBookFile is the book_file twin of ModifyBook: under the row's
// lockBookFile stripe it reads the stored row, hands a copy to fn, and writes
// fn's result through the same commit UpdateBookFile uses (updateBookFileLocked:
// shared merge rule, secondary-index rewrite on a path or PID change). The read
// and the write are one step, so a precondition fn checks on the row cannot be
// invalidated by another stripe-holding writer before the commit.
//
// (nil, nil) when the row does not exist. fn must not change ID or BookID and
// must not write any book_file itself.
func (s *PebbleStore) ModifyBookFile(bookID, fileID string, fn func(*BookFile) error) (*BookFile, error) {
	unlock := s.lockBookFile(fileID)
	notify := false
	defer func() {
		unlock()
		// After the unlock, as in updateBookFile: the aggregate recompute
		// takes the book's stripe, and no file stripe is held while one is.
		if notify {
			s.notifyBookFileChange(bookID)
		}
	}()

	stored, err := s.getBookFileByID(bookID, fileID)
	if err != nil {
		return nil, fmt.Errorf("ModifyBookFile: read %s: %w", fileID, err)
	}
	if stored == nil {
		return nil, nil
	}
	row := *stored
	if err := fn(&row); err != nil {
		if errors.Is(err, ErrSkipBookFileWrite) {
			return stored, nil
		}
		return nil, err
	}
	if row.ID != stored.ID || row.BookID != stored.BookID {
		return nil, fmt.Errorf("ModifyBookFile: callback changed the identity of %s (id %q→%q, book %q→%q)",
			fileID, stored.ID, row.ID, stored.BookID, row.BookID)
	}
	n, err := s.updateBookFileLocked(fileID, &row, true, true)
	if err != nil {
		return nil, fmt.Errorf("ModifyBookFile: write %s: %w", fileID, err)
	}
	notify = n
	return &row, nil
}
