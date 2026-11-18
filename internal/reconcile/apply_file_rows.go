// file: internal/reconcile/apply_file_rows.go
// version: 1.1.0
// guid: 079ffa9c-f1f8-4388-a615-f9875fd5419f
// last-edited: 2026-11-18

package reconcile

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/oklog/ulid/v2"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// The file-row half of ExecuteReconcile.
//
// A confirmed match points a book at the file the scan found for it. The book
// row's file_path is only one of the places that names the file: the book's
// book_file row does too, and every reader that goes through the rows (the
// player, the Files tab, the missing-file flag, the single-owner path index)
// reads THAT. Until 2026-11-18 the apply wrote the book row alone, so a
// repointed book kept a row at the old path, still flagged Missing, and the
// matched file stayed an "untracked" file as far as the path index could tell.
// The row is now repointed in the same apply, never deleted: its path, Missing
// flag and size are the matched file's, and its hash is cleared (see
// fileRowMove.to).

// errFileRowChanged is a file row that no longer is what the apply read: moved
// to another path, flagged differently, or gone.
var errFileRowChanged = errors.New("file row changed since it was read")

// errBookRepointed is the book write's own refusal: another writer set the
// book's path after the plan read it. The callback saw that under the book's
// write lock and returned before writing, so nothing of this match landed.
var errBookRepointed = errors.New("book path changed since it was read")

// fileRowStore is what repointing a file row needs beyond Store. Store does not
// carry it (Store only narrows, TestStoreFlattenedWidth); ExecuteReconcile
// resolves it with database.AsCapability and refuses a match that needs a row
// move when the store cannot do one, instead of moving the book alone.
type fileRowStore interface {
	database.BookFilePathLookup
	ModifyBookFile(bookID, fileID string, fn func(*database.BookFile) error) (*database.BookFile, error)
}

// fileRowMove is the one book_file row a match repoints: bookID's row fileID,
// from location was to location to.
type fileRowMove struct {
	rows           fileRowStore
	bookID, fileID string
	was, to        database.BookFileLocation
}

// applyPlan is what planFileRow decided for one match besides the book: move is
// the row to repoint (nil when none needs to), and rowAtNew is the row already at
// the matched file (the stranded state a cut-off earlier apply leaves).
type applyPlan struct {
	move     *fileRowMove
	rowAtNew *database.BookFile
}

