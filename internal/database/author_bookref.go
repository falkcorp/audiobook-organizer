// file: internal/database/author_bookref.go
// version: 1.3.0
// guid: 436a4092-01fc-4768-b57c-942068cb726d
// last-edited: 2026-08-23

package database

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
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
	// BOTH tables, because both passes below feed the same answer. The series
	// twin names only memTableBooks; this one would be fail-open if it did the
	// same, since a lost book_authors row is a co-author credit that exists
	// NOWHERE else -- the legacy Book.AuthorID field holds one author, so pass 2
	// cannot recover it the way it can recover a primary credit.
	if err := m.requireTablesComplete("author reference count", memTableBookAuthors, memTableBooks); err != nil {
		return nil, err
	}

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

// GetAllAuthorBookRefCounts prefers the memdb when it is warm — which in
// production is ALWAYS, because UseMemDB is hardcoded true (pebble_store.go).
// That is why the hardening below the memdb branch is not optional: for the
// whole life of this guard, every production call has taken the memdb path, and
// a memdb that quietly lost a book_authors row answers "referenced by nothing"
// with a nil error to a caller that deletes on it.
//
// When the memdb knows it is short, this falls THROUGH to Pebble rather than
// refusing. Pebble is the source of truth and its scan aborts on an undecodable
// row, so the fall-through yields a CORRECT answer where a refusal would only
// have yielded a safe one — purge-empty-authors keeps working instead of
// stalling until the next restart. The fall-through is bounded: it happens only
// when a row was actually lost, and no caller counts inside a loop (both
// entities handlers and the purge op build the map once per operation).
//
// The fall-through is only trustworthy because the Pebble scan's own
// completeness bugs were fixed first: it had a hand-written "book_authors:~"
// upper bound that excluded every non-ASCII book id, and it skipped undecodable
// rows without reporting them. Falling back to a scan with those defects would
// have swapped one short count for another.
//
// Any other error is propagated unchanged — falling back to a full scan on an
// unrecognized failure would be guessing at its cause.
func (p *PebbleStore) GetAllAuthorBookRefCounts() (map[int]int, error) {
	// Loaded ONCE. Reset can swap memPtr underneath us, and reading it twice
	// could report a refusal from one MemStore next to the (empty) loss map of
	// its freshly-reset replacement -- a log line contradicting itself.
	if m := p.mem(); p.UseMemDB && m != nil {
		counts, err := m.GetAllAuthorBookRefCounts()
		if err == nil {
			return counts, nil
		}
		if !errors.Is(err, ErrMemdbIncomplete) {
			return nil, err
		}
		slog.Warn("author ref count: memdb is missing rows, falling through to the authoritative Pebble scan",
			"error", err, "lost_rows", m.LostRows())
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
	// UpperBound comes from prefixUpperBound, NOT a hand-written "book_authors:~".
	// bookID is an opaque caller-suppliable string (CreateBook only mints a ULID
	// when book.ID == "", so importers and restore paths supply their own), and a
	// literal '~' (0x7E) excludes every id whose first byte sorts above it --
	// which is every non-ASCII id, since UTF-8 continuation bytes start at 0xC2.
	// Those books' credit lists would silently fall outside the scan, and an
	// author credited only there would count 0 and be deletable. That is the same
	// fail-open this file exists to close, arriving through the key range instead
	// of the decoder. pebble_store_authors.go:221-227 warns against exactly this
	// for exactly this keyspace.
	jPrefix := []byte("book_authors:")
	jIter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: jPrefix,
		UpperBound: prefixUpperBound(jPrefix),
	})
	if err != nil {
		return nil, err
	}
	for jIter.First(); jIter.Valid(); jIter.Next() {
		var authors []BookAuthor
		if err := json.Unmarshal(jIter.Value(), &authors); err != nil {
			// FATAL, not skippable. A credit list we cannot decode may hold the
			// ONLY reference to an author -- authors 2..n of a book live here and
			// nowhere else. Skipping it undercounts, and undercounting is
			// fail-OPEN for every caller: the delete proceeds and strands the
			// very row we could not read.
			// Capture the key BEFORE closing: jIter.Key() after Close returns
			// "", which made the abort message's only actionable field always
			// empty ("undecodable book_authors row \"\""). The book pass below
			// already does this correctly.
			badKey := string(jIter.Key())
			_ = jIter.Close()
			return nil, fmt.Errorf("author ref scan: undecodable book_authors row %q: %w", badKey, err)
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
	// The loop exits on end-of-range OR on an iteration error, and the two are
	// indistinguishable without this check. A truncated map with a nil error
	// answers "nothing else references anything" -- the permissive answer -- to
	// a caller that deletes on the strength of it.
	if err := jIter.Error(); err != nil {
		_ = jIter.Close()
		return nil, fmt.Errorf("author ref scan truncated over book_authors, refusing to answer from a partial count: %w", err)
	}
	if cErr := jIter.Close(); cErr != nil {
		return nil, fmt.Errorf("author ref scan: closing book_authors iterator: %w", cErr)
	}

	// Pass 2: every book row, for the legacy AuthorID field.
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("book:0"),
		UpperBound: []byte("book:;"),
	})
	if err != nil {
		return nil, err
	}

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
			// FATAL for the same reason as the junction pass above: an
			// undecodable book row may carry a legacy author_id.
			_ = iter.Close()
			return nil, fmt.Errorf("author ref scan: undecodable book row %q: %w", key, err)
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
	if err := iter.Error(); err != nil {
		_ = iter.Close()
		return nil, fmt.Errorf("author ref scan truncated over books, refusing to answer from a partial count: %w", err)
	}
	if err := iter.Close(); err != nil {
		return nil, fmt.Errorf("author ref scan: closing book iterator: %w", err)
	}
	return counts, nil
}

// AuthorRefCounts returns, per author ID, how many books reference it in ANY
// state -- including books in the trash, non-primary (duplicate) versions, and
// co-authors credited only through the book_authors junction. An author ID
// absent from the map is referenced by nothing and is the only thing safe to
// delete.
//
// It is the exported twin of SeriesRefCounts, promoted here for the same reason:
// the packages that cannot import internal/server (the maintenance plugin that
// owns the bulk purge, and any future caller) must reach the same guard instead
// of growing inline copies of it that drift apart.
//
// It fails CLOSED. If the store cannot answer the unfiltered question, the
// caller must refuse to delete rather than fall back to the filtered count,
// because that fallback is precisely the bug: it deletes rows while reporting
// success. See the file comment above for the damage that causes.
//
// Resolution goes through AsAuthorBookRefStore, and therefore AsCapability, so
// it looks THROUGH the decorator chain. A bare type assertion against
// *PebbleStore is wrong in production, where the Bleve search-index decorator
// always wraps the store.
func AuthorRefCounts(store any) (map[int]int, error) {
	refCounter := AsAuthorBookRefStore(store)
	if refCounter == nil {
		return nil, fmt.Errorf("store cannot count unfiltered author references (got %T); "+
			"refusing to delete from a filtered count, which silently strands "+
			"books whose author is trashed, non-primary, or a junction-only co-author", store)
	}
	return refCounter.GetAllAuthorBookRefCounts()
}
