// file: internal/database/pebble_store_bookfile_modify.go
// version: 1.2.0
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
// serialise against the writers that do not (a create claiming an iTunes ID,
// the bulk moves, the deletes, the fingerprint sweeps, the scan-cache stamp), so
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
	return s.modifyBookFile(bookID, fileID, fn, true)
}

// ModifyBookFileDeferAggregates is ModifyBookFile without the per-write
// RecomputeBookAggregates: the row is read and written under the same stripe and
// through the same compare-and-swap loop (modifyBookFile; fn may run more than
// once and a row that keeps changing returns ErrBookFileConflict, exactly as for
// ModifyBookFile), but the book's stored aggregates are NOT refreshed. The caller
// owns that and must call RecomputeBookAggregates(bookID) once after its batch of
// writes to the book. It exists for a loop that writes many rows of one book (a
// 400-chapter book written row by row would otherwise decode every chapter row
// 400 times, once per write, for 160k row decodes) and can pay the recompute once.
//
// It is deliberately NOT a Store method (TestStoreFlattenedWidth lets Store only
// narrow). A consumer declares the one method as its own interface and resolves it
// with AsCapability (backfill-file-hashes: hbDeferredStore).
func (s *PebbleStore) ModifyBookFileDeferAggregates(bookID, fileID string, fn func(*BookFile) error) (*BookFile, error) {
	return s.modifyBookFile(bookID, fileID, fn, false)
}

func (s *PebbleStore) modifyBookFile(bookID, fileID string, fn func(*BookFile) error, recompute bool) (*BookFile, error) {
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
		n, err := s.updateBookFileOver(stored, raw, &row, bookFileMergeReplace, recompute)
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
	// A deferred write never notifies: its caller owns the one recompute after
	// its batch (ModifyBookFileDeferAggregates).
	notify = recompute && got.notify
	if err != nil {
		if errors.Is(err, ErrBookFileConflict) {
			err = fmt.Errorf("ModifyBookFile: write %s: %w", fileID, err)
		}
		return nil, err
	}
	return got.row, nil
}
