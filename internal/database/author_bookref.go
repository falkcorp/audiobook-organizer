// file: internal/database/author_bookref.go
// version: 1.6.0
// guid: 436a4092-01fc-4768-b57c-942068cb726d
// last-edited: 2026-09-12

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
	buckets, err := m.GetAllAuthorBookRefBuckets()
	if err != nil {
		return nil, err
	}
	return sumAuthorRefBuckets(buckets), nil
}

// GetAllAuthorBookRefBuckets is GetAllAuthorBookRefCounts split by the state of
// the referencing book: live, trashed (soft-deleted), or dangling (a junction
// row whose book row no longer exists). The three buckets always sum to the
// flat count, because the flat count is now computed from them.
//
// Books are read FIRST so each junction pair can be classified as it is seen.
// The (book, author) pair dedup spans both passes exactly as it always has.
//
// It keeps the table-completeness refusal of the flat count, and that refusal
// matters more here than there: this method classifies a junction pair whose
// book row is absent as DANGLING, and the dup-merge guard does not hold an
// author back for dangling refs. A memdb that silently lost book rows would
// otherwise turn live references into dangling ones and switch the guard off.
func (m *MemStore) GetAllAuthorBookRefBuckets() (map[int]AuthorRefBuckets, error) {
	if err := m.requireTablesComplete("author reference count (live/trashed/dangling)", memTableBookAuthors, memTableBooks); err != nil {
		return nil, err
	}

	txn := m.db.Txn(false)
	defer txn.Abort()

	acc := newAuthorRefBucketAccumulator()

	bIter, err := txn.Get(memTableBooks, memIdxID)
	if err != nil {
		return nil, fmt.Errorf("memdb books scan: %w", err)
	}
	for obj := bIter.Next(); obj != nil; obj = bIter.Next() {
		acc.addBook(obj.(*Book), "")
	}

	baIter, err := txn.Get(memTableBookAuthors, memIdxID)
	if err != nil {
		return nil, fmt.Errorf("memdb book_authors scan: %w", err)
	}
	for obj := baIter.Next(); obj != nil; obj = baIter.Next() {
		ba := obj.(*BookAuthor)
		acc.addJunction(ba.BookID, ba.AuthorID)
	}
	return acc.finish(), nil
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
// stalling until the next restart.
//
// The cost is NOT once-off, and an earlier version of this comment said it was.
// lostRows is sticky for the life of the process: the only things that clear it
// are publishLostRows and Reset, and nothing re-warms in steady state. So once
// anything taints the store, EVERY call takes the full two-keyspace Pebble scan
// until restart — including every DELETE /authors/:id, which is a per-request
// path. Deleting 50 authors through the UI after one taint is 50 full scans.
// That is still the right trade against deleting a referenced author, but it is
// a standing cost to be aware of, not a rare blip.
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
	buckets, err := p.GetAllAuthorBookRefBuckets()
	if err != nil {
		return nil, err
	}
	return sumAuthorRefBuckets(buckets), nil
}

// GetAllAuthorBookRefBuckets is the bucketed form of GetAllAuthorBookRefCounts,
// with the same memdb-first, fail-closed fallthrough: ErrMemdbIncomplete drops
// to the authoritative Pebble scan, any other memdb error propagates.
func (p *PebbleStore) GetAllAuthorBookRefBuckets() (map[int]AuthorRefBuckets, error) {
	if m := p.mem(); p.UseMemDB && m != nil {
		buckets, err := m.GetAllAuthorBookRefBuckets()
		if err == nil {
			return buckets, nil
		}
		if !errors.Is(err, ErrMemdbIncomplete) {
			return nil, err
		}
		slog.Error("author ref buckets: memdb is missing rows and will stay short until restart; falling through to the authoritative Pebble scan",
			"error", err, "lost_rows", m.LostRows())
	}
	return p.getAllAuthorBookRefBucketsPebble()
}

