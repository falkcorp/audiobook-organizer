// file: internal/database/pebble_store_bookfile_modify.go
// version: 1.1.0
// guid: 6d3a9e27-b4c1-4f58-8a02-e7c5d1b9f463
// last-edited: 2026-10-25

package database

import (
	"errors"
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
	// The read-modify-write is shared with ModifyBookFiles (modifyBookFileNoNotify).
	// It has released the row's stripe by the time it returns, so the aggregate
	// recompute, which takes the book's stripe, runs with no file stripe held.
	// It runs before the error return: a write whose fsync failed
	// (ErrBookFileDurabilityUnknown) was still applied, so the book's aggregates
	// must follow it (bookFileApplied's contract).
	row, _, recompute, err := s.modifyBookFileNoNotify("ModifyBookFile", bookID, fileID, fn)
	if recompute {
		s.notifyBookFileChange(bookID)
	}
	if err != nil {
		return nil, err
	}
	return row, nil
}
