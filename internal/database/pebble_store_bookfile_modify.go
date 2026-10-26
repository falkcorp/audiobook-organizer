// file: internal/database/pebble_store_bookfile_modify.go
// version: 1.1.0
// guid: 6d3a9e27-b4c1-4f58-8a02-e7c5d1b9f463
// last-edited: 2026-10-26

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
// fn's result through the same commit UpdateBookFile uses (updateBookFileOver:
// shared merge rule, secondary-index rewrite on a path or PID change).
//
// The file stripe serialises this against every other writer that takes it
// (ModifyBookFile, UpdateBookFile, PatchBookFileFields, ...). It does NOT
// serialise against the writers that do not (the scanner's and tag backfill's
// batch upserts, a create claiming an iTunes ID, the fingerprint sweeps), so
// the commit is a compare-and-swap: under the book's owner stripe the stored
// bytes must still equal the bytes fn's input was copied from
// (commitBookFileBatch). When they do not, the row is re-read, fn is run again
// on the fresh copy, and the write is re-staged, so the concurrent writer's
// change survives and fn's is applied on top of it. After maxStageRetries lost
// races it returns an error wrapping ErrBookFileConflict (a
// *BookFileConflictError) and writes nothing.
//
// fn therefore MAY RUN MORE THAN ONCE, each time on a fresh copy of the row.
// A precondition it checks is checked against the row it is about to replace;
// a side effect outside the row would repeat. ErrSkipBookFileWrite is honoured
// on any run.
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

	type modified struct {
		row    *BookFile
		notify bool
	}
	book := bookID
	got, err := rmwBookFile(&book, fileID, func() (modified, error) {
		stored, raw, err := s.readBookFileRow(bookID, fileID)
		if err != nil {
			return modified{}, fmt.Errorf("ModifyBookFile: read %s: %w", fileID, err)
		}
		if stored == nil {
			return modified{}, nil
		}
		row := *stored
		if err := fn(&row); err != nil {
			if errors.Is(err, ErrSkipBookFileWrite) {
				return modified{row: stored}, nil
			}
			return modified{}, err
		}
		if row.ID != stored.ID || row.BookID != stored.BookID {
			return modified{}, fmt.Errorf("ModifyBookFile: callback changed the identity of %s (id %q→%q, book %q→%q)",
				fileID, stored.ID, row.ID, stored.BookID, row.BookID)
		}
		n, err := s.updateBookFileOver(stored, raw, &row, bookFileMergeReplace, true)
		if errors.Is(err, errBookFileRowVanished) {
			return modified{}, err // lost the compare-and-swap: rmwBookFile re-reads and re-runs fn
		}
		if err != nil {
			err = fmt.Errorf("ModifyBookFile: write %s: %w", fileID, err)
		}
		// A write whose fsync failed (ErrBookFileDurabilityUnknown) was still
		// applied, so the book's aggregates must follow it (bookFileApplied's
		// contract); the error is returned with it.
		return modified{row: &row, notify: n && bookFileApplied(err)}, err
	})
	notify = got.notify
	if err != nil {
		if errors.Is(err, ErrBookFileConflict) {
			err = fmt.Errorf("ModifyBookFile: write %s: %w", fileID, err)
		}
		return nil, err
	}
	return got.row, nil
}
