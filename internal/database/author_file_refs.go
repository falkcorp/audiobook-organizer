// file: internal/database/author_file_refs.go
// version: 1.0.0
// guid: c6e57d72-7048-499d-85aa-1714156d9481
// last-edited: 2026-09-10

package database

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cockroachdb/pebble/v2"
	"github.com/hashicorp/go-memdb"
)

// Unfiltered AUTHOR FILE counting, for the purge-empty-authors file-safety
// gate. The file-side sibling of author_bookref.go; read that file first — the
// traversal, the per-(book, author) dedup and the reasons for both are argued
// there in full and are not repeated here.
//
// WHY THIS EXISTS SEPARATELY FROM GetAllAuthorFileCounts
//
// GetAllAuthorFileCounts (memdb_reads.go, and its Pebble twin in
// pebble_store_authors.go) is a DISPLAY counter. It scans only the
// memIdxIsPrimaryVersion index, skips books that bookIsSoftDeleted, and maps a
// book to an author through the legacy Book.AuthorID field alone — never the
// book_authors junction. That is correct for the number rendered on the Authors
// tab and in diagnostics, and it stays in place for those callers.
//
// It was ALSO the input to purge-empty-authors' require_zero_files gate, which
// the op labels "🔴 THIS IS THE SAFETY THAT MATTERS". Read as a safety signal it
// returns an unconditional 0 for three populations whose files are real:
//
//   - a junction-only co-author (authors 2..n of a credit list exist nowhere
//     else),
//   - an author whose books are all soft-deleted, and
//   - an author whose books are all non-primary versions.
//
// Every one of those books still has book_file rows on disk. Reporting 0 files
// for them turned the gate into a rubber stamp on exactly the rows it was
// written to hold back.
//
// WHAT THIS COUNTER IS, AND WHAT IT IS NOT
//
// It counts non-missing book_file rows for EVERY book that references an author
// by ANY route, in ANY state, so the three populations above are visible to it.
//
// It is NOT a second line of defence behind AuthorRefCounts, and no comment
// here should be read as claiming that. It derives from the SAME (book, author)
// pair set that AuthorRefCounts builds, so fileRefs[a] > 0 implies refs[a] > 0,
// and purge-empty-authors consults refs first and skips the author outright.
// The gate cannot fire in production while that ordering holds. What this
// change actually buys:
//
//   - the number the op reports (ZeroBooksWithFiles) and any future caller that
//     does NOT have a ref gate in front of it stop being told 0 for a book with
//     files;
//   - the memdb arm refuses on a short book_files table. requireTablesComplete
//     here names memTableBookFiles, which AuthorRefCounts never looks at — this
//     is the one axis on which the two guards genuinely differ;
//   - the three silent-failure defects the display counter still carries (an
//     undecodable row skipped, iter.Error() unchecked, a GetBookFilesForIDsCore
//     error swallowed by `if ... err == nil`) are fatal here instead.
//
// NO MIN-1 FUDGE. The display counter scores a book with no files as 1 ("books
// with no files count as 1"). That is a display convenience and it makes the
// name a lie: require_zero_files and ZeroBooksWithFiles would then be non-zero
// for an author with no files at all. This counter counts files. Dropping the
// fudge cannot weaken the op, because the gate is only reached when the
// unfiltered ref count is already 0 and both counters share a pair set, so both
// are 0 there.
//
// MISSING FILES ARE STILL EXCLUDED, matching the display counter's
// memIdxMissing=false scan. Changing that is a different question from the one
// this file answers and is deliberately left alone.

// AuthorFileRefStore is a narrow capability interface, deliberately kept OUT of
// database.Store for the same reason as AuthorBookRefStore: widening Store
// forces every implementation and every generated mock to grow with it. Reach
// it through AsAuthorFileRefStore, which looks through the indexedStore
// decorator.
type AuthorFileRefStore interface {
	// GetAllAuthorFileRefCounts returns authorID -> number of non-missing
	// book_file rows belonging to books that reference it, via the book_authors
	// junction or the legacy Book.AuthorID field, counting soft-deleted and
	// non-primary books.
	GetAllAuthorFileRefCounts() (map[int]int, error)
}

// AsAuthorFileRefStore returns s as an AuthorFileRefStore, or nil if the
// backing store cannot answer the unfiltered question. Callers MUST nil-check
// and MUST fail rather than falling back to GetAllAuthorFileCounts — that
// fallback is the defect this file exists to remove, and it is silent.
//
// It goes through AsCapability, not a bare type assertion, because in
// production the store is wrapped in the Bleve indexedStore decorator and a
// bare `s.(*PebbleStore)` returns nil exactly where the guard matters.
func AsAuthorFileRefStore(s any) AuthorFileRefStore {
	if s == nil {
		return nil
	}
	if rs, ok := AsCapability[AuthorFileRefStore](s); ok {
		return rs
	}
	return nil
}

