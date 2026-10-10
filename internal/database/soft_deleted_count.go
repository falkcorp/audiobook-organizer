// file: internal/database/soft_deleted_count.go
// version: 1.4.1
// guid: 7e50b3c8-1a92-4d67-8f24-c65e09a1d3b7
// last-edited: 2026-10-10

package database

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

// softDeletedCountLog carries this file's diagnostics through the log-injection barrier.
var softDeletedCountLog = logger.New("database.soft-deleted-count")

// SoftDeletedCountStore is the optional capability of counting the soft-deleted
// set without materializing it. It exists because the ONLY prior way to answer
// "how many books are in the trash" was ListSoftDeletedBooks with a large
// limit and len() on the result — which silently saturates at whatever limit
// the caller guessed (10,000 in the one production caller) and pays a full
// materialization for a number. Obtained by assertion rather than widening the
// Store interface, per the repo convention for new capabilities.
type SoftDeletedCountStore interface {
	// CountSoftDeletedBooks reports how many books are soft-deleted, honouring
	// the same olderThan semantics as ListSoftDeletedBooks: when olderThan is
	// non-nil, a book is counted unless its MarkedForDeletionAt is set AND
	// after the cutoff (a nil timestamp is counted — identical to the listing,
	// so the count can never disagree with the list it paginates).
	CountSoftDeletedBooks(olderThan *time.Time) (int, error)
}

// AsSoftDeletedCountStore returns the counting capability, or nil when the
// store does not have it. It resolves through any opted-in decorator chain
// (AsCapability), like every other As*Store helper.
//
// It was a bare type assertion until 2026-10-06, and that assertion ALWAYS
// missed in production: the audiobook service holds the server's indexedStore,
// which embeds database.Store and so promotes only Store's methods, and
// CountSoftDeletedBooks is not one of them. Every trash count therefore took
// AudiobookService's paging fallback -- 49 calls of ListSoftDeletedBooks(1000,
// offset) for a 48k-book trash, each copying and sorting the whole set -- and
// GET /audiobooks/soft-deleted measured 30-58 s warm, 733 s cold. Going past
// the decorator is safe here: this is a read, and indexedStore only overrides
// writes (to reindex them).
func AsSoftDeletedCountStore(s any) SoftDeletedCountStore {
	if cs, ok := AsCapability[SoftDeletedCountStore](s); ok {
		return cs
	}
	return nil
}

// CountSoftDeletedBooks counts via the marked_for_deletion index, so cost is
// O(deleted_count) with no Book copies at all.
//
// It refuses (ErrMemdbIncomplete) when the books table is known to be missing
// rows, exactly like MemStore.ListSoftDeletedBooks: the count is the total the
// listing paginates, and the two must never disagree.
func (m *MemStore) CountSoftDeletedBooks(olderThan *time.Time) (int, error) {
	if err := m.requireTablesComplete("soft-deleted count (the total the trash listing paginates)", memTableBooks); err != nil {
		return 0, err
	}
	txn := m.db.Txn(false)
	defer txn.Abort()

	iter, err := txn.Get(memTableBooks, memIdxMarkedForDeletion, true)
	if err != nil {
		return 0, fmt.Errorf("memdb soft-deleted count: %w", err)
	}
	n := 0
	for obj := iter.Next(); obj != nil; obj = iter.Next() {
		b := obj.(*Book)
		if olderThan != nil && b.MarkedForDeletionAt != nil && b.MarkedForDeletionAt.After(*olderThan) {
			continue
		}
		n++
	}
	return n, nil
}

// CountSoftDeletedBooks mirrors ListSoftDeletedBooks' dual dispatch: the memdb
// index path when available, else a Pebble scan that unmarshals each row only
// to read its deletion flag — no slice, no sort, no pagination.
//
// A memdb that knows it is missing book rows falls through to the Pebble scan,
// as ListSoftDeletedBooks does; any other memdb error is returned unchanged.
func (p *PebbleStore) CountSoftDeletedBooks(olderThan *time.Time) (int, error) {
	if m := p.memOrFallback("CountSoftDeletedBooks"); m != nil {
		n, err := m.CountSoftDeletedBooks(olderThan)
		if err == nil {
			return n, nil
		}
		if !errors.Is(err, ErrMemdbIncomplete) {
			return 0, err
		}
		softDeletedCountLog.Error("soft-deleted count: memdb is missing rows; falling through to the authoritative Pebble scan: error=%v lost_rows=%v",
			err, m.LostRows())
	}

	n := 0
	if err := forEachBookRow(p.db, func(rowID string, rowValue []byte) error {
		var book Book
		if err := json.Unmarshal(rowValue, &book); err != nil {
			return err
		}
		if book.MarkedForDeletion == nil || !*book.MarkedForDeletion {
			return nil
		}
		if olderThan != nil && book.MarkedForDeletionAt != nil && book.MarkedForDeletionAt.After(*olderThan) {
			return nil
		}
		n++
		return nil
	}); err != nil {
		return 0, err
	}
	return n, nil
}
