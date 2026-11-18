// file: internal/books/store/modify_book_respecting_locks.go
// version: 1.0.0
// guid: e45633de-67fc-4a94-9870-20fcc2b9cc16
// last-edited: 2026-11-18

package store

import "fmt"

// BookModifier is the one book write ModifyBookRespectingLocks makes.
type BookModifier interface {
	ModifyBook(id string, fn func(*Book) error) (*Book, error)
}

// ModifyBookRespectingLocks is ModifyBook for a writer that changes a
// user-lockable column: under the book's write lock it loads the book's field
// locks (fail closed), runs mutate on the fresh row, and writes NOTHING when
// mutate changed a column the user locked.
//
// Why all-or-nothing and not ApplyRespectingLocks' put-the-locked-column-back:
// the maintenance writers that reach for this change two or three columns as one
// decision (a series unlink clears SeriesID AND SeriesSequence, a read-by repair
// derives the narrator from the very title it rewrites). Taking one column back
// and writing the rest leaves a row neither the user nor the repair intended.
// FieldLocks.Apply is still the one place that maps a lock to its columns; this
// only decides what a restored column means for the whole write.
//
// held is the lock keys mutate tried to change (FieldKey* vocabulary, display
// order), non-empty exactly when the book was left alone. The returned row is the
// row as read, untouched, in that case. A failure to read the locks returns an
// error wrapping ErrFieldLocksUnavailable and mutate is not run; any other
// error, including ErrSkipBookWrite from mutate, is ModifyBook's.
//
// reader and modifier are separate arguments because a caller may hold a gated
// writer beside its read store. The lock read runs inside ModifyBook's callback,
// which the LOCK RULES allow: it only reads field-state rows.
func ModifyBookRespectingLocks(modifier BookModifier, reader MetadataFieldStateReader, id string, mutate func(*Book) error) (written *Book, held []string, err error) {
	written, err = modifier.ModifyBook(id, func(cur *Book) error {
		locks, lerr := LoadFieldLocks(reader, cur.ID)
		if lerr != nil {
			return fmt.Errorf("book %s NOT written: %w", cur.ID, lerr)
		}
		var before *Book
		if locks.Any() {
			// The deep copy is what makes "all or nothing" cheap to state: whatever
			// mutate touched, the row goes back to this.
			var serr error
			if before, serr = SnapshotBook(cur); serr != nil {
				return serr
			}
		}
		var mutateErr error
		restored := locks.Apply(cur, func(b *Book) { mutateErr = mutate(b) })
		if mutateErr != nil {
			return mutateErr
		}
		if len(restored) > 0 {
			held = restored
			*cur = *before
			return ErrSkipBookWrite
		}
		return nil
	})
	return written, held, err
}