// getAllAuthorBookRefCountsPebble mirrors GetAllAuthorBookCounts's key ranges
// and row-shape guards, minus every IsPrimaryVersion / soft-delete filter, and
// dedups per (book, author) pair rather than per book.
func (p *PebbleStore) getAllAuthorBookRefCountsPebble() (map[int]int, error) {
	buckets, err := p.getAllAuthorBookRefBucketsPebble()
	if err != nil {
		return nil, err
	}
	return sumAuthorRefBuckets(buckets), nil
}

// getAllAuthorBookRefBucketsPebble is the authoritative scan behind both the
// flat and the bucketed count. One snapshot, two passes, books FIRST so every
// junction pair can be classified by the state of its book as it is read.
//
// Pass order changed on 2026-09-12 (was junction first). The pair dedup is
// order-independent -- a (book, author) pair is counted once whichever pass
// sees it first -- and a pair seen by both passes belongs to one book, so it
// lands in the same bucket either way. The flat count is therefore unchanged.
//
// Pass 1 bounds are the true "book:" prefix range with the strings.Count(key,
// ":") != 1 structural filter. The narrower ["book:0", "book:;") range that
// used to be here missed caller-supplied letter- or "_"-leading book IDs and
// lost their legacy AuthorID reference; widening the bounds admits the
// secondary indexes (book:path:, book:hash:, book:versiongroup:), whose values
// are bare IDs, which is why bounds and filter are one change. Same fix as
// series_bookref.go and pebble_store_versiongroup_backfill.go.
//
// Both passes fail CLOSED: an undecodable row or an iterator error aborts the
// count rather than answering short, because the callers delete on a short
// answer.
func (p *PebbleStore) getAllAuthorBookRefBucketsPebble() (map[int]AuthorRefBuckets, error) {
	acc := newAuthorRefBucketAccumulator()

	snap := p.db.NewSnapshot()
	defer func() { _ = snap.Close() }()

	// Pass 1: every book row -- its state, and its legacy AuthorID.
	iter, err := snap.NewIter(&pebble.IterOptions{
		LowerBound: []byte("book:"),
		UpperBound: []byte("book;"),
	})
	if err != nil {
		return nil, err
	}
	for iter.First(); iter.Valid(); iter.Next() {
		key := string(iter.Key())
		if !strings.HasPrefix(key, "book:") || strings.Count(key, ":") != 1 {
			continue
		}
		var b Book
		if err := json.Unmarshal(iter.Value(), &b); err != nil {
			_ = iter.Close()
			return nil, fmt.Errorf("author ref scan: undecodable book row %q: %w", key, err)
		}
		acc.addBook(&b, strings.TrimPrefix(key, "book:"))
	}
	if err := iter.Error(); err != nil {
		_ = iter.Close()
		return nil, fmt.Errorf("author ref scan truncated over books, refusing to answer from a partial count: %w", err)
	}
	if err := iter.Close(); err != nil {
		return nil, fmt.Errorf("author ref scan: closing book iterator: %w", err)
	}

	// Pass 2: the book_authors junction, which is the only record of a
	// co-author credit (Book.AuthorID holds the first author and nothing else).
	// An undecodable junction row is FATAL: it may credit an author that would
	// otherwise be counted as unreferenced and deleted.
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
			badKey := string(jIter.Key())
			_ = jIter.Close()
			return nil, fmt.Errorf("author ref scan: undecodable book_authors row %q: %w", badKey, err)
		}
		bookID := strings.TrimPrefix(string(jIter.Key()), "book_authors:")
		for _, a := range authors {
			acc.addJunction(bookID, a.AuthorID)
		}
	}
	if err := jIter.Error(); err != nil {
		_ = jIter.Close()
		return nil, fmt.Errorf("author ref scan truncated over book_authors, refusing to answer from a partial count: %w", err)
	}
	if cErr := jIter.Close(); cErr != nil {
		return nil, fmt.Errorf("author ref scan: closing book_authors iterator: %w", cErr)
	}
	return acc.finish(), nil
}

// AuthorRefBuckets splits one author's unfiltered reference count by the state
// of the referencing book. Live + Trashed + Dangling is exactly the value
// AuthorRefCounts reports for that author.
//
//   - Live: the book row exists and is not in the trash (primary or not).
//   - Trashed: the book row exists and is soft-deleted. A relink must move
//     these too, or DeleteAuthor's junction sweep erases the credit.
//   - Dangling: a book_authors row whose book row no longer exists. Nothing
//     can relink these, and nothing needs to: DeleteAuthor's sweep removes
//     them, so they must not hold a merge back.
type AuthorRefBuckets struct {
	Live     int
	Trashed  int
	Dangling int
}

