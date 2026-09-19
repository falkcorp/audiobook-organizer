// file: internal/database/book_delete_owns_files.go
// version: 1.0.0
// guid: 8ffda8a0-e303-4a65-9f2f-71ab98e1b796
// last-edited: 2026-09-19

package database

import (
	"errors"
	"fmt"

	"github.com/cockroachdb/pebble/v2"
)

// ErrBookOwnsFiles is returned by DeleteBook when the book still owns
// book_file rows.
//
// DeleteBook tears down the book row and every index and sidecar keyed by the
// book's ID, but it does NOT delete book_file rows, and it must not: a
// book_file row is the only record tying an audio file on disk to the library,
// and deleting one as a side effect of a book delete is data loss (standing
// rule: never delete book_file rows as a repair — repoint them). Before this
// guard, every hard delete of a book that still owned files left those rows
// naming a book with no row: the soft-delete purge (a dedup-merge loser keeps
// its own files, so every purged loser orphaned them), the archive sweep,
// reconcile's version-group cleanup, batch hard-delete and the user-facing
// hard delete all did it.
//
// So a book that owns file rows cannot be hard-deleted. A caller that means to
// remove such a book must first move its rows to the book that should own them
// (MoveBookFilesToBook / MoveBookFilesToBookBulk) and then delete the empty
// shell. The check is in the primitive, not the callers, so it covers every
// caller by construction — including the next one somebody adds — which is the
// same reasoning DeleteBook's own dedup-candidate teardown gives.
var ErrBookOwnsFiles = errors.New("book still owns book_file rows")

// countBookFileRows counts the committed book_file:<bookID>: rows in Pebble.
//
// It reads Pebble, never memdb: this count authorizes (or refuses) a hard
// delete, and memdb is a derived projection that can be short. It counts keys
// only — no value is decoded — so it is O(rows this book owns).
func countBookFileRows(db *pebble.DB, bookID string) (int, error) {
	prefix := []byte("book_file:" + bookID + ":")
	iter, err := db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: prefixEnd(prefix),
	})
	if err != nil {
		return 0, err
	}
	defer iter.Close()
	n := 0
	for iter.First(); iter.Valid(); iter.Next() {
		n++
	}
	if err := iter.Error(); err != nil {
		return 0, err
	}
	return n, nil
}

// refuseDeleteIfBookOwnsFiles returns an ErrBookOwnsFiles-wrapping error when
// bookID still owns book_file rows, and fails closed (returns the read error)
// when the count cannot be taken.
func (p *PebbleStore) refuseDeleteIfBookOwnsFiles(bookID string) error {
	n, err := countBookFileRows(p.db, bookID)
	if err != nil {
		return fmt.Errorf("delete book %s: cannot verify it owns no book_file rows: %w", bookID, err)
	}
	if n > 0 {
		return fmt.Errorf("delete book %s: %w (%d row(s)); move them to the owning book first", bookID, ErrBookOwnsFiles, n)
	}
	return nil
}
