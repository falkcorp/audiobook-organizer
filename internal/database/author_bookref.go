// file: internal/database/author_bookref.go
// version: 1.0.0
// guid: 436a4092-01fc-4768-b57c-942068cb726d
// last-edited: 2026-08-23

package database

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cockroachdb/pebble/v2"
)

// Unfiltered AUTHOR reference counting, for DELETION decisions. The author-side
// twin of series_bookref.go; read that file first for the history.
//
// WHY THIS EXISTS SEPARATELY FROM GetAllAuthorBookCounts
//
// GetAllAuthorBookCounts (memdb_reads.go) and GetBooksByAuthorIDCore skip two
// categories of book:
//
//	if b.IsPrimaryVersion  != nil && !*b.IsPrimaryVersion  { continue }
//	if bookIsSoftDeleted(b)                                { continue }
//
// That is CORRECT for the count rendered next to an author in the UI — the
// trash and secondary versions must not inflate a badge. It is WRONG as an
// existence test for deletion, and both author delete handlers used it as
// exactly that: an author whose books are all trashed, or all non-primary,
// counted 0 and the row was deleted while those books stayed on disk holding
// an author_id that no longer resolves. The author's NAME lives only in the
// deleted row, so the reference is not recoverable afterwards.
//
// The same instrument-confusion on the series side had, as measured on
// production 2026-08-14, already stranded 13,322 live books behind 6,893 series
// IDs that no longer existed. Nothing about authors makes them immune; the
// junction table only adds a second way to hold a reference.
//
// These counters therefore apply NO filters. Every book row that names an
// author counts, whatever its deletion or primary-version state.
//
// TWO PASSES, AND WHY THE DEDUP IS PER (book, author) PAIR
//
// An author can be attached to a book two ways:
//
//   - the book_authors junction table (multi-author credit lists; authors
//     2..n of a book live only here), and
//   - the legacy denormalized Book.AuthorID field.
//
// GetAllAuthorBookCounts dedups by skipping, in pass 2, any book that had ANY
// junction row. That is safe for a display badge but FAILS OPEN as a delete
// guard: a book may carry junction rows that do not mention its own
// Book.AuthorID (see internal/database/author_getter_conformance_test.go, the
// "Coauthor But Trashed" fixture, where Book.AuthorID is the primary author yet
// the only junction row names the co-author). Skipping that whole book loses
// the legacy author's reference and makes a still-referenced author deletable
// — the same bug in a new place.
//
// So the dedup here is per (bookID, authorID) PAIR, not per book. A pair seen
// in the junction is not counted again from the legacy field; a legacy author
// the junction never mentions is still counted. This also makes both
// implementations agree regardless of duplicate-row handling: memdb's
// book_authors primary index is a UNIQUE compound of BookID+AuthorID and
// therefore collapses a repeated pair, while Pebble stores the credit list as
// one JSON array under book_authors:<bookID> and would otherwise count a
// repeated pair twice.

// AuthorBookRefStore is a narrow capability interface, deliberately kept OUT of
// database.Store — same rationale as SeriesBookRefStore: widening Store forces
// every implementation and generated mock to grow with it. Reach it through
// AsAuthorBookRefStore, which looks through the indexedStore decorator.
type AuthorBookRefStore interface {
	// GetAllAuthorBookRefCounts returns authorID -> number of distinct book
	// rows that reference it, via the book_authors junction table or the legacy
	// Book.AuthorID field, counting trashed and non-primary books. An author
	// absent from the map is referenced by NOTHING and is safe to delete.
	GetAllAuthorBookRefCounts() (map[int]int, error)
}

// AsAuthorBookRefStore returns s as an AuthorBookRefStore, or nil if the
// backing store cannot answer the unfiltered question. Callers MUST nil-check
// and MUST fail rather than falling back to the filtered counter — that
// fallback is the bug this file exists to remove, and it would be silent.
//
// It goes through AsCapability, not a bare type assertion, because in
// production the store is wrapped in the Bleve indexedStore decorator and a
// bare `s.(*PebbleStore)` returns nil exactly where the guard matters.
func AsAuthorBookRefStore(s any) AuthorBookRefStore {
	if s == nil {
		return nil
	}
	if rs, ok := AsCapability[AuthorBookRefStore](s); ok {
		return rs
	}
	return nil
}

// authorRefKey identifies one (book, author) attachment, so the junction pass
// and the legacy pass cannot count the same attachment twice.
type authorRefKey struct {
	bookID   string
	authorID int
}

