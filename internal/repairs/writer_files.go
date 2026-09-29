// file: internal/repairs/writer_files.go
// version: 1.1.0
// guid: 4d8a2f61-3c7e-4b19-8e05-9a1f6c3d7b28
// last-edited: 2026-09-28

package repairs

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/oklog/ulid/v2"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// A fixer that moves or repoints book_file rows, or retires books, needs more
// than the metadata history Writer.Modify records: "undo last apply" reverts
// book fields only (and refuses a Repairs batch, see recordHistory). Every step
// below is therefore journaled as an OperationChange under the apply op's id,
// in a change type the op revert (POST /operations/:id/revert) reverses, so a
// whole apply is undone from the Operations page.
//
// JOURNAL FIRST. Each row is written BEFORE the write it describes. A row
// written after could be lost to a crash between the two, leaving a committed
// write nothing can undo. Written first, the crash leaves a row whose write
// may not have happened; the resumed apply (repairs.apply is ResumeRestart and
// replays the same op id) finds its own row already in the op journal and
// does not write it again (Journal dedupes), then makes the write. A row whose
// write never lands -- the compare-and-set found a change, or the op was
// abandoned rather than resumed -- describes a value the book does not hold,
// and every revert this package's change types reach is a compare-and-set
// against NewValue, so such a row is REFUSED (loudly, in the revert result
// and the preflight), never applied over the current value.
//
// A journal failure returns ErrNotJournaled and the write is not made.

// ErrNotJournaled: the undo row could not be recorded, so the write it would
// describe was not made.
var ErrNotJournaled = errors.New("repairs: undo row not recorded; the write was not made")

// ChangeJournal records operation change rows and lists an op's rows (for the
// resume dedupe).
type ChangeJournal interface {
	CreateOperationChange(change *database.OperationChange) error
	GetOperationChanges(operationID string) ([]*database.OperationChange, error)
}

// BookFileWriter is the book_file surface Writer offers a fixer. It has no
// delete method on purpose.
type BookFileWriter interface {
	GetBookFileByID(bookID, fileID string) (*database.BookFile, error)
	ModifyBookFile(bookID, fileID string, fn func(*database.BookFile) error) (*database.BookFile, error)
	MoveBookFilesToBook(fileIDs []string, sourceBookID, targetBookID string) error
	RecomputeBookAggregates(bookID string) error
}

// journalIndex is the set of rows already in the op journal, loaded once.
type journalIndex struct {
	mu     sync.Mutex
	loaded bool
	keys   map[string]bool
}

func journalKey(bookID, changeType, field, oldV, newV string) string {
	return strings.Join([]string{bookID, changeType, field, oldV, newV}, "\x00")
}