// GetAllAuthorFileRefCounts counts non-missing files per author over every book
// in the memdb that names the author, with NO deletion or primary-version
// filtering.
func (m *MemStore) GetAllAuthorFileRefCounts() (map[int]int, error) {
	// THREE tables, not the two AuthorRefCounts names. book_files joins the list
	// because it is the table this counter actually reports on: a lost book_file
	// row is a file that exists on disk and is invisible here, and "0 files" is
	// the permissive answer for the gate reading it.
	if err := m.requireTablesComplete("author file reference count",
		memTableBookAuthors, memTableBooks, memTableBookFiles); err != nil {
		return nil, err
	}

	txn := m.db.Txn(false)
	defer txn.Abort()

	bookAuthors, err := m.authorRefPairs(txn)
	if err != nil {
		return nil, err
	}

	out := make(map[int]int)
	fIter, err := txn.Get(memTableBookFiles, memIdxMissing, false)
	if err != nil {
		return nil, fmt.Errorf("memdb book_files scan: %w", err)
	}
	for obj := fIter.Next(); obj != nil; obj = fIter.Next() {
		bf := obj.(*BookFile)
		for _, authorID := range bookAuthors[bf.BookID] {
			out[authorID]++
		}
	}
	return out, nil
}

// authorRefPairs builds bookID -> the author IDs that book references, over the
// junction table and then the legacy Book.AuthorID field, deduplicated per
// (book, author) PAIR rather than per book. The pair dedup is load-bearing and
// author_bookref.go explains why: a book may carry junction rows that do not
// mention its own Book.AuthorID, so skipping the whole book in pass 2 would
// lose the legacy author's attachment.
//
// It takes the caller's read txn so the file scan above sees the same snapshot
// as the two passes here.
func (m *MemStore) authorRefPairs(txn *memdb.Txn) (map[string][]int, error) {
	bookAuthors := make(map[string][]int)
	seen := make(map[authorRefKey]bool)

	// Pass 1: every junction row, in any book state. The book row's flags are
	// exactly what must NOT influence this, so the book is never looked up.
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
		bookAuthors[ba.BookID] = append(bookAuthors[ba.BookID], ba.AuthorID)
	}

	// Pass 2: ALL books (memIdxID, not memIdxIsPrimaryVersion), for the legacy
	// AuthorID of any pair the junction did not already account for.
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
		bookAuthors[b.ID] = append(bookAuthors[b.ID], *b.AuthorID)
	}
	return bookAuthors, nil
}

// GetAllAuthorFileRefCounts FAILS CLOSED on a memdb known to be missing rows,
// and this is a DELIBERATE DIVERGENCE from GetAllAuthorBookRefCounts, which
// falls through to a full Pebble scan in the same situation.
//
// Two reasons the twin's fall-through must not be copied here:
//
//  1. It would be fail-OPEN, not slow-but-correct. GetBookFilesForIDsCore
//     (pebble_store_bookfiles.go) itself delegates to the memdb whenever the
//     memdb is warm. A fall-through would therefore read the complete book set
//     from Pebble and the SHORT file set from the very memdb it just refused,
//     undercount, and report "no files" for an author whose files are on disk.
//     The twin has no such second source and does not have this problem.
//  2. The trade is different. The twin justifies its fall-through by the cost of
//     refusing a per-REQUEST path (every DELETE /authors/:id). The only caller
//     here is a bulk, manual, dry-run-by-default maintenance op; stalling it
//     until the next restart is cheap, and it is the outcome a safety gate
//     should have.
//
// The Pebble scan below is therefore reached only when UseMemDB is off, where
// GetBookFilesForIDsCore reads Pebble too and the two halves agree.
func (p *PebbleStore) GetAllAuthorFileRefCounts() (map[int]int, error) {
	// Loaded ONCE: Reset can swap memPtr underneath us.
	if m := p.mem(); p.UseMemDB && m != nil {
		return m.GetAllAuthorFileRefCounts()
	}
	return p.getAllAuthorFileRefCountsPebble()
}

