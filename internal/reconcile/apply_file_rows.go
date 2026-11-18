// file: internal/reconcile/apply_file_rows.go
// version: 1.0.0
// guid: 079ffa9c-f1f8-4388-a615-f9875fd5419f
// last-edited: 2026-11-18

package reconcile

import (
	"errors"
	"fmt"

	"github.com/oklog/ulid/v2"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
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

// planFileRow decides, with no write, what the match moves besides the book. It
// returns (nil, nil) when no row needs to move: the book has no file rows (the
// match repoints the book alone, as it always did), or its row is already at
// newPath (a cut-off earlier apply, or a file the organizer moved, wrote the row
// and not the book). Otherwise it returns the row to repoint.
//
// A book with rows is repointed only when exactly one of them is at the book's
// current path and none is at newPath. Any other shape (several rows at the old
// path, rows that are neither there nor at newPath, a row at both) is refused:
// repointing "the" row would be a guess, and the book alone must not move while
// its rows say something else. The row's own book must be the only one holding
// newPath: a row of another book there, or a retired row (a claim on the file
// whoever holds it, database.RetiredClaimOnPath), refuses the match, because
// the repoint writes the single-owner path index. A lookup that cannot be
// complete (memdb warming up) refuses too, wrapping
// database.ErrOwnershipLookupIncomplete, and never reads as "nobody holds it".
func planFileRow(store Store, book *database.Book, newPath string, newSize int64) (*fileRowMove, error) {
	files, err := store.GetBookFiles(book.ID)
	if err != nil {
		return nil, fmt.Errorf("read its file rows: %w", err)
	}
	if len(files) == 0 {
		return nil, nil
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
	switch {
	case len(atNew) == 1 && len(atOld) == 0:
		// The row is where the book is going.
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
	return move, nil
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
func (m *fileRowMove) forward() error { return m.repoint(m.was, m.to) }

// rollBack puts the row back, only while it still is where forward put it.
func (m *fileRowMove) rollBack() error { return m.repoint(m.to, m.was) }

// repoint writes to onto the row under the store's compare-and-swap, only while
// the row is at from (path and Missing flag: a hash or size filled in since does
// not count, database.SameFileLocation). The callback may run more than once
// (ModifyBookFile retries a lost race) and checks the row every time.
func (m *fileRowMove) repoint(from, to database.BookFileLocation) error {
	written, err := m.rows.ModifyBookFile(m.bookID, m.fileID, func(f *database.BookFile) error {
		if cur := database.BookFileLocationOf(f); !database.SameFileLocation(cur, from) {
			return fmt.Errorf("%w: it is at %q (missing=%t), not %q (missing=%t)",
				errFileRowChanged, cur.Path, cur.Missing, from.Path, from.Missing)
		}
		to.Apply(f)
		return nil
	})
	if err != nil {
		return err
	}
	if written == nil {
		return fmt.Errorf("%w: it is gone", errFileRowChanged)
	}
	return nil
}
