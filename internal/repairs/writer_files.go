// file: internal/repairs/writer_files.go
// version: 1.21.0
// guid: 4d8a2f61-3c7e-4b19-8e05-9a1f6c3d7b28
// last-edited: 2026-10-30

package repairs

import (
	"errors"
	"fmt"
	"slices"
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

// ErrUnclassifiedChangeType: a fixer asked the Writer to journal a change type
// the journal's classification table (database.ClassifyChangeType) does not
// know, so nobody has decided whether its rows carry one user's data. It is
// database.ErrUnclassifiedChangeType, the error every journal writer gets from
// database.RecordOperationChange, so one errors.Is matches all of them. The
// write is not made, so it also matches ErrNotJournaled. Classify the type in
// internal/database/change_type_class.go.
var ErrUnclassifiedChangeType = database.ErrUnclassifiedChangeType

// checkChangeTypeClassified refuses a change type the journal does not
// classify. Every journaled write reaches the journal through journalRow or
// journalRows, and both ask it first, before the lease beat and before anything
// is written; it is database.CheckChangeTypeClassified, the one check, wrapped
// in ErrNotJournaled.
func checkChangeTypeClassified(changeType string) error {
	if err := database.CheckChangeTypeClassified(changeType); err != nil {
		return fmt.Errorf("%w: %w", ErrNotJournaled, err)
	}
	return nil
}

// ChangeJournal records operation change rows and lists an op's rows (for the
// resume dedupe). CreateOperationChange stores a row under its id, so writing
// a row again with the same id replaces it: that is how JournalStep and
// LockFields void the row they just wrote when the write it describes was
// refused (Voided, RevertedAt), by id, without reading the op's journal. A call
// that journals several rows at once (JournalSteps) also needs the journal's
// ChangeBatchJournal.
type ChangeJournal interface {
	CreateOperationChange(change *database.OperationChange) error
	GetOperationChanges(operationID string) ([]*database.OperationChange, error)
}

// ChangeReverter is the optional journal capability SupersedeChanges needs:
// stamping RevertedAt on exactly the listed rows of an operation
// (database.OperationChangeStore.MarkOperationChangesReverted). It is asked
// for by type assertion on the ChangeJournal, so ChangeJournal does not grow.
type ChangeReverter interface {
	MarkOperationChangesReverted(operationID string, changeIDs []string) error
}

// ErrSupersedeUnsupported: the writer's journal cannot mark another
// operation's rows (it is not a ChangeReverter), so nothing was marked.
var ErrSupersedeUnsupported = errors.New("repairs: the op journal cannot mark another operation's rows")

// BookFileWriter is the book_file surface Writer offers a fixer. It has no
// delete method on purpose.
type BookFileWriter interface {
	GetBookFileByID(bookID, fileID string) (*database.BookFile, error)
	// GetBookFileByPath names the row holding a path's single-owner key:
	// RepointBookFile records it so the revert can hand the key back. The
	// key's holder is read with database.BookFileByPathIncludingRetired, so a
	// retired row that holds the key is journaled as its owner instead of
	// being invisible. That helper resolves database.RetiredBookFileReader
	// through decorators; a store that does not resolve to it (this method is
	// no fallback) makes RepointBookFile fail with
	// database.ErrRetiredReaderUnavailable rather than read a retired row's
	// path as free.
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
	if err := checkChangeTypeClassified(changeType); err != nil {
		return journaledRow{}, err
	}
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
	if err := database.RecordOperationChange(w.journal, change); err != nil {
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

// journalRows is journalRow for the several steps of one JournalSteps call: every
// row that is not already in the op journal is created in ONE commit (the
// journal's CreateOperationChanges, ChangeBatchJournal), so the rows of one step
// that belong to each other (a carry's survivor-side marker and the retired
// book's row) land together or not at all, and the writer's own index is updated
// only after the commit. The result is aligned with steps: a step whose row was
// already in the journal (a resumed run, or an identical step twice in one call)
// has the zero journaledRow.
//
// Two rows or more need the journal to write them in one commit. A journal that
// cannot is refused up front, before anything is written: written one by one, a
// failure part-way would leave one row of a pair without the other, which a
// revert reads as a marker standing on nothing.
func (w *Writer) journalRows(steps []BookStep) ([]journaledRow, error) {
	if w.journal == nil || w.opID == "" || w.index == nil {
		return nil, fmt.Errorf("%w: no journal wired (%d steps)", ErrNotJournaled, len(steps))
	}
	for _, st := range steps {
		if err := checkChangeTypeClassified(st.Entry.ChangeType); err != nil {
			return nil, err
		}
	}
	for _, st := range steps {
		// Every journaled write journals first, so beating here renews the lease
		// before each of them.
		if err := w.beat(st.Entry.ChangeType + " on book " + st.BookID); err != nil {
			return nil, err
		}
	}
	ix := w.index
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if err := ix.load(w); err != nil {
		return nil, err
	}
	rows := make([]journaledRow, len(steps))
	var fresh []int // indexes into steps of the rows to create
	var changes []*database.OperationChange
	pending := map[string]bool{}
	for i, st := range steps {
		key := journalKey(st.BookID, st.Entry.ChangeType, st.Entry.Field, st.Entry.Old, st.Entry.New)
		if ix.keys[key] || pending[key] {
			ix.books[st.BookID] = true
			ix.found[st.BookID] = true
			continue
		}
		pending[key] = true
		id := ulid.Make().String()
		fresh = append(fresh, i)
		changes = append(changes, &database.OperationChange{
			ID: id, OperationID: w.opID, BookID: st.BookID,
			ChangeType: st.Entry.ChangeType, FieldName: st.Entry.Field, OldValue: st.Entry.Old, NewValue: st.Entry.New,
			// The fixer's id rides on the row itself (see journalRow).
			Source: w.source,
		})
		rows[i] = journaledRow{row: changes[len(changes)-1], id: id, key: key, valueKey: valueKey(st.BookID, st.Entry.ChangeType, st.Entry.Field)}
	}
	if len(changes) == 0 {
		return rows, nil
	}
	var err error
	switch batch, ok := database.AsCapability[ChangeBatchJournal](w.journal); {
	case len(changes) == 1:
		err = database.RecordOperationChange(w.journal, changes[0])
	case !ok:
		err = errBatchJournalMissing
	default:
		err = database.RecordOperationChanges(batch, changes)
	}
	if err != nil {
		w.log.Warn("%s: undo rows not recorded; the write is not made: rows=%d err=%s",
			w.source, len(changes), logger.SanitizeLogValue(err.Error()))
		return nil, fmt.Errorf("%w: %d rows of %s: %w", ErrNotJournaled, len(changes), steps[fresh[0]].Entry.ChangeType, err)
	}
	// The commit landed: the index follows it, in step order.
	for _, i := range fresh {
		st := steps[i]
		row := &rows[i]
		row.prevLatest, row.hadLatest = ix.latest[row.valueKey]
		ix.keys[row.key] = true
		ix.books[st.BookID] = true
		ix.created[st.BookID]++
		ix.latest[row.valueKey] = st.Entry.New
		w.journaled.Add(1)
	}
	return rows, nil
}

// errBatchJournalMissing: the journal cannot write several rows in one commit.
var errBatchJournalMissing = errors.New("the op journal cannot write several rows in one commit (ChangeBatchJournal)")

// voidRow marks a row this writer just created reverted, because the write
// it describes was refused and never made: left in place, the op revert would
// "restore" a change that never happened, over whatever the field holds then
// (another operation's write included). A row that was already in the journal
// (a resumed run) is left alone.
func (w *Writer) voidRow(row journaledRow) error {
	return w.voidRows([]journaledRow{row})
}

// ChangeBatchJournal is the journal capability for writing several rows in one
// commit (database.PebbleStore CreateOperationChanges): every row lands or none
// does. JournalSteps creates the rows of a multi-row step with it, and voidRows
// takes them back with it, so the rows of one step that belong to each other (a
// carry's survivor-side marker and the retired book's row) are never seen apart.
// It is resolved with database.AsCapability on the ChangeJournal, so
// ChangeJournal does not grow; a caller that journals several rows at once
// refuses a journal without it up front (journalRows).
type ChangeBatchJournal interface {
	CreateOperationChanges(changes []*database.OperationChange) error
}

// JournalsInBatches reports whether the writer's op journal can write several
// rows in one commit (ChangeBatchJournal). A caller whose step journals rows that
// belong to each other (a retire's carry of a book's place in a list) asks it
// BEFORE its first write and refuses when it is false, as it does for a store
// without the list capabilities: JournalSteps refuses such a step anyway, but
// only after the retire has already moved other things.
func (w *Writer) JournalsInBatches() bool {
	if w.journal == nil {
		return false
	}
	_, ok := database.AsCapability[ChangeBatchJournal](w.journal)
	return ok
}

// voidRows is voidRow for the rows of one step, taken back together in one
// commit: rows that belong to each other must not be seen apart, so a failure
// leaves all of them standing, never one voided and the other standing. Several
// rows need the journal's ChangeBatchJournal (journalRows already required it to
// create them); without it nothing is voided and the error says so.
func (w *Writer) voidRows(rows []journaledRow) error {
	var voided []*database.OperationChange
	var todo []journaledRow
	now := time.Now()
	for _, row := range rows {
		if row.id == "" {
			continue
		}
		v := *row.row
		// RevertedAt is the wall time; the journal clock stamps RevertedSeq (the
		// order the decisions use) as the store writes the row.
		v.RevertedAt, v.Voided = &now, true
		voided, todo = append(voided, &v), append(todo, row)
	}
	switch {
	case len(voided) == 0:
		return nil
	case len(voided) == 1:
		if err := database.RecordOperationChange(w.journal, voided[0]); err != nil {
			return fmt.Errorf("void undo row %s of a refused write: %w", todo[0].id, err)
		}
	default:
		batch, ok := database.AsCapability[ChangeBatchJournal](w.journal)
		if !ok {
			return fmt.Errorf("void %d undo rows of a refused write: %w", len(voided), errBatchJournalMissing)
		}
		if err := database.RecordOperationChanges(batch, voided); err != nil {
			return fmt.Errorf("void undo rows of a refused write: %w", err)
		}
	}
	w.forgetVoided(todo)
	return nil
}

// forgetVoided takes rows the journal no longer holds as live out of the
// writer's own index.
func (w *Writer) forgetVoided(rows []journaledRow) {
	ix := w.index
	ix.mu.Lock()
	defer ix.mu.Unlock()
	for _, row := range rows {
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
	}
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

// BookStep is one row of JournalSteps: the book it is journaled on and the
// change it describes.
type BookStep struct {
	BookID string
	Entry  UndoEntry
}

// JournalSteps is JournalStep for one write that makes several journaled
// changes at once (a crown that demotes several books): every step is
// journaled, then write runs once and reports, per step (done[i] for
// steps[i]), whether it performed that change. Every row whose step was not
// performed -- all of them when write refused before writing, or returned
// fewer entries than steps -- is voided, so the op revert never "restores" a
// change that never happened; write's error is returned either way.
//
// All the rows of the call are created in ONE commit (journalRows,
// ChangeBatchJournal), so they exist together or not at all: a journal failure
// or a crash while journaling leaves none of them, never one half of a pair (a
// carry's survivor-side marker without the retired book's row, which a revert
// would read as a marker standing on nothing), and write is never called. Rows
// already in the journal (a resumed run) are not written again; only the missing
// ones are created. The rows of a refused write are voided in one commit too.
// A journal that cannot write several rows in one commit is refused for a call
// of two rows or more, before anything is written.
// The crash window of JournalStep stands: a crash between the rows and the
// write leaves them live.
func (w *Writer) JournalSteps(steps []BookStep, write func() (done []bool, err error)) error {
	if err := w.denyTagsOnly("JournalSteps"); err != nil {
		return err
	}
	// All the rows of the call are created in one commit, then write runs. A
	// journal failure creates none of them and never calls write.
	rows, err := w.journalRows(steps)
	if err != nil {
		return err
	}
	voidUndone := func(done []bool, cause error) error {
		errs := []error{cause}
		var undone []journaledRow
		for i, row := range slices.Backward(rows) {
			if i < len(done) && done[i] {
				continue
			}
			undone = append(undone, row)
		}
		// Together, in one commit (voidRows).
		if verr := w.voidRows(undone); verr != nil {
			errs = append(errs, verr)
		}
		return errors.Join(errs...)
	}
	w.Touch()
	done, werr := write()
	return voidUndone(done, werr)
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

// SupersedeChanges marks the listed rows of ANOTHER operation (opID, never
// this writer's own) reverted without writing anything: this writer's op has
// journaled the same changes and now owns them, so reverting opID must not
// undo them a second time, or half of them. The revert of opID counts them
// already reverted. A journal that is not a ChangeReverter returns
// ErrSupersedeUnsupported and marks nothing.
func (w *Writer) SupersedeChanges(opID string, ids []string) error {
	if err := w.denyTagsOnly("SupersedeChanges"); err != nil {
		return err
	}
	if len(ids) == 0 {
		return nil
	}
	if opID == "" || opID == w.opID {
		return fmt.Errorf("repairs: SupersedeChanges marks another operation's rows, not %q", opID)
	}
	m, ok := w.journal.(ChangeReverter)
	if !ok {
		return ErrSupersedeUnsupported
	}
	return m.MarkOperationChangesReverted(opID, ids)
}

// Journaled is how many operation change rows were recorded.
func (w *Writer) Journaled() int { return int(w.journaled.Load()) }

// RepointBookFile points the book_file row fileID of bookID at the location
// to, only while the row still names expect's path and Missing flag
// (database.SameFileLocation: a hash or size filled in since does not count as a
// change). Nothing on disk moves. Journaled first as
// database.ChangeTypeBookFileRepoint with the row's whole location before and
// after, so the revert restores path, Missing, hash and size exactly; the
// write is then a compare-and-set against the location journaled. The
// journaled after-location also names the row that held to.Path's
// single-owner path key (KeyOwnerBook/KeyOwnerRow), read before the write
// and including a retired row (a version another book holds), which owns the
// key while no live row does: the revert hands the key back to that row
// (database.RetiredPathKeyClaimer) unless a live row has the path, which
// always outranks it. A compare-and-set refusal at the write, or the store
// giving up after concurrent writers kept changing the row
// (database.ErrBookFileConflict), voids the row it journaled.
func (w *Writer) RepointBookFile(bookID, fileID string, expect, to database.BookFileLocation) error {
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
	was := database.BookFileLocationOf(cur)
	if !database.SameFileLocation(was, expect) {
		if was.Path != expect.Path {
			return fmt.Errorf("%w: book_file %s is at %q, not %q", ErrChangedSincePlan, fileID, was.Path, expect.Path)
		}
		return fmt.Errorf("%w: book_file %s at %q is marked missing=%t, not %t",
			ErrChangedSincePlan, fileID, was.Path, was.Missing, expect.Missing)
	}
	to.KeyOwnerBook, to.KeyOwnerRow = "", ""
	owner, err := database.BookFileByPathIncludingRetired(w.files, to.Path)
	if err != nil {
		return fmt.Errorf("read the row at %s: %w", to.Path, err)
	}
	if owner != nil && owner.ID != fileID {
		to.KeyOwnerBook, to.KeyOwnerRow = owner.BookID, owner.ID
	}
	row, err := w.journalRow(bookID, database.ChangeTypeBookFileRepoint, "book_file:"+fileID, was.Encode(), to.Encode())
	if err != nil {
		return err
	}
	w.Touch()
	written, werr := w.files.ModifyBookFile(bookID, fileID, func(f *database.BookFile) error {
		if database.BookFileLocationOf(f) != was {
			return fmt.Errorf("%w: book_file %s changed between the read and the write", ErrChangedSincePlan, fileID)
		}
		to.Apply(f)
		return nil
	})
	if werr == nil && written == nil {
		werr = fmt.Errorf("%w: book_file %s vanished", ErrChangedSincePlan, fileID)
	}
	if werr != nil {
		// A compare-and-set refusal (or a vanished row) wrote nothing: void
		// the row, so no revert or later reader takes it for a repoint that
		// happened. That includes the store giving up on the row after other
		// writers kept changing it (database.ErrBookFileConflict: its retry
		// budget ran out before any commit landed). Any other error stands
		// with its row (JOURNAL FIRST): a commit whose durability is unknown
		// (database.ErrBookFileDurabilityUnknown) may have been applied.
		if errors.Is(werr, ErrChangedSincePlan) || errors.Is(werr, database.ErrBookFileConflict) {
			if verr := w.voidRow(row); verr != nil {
				return errors.Join(werr, verr)
			}
		}
		return werr
	}
	w.writes.Add(1)
	return nil
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

// ClearBookFileITunesPath clears the itunes_path of book_file row fileID of
// bookID, only while it still holds expect (the planned value). Nothing else
// on the row changes; nothing on disk or in iTunes is touched. Journaled
// first as undo.ChangeTypeITunesPathClear ("book_file:<id>", expect -> ""),
// and voided when the compare-and-set refuses, so the op revert restores
// exactly the writes made.
func (w *Writer) ClearBookFileITunesPath(bookID, fileID, expect string) error {
	if err := w.denyTagsOnly("ClearBookFileITunesPath"); err != nil {
		return err
	}
	if w.files == nil {
		return errors.New("repairs: writer has no book_file store")
	}
	if expect == "" {
		return fmt.Errorf("repairs: book_file %s: no iTunes path to clear", fileID)
	}
	e := UndoEntry{ChangeType: undo.ChangeTypeITunesPathClear, Field: "book_file:" + fileID, Old: expect, New: ""}
	return w.JournalStep(bookID, e, func() error {
		written, err := w.files.ModifyBookFile(bookID, fileID, func(f *database.BookFile) error {
			if f.ITunesPath != expect {
				return fmt.Errorf("%w: book_file %s iTunes path is %q, not the planned %q", ErrChangedSincePlan, fileID, f.ITunesPath, expect)
			}
			f.ITunesPath = ""
			return nil
		})
		if err != nil {
			return err
		}
		if written == nil {
			return fmt.Errorf("%w: book_file %s is no longer on book %s", ErrChangedSincePlan, fileID, bookID)
		}
		w.writes.Add(1)
		return nil
	})
}

// ClearBookITunesPath clears book bookID's own itunes_path, only while it
// still holds expect. Journaled first as undo.ChangeTypeITunesPathClear
// (undo.ITunesPathBookField, expect -> ""), voided on a refusal.
func (w *Writer) ClearBookITunesPath(bookID, expect string) error {
	if err := w.denyTagsOnly("ClearBookITunesPath"); err != nil {
		return err
	}
	if expect == "" {
		return fmt.Errorf("repairs: book %s: no iTunes path to clear", bookID)
	}
	e := UndoEntry{ChangeType: undo.ChangeTypeITunesPathClear, Field: undo.ITunesPathBookField, Old: expect, New: ""}
	return w.JournalStep(bookID, e, func() error {
		_, err := w.Modify(bookID, func(b *database.Book) error {
			cur := ""
			//lint:ignore SA1019 clears the legacy book.itunes_path column (journaled so the repair can be undone); it must touch that exact column
			if b.ITunesPath != nil {
				//lint:ignore SA1019 clears the legacy book.itunes_path column (journaled so the repair can be undone); it must touch that exact column
				cur = *b.ITunesPath
			}
			if cur != expect {
				return fmt.Errorf("%w: book %s iTunes path is %q, not the planned %q", ErrChangedSincePlan, bookID, cur, expect)
			}
			//lint:ignore SA1019 clears the legacy book.itunes_path column (journaled so the repair can be undone); it must touch that exact column
			b.ITunesPath = nil
			return nil
		})
		return err
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
