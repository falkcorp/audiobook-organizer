// file: internal/dedup/collectors_isbn_test.go
// version: 1.0.0
// guid: 5a1f7c92-6d84-4e3b-9f21-8c0a4b6e2d75
// last-edited: 2026-07-07

// Tests for CollectISBNASIN (scoring-path ISBN/ASIN collector): the indexed fast
// path must emit a signal set identical to the O(N) GetAllBooksCore scan, and
// both paths must honor context cancellation promptly. See PLAN.md / #19.

package dedup

import (
	"context"
	"sort"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/dedup/unified"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeISBNASINStore implements ISBNASINStore for collector tests: a fixed book
// list for the O(N) scan path plus an ID→Book map for indexed hydration.
type fakeISBNASINStore struct {
	all           []database.BookCore // returned by GetAllBooksCore (already deletion-filtered)
	byID          map[string]*database.Book
	getAllCalls   int
	getByIDCalls  int
}

func (f *fakeISBNASINStore) GetAllBooksCore(limit, offset int) ([]database.BookCore, error) {
	f.getAllCalls++
	if offset >= len(f.all) {
		return nil, nil
	}
	end := offset + limit
	if limit <= 0 || end > len(f.all) {
		end = len(f.all)
	}
	return f.all[offset:end], nil
}

func (f *fakeISBNASINStore) GetBookByID(id string) (*database.Book, error) {
	f.getByIDCalls++
	return f.byID[id], nil
}

// isbnBook builds a Book + matching BookCore fixture pair sharing the given IDs.
func isbnCore(id, isbn10, isbn13, asin string) database.BookCore {
	var i10, i13, a *string
	if isbn10 != "" {
		i10 = strPtr(isbn10)
	}
	if isbn13 != "" {
		i13 = strPtr(isbn13)
	}
	if asin != "" {
		a = strPtr(asin)
	}
	return database.BookCore{ID: id, ISBN10: i10, ISBN13: i13, ASIN: a}
}

func isbnFull(id, isbn10, isbn13, asin string) *database.Book {
	b := &database.Book{ID: id}
	if isbn10 != "" {
		b.ISBN10 = strPtr(isbn10)
	}
	if isbn13 != "" {
		b.ISBN13 = strPtr(isbn13)
	}
	if asin != "" {
		b.ASIN = strPtr(asin)
	}
	return b
}

// sigKeys returns a comparable, order-independent view of a signal set:
// "kind|confidence|evidence" per signal, sorted.
func sigKeys(sigs []unified.Signal) []string {
	keys := make([]string, 0, len(sigs))
	for _, s := range sigs {
		keys = append(keys, string(s.Kind)+"|"+s.Evidence)
	}
	sort.Strings(keys)
	return keys
}

// buildISBNFixture returns a store where BOOK_A shares ISBN13 with B, ISBN10 with
// C, and ASIN with D; E matches nothing. All non-A books are the "others".
func buildISBNFixture() (*fakeISBNASINStore, *database.Book, []string) {
	others := []database.BookCore{
		isbnCore("BOOK_B", "", "9780000000001", ""),      // ISBN13 match
		isbnCore("BOOK_C", "0000000010", "", ""),         // ISBN10 match
		isbnCore("BOOK_D", "", "", "B00ASIN0001"),        // ASIN match
		isbnCore("BOOK_E", "9999999999", "9789999999999", "B00NOPE0000"), // no match
	}
	bookA := isbnFull("BOOK_A", "0000000010", "9780000000001", "B00ASIN0001")
	store := &fakeISBNASINStore{
		all:  append([]database.BookCore{isbnCore("BOOK_A", "0000000010", "9780000000001", "B00ASIN0001")}, others...),
		byID: map[string]*database.Book{},
	}
	for _, c := range others {
		store.byID[c.ID] = isbnFull(c.ID, derefStr(c.ISBN10), derefStr(c.ISBN13), derefStr(c.ASIN))
	}
	// Matching IDs the index would return (B, C, D — not E, not self A).
	return store, bookA, []string{"BOOK_B", "BOOK_C", "BOOK_D"}
}

// TestCollectISBNASIN_IndexedMatchesScan is the core equivalence property: for the
// same fixture, the indexed fast path and the O(N) scan path emit an identical
// signal set, and each path touches only its own store method.
func TestCollectISBNASIN_IndexedMatchesScan(t *testing.T) {
	ctx := context.Background()

	// Scan path: index nil.
	scanStore, bookA, matchIDs := buildISBNFixture()
	scanSigs, err := CollectISBNASIN(ctx, scanStore, nil, bookA)
	require.NoError(t, err)
	assert.Positive(t, scanStore.getAllCalls, "scan path must call GetAllBooksCore")
	assert.Zero(t, scanStore.getByIDCalls, "scan path must not hydrate by ID")

	// Indexed path: index built, returns the matching IDs.
	idxStore, _, _ := buildISBNFixture()
	idx := &fakeISBNIndexStore{built: true, returnIDs: matchIDs}
	idxSigs, err := CollectISBNASIN(ctx, idxStore, idx, bookA)
	require.NoError(t, err)
	assert.Equal(t, 1, idx.getCallCount, "indexed path must query the index exactly once")
	assert.Zero(t, idxStore.getAllCalls, "indexed path must NOT full-scan")
	assert.Positive(t, idxStore.getByIDCalls, "indexed path must hydrate matches by ID")

	// Both paths must find exactly the 3 matches (B, C, D).
	assert.Len(t, scanSigs, 3, "scan path should find 3 ISBN/ASIN matches")
	assert.Equal(t, sigKeys(scanSigs), sigKeys(idxSigs), "indexed and scan signal sets must be identical")
	for _, s := range idxSigs {
		assert.Equal(t, unified.SigISBNASIN, s.Kind)
		assert.InDelta(t, 0.98, s.Confidence, 1e-9)
	}
}

// TestCollectISBNASIN_CtxCancel verifies prompt cancellation on both paths.
func TestCollectISBNASIN_CtxCancel(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	scanStore, bookA, matchIDs := buildISBNFixture()
	_, err := CollectISBNASIN(cancelled, scanStore, nil, bookA)
	require.ErrorIs(t, err, context.Canceled, "scan path must return promptly on cancelled ctx")

	idxStore, _, _ := buildISBNFixture()
	idx := &fakeISBNIndexStore{built: true, returnIDs: matchIDs}
	_, err = CollectISBNASIN(cancelled, idxStore, idx, bookA)
	require.ErrorIs(t, err, context.Canceled, "indexed path must return promptly on cancelled ctx")
}

// TestCollectISBNASIN_EmptyISBNEarlyReturn: a book with no ISBN/ASIN does no work.
func TestCollectISBNASIN_EmptyISBNEarlyReturn(t *testing.T) {
	store, _, _ := buildISBNFixture()
	idx := &fakeISBNIndexStore{built: true}
	sigs, err := CollectISBNASIN(context.Background(), store, idx, isbnFull("BOOK_X", "", "", ""))
	require.NoError(t, err)
	assert.Empty(t, sigs)
	assert.Zero(t, store.getAllCalls)
	assert.Zero(t, store.getByIDCalls)
	assert.Zero(t, idx.getCallCount)
}

// TestCollectISBNASIN_FallbackWhenIndexNotBuilt: index present but not built →
// scan path, index not queried.
func TestCollectISBNASIN_FallbackWhenIndexNotBuilt(t *testing.T) {
	store, bookA, _ := buildISBNFixture()
	idx := &fakeISBNIndexStore{built: false}
	sigs, err := CollectISBNASIN(context.Background(), store, idx, bookA)
	require.NoError(t, err)
	assert.Len(t, sigs, 3)
	assert.Positive(t, store.getAllCalls, "must fall back to scan when index not built")
	assert.Zero(t, idx.getCallCount, "index must not be queried when not built")
}
