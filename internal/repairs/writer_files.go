// file: internal/repairs/writer_files.go
// version: 1.9.0
// guid: 4d8a2f61-3c7e-4b19-8e05-9a1f6c3d7b28
// last-edited: 2026-10-05

package repairs

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

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
// resume dedupe). CreateOperationChange stores a row under its id, so writing
// a row again with the same id replaces it: that is how JournalStep and
// LockFields void the row they just wrote when the write it describes was
// refused (Voided, RevertedAt), by id, without reading the op's journal.
type ChangeJournal interface {
	CreateOperationChange(change *database.OperationChange) error
	GetOperationChanges(operationID string) ([]*database.OperationChange, error)
}

// BookFileWriter is the book_file surface Writer offers a fixer. It has no
// delete method on purpose.
type BookFileWriter interface {
	GetBookFileByID(bookID, fileID string) (*database.BookFile, error)
	// GetBookFileByPath names the row holding a path's single-owner key:
	// RepointBookFile records it so the revert can hand the key back.
	GetBookFileByPath(filePath string) (*database.BookFile, error)
	ModifyBookFile(bookID, fileID string, fn func(*database.BookFile) error) (*database.BookFile, error)
	MoveBookFilesToBook(fileIDs []string, sourceBookID, targetBookID string) error
	RecomputeBookAggregates(bookID string) error
}

// journalIndex is the set of rows already in the op journal, loaded once,
// and the books this writer has journaled a change for (books).
type journalIndex struct {
	mu     sync.Mutex
	loaded bool
	keys   map[string]bool
	books  map[string]bool
	// created counts the rows this writer created per book, and found
	// marks a book whose rows were already in the journal (a resumed run),
	// so voiding a book's only row clears books for it again.
	created map[string]int
	found   map[string]bool
	// latest is the NewValue of the newest row per book, type and field
	// (valueKey), for JournaledValue.
	latest map[string]string
}

func valueKey(bookID, changeType, field string) string {
	return strings.Join([]string{bookID, changeType, field}, "\x00")
}

// load reads the op journal once. The caller holds ix.mu.
func (ix *journalIndex) load(w *Writer) error {
	if ix.loaded {
		return nil
	}
	rows, err := w.journal.GetOperationChanges(w.opID)
	if err != nil {
		return fmt.Errorf("%w: read the op journal: %v", ErrNotJournaled, err)
	}
	ix.keys = map[string]bool{}
	ix.books = map[string]bool{}
	ix.created = map[string]int{}
	ix.found = map[string]bool{}
	ix.latest = map[string]string{}
	for _, r := range rows {
		if r.RevertedAt == nil {
			ix.keys[journalKey(r.BookID, r.ChangeType, r.FieldName, r.OldValue, r.NewValue)] = true
			ix.latest[valueKey(r.BookID, r.ChangeType, r.FieldName)] = r.NewValue
		}
	}
	ix.loaded = true
	return nil
}

// JournaledValue returns the NewValue of the newest un-reverted row of this
// op's journal for bookID, changeType and field, so a resumed step whose value
// is not deterministic (a soft-delete's stamp) journals and writes the value
// its cut-off run journaled instead of a second one.
func (w *Writer) JournaledValue(bookID, changeType, field string) (string, bool, error) {
	if w.journal == nil || w.opID == "" || w.index == nil {
		return "", false, fmt.Errorf("%w: no journal wired (book %s, %s)", ErrNotJournaled, bookID, changeType)
	}
	ix := w.index
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if err := ix.load(w); err != nil {
		return "", false, err
	}
	v, ok := ix.latest[valueKey(bookID, changeType, field)]
	return v, ok, nil
}

