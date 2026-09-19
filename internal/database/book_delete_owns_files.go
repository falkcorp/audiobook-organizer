// file: internal/database/book_delete_owns_files.go
// version: 1.1.0
// guid: 8ffda8a0-e303-4a65-9f2f-71ab98e1b796
// last-edited: 2026-09-19

package database

import (
	"errors"
	"fmt"
	"slices"

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

// ── Serializing the check against book_file writers ─────────────────────────
//
// DeleteBook's "owns no rows" check is only true at the instant it is read. A
// book_file writer that commits a row naming the book between that read and
// DeleteBook's commit would leave an orphan all the same. So both sides take
// the book's OWNER stripe (bookOwnerLocks):
//
//   - DeleteBook holds it across the count and its commit.
//   - A writer that CREATES or MOVES a row under a book (CreateBookFile,
//     BatchCreateBookFiles, batch upserts, MoveBookFilesToBookBulk) holds the
//     stripes of every book its batch names across a committed existence
//     check of each of those books and the commit, and refuses
//     (ErrBookFileOwnerMissing) when one is gone.
//   - A writer that REWRITES an existing row in place (UpdateBookFile and
//     friends, PatchBookFileFields, the scan-cache stamp) holds the stripe
//     across its read of the row and its write. While it holds it the row is
//     visible to DeleteBook's count, so the delete cannot slip between.
//
// Whichever commits first wins, and the other sees it: a delete that commits
// first makes the writer's existence check fail; a writer that commits first
// makes the delete's count non-zero.
//
// LOCK ORDER: owner stripes are always the INNERMOST lock. They may be taken
// while a book stripe (DeleteBook) or a book_file stripe (UpdateBookFile) is
// held, but nothing takes a book or book_file stripe while holding one, and
// nothing slow runs under one. A writer holding several takes them in
// ascending stripe order, de-duplicated, so two multi-book writers cannot
// deadlock and DeleteBook, which holds one, cannot either.

// ErrBookFileOwnerMissing is returned by a book_file writer asked to create or
// move a row under a book that has no row: committing it would create an
// orphan.
var ErrBookFileOwnerMissing = errors.New("book_file owner book does not exist")

// lockBookOwners takes the owner stripes of every id (de-duplicated, ascending)
// and returns the unlock func. Empty ids are ignored.
func (p *PebbleStore) lockBookOwners(ids ...string) func() {
	stripes := make([]int, 0, len(ids))
	for _, id := range ids {
		if id == "" {
			continue
		}
		stripes = append(stripes, stripeFor(id))
	}
	slices.Sort(stripes)
	stripes = slices.Compact(stripes)
	for _, st := range stripes {
		p.bookOwnerLocks[st].Lock()
	}
	return func() {
		for i := len(stripes) - 1; i >= 0; i-- {
			p.bookOwnerLocks[stripes[i]].Unlock()
		}
	}
}

// requireBookRowsExist returns ErrBookFileOwnerMissing unless every id has a
// committed book row (live or soft-deleted). Called with the ids' owner
// stripes held. An empty id is refused too: a row naming no book is an orphan.
func (p *PebbleStore) requireBookRowsExist(ids ...string) error {
	for _, id := range ids {
		if id == "" {
			return fmt.Errorf("%w: empty book id", ErrBookFileOwnerMissing)
		}
		_, closer, err := p.db.Get([]byte("book:" + id))
		if errors.Is(err, pebble.ErrNotFound) {
			return fmt.Errorf("%w: %s", ErrBookFileOwnerMissing, id)
		}
		if err != nil {
			return fmt.Errorf("check book %s exists: %w", id, err)
		}
		_ = closer.Close()
	}
	return nil
}

// commitBookFileBatch commits a book_file writer's batch under the owner
// stripes of lockIDs, after verifying — with the stripes held — that every
// book in newOwners has a row (rows are being created or moved under them)
// and that every key in rewriteKeys is still committed (rows being rewritten
// in place were read before the stripes were taken; if one vanished since, a
// concurrent delete may have let its book go, and rewriting it would recreate
// an orphan). On refusal the batch is closed and nothing is written.
func (p *PebbleStore) commitBookFileBatch(batch *pebble.Batch, lockIDs, newOwners []string, rewriteKeys [][]byte) error {
	unlock := p.lockBookOwners(append(append([]string(nil), lockIDs...), newOwners...)...)
	defer unlock()
	if err := p.requireBookRowsExist(newOwners...); err != nil {
		_ = batch.Close()
		return err
	}
	for _, k := range rewriteKeys {
		_, closer, err := p.db.Get(k)
		if errors.Is(err, pebble.ErrNotFound) {
			_ = batch.Close()
			return fmt.Errorf("%w: row %s was deleted while being rewritten", ErrBookFileOwnerMissing, k)
		}
		if err != nil {
			_ = batch.Close()
			return fmt.Errorf("re-check %s: %w", k, err)
		}
		_ = closer.Close()
	}
	return batch.Commit(pebble.Sync)
}

// setBookFileRowIfPresent writes data at a book_file row key only if that key
// is still committed, under the owning book's owner stripe. It is the
// in-place rewrite form of commitBookFileBatch for the paths that write a
// single primary key with db.Set. Returns false (and writes nothing) when the
// row is gone.
func (p *PebbleStore) setBookFileRowIfPresent(bookID string, key, data []byte) (bool, error) {
	unlock := p.lockBookOwners(bookID)
	defer unlock()
	_, closer, err := p.db.Get(key)
	if errors.Is(err, pebble.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	_ = closer.Close()
	return true, p.db.Set(key, data, pebble.Sync)
}
