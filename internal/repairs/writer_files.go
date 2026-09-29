// file: internal/repairs/writer_files.go
// version: 1.0.0
// guid: 4d8a2f61-3c7e-4b19-8e05-9a1f6c3d7b28
// last-edited: 2026-09-28

package repairs

import (
	"errors"
	"fmt"
	"strconv"

	"github.com/oklog/ulid/v2"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// A fixer that moves or repoints book_file rows needs more than the metadata
// history Writer.Modify records: "undo last apply" reverts book fields only.
// Every step below is therefore also journaled as an OperationChange under
// the apply op's id, in a change type the op revert (POST
// /operations/:id/revert) reverses, so a whole apply can be undone from the
// Operations page.
//
// Each journal row is written AFTER the write it describes commits: a row
// written first would describe a write that may never land (the
// ledger-before-write bug class). A journal failure is returned wrapped in
// ErrNotJournaled: the write committed but cannot be undone from the ledger,
// and the fixer must not report the row applied.

// ErrNotJournaled wraps a write that committed but whose undo row could not
// be recorded.
var ErrNotJournaled = errors.New("repairs: write committed but its undo row was not recorded")

// ChangeJournal records operation change rows.
type ChangeJournal interface {
	CreateOperationChange(change *database.OperationChange) error
}

// BookFileWriter is the book_file write surface Writer offers a fixer. It has
// no delete method on purpose.
type BookFileWriter interface {
	ModifyBookFile(bookID, fileID string, fn func(*database.BookFile) error) (*database.BookFile, error)
	MoveBookFilesToBook(fileIDs []string, sourceBookID, targetBookID string) error
	RecomputeBookAggregates(bookID string) error
}

// WithJournal wires w to journal every step under opID and to write book_file
// rows through files. Without it the book_file methods and Journal fail.
func (w *Writer) WithJournal(files BookFileWriter, journal ChangeJournal, opID string) *Writer {
	w.files, w.journal, w.opID = files, journal, opID
	return w
}

// WithLiveness sets the function Touch calls: the op's liveness stamp, so a
// fixer writing many files inside one row keeps the watchdog fed.
func (w *Writer) WithLiveness(touch func()) *Writer {
	w.touch = touch
	return w
}

// Touch stamps the op's liveness (a no-op when none is wired).
func (w *Writer) Touch() {
	if w.touch != nil {
		w.touch()
	}
}

// Journal records one OperationChange under the apply op's id.
func (w *Writer) Journal(bookID, changeType, field, oldV, newV string) error {
	if w.journal == nil || w.opID == "" {
		return fmt.Errorf("%w: no journal wired (book %s, %s)", ErrNotJournaled, bookID, changeType)
	}
	if err := w.journal.CreateOperationChange(&database.OperationChange{
		ID: ulid.Make().String(), OperationID: w.opID, BookID: bookID,
		ChangeType: changeType, FieldName: field, OldValue: oldV, NewValue: newV,
	}); err != nil {
		w.log.Warn("%s: undo row not recorded (the write committed): book_id=%s change=%s err=%s",
			w.source, logger.SanitizeLogValue(bookID), changeType, logger.SanitizeLogValue(err.Error()))
		return fmt.Errorf("%w: %s on %s: %v", ErrNotJournaled, changeType, bookID, err)
	}
	w.journaled.Add(1)
	return nil
}

// Journaled is how many operation change rows were recorded.
func (w *Writer) Journaled() int { return int(w.journaled.Load()) }

// RepointBookFile points the book_file row fileID of bookID at the location
// to, only while the row still holds expect (compare-and-set under the row's
// lock; a mismatch returns ErrChangedSincePlan and writes nothing). Nothing on
// disk moves. Journaled as undo.ChangeTypeBookFileRepoint with both
// locations, so the revert restores path, Missing, hash and size exactly.
func (w *Writer) RepointBookFile(bookID, fileID string, expect, to undo.BookFileLocation) error {
	if w.files == nil {
		return errors.New("repairs: writer has no book_file store")
	}
	var was undo.BookFileLocation
	written, err := w.files.ModifyBookFile(bookID, fileID, func(f *database.BookFile) error {
		was = undo.LocationOf(f)
		if was != expect {
			return ErrChangedSincePlan
		}
		to.Apply(f)
		return nil
	})
	if err != nil {
		return err
	}
	if written == nil {
		return fmt.Errorf("%w: book_file %s vanished", ErrChangedSincePlan, fileID)
	}
	w.writes.Add(1)
	w.Touch()
	return w.Journal(bookID, undo.ChangeTypeBookFileRepoint, "book_file:"+fileID, was.Encode(), to.Encode())
}

// MoveBookFiles moves fileIDs from source onto target (the store refuses a
// row no longer on source) and journals one undo.ChangeTypeBookFileReassign
// row per file.
func (w *Writer) MoveBookFiles(fileIDs []string, source, target string) error {
	if w.files == nil {
		return errors.New("repairs: writer has no book_file store")
	}
	if len(fileIDs) == 0 {
		return nil
	}
	if err := w.files.MoveBookFilesToBook(fileIDs, source, target); err != nil {
		return err
	}
	w.writes.Add(1)
	var errs []error
	for _, id := range fileIDs {
		w.Touch()
		if err := w.Journal(target, undo.ChangeTypeBookFileReassign, "book_file:"+id, source, target); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// SetTrackNumber sets one row's track while it is still from, and journals
// undo.ChangeTypeBookFileTrack.
func (w *Writer) SetTrackNumber(bookID, fileID string, from, to int) error {
	if w.files == nil {
		return errors.New("repairs: writer has no book_file store")
	}
	if from == to {
		return nil
	}
	written, err := w.files.ModifyBookFile(bookID, fileID, func(f *database.BookFile) error {
		if f.TrackNumber != from {
			return ErrChangedSincePlan
		}
		f.TrackNumber = to
		return nil
	})
	if err != nil {
		return err
	}
	if written == nil {
		return fmt.Errorf("%w: book_file %s vanished", ErrChangedSincePlan, fileID)
	}
	w.writes.Add(1)
	w.Touch()
	return w.Journal(bookID, undo.ChangeTypeBookFileTrack, "book_file:"+fileID, strconv.Itoa(from), strconv.Itoa(to))
}

// Recompute recomputes a book's duration and size aggregates after its rows
// changed.
func (w *Writer) Recompute(bookID string) error {
	if w.files == nil {
		return errors.New("repairs: writer has no book_file store")
	}
	return w.files.RecomputeBookAggregates(bookID)
}