// WithJournal wires w to journal every step under opID and to write book_file
// rows through files. Without it the book_file methods and Journal fail.
func (w *Writer) WithJournal(files BookFileWriter, journal ChangeJournal, opID string) *Writer {
	w.files, w.journal, w.opID = files, journal, opID
	w.index = &journalIndex{}
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

// Journal records one OperationChange under the apply op's id, unless an
// identical row (same book, type, field and values) is already in the op's
// journal: a resumed apply re-journals the step it was cut off in.
func (w *Writer) Journal(bookID, changeType, field, oldV, newV string) error {
	if w.journal == nil || w.opID == "" || w.index == nil {
		return fmt.Errorf("%w: no journal wired (book %s, %s)", ErrNotJournaled, bookID, changeType)
	}
	key := journalKey(bookID, changeType, field, oldV, newV)
	ix := w.index
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if !ix.loaded {
		rows, err := w.journal.GetOperationChanges(w.opID)
		if err != nil {
			return fmt.Errorf("%w: read the op journal: %v", ErrNotJournaled, err)
		}
		ix.keys = map[string]bool{}
		for _, r := range rows {
			if r.RevertedAt == nil {
				ix.keys[journalKey(r.BookID, r.ChangeType, r.FieldName, r.OldValue, r.NewValue)] = true
			}
		}
		ix.loaded = true
	}
	if ix.keys[key] {
		return nil
	}
	if err := w.journal.CreateOperationChange(&database.OperationChange{
		ID: ulid.Make().String(), OperationID: w.opID, BookID: bookID,
		ChangeType: changeType, FieldName: field, OldValue: oldV, NewValue: newV,
	}); err != nil {
		w.log.Warn("%s: undo row not recorded; the write is not made: book_id=%s change=%s err=%s",
			w.source, logger.SanitizeLogValue(bookID), changeType, logger.SanitizeLogValue(err.Error()))
		return fmt.Errorf("%w: %s on %s: %v", ErrNotJournaled, changeType, bookID, err)
	}
	ix.keys[key] = true
	w.journaled.Add(1)
	return nil
}

// Step journals one change and then makes it (write). write's error is
// returned as is; the journal row stands (see JOURNAL FIRST).
func (w *Writer) Step(bookID, changeType, field, oldV, newV string, write func() error) error {
	if err := w.Journal(bookID, changeType, field, oldV, newV); err != nil {
		return err
	}
	w.Touch()
	return write()
}

// Journaled is how many operation change rows were recorded.
func (w *Writer) Journaled() int { return int(w.journaled.Load()) }

// RepointBookFile points the book_file row fileID of bookID at the location
// to, only while the row still names expect's path and Missing flag
// (undo.SameFileLocation: a hash or size filled in since does not count as a
// change). Nothing on disk moves. Journaled first as
// undo.ChangeTypeBookFileRepoint with the row's whole location before and
// after, so the revert restores path, Missing, hash and size exactly; the
// write is then a compare-and-set against the location journaled.
func (w *Writer) RepointBookFile(bookID, fileID string, expect, to undo.BookFileLocation) error {
	if w.files == nil {
		return errors.New("repairs: writer has no book_file store")
	}
	cur, err := w.files.GetBookFileByID(bookID, fileID)
	if err != nil {
		return fmt.Errorf("read book_file %s: %w", fileID, err)
	}
	if cur == nil {
		return fmt.Errorf("%w: book_file %s is no longer on book %s", ErrChangedSincePlan, fileID, bookID)
	}
	was := undo.LocationOf(cur)
	if !undo.SameFileLocation(was, expect) {
		return fmt.Errorf("%w: book_file %s is at %q, not %q", ErrChangedSincePlan, fileID, was.Path, expect.Path)
	}
	return w.Step(bookID, undo.ChangeTypeBookFileRepoint, "book_file:"+fileID, was.Encode(), to.Encode(), func() error {
		written, err := w.files.ModifyBookFile(bookID, fileID, func(f *database.BookFile) error {
			if undo.LocationOf(f) != was {
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
		return nil
	})
}

// MoveBookFiles journals one undo.ChangeTypeBookFileReassign row per file and
// then moves fileIDs from source onto target (the store refuses a row no
// longer on source).
func (w *Writer) MoveBookFiles(fileIDs []string, source, target string) error {
	if w.files == nil {
		return errors.New("repairs: writer has no book_file store")
	}
	if len(fileIDs) == 0 {
		return nil
	}
	for _, id := range fileIDs {
		if err := w.Journal(target, undo.ChangeTypeBookFileReassign, "book_file:"+id, source, target); err != nil {
			return err
		}
	}
	w.Touch()
	if err := w.files.MoveBookFilesToBook(fileIDs, source, target); err != nil {
		return err
	}
	w.writes.Add(1)
	return nil
}

// SetTrackNumber journals undo.ChangeTypeBookFileTrack and then sets one
// row's track while it is still from.
func (w *Writer) SetTrackNumber(bookID, fileID string, from, to int) error {
	if w.files == nil {
		return errors.New("repairs: writer has no book_file store")
	}
	if from == to {
		return nil
	}
	return w.Step(bookID, undo.ChangeTypeBookFileTrack, "book_file:"+fileID, strconv.Itoa(from), strconv.Itoa(to), func() error {
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
		return nil
	})
}

// Recompute recomputes a book's duration and size aggregates after its rows
// changed.
func (w *Writer) Recompute(bookID string) error {
	if w.files == nil {
		return errors.New("repairs: writer has no book_file store")
	}
	return w.files.RecomputeBookAggregates(bookID)
}
