// file: internal/books/store/pebble_store_bookfile_modify.go
// version: 1.3.0
// guid: 6d3a9e27-b4c1-4f58-8a02-e7c5d1b9f463
// last-edited: 2026-11-19

package store

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
	return s.modifyBookFileReporting(bookID, fileID, fn, modifyBookFileOpts{recompute: true})
}

// ErrBookFilePathHeld is the refusal of ModifyBookFileUnlessPathHeld when the new
// path is held, at the commit, by another row (live or retired). Nothing was
// written: taking the path's single-owner key from that row would leave two rows
// holding one file.
var ErrBookFilePathHeld = errors.New("book file path is held by another file row")

// ModifyBookFileUnlessPathHeld is ModifyBookFile that refuses to MOVE the row onto
// a path some other row holds, atomically. ModifyBookFile (and every writer of a
// row's path) writes the single-owner path key (book_file_path:<crc>) with a
// plain Set, so a caller that checked "is anyone at this path?" before the write
// could be raced by a row created at the path in between, and the repoint would
// take the key from it. Here the check is the commit's verify hook: it runs with
// the owner stripes and the single-owner index mutex held (lockSingleOwnerIndex),
// which every other writer of a path key takes around its own apply, so no row
// can take the path between the check and the write. A holder is any row whose
// path key names the path and which is at the path, retired or not, other than
// this row itself.
//
// The check runs only when fn changes the row's path, on every attempt (a
// compare-and-swap retry re-runs fn and the check). A refusal returns an error
// wrapping ErrBookFilePathHeld and writes nothing. Everything else is
// ModifyBookFile: fn may run more than once, ErrSkipBookFileWrite writes nothing,
// and (nil, nil) means the row does not exist.
//
// Like ModifyBookFileDeferAggregates it is deliberately NOT a Store method
// (TestStoreFlattenedWidth lets Store only narrow); a consumer declares it as its
// own interface and resolves it with AsCapability. Its consumer is
// reconcile.apply's row repoint. repairs.Writer.RepointBookFile deliberately does
// not use it: it hands a path key back from a retired holder on purpose (the
// KeyOwner rows), which this refuses.
func (s *PebbleStore) ModifyBookFileUnlessPathHeld(bookID, fileID string, fn func(*BookFile) error) (*BookFile, error) {
	return s.modifyBookFileReporting(bookID, fileID, fn, modifyBookFileOpts{recompute: true, unlessPathHeld: true})
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
	return s.modifyBookFileReporting(bookID, fileID, fn, modifyBookFileOpts{})
}

// ErrBookAggregatesStale is the error of ModifyBookFileReportingRecompute when
// the row WAS written but the recompute of the book's aggregates that follows
// it failed: the book's stored Duration and FileSize may not read the row. The
// returned row is non-nil then, and the error wraps the recompute's own error.
var ErrBookAggregatesStale = errors.New("book file written but its book's aggregates were not refreshed")

// ModifyBookFileReportingRecompute is ModifyBookFile with the recompute's outcome
// reported: the same stripe, the same compare-and-swap loop, the same callback
// contract (fn may run more than once, ErrSkipBookFileWrite writes nothing), the
// same commit (updateBookFileOver) and the same RecomputeBookAggregates after the
// unlock, in the same cases (a write that changed what the book's aggregates
// read, including a write whose fsync was in doubt). Only the reporting differs:
// ModifyBookFile logs a failed recompute (notifyBookFileChange) and returns nil,
// where this returns it, so the caller that counts or fails on a recompute (the
// duration-backfill unknownRows run, whose last row of a book is one write that
// recomputes) can tell a recomputed book from one that is not.
//
// When the row was written and the recompute failed the result is (the written
// row, an error wrapping ErrBookAggregatesStale and the recompute's error): the
// row is non-nil because it WAS written. A write that failed, was skipped or
// found no row returns exactly what ModifyBookFile returns. A write whose fsync
// was in doubt and whose recompute also failed returns both errors (errors.Is
// matches either) and a nil row, as ModifyBookFile does for the fsync error.
//
// Like ModifyBookFileDeferAggregates it is deliberately NOT a Store method
// (TestStoreFlattenedWidth lets Store only narrow); a consumer declares it as its
// own interface and resolves it with AsCapability.
func (s *PebbleStore) ModifyBookFileReportingRecompute(bookID, fileID string, fn func(*BookFile) error) (*BookFile, error) {
	return s.modifyBookFileReporting(bookID, fileID, fn, modifyBookFileOpts{recompute: true, report: true})
}

// modifyBookFileOpts says how modifyBookFileReporting writes: recompute whether
// the write refreshes the book's aggregates, report whether a failed refresh is
// returned (ErrBookAggregatesStale) rather than logged, unlessPathHeld whether a
// move to a path another row holds is refused at the commit (ErrBookFilePathHeld).
type modifyBookFileOpts struct {
	recompute, report, unlessPathHeld bool
}

// modifyBookFileReporting is the one body of every ModifyBookFile variant;
// opts says how it writes (modifyBookFileOpts).
func (s *PebbleStore) modifyBookFileReporting(bookID, fileID string, fn func(*BookFile) error, opts modifyBookFileOpts) (out *BookFile, outErr error) {
	recompute, report := opts.recompute, opts.report
	type modified struct {
		row    *BookFile
		notify bool
	}
	unlock := s.lockBookFile(fileID)
	notify := false
	defer func() {
		unlock()
		// After the unlock, as in updateBookFile: the aggregate recompute
		// takes the book's stripe, and no file stripe is held while one is.
		if !notify {
			return
		}
		if !report {
			s.notifyBookFileChange(bookID)
			return
		}
		if rcErr := s.RecomputeBookAggregates(bookID); rcErr != nil {
			outErr = errors.Join(outErr, fmt.Errorf("%w: book %s: %w", ErrBookAggregatesStale, bookID, rcErr))
		}
	}()

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
		// The path is judged at the commit, under the index mutex, so only the
		// intent is captured here (updateBookFileOver merges the row).
		var verify func() error
		if opts.unlessPathHeld && row.FilePath != "" && row.FilePath != stored.FilePath {
			newPath := row.FilePath
			verify = func() error { return s.requirePathNotHeld(newPath, fileID) }
		}
		n, err := s.updateBookFileOver(stored, raw, &row, bookFileMergeReplace, recompute, verify)
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