// Total is the flat AuthorRefCounts value.
func (b AuthorRefBuckets) Total() int { return b.Live + b.Trashed + b.Dangling }

// Resolvable is the references a relink can see and must move: every
// reference whose book row still exists, in or out of the trash.
func (b AuthorRefBuckets) Resolvable() int { return b.Live + b.Trashed }

// AuthorBookRefBucketStore is the bucketed twin of AuthorBookRefStore.
type AuthorBookRefBucketStore interface {
	GetAllAuthorBookRefBuckets() (map[int]AuthorRefBuckets, error)
}

// AsAuthorBookRefBucketStore resolves the capability through the decorator
// chain, like AsAuthorBookRefStore.
func AsAuthorBookRefBucketStore(s any) AuthorBookRefBucketStore {
	if s == nil {
		return nil
	}
	if rs, ok := AsCapability[AuthorBookRefBucketStore](s); ok {
		return rs
	}
	return nil
}

// AuthorRefBucketCounts is AuthorRefCounts split into live, trashed and
// dangling references. It fails CLOSED exactly like AuthorRefCounts: a store
// that cannot answer is an error, never an empty map.
func AuthorRefBucketCounts(store any) (map[int]AuthorRefBuckets, error) {
	rs := AsAuthorBookRefBucketStore(store)
	if rs == nil {
		return nil, fmt.Errorf("store cannot bucket unfiltered author references (got %T); "+
			"refusing to merge from a filtered count, which silently strands "+
			"books whose author is trashed, non-primary, or a junction-only co-author", store)
	}
	return rs.GetAllAuthorBookRefBuckets()
}

type authorBookState uint8

const (
	authorBookLive authorBookState = iota + 1
	authorBookTrashed
)

// authorRefBucketAccumulator is the one classification rule both the memdb and
// the Pebble scan use, so the two cannot disagree on what a bucket means.
// Books must be added before junction rows: a junction pair whose book was
// never added is classified as dangling.
type authorRefBucketAccumulator struct {
	state map[string]authorBookState
	seen  map[authorRefKey]bool
	out   map[int]AuthorRefBuckets
}

func newAuthorRefBucketAccumulator() *authorRefBucketAccumulator {
	return &authorRefBucketAccumulator{
		state: make(map[string]authorBookState),
		seen:  make(map[authorRefKey]bool),
		out:   make(map[int]AuthorRefBuckets),
	}
}

// addBook records the book's state and counts its legacy AuthorID. keyID is
// the ID taken from the storage key, used when the row's own ID is empty.
func (a *authorRefBucketAccumulator) addBook(b *Book, keyID string) {
	bookID := b.ID
	if bookID == "" {
		bookID = keyID
	}
	st := authorBookLive
	if bookIsSoftDeleted(b) {
		st = authorBookTrashed
	}
	a.state[bookID] = st
	if b.AuthorID != nil {
		a.count(bookID, *b.AuthorID)
	}
}

func (a *authorRefBucketAccumulator) addJunction(bookID string, authorID int) {
	a.count(bookID, authorID)
}

func (a *authorRefBucketAccumulator) count(bookID string, authorID int) {
	k := authorRefKey{bookID: bookID, authorID: authorID}
	if a.seen[k] {
		return
	}
	a.seen[k] = true
	b := a.out[authorID]
	switch a.state[bookID] {
	case authorBookLive:
		b.Live++
	case authorBookTrashed:
		b.Trashed++
	default:
		b.Dangling++
	}
	a.out[authorID] = b
}

func (a *authorRefBucketAccumulator) finish() map[int]AuthorRefBuckets { return a.out }

func sumAuthorRefBuckets(buckets map[int]AuthorRefBuckets) map[int]int {
	out := make(map[int]int, len(buckets))
	for id, b := range buckets {
		out[id] = b.Total()
	}
	return out
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