// journaledBook reports whether this writer journaled (or, on a resume, found
// already journaled) an operation change on bookID. Every Step journals before
// it writes, so a Modify inside a Step sees its own book here.
func (w *Writer) journaledBook(bookID string) bool {
	ix := w.index
	if ix == nil {
		return false
	}
	ix.mu.Lock()
	defer ix.mu.Unlock()
	return ix.books[bookID]
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

// Journal records one OperationChange under the apply op's id, stamped
// with the Writer's source (OperationChange.Source), unless an
// identical row (same book, type, field and values) is already in the op's
// journal: a resumed apply re-journals the step it was cut off in.
func (w *Writer) Journal(bookID, changeType, field, oldV, newV string) error {
	if err := w.denyTagsOnly("Journal"); err != nil {
		return err
	}
	_, err := w.journalRow(bookID, changeType, field, oldV, newV)
	return err
}

// journaledRow is a row journalRow created in this call; voidRow takes it
// back. A zero value (the row was already in the journal) voids nothing.
type journaledRow struct {
	row               *database.OperationChange
	id, key, valueKey string
	// prevLatest / hadLatest restore JournaledValue's answer on a void.
	prevLatest string
	hadLatest  bool
}

func (w *Writer) journalRow(bookID, changeType, field, oldV, newV string) (journaledRow, error) {
	if w.journal == nil || w.opID == "" || w.index == nil {
		return journaledRow{}, fmt.Errorf("%w: no journal wired (book %s, %s)", ErrNotJournaled, bookID, changeType)
	}
	// Every journaled write (Step, the book_file methods, credits) journals
	// first, so beating here renews the lease before each of them.
	if err := w.beat(changeType + " on book " + bookID); err != nil {
		return journaledRow{}, err
	}
	key := journalKey(bookID, changeType, field, oldV, newV)
	ix := w.index
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if err := ix.load(w); err != nil {
		return journaledRow{}, err
	}
	if ix.keys[key] {
		ix.books[bookID] = true
		ix.found[bookID] = true
		return journaledRow{}, nil
	}
	id := ulid.Make().String()
	change := &database.OperationChange{
		ID: id, OperationID: w.opID, BookID: bookID,
		ChangeType: changeType, FieldName: field, OldValue: oldV, NewValue: newV,
		// The fixer's id rides on the row itself: the op row can be
		// discarded (registry.Discard) while this row stays, and a fixer
		// telling its own earlier writes from another's still can.
		Source: w.source,
	}
	if err := w.journal.CreateOperationChange(change); err != nil {
		w.log.Warn("%s: undo row not recorded; the write is not made: book_id=%s change=%s err=%s",
			w.source, logger.SanitizeLogValue(bookID), changeType, logger.SanitizeLogValue(err.Error()))
		return journaledRow{}, fmt.Errorf("%w: %s on %s: %v", ErrNotJournaled, changeType, bookID, err)
	}
	vk := valueKey(bookID, changeType, field)
	row := journaledRow{row: change, id: id, key: key, valueKey: vk}
	row.prevLatest, row.hadLatest = ix.latest[vk]
	ix.keys[key] = true
	ix.books[bookID] = true
	ix.created[bookID]++
	ix.latest[vk] = newV
	w.journaled.Add(1)
	return row, nil
}

// voidRow marks a row this writer just created reverted, because the write
// it describes was refused and never made: left in place, the op revert would
// "restore" a change that never happened, over whatever the field holds then
// (another operation's write included). A row that was already in the journal
// (a resumed run) is left alone.
func (w *Writer) voidRow(row journaledRow) error {
	if row.id == "" {
		return nil
	}
	voided := *row.row
	now := time.Now()
	voided.RevertedAt, voided.Voided = &now, true
	if err := w.journal.CreateOperationChange(&voided); err != nil {
		return fmt.Errorf("void undo row %s of a refused write: %w", row.id, err)
	}
	ix := w.index
	ix.mu.Lock()
	defer ix.mu.Unlock()
	delete(ix.keys, row.key)
	// A book whose only row was voided has nothing journaled: its history
	// batches are undone field by field again, not marked apply_op_journaled.
	b := row.row.BookID
	if ix.created[b]--; ix.created[b] <= 0 && !ix.found[b] {
		delete(ix.books, b)
	}
	if row.hadLatest {
		ix.latest[row.valueKey] = row.prevLatest
	} else {
		delete(ix.latest, row.valueKey)
	}
	w.journaled.Add(-1)
	return nil
}

// JournalStep journals e and then makes the write it describes, like Step,
// except that a failed write voids the row it journaled. write must return an
// error only when it wrote nothing (a ModifyBook whose callback refused, or
// whose commit failed); a step that can fail after writing reports that some
// other way. JOURNAL FIRST still holds, with its exposure: a crash
// between the row and the write leaves a live row for a write that never
// happened. Its revert compare-and-sets against the row's NewValue, so if a
// later operation writes that same value, reverting THIS operation puts the
// old value back over the later one's write. Voiding covers refusals only;
// the crash window stays.
func (w *Writer) JournalStep(bookID string, e UndoEntry, write func() error) error {
	if err := w.denyTagsOnly("JournalStep"); err != nil {
		return err
	}
	return w.journalStep(bookID, e, write)
}

// journalStep is JournalStep without the tags-only refusal: the tag
// primitive (AddBookTag) journals through it.
func (w *Writer) journalStep(bookID string, e UndoEntry, write func() error) error {
	row, err := w.journalRow(bookID, e.ChangeType, e.Field, e.Old, e.New)
	if err != nil {
		return err
	}
	w.Touch()
	werr := write()
	if werr != nil {
		if verr := w.voidRow(row); verr != nil {
			return errors.Join(werr, verr)
		}
	}
	return werr
}

// Step journals one change and then makes it (write). write's error is
// returned as is; the journal row stands (see JOURNAL FIRST).
func (w *Writer) Step(bookID, changeType, field, oldV, newV string, write func() error) error {
	if err := w.denyTagsOnly("Step"); err != nil {
		return err
	}
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
// write is then a compare-and-set against the location journaled. The
// journaled after-location also names the row that held to.Path's
// single-owner path key (KeyOwnerBook/KeyOwnerRow), read before the write:
// the revert hands the key back to that row and no other.
func (w *Writer) RepointBookFile(bookID, fileID string, expect, to undo.BookFileLocation) error {
	if err := w.denyTagsOnly("RepointBookFile"); err != nil {
		return err
	}
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
	to.KeyOwnerBook, to.KeyOwnerRow = "", ""
	owner, err := w.files.GetBookFileByPath(to.Path)
	if err != nil {
		return fmt.Errorf("read the row at %s: %w", to.Path, err)
	}
	if owner != nil && owner.ID != fileID {
		to.KeyOwnerBook, to.KeyOwnerRow = owner.BookID, owner.ID
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
	if err := w.denyTagsOnly("MoveBookFiles"); err != nil {
		return err
	}
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
	if err := w.denyTagsOnly("SetTrackNumber"); err != nil {
		return err
	}
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
	if err := w.denyTagsOnly("Recompute"); err != nil {
		return err
	}
	if w.files == nil {
		return errors.New("repairs: writer has no book_file store")
	}
	if err := w.beat("aggregates of book " + bookID); err != nil {
		return err
	}
	return w.files.RecomputeBookAggregates(bookID)
}