// planFileRow decides, with no write, what the match moves besides the book. No
// row needs to move when the book has no file rows (the match repoints the book
// alone, as it always did), or when its row is already at newPath (a cut-off
// earlier apply, or a file the organizer moved, wrote the row and not the book;
// rowAtNew is then set). Otherwise plan.move is the row to repoint.
//
// A book with rows is repointed only when exactly one of them is at the book's
// current path and none is at newPath. Any other shape (several rows at the old
// path, rows that are neither there nor at newPath, a row at both) is refused:
// repointing "the" row would be a guess, and the book alone must not move while
// its rows say something else. The row's own book must be the only one holding
// newPath: a row of another book there, or a retired row (a claim on the file
// whoever holds it, database.RetiredClaimOnPath), refuses the match, because
// the repoint writes the single-owner path index. The holder lookup is the
// store's write-path lookup (database.BookFileRowsAtPathForWrite): once a retired
// version exists, or when the store cannot say, a lookup that cannot be complete
// refuses too, wrapping database.ErrOwnershipLookupIncomplete. While the store
// can prove no retired version exists, the lookup falls back to the single-owner
// path index when memdb is not serving, which can report fewer holders than
// exist. The check also runs once, at plan time: a row another writer gives the
// path afterwards is not seen (TODO RECONCILE-HOLDER-CHECK-AT-WRITE).
func planFileRow(store Store, book *database.Book, newPath string, newSize int64) (*applyPlan, error) {
	files, err := store.GetBookFiles(book.ID)
	if err != nil {
		return nil, fmt.Errorf("read its file rows: %w", err)
	}
	if len(files) == 0 {
		return &applyPlan{}, nil
	}
	oldPath := book.FilePath
	var atOld, atNew []database.BookFile
	for _, f := range files {
		if f.FilePath == newPath {
			atNew = append(atNew, f)
		}
		if f.FilePath == oldPath && oldPath != newPath {
			atOld = append(atOld, f)
		}
	}
	var move *fileRowMove
	var rowAtNew *database.BookFile
	switch {
	case len(atNew) == 1 && len(atOld) == 0:
		// The row is where the book is going.
		rowAtNew = &atNew[0]
	case len(atOld) == 1 && len(atNew) == 0:
		row := atOld[0]
		was := database.BookFileLocationOf(&row)
		move = &fileRowMove{
			bookID: book.ID, fileID: row.ID, was: was,
			// The hash is cleared, not carried: ReconcileApplyItem does not say
			// how the file was matched, so nothing proves the matched file holds
			// the content the row's hash names (the Missing files fixer keeps
			// it only for a "hash" match, and clears it otherwise for the
			// backfill-file-hashes job to refill). An empty hash is "unknown"; a
			// carried one could be a wrong claim dedup's exact-hash collectors
			// pair on.
			to: database.BookFileLocation{Path: newPath, Missing: false, Size: newSize},
		}
	default:
		return nil, fmt.Errorf("it has %d file rows, %d at its path %q and %d at %q, not exactly one row to repoint",
			len(files), len(atOld), oldPath, len(atNew), newPath)
	}
	rows, ok := database.AsCapability[fileRowStore](store)
	if !ok {
		return nil, fmt.Errorf("the store cannot look up or rewrite file rows, so the file row of %q is not repointed and the book is left alone", oldPath)
	}
	holders, err := database.BookFileRowsAtPathForWrite(rows, newPath)
	if err != nil {
		return nil, fmt.Errorf("cannot tell who holds %s: %w", newPath, err)
	}
	for _, h := range holders {
		if h.BookID != book.ID {
			return nil, fmt.Errorf("%s is held by a file row of book %s", newPath, h.BookID)
		}
	}
	if move != nil {
		if r := database.RetiredClaimOnPath(holders); r != nil {
			return nil, fmt.Errorf("%s is claimed by a retired file row (%s) of book %s", newPath, r.ID, r.BookID)
		}
		move.rows = rows
	}
	return &applyPlan{move: move, rowAtNew: rowAtNew}, nil
}

// journalRow is the undo row for the move: the row's location before and after,
// in the change type the operation revert restores
// (database.ChangeTypeBookFileRepoint), so reverting the operation puts the row
// back with the book. It is recorded before the write, as the book's own change
// is. A write that then does not land leaves a row the revert reads as already
// restored (undo.CheckRepointCurrent: the row is still at OldValue).
func (m *fileRowMove) journalRow(operationID string) *database.OperationChange {
	return &database.OperationChange{
		ID:          ulid.Make().String(),
		OperationID: operationID,
		BookID:      m.bookID,
		ChangeType:  database.ChangeTypeBookFileRepoint,
		FieldName:   "book_file:" + m.fileID,
		OldValue:    m.was.Encode(),
		NewValue:    m.to.Encode(),
	}
}

// forward repoints the row, only while it still is where the plan read it.
// unsynced reports a write that landed but whose fsync failed.
func (m *fileRowMove) forward() (unsynced bool, err error) { return m.repoint(m.was, m.to) }

// rollBack puts the row back, only while it still is where forward put it.
func (m *fileRowMove) rollBack() (unsynced bool, err error) { return m.repoint(m.to, m.was) }