// GetAllAuthorBookRefCounts counts every book in the memdb that names an
// author, with NO deletion or primary-version filtering. Contrast
// GetAllAuthorBookCounts in memdb_reads.go, which checks IsPrimaryVersion and
// bookIsSoftDeleted on both passes and scans only the memIdxIsPrimaryVersion
// index.
func (m *MemStore) GetAllAuthorBookRefCounts() (map[int]int, error) {
	txn := m.db.Txn(false)
	defer txn.Abort()

	out := make(map[int]int)
	seen := make(map[authorRefKey]bool)

	// Pass 1: every junction row, in any book state. No lookup of the book row
	// at all — the row's flags are exactly what must NOT influence this.
	baIter, err := txn.Get(memTableBookAuthors, memIdxID)
	if err != nil {
		return nil, fmt.Errorf("memdb book_authors scan: %w", err)
	}
	for obj := baIter.Next(); obj != nil; obj = baIter.Next() {
		ba := obj.(*BookAuthor)
		k := authorRefKey{bookID: ba.BookID, authorID: ba.AuthorID}
		if seen[k] {
			continue
		}
		seen[k] = true
		out[ba.AuthorID]++
	}

	// Pass 2: ALL books (memIdxID, not memIdxIsPrimaryVersion), counting the
	// legacy AuthorID for any (book, author) pair the junction did not already
	// account for.
	bIter, err := txn.Get(memTableBooks, memIdxID)
	if err != nil {
		return nil, fmt.Errorf("memdb books scan: %w", err)
	}
	for obj := bIter.Next(); obj != nil; obj = bIter.Next() {
		b := obj.(*Book)
		if b.AuthorID == nil {
			continue
		}
		k := authorRefKey{bookID: b.ID, authorID: *b.AuthorID}
		if seen[k] {
			continue
		}
		seen[k] = true
		out[*b.AuthorID]++
	}
	return out, nil
}

// GetAllAuthorBookRefCounts prefers the memdb when it is warm (the prod
// default) and otherwise scans Pebble directly.
func (p *PebbleStore) GetAllAuthorBookRefCounts() (map[int]int, error) {
	if p.UseMemDB && p.mem() != nil {
		return p.mem().GetAllAuthorBookRefCounts()
	}
	return p.getAllAuthorBookRefCountsPebble()
}

// getAllAuthorBookRefCountsPebble mirrors GetAllAuthorBookCounts's key ranges
// and row-shape guards, minus every IsPrimaryVersion / soft-delete filter, and
// dedups per (book, author) pair rather than per book.
func (p *PebbleStore) getAllAuthorBookRefCountsPebble() (map[int]int, error) {
	counts := make(map[int]int)
	seen := make(map[authorRefKey]bool)

	// Pass 1: the book_authors junction table. Each key holds the whole credit
	// list for one book as a JSON array.
	jIter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("book_authors:"),
		UpperBound: []byte("book_authors:~"),
	})
	if err != nil {
		return nil, err
	}
	for jIter.First(); jIter.Valid(); jIter.Next() {
		var authors []BookAuthor
		if json.Unmarshal(jIter.Value(), &authors) != nil {
			continue
		}
		bookID := strings.TrimPrefix(string(jIter.Key()), "book_authors:")
		for _, a := range authors {
			k := authorRefKey{bookID: bookID, authorID: a.AuthorID}
			if seen[k] {
				continue
			}
			seen[k] = true
			counts[a.AuthorID]++
		}
	}
	if cErr := jIter.Close(); cErr != nil {
		return nil, cErr
	}

	// Pass 2: every book row, for the legacy AuthorID field.
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("book:0"),
		UpperBound: []byte("book:;"),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	for iter.First(); iter.Valid(); iter.Next() {
		key := string(iter.Key())
		if !strings.HasPrefix(key, "book:") {
			continue
		}
		// Exactly one colon: skip the secondary indexes (book:path:, book:hash:,
		// book:versiongroup:) that share the prefix.
		if strings.Count(key, ":") != 1 {
			continue
		}
		var b Book
		if err := json.Unmarshal(iter.Value(), &b); err != nil {
			continue
		}
		if b.AuthorID == nil {
			continue
		}
		bookID := b.ID
		if bookID == "" {
			bookID = strings.TrimPrefix(key, "book:")
		}
		k := authorRefKey{bookID: bookID, authorID: *b.AuthorID}
		if seen[k] {
			continue
		}
		seen[k] = true
		counts[*b.AuthorID]++
	}
	return counts, nil
}
