// file: internal/books/store/modify_book_respecting_locks.go
// version: 1.1.0
// guid: e45633de-67fc-4a94-9870-20fcc2b9cc16
// last-edited: 2026-11-18

package store

import (
	"errors"
	"fmt"
	"slices"
)

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
//
// What "the same lock" does and does not cover: the lock is read under the
// book's write lock, so a lock saved before that read holds the write and a lock
// saved after the batch commits does not. A lock WRITER does not take the book's
// write lock (RecordUserOverrides takes LockMetadataState), so a lock saved
// between the read and the commit, one Pebble batch, is not seen. The helper
// narrows that window to one batch; it does not close it. Closing it needs the
// helper to take LockMetadataState around the write, which needs an audit of
// every LockMetadataState holder's lock order first (todo.d fragment
// 20261118_lock-metadata-state-order-audit.md).
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

// BookCreditsRowModifier is the one write ModifyBookCreditsRespectingLocks
// makes: a book row and its credit lists in one commit (BookCreditsModifier's
// ModifyBookCredits, which a store may offer without the rest of it).
type BookCreditsRowModifier interface {
	ModifyBookCredits(bookID string, fn func(book *Book, credits *BookCreditsEdit) error) (*Book, BookCreditsEdit, error)
}

// ErrBookCreditsUnsupported marks a store that cannot write a book's row and its
// credits in one commit. A caller that must keep a lock check and a credit write
// atomic refuses to write instead of falling back to two writes with a window
// between them.
var ErrBookCreditsUnsupported = errors.New("store cannot write a book's credits and row together (no BookCreditsModifier capability)")

// ModifyBookCreditsRespectingLocks is ModifyBookRespectingLocks for a writer
// that changes a book's credit lists, alone or beside its row: under the book's
// write lock and both credit locks it loads the field locks (fail closed), runs
// mutate on the fresh row and lists, and writes NOTHING when mutate changed
// something the user locked.
//
// Credits are not a Book column, so FieldLocks.Apply cannot see them; the helper
// compares the lists itself. A changed author list is held by the author lock
// (FieldKeyAuthorName, the lock that covers AuthorID), a changed narrator list by
// the narrator lock. That is the case a book column check alone gets wrong: a
// co-author's credit moves while the primary AuthorID is never touched.
//
// held is the lock keys mutate tried to change, non-empty exactly when the book
// was left alone; the returned row is the row as read in that case. Everything
// else is ModifyBookRespectingLocks' contract: an unreadable lock set returns an
// error wrapping ErrFieldLocksUnavailable before mutate runs, a mutate error
// (ErrSkipBookWrite included) is ModifyBookCredits', and a missing book is
// (nil, nil, nil).
//
// mutate decides on the lists and row it is handed, which are the fresh ones: do
// not write back a list read earlier (that is the lost update ModifyBookCredits
// exists to prevent). The lock read inside the callback is a field-state read
// that takes no book lock, as in ModifyBookRespectingLocks.
func ModifyBookCreditsRespectingLocks(modifier BookCreditsRowModifier, reader MetadataFieldStateReader, id string, mutate func(*Book, *BookCreditsEdit) error) (written *Book, held []string, err error) {
	written, _, err = modifier.ModifyBookCredits(id, func(cur *Book, credits *BookCreditsEdit) error {
		locks, lerr := LoadFieldLocks(reader, cur.ID)
		if lerr != nil {
			return fmt.Errorf("book %s NOT written: %w", cur.ID, lerr)
		}
		var (
			before        *Book
			creditsBefore BookCreditsEdit
		)
		if locks.Any() {
			var serr error
			if before, serr = SnapshotBook(cur); serr != nil {
				return serr
			}
			creditsBefore = BookCreditsEdit{
				Authors:   slices.Clone(credits.Authors),
				Narrators: slices.Clone(credits.Narrators),
			}
		}
		var mutateErr error
		restored := locks.Apply(cur, func(b *Book) { mutateErr = mutate(b, credits) })
		if mutateErr != nil {
			return mutateErr
		}
		if locks.Any() {
			if locks.Locked(FieldKeyAuthorName) && !slices.Equal(creditsBefore.Authors, credits.Authors) && !slices.Contains(restored, FieldKeyAuthorName) {
				restored = append(restored, FieldKeyAuthorName)
			}
			if locks.Locked(FieldKeyNarrator) && !slices.Equal(creditsBefore.Narrators, credits.Narrators) && !slices.Contains(restored, FieldKeyNarrator) {
				restored = append(restored, FieldKeyNarrator)
			}
		}
		if len(restored) > 0 {
			held = restored
			*cur = *before
			*credits = creditsBefore
			return ErrSkipBookWrite
		}
		return nil
	})
	return written, held, err
}