// repoint writes to onto the row under the store's compare-and-swap, only while
// the row is at from (path and Missing flag: a hash or size filled in since does
// not count, database.SameFileLocation). The callback may run more than once
// (ModifyBookFile retries a lost race) and checks the row every time.
//
// A write the store reports as database.ErrBookFileDurabilityUnknown was
// APPLIED (only its fsync failed, and the caller must not roll back or retry it
// as if it were lost): repoint returns nil with unsynced set.
func (m *fileRowMove) repoint(from, to database.BookFileLocation) (unsynced bool, err error) {
	written, err := m.rows.ModifyBookFile(m.bookID, m.fileID, func(f *database.BookFile) error {
		if cur := database.BookFileLocationOf(f); !database.SameFileLocation(cur, from) {
			return fmt.Errorf("%w: it is at %q (missing=%t), not %q (missing=%t)",
				errFileRowChanged, cur.Path, cur.Missing, from.Path, from.Missing)
		}
		to.Apply(f)
		return nil
	})
	if errors.Is(err, database.ErrBookFileDurabilityUnknown) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if written == nil {
		return false, fmt.Errorf("%w: it is gone", errFileRowChanged)
	}
	return false, nil
}

// applyJournal is the journal rows one match has recorded. They are written
// BEFORE the writes they describe (so a crash leaves them), and they are
// reversible (book_path_update, book_file_repoint_location), so a revert acts on
// every one that is not voided. A match whose writes did not land must therefore
// take its rows back: a live row for a write that never happened lets a revert of
// this operation undo a later, successful apply of the same match (it reads the
// row's NewValue as current and "restores" it).
type applyJournal struct {
	store Store
	rows  []*database.OperationChange
}

// record writes one row and remembers it.
func (j *applyJournal) record(c *database.OperationChange) error {
	if err := database.RecordOperationChange(j.store, c); err != nil {
		return err
	}
	j.rows = append(j.rows, c)
	return nil
}

// void takes back every recorded row the way every journaling writer does
// (credits_backfill voidJournal, repairs.Writer): the row is rewritten under its
// id with Voided and RevertedAt set, so no revert acts on it. It returns the
// failures joined; a row it could not void stays live.
func (j *applyJournal) void() error {
	var errs []error
	for _, c := range j.rows {
		v := *c
		now := time.Now()
		v.RevertedAt, v.Voided = &now, true
		if err := database.RecordOperationChange(j.store, &v); err != nil {
			errs = append(errs, fmt.Errorf("row %s: %w", c.ID, err))
		}
	}
	j.rows = nil
	return errors.Join(errs...)
}

// bookChangeLookup is the journal read finishing a stranded match needs. It is
// not part of Store (Store only narrows); a store without it finishes the match
// without the row's twin row.
type bookChangeLookup interface {
	GetBookChanges(bookID string) ([]*database.OperationChange, error)
}

// strandedRowTwin is the journal-only row that makes the apply finishing a
// stranded match revertible ON ITS OWN, the pattern of the Missing files fixer's
// rmFinishStepTwo. The stranded state is the row at the matched file and the book
// at the old path, left by a failed rollback or a crash between the row write and
// the book write. The earlier operation journaled the row's repoint; this apply
// writes only the book. Reverting this apply alone would put the book back and
// leave the row at the matched file, so it journals a twin of the earlier
// operation's live repoint row (nothing is written for it: the row already holds
// its NewValue). Reverting this apply then restores book and row together, and
// the earlier operation's rows find their values already restored, in either
// order. It returns nil when no such row exists (the organizer, not an earlier
// apply, left the row there), when the earlier row belongs to this very
// operation (a resume: it is already in this journal), or when the journal cannot
// be read.
func strandedRowTwin(store Store, log logger.Logger, bookID string, row *database.BookFile, oldPath, newPath, operationID string) *database.OperationChange {
	lookup, ok := database.AsCapability[bookChangeLookup](store)
	if !ok {
		return nil
	}
	changes, err := lookup.GetBookChanges(bookID)
	if err != nil {
		log.Warn("reconcile: book %s: cannot read its journal, finishing without a revertible row repoint: %v", bookID, err)
		return nil
	}
	var latest *database.OperationChange
	for _, c := range changes {
		if c == nil || c.Voided || c.RevertedAt != nil || c.ChangeType != database.ChangeTypeBookFileRepoint || c.FieldName != "book_file:"+row.ID {
			continue
		}
		was, werr := database.DecodeBookFileLocation(c.OldValue)
		to, terr := database.DecodeBookFileLocation(c.NewValue)
		if werr != nil || terr != nil || was.Path != oldPath || to.Path != newPath || to.Missing {
			continue
		}
		if latest == nil || c.CreatedAt.After(latest.CreatedAt) || (c.CreatedAt.Equal(latest.CreatedAt) && c.ID > latest.ID) {
			latest = c
		}
	}
	if latest == nil || latest.OperationID == operationID {
		return nil
	}
	return &database.OperationChange{
		ID:          ulid.Make().String(),
		OperationID: operationID,
		BookID:      bookID,
		ChangeType:  database.ChangeTypeBookFileRepoint,
		FieldName:   latest.FieldName,
		OldValue:    latest.OldValue,
		NewValue:    latest.NewValue,
	}
}

