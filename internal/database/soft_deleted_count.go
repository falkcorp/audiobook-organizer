// file: internal/database/soft_deleted_count.go
// version: 1.0.0
// guid: 7e50b3c8-1a92-4d67-8f24-c65e09a1d3b7
// last-edited: 2026-08-14

package database

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/cockroachdb/pebble/v2"
)

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
// store does not have it.
func AsSoftDeletedCountStore(s any) SoftDeletedCountStore {
	if cs, ok := s.(SoftDeletedCountStore); ok {
		return cs
	}
	return nil
}

// CountSoftDeletedBooks counts via the marked_for_deletion index, so cost is
// O(deleted_count) with no Book copies at all.
func (m *MemStore) CountSoftDeletedBooks(olderThan *time.Time) (int, error) {
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
func (p *PebbleStore) CountSoftDeletedBooks(olderThan *time.Time) (int, error) {
	if p.UseMemDB && p.mem() != nil {
		return p.mem().CountSoftDeletedBooks(olderThan)
	}
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("book:0"),
		UpperBound: []byte("book:;"),
	})
	if err != nil {
		return 0, err
	}
	defer iter.Close()

	n := 0
	for iter.First(); iter.Valid(); iter.Next() {
		key := string(iter.Key())
		if strings.Contains(key, ":path:") || strings.Contains(key, ":series:") ||
			strings.Contains(key, ":author:") || strings.Contains(key, ":version:") {
			continue
		}
		var book Book
		if err := json.Unmarshal(iter.Value(), &book); err != nil {
			return 0, err
		}
		if book.MarkedForDeletion == nil || !*book.MarkedForDeletion {
			continue
		}
		if olderThan != nil && book.MarkedForDeletionAt != nil && book.MarkedForDeletionAt.After(*olderThan) {
			continue
		}
		n++
	}
	return n, nil
}