// getAllAuthorFileRefCountsPebble mirrors getAllAuthorBookRefCountsPebble's key
// ranges, snapshot and row-shape guards — see author_bookref.go for why each of
// them is what it is (prefixUpperBound over a hand-written "~" bound, the true
// "book:" .. "book;" range paired with the one-colon filter, one snapshot
// across both passes so a book created mid-scan cannot hide its junction rows)
// — and then counts files rather than books.
func (p *PebbleStore) getAllAuthorFileRefCountsPebble() (map[int]int, error) {
	bookAuthors := make(map[string][]int)
	seen := make(map[authorRefKey]bool)

	snap := p.db.NewSnapshot()
	defer func() { _ = snap.Close() }()

	jPrefix := []byte("book_authors:")
	jIter, err := snap.NewIter(&pebble.IterOptions{
		LowerBound: jPrefix,
		UpperBound: prefixUpperBound(jPrefix),
	})
	if err != nil {
		return nil, err
	}
	for jIter.First(); jIter.Valid(); jIter.Next() {
		var authors []BookAuthor
		if err := json.Unmarshal(jIter.Value(), &authors); err != nil {
			// FATAL, not skippable: a credit list we cannot decode may hold the
			// ONLY attachment for an author, and dropping it undercounts, which
			// is the permissive direction for every caller.
			badKey := string(jIter.Key())
			_ = jIter.Close()
			return nil, fmt.Errorf("author file ref scan: undecodable book_authors row %q: %w", badKey, err)
		}
		bookID := strings.TrimPrefix(string(jIter.Key()), "book_authors:")
		for _, a := range authors {
			k := authorRefKey{bookID: bookID, authorID: a.AuthorID}
			if seen[k] {
				continue
			}
			seen[k] = true
			bookAuthors[bookID] = append(bookAuthors[bookID], a.AuthorID)
		}
	}
	// End-of-range and an iteration error are indistinguishable without this: a
	// truncated map with a nil error answers "no files anywhere" to a gate that
	// deletes on the strength of it.
	if err := jIter.Error(); err != nil {
		_ = jIter.Close()
		return nil, fmt.Errorf("author file ref scan truncated over book_authors, refusing to answer from a partial count: %w", err)
	}
	if cErr := jIter.Close(); cErr != nil {
		return nil, fmt.Errorf("author file ref scan: closing book_authors iterator: %w", cErr)
	}

	iter, err := snap.NewIter(&pebble.IterOptions{
		LowerBound: []byte("book:"),
		UpperBound: []byte("book;"),
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
		// book:versiongroup:) that share the widened prefix range.
		if strings.Count(key, ":") != 1 {
			continue
		}
		var b Book
		if err := json.Unmarshal(iter.Value(), &b); err != nil {
			_ = iter.Close()
			return nil, fmt.Errorf("author file ref scan: undecodable book row %q: %w", key, err)
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
		bookAuthors[bookID] = append(bookAuthors[bookID], *b.AuthorID)
	}
	if err := iter.Error(); err != nil {
		_ = iter.Close()
		return nil, fmt.Errorf("author file ref scan truncated over books, refusing to answer from a partial count: %w", err)
	}
	if err := iter.Close(); err != nil {
		return nil, fmt.Errorf("author file ref scan: closing book iterator: %w", err)
	}

	bookIDs := make([]string, 0, len(bookAuthors))
	for id := range bookAuthors {
		bookIDs = append(bookIDs, id)
	}
	counts := make(map[int]int)
	if len(bookIDs) == 0 {
		return counts, nil
	}
	// PROPAGATED, not swallowed. The display counter writes
	// `if bfm, err := p.GetBookFilesForIDsCore(bookIDs); err == nil` and carries
	// on with an empty map, which reports zero files for the whole library on a
	// read error.
	filesByBook, err := p.GetBookFilesForIDsCore(bookIDs)
	if err != nil {
		return nil, fmt.Errorf("author file ref scan: loading book files: %w", err)
	}
	for bookID, authorIDs := range bookAuthors {
		n := 0
		for _, f := range filesByBook[bookID] {
			if f.Missing {
				continue
			}
			n++
		}
		if n == 0 {
			continue
		}
		for _, authorID := range authorIDs {
			counts[authorID] += n
		}
	}
	return counts, nil
}

// AuthorFileRefCounts returns, per author ID, how many non-missing files are
// held by books that reference it in ANY state — including books in the trash,
// non-primary (duplicate) versions, and co-authors credited only through the
// book_authors junction. An author absent from the map has no files reachable
// through any of those routes.
//
// It fails CLOSED, exactly like AuthorRefCounts: if the store cannot answer the
// unfiltered question, the caller must refuse to act rather than fall back to
// the filtered display counter, because a missing signal is not permission.
func AuthorFileRefCounts(store any) (map[int]int, error) {
	counter := AsAuthorFileRefStore(store)
	if counter == nil {
		return nil, fmt.Errorf("store cannot count unfiltered author file references (got %T); "+
			"refusing to evaluate the file-safety gate from a filtered display count, which "+
			"reports zero files for junction-only co-authors and for authors whose books are "+
			"all trashed or all non-primary", store)
	}
	return counter.GetAllAuthorFileRefCounts()
}