// applyMatch applies one confirmed match and returns "" when it landed, or the
// message for the result's errors when it did not. See ExecuteReconcile for the
// order of the steps.
//
// What a failure leaves, step by step (the journal rows are reversible, so each
// must be voided unless its write landed or may have):
//   - refused before the first journal row: nothing written, nothing to void;
//   - the row's journal write fails: the book's row is voided;
//   - the row write is refused or fails: both rows are voided (a write that
//     landed with a failed fsync is applied, not failed);
//   - the book write fails and the row is put back: the book is read again, and
//     both rows are voided only if it did not take the matched path (still at
//     the old path, or another writer's: ModifyBook's error alone cannot tell a
//     failure from a write that landed, so the book is the witness);
//   - the rollback fails: nothing is voided. The row landed, so its repoint row
//     stays live, and so does the book's, so reverting this operation restores
//     both even if a rerun sets the book.
func applyMatch(store Store, operationID string, m ReconcileApplyItem, log logger.Logger) string {
	book, err := store.GetBookByID(m.BookID)
	if err != nil || book == nil {
		return fmt.Sprintf("book %s: not found", m.BookID)
	}
	if book.IsSoftDeleted() {
		return fmt.Sprintf("book %s: it is in the trash, not repointed", m.BookID)
	}

	// The matched file must exist and be a file: a directory would give the book
	// and its row a path nothing can play.
	newInfo, err := os.Stat(m.NewPath)
	if err != nil {
		return fmt.Sprintf("book %s: new path does not exist: %s", m.BookID, m.NewPath)
	}
	if !newInfo.Mode().IsRegular() {
		return fmt.Sprintf("book %s: new path is not a regular file: %s", m.BookID, m.NewPath)
	}

	oldPath := book.FilePath

	// The book's file row at the old path moves with it. Planned (and refused)
	// before anything is journaled or written.
	plan, err := planFileRow(store, book, m.NewPath, newInfo.Size())
	if err != nil {
		return fmt.Sprintf("book %s: not applied, nothing written: %v", m.BookID, err)
	}
	move := plan.move

	j := &applyJournal{store: store}
	// voided is what a failure path appends once it has taken its rows back.
	voided := func() string {
		if err := j.void(); err != nil {
			return fmt.Sprintf("; its journal rows could not be voided and stay live: %v", err)
		}
		return "; its journal rows are voided"
	}

	// Record the change for undo support
	// book_path_update, not the legacy "file_path_update" this apply
	// journaled until 2026-11-18: that type is one the revert engine
	// cannot reverse, so with the file row's repoint row (which it CAN
	// reverse) beside it a revert would put the row back and leave the
	// book at the matched file. With both reversible, reverting the
	// operation restores the book and its row together.
	if err := j.record(&database.OperationChange{
		ID:          ulid.Make().String(),
		OperationID: operationID,
		BookID:      book.ID,
		ChangeType:  undo.ChangeTypeBookPathUpdate,
		FieldName:   "file_path",
		OldValue:    oldPath,
		NewValue:    m.NewPath,
	}); err != nil {
		return fmt.Sprintf("book %s: failed to record change: %v", m.BookID, err)
	}

	var rowRow *database.OperationChange
	switch {
	case move != nil:
		rowRow = move.journalRow(operationID)
	case plan.rowAtNew != nil:
		rowRow = strandedRowTwin(store, log, book.ID, plan.rowAtNew, oldPath, m.NewPath, operationID)
	}
	if rowRow != nil {
		if err := j.record(rowRow); err != nil {
			return fmt.Sprintf("book %s: failed to record the file row change: %v%s", m.BookID, err, voided())
		}
	}
	if move != nil {
		unsynced, err := move.forward()
		if err != nil {
			return fmt.Sprintf("book %s: file row %s not repointed, the book is left at %s: %v%s", m.BookID, move.fileID, oldPath, err, voided())
		}
		if unsynced {
			log.Warn("reconcile: book %s: file row %s was repointed but its fsync failed, durability unknown", m.BookID, move.fileID)
		}
	}

	// Update the book's file path through ModifyBook: FilePath alone is set
	// on the row as it is under the book's write lock, so a column another
	// writer committed after the GetBookByID above (the os.Stat and the
	// change record sit between them) is not reverted (audit A1#15). The
	// path it replaces must still be the one the row move and the journal
	// were made from.
	written, err := store.ModifyBook(book.ID, func(b *database.Book) error {
		if b.FilePath == m.NewPath {
			return database.ErrSkipBookWrite
		}
		if b.FilePath != oldPath {
			return fmt.Errorf("%w: it now points at %q, not %q", errBookRepointed, b.FilePath, oldPath)
		}
		b.FilePath = m.NewPath
		return nil
	})
	if err == nil && written == nil {
		err = errors.New("it vanished before its path could be updated")
	}
	if err != nil {
		msg := fmt.Sprintf("book %s: failed to update: %v", m.BookID, err)
		rolledBack := true
		if move != nil {
			unsynced, rerr := move.rollBack()
			switch {
			case rerr != nil:
				rolledBack = false
				if errors.Is(err, errBookRepointed) {
					msg += fmt.Sprintf("; file row %s is left at %s and could not be put back to %s (%v), another writer changed the book's path: check the book before rerunning the match",
						move.fileID, m.NewPath, oldPath, rerr)
				} else {
					msg += fmt.Sprintf("; file row %s is left at %s and could not be put back to %s (%v), rerun the match to finish it",
						move.fileID, m.NewPath, oldPath, rerr)
				}
			default:
				if unsynced {
					log.Warn("reconcile: book %s: file row %s was put back but its fsync failed, durability unknown", m.BookID, move.fileID)
				}
				msg += fmt.Sprintf("; file row %s put back to %s", move.fileID, oldPath)
			}
		}
		if rolledBack {
			// Void only when the book is the witness that the write did not
			// land. A refusal that saw another writer's path proves it itself.
			cur, gerr := store.GetBookByID(book.ID)
			if errors.Is(err, errBookRepointed) || (gerr == nil && cur != nil && cur.FilePath == oldPath) {
				msg += voided()
			} else {
				msg += "; the book could not be confirmed unwritten, so its journal rows stay live"
			}
		}
		return msg
	}

	if move != nil {
		log.Info("Updated book %s and its file row %s: %s -> %s", book.ID, move.fileID, oldPath, m.NewPath)
	} else {
		log.Info("Updated book %s: %s -> %s", book.ID, oldPath, m.NewPath)
	}
	return ""
}
