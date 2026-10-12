// file: internal/database/book_credit_index.go
// version: 1.0.0
// guid: 5b3e9d17-42c8-4f60-9a1e-7d0c6b284fa3
// last-edited: 2026-10-12

package database

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/cockroachdb/pebble/v2"

	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

// BookCreditIndexStore is a narrow capability interface, deliberately kept OUT
// of database.Store (widening Store forces every implementation and generated
// mock to grow with it). Reach it through AsCapability, which looks through the
// search-index decorator.
type BookCreditIndexStore interface {
	// GetAllBookCreditAuthorIDs returns bookID -> the author IDs credited on
	// that book in the book_authors junction, in credit (Position) order, each
	// author once. A book with no junction rows is absent from the map. It
	// reads every credit row once, so a caller filtering a whole library pays
	// one load per query rather than one store read per book.
	GetAllBookCreditAuthorIDs() (map[string][]int, error)
}

// orderedCreditAuthorIDs orders one book's junction rows by Position (ties keep their
// input order) and returns each positive author ID once. It never sorts by ID
// or name: the order is the credit list.
func orderedCreditAuthorIDs(rows []BookAuthor) []int {
	sorted := append([]BookAuthor(nil), rows...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Position < sorted[j].Position })
	ids := make([]int, 0, len(sorted))
	seen := make(map[int]bool, len(sorted))
	for _, r := range sorted {
		if r.AuthorID <= 0 || seen[r.AuthorID] {
			continue
		}
		seen[r.AuthorID] = true
		ids = append(ids, r.AuthorID)
	}
	return ids
}

var bookCreditIndexLog = logger.New("database.book-credit-index")

// GetAllBookCreditAuthorIDs reads the memdb book_authors table. It refuses
// (ErrMemdbIncomplete) when that table is known to be short: PebbleStore then
// falls through to the authoritative scan.
func (m *MemStore) GetAllBookCreditAuthorIDs() (map[string][]int, error) {
	if err := m.requireTablesComplete("book credit index", memTableBookAuthors); err != nil {
		return nil, err
	}
	txn := m.db.Txn(false)
	defer txn.Abort()

	it, err := txn.Get(memTableBookAuthors, memIdxID)
	if err != nil {
		return nil, fmt.Errorf("memdb book_authors scan: %w", err)
	}
	rowsByBook := make(map[string][]BookAuthor)
	for obj := it.Next(); obj != nil; obj = it.Next() {
		ba := obj.(*BookAuthor)
		rowsByBook[ba.BookID] = append(rowsByBook[ba.BookID], *ba)
	}
	out := make(map[string][]int, len(rowsByBook))
	for bookID, rows := range rowsByBook {
		if ids := orderedCreditAuthorIDs(rows); len(ids) > 0 {
			out[bookID] = ids
		}
	}
	return out, nil
}

// GetAllBookCreditAuthorIDs is memdb-first; a memdb known to be short drops to
// the Pebble scan, any other memdb error propagates.
func (p *PebbleStore) GetAllBookCreditAuthorIDs() (map[string][]int, error) {
	if m := p.memOrFallback("GetAllBookCreditAuthorIDs"); m != nil {
		ids, err := m.GetAllBookCreditAuthorIDs()
		if err == nil {
			return ids, nil
		}
		if !errors.Is(err, ErrMemdbIncomplete) {
			return nil, err
		}
		bookCreditIndexLog.Error("book credit index: memdb is missing rows and will stay short until restart; falling through to the Pebble scan: err=%v lost_rows=%v", err, m.LostRows())
	}
	return p.getAllBookCreditAuthorIDsPebble()
}

// getAllBookCreditAuthorIDsPebble scans every book_authors:<bookID> row on one
// snapshot. An undecodable row or an iterator error is fatal: a short index
// would make an author filter silently miss co-credited books.
func (p *PebbleStore) getAllBookCreditAuthorIDsPebble() (map[string][]int, error) {
	snap := p.db.NewSnapshot()
	defer func() { _ = snap.Close() }()

	prefix := []byte("book_authors:")
	iter, err := snap.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: prefixUpperBound(prefix),
	})
	if err != nil {
		return nil, err
	}
	defer func() { _ = iter.Close() }()

	out := make(map[string][]int)
	for iter.First(); iter.Valid(); iter.Next() {
		var rows []BookAuthor
		if err := json.Unmarshal(iter.Value(), &rows); err != nil {
			return nil, fmt.Errorf("book credit index: undecodable book_authors row %q: %w", string(iter.Key()), err)
		}
		bookID := strings.TrimPrefix(string(iter.Key()), "book_authors:")
		if ids := orderedCreditAuthorIDs(rows); len(ids) > 0 {
			out[bookID] = ids
		}
	}
	if err := iter.Error(); err != nil {
		return nil, fmt.Errorf("book credit index: scan over book_authors truncated: %w", err)
	}
	return out, nil
}
