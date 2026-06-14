// file: internal/dedup/engine_isbn_test.go
// version: 1.0.0
// guid: b3c4d5e6-f7a8-9012-bcde-f01234567890
// last-edited: 2026-06-14

// Tests for checkExactISBN: verifies that the indexed fast path is used when
// the isbn-index-build flag is set, and that the O(N) GetAllBooks fallback is
// used when the flag is absent.

package dedup

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeISBNIndexStore implements ISBNIndexStore for tests.
type fakeISBNIndexStore struct {
	built bool
	// getCallCount records how many times GetBookIDsByISBNASIN was called.
	getCallCount int
	// returnIDs is the set of book IDs to return from GetBookIDsByISBNASIN.
	returnIDs []string
}

func (f *fakeISBNIndexStore) IsISBNIndexBuilt() bool { return f.built }
func (f *fakeISBNIndexStore) GetBookIDsByISBNASIN(isbn10, isbn13, asin string) ([]string, error) {
	f.getCallCount++
	return f.returnIDs, nil
}

// plausibleBook returns a Book with enough fields set for hasPlausibleAudio to
// return true (duration > 0 and a file hash).
func plausibleBook(id, isbn13 string) *database.Book {
	dur := 3600
	marked := false
	return &database.Book{
		ID:                id,
		Title:             "Book " + id,
		FilePath:          "/audio/" + id + ".m4b",
		ISBN13:            strPtr(isbn13),
		Duration:          &dur,
		MarkedForDeletion: &marked,
		FileHash:          strPtr("hash-" + id),
	}
}

// TestCheckExactISBN_IndexedPathUsed verifies that when the isbn-index is built
// and ISBNIndexStore is wired, checkExactISBN calls GetBookIDsByISBNASIN instead
// of GetAllBooks.
func TestCheckExactISBN_IndexedPathUsed(t *testing.T) {
	engine, mock, es := setupTestEngine(t)

	bookA := plausibleBook("BOOK_A", "9780000000001")
	bookB := plausibleBook("BOOK_B", "9780000000001") // same ISBN13 as A

	// Wire the fake index store — index is built, returns bookB as the match.
	fakeIdx := &fakeISBNIndexStore{
		built:     true,
		returnIDs: []string{"BOOK_B"},
	}
	engine.SetISBNIndexStore(fakeIdx)

	// GetAllBooks must NOT be called when indexed path is used.
	getAllCallCount := 0
	mock.GetAllBooksFunc = func(limit, offset int) ([]database.Book, error) {
		getAllCallCount++
		return nil, nil
	}
	mock.GetBookByIDFunc = func(id string) (*database.Book, error) {
		switch id {
		case "BOOK_B":
			return bookB, nil
		}
		return nil, nil
	}

	err := engine.checkExactISBN(bookA)
	require.NoError(t, err)

	assert.Equal(t, 0, getAllCallCount, "GetAllBooks must not be called when indexed path is active")
	assert.Equal(t, 1, fakeIdx.getCallCount, "GetBookIDsByISBNASIN must be called exactly once")

	// A dedup candidate should have been created.
	candidates, total, err := es.ListCandidates(database.CandidateFilter{EntityType: "book"})
	require.NoError(t, err)
	assert.Equal(t, 1, total, "expected 1 candidate from indexed ISBN match")
	if len(candidates) > 0 {
		c := candidates[0]
		assert.Equal(t, "exact", c.Layer)
		assert.Equal(t, "pending", c.Status)
		// EntityAID and EntityBID may be in either order.
		ids := []string{c.EntityAID, c.EntityBID}
		assert.Contains(t, ids, "BOOK_A")
		assert.Contains(t, ids, "BOOK_B")
	}
}

// TestCheckExactISBN_FallbackWhenIndexNotBuilt verifies that when the isbn-index
// flag is NOT set (IsISBNIndexBuilt returns false), checkExactISBN falls back to
// GetAllBooks and does NOT call GetBookIDsByISBNASIN.
func TestCheckExactISBN_FallbackWhenIndexNotBuilt(t *testing.T) {
	engine, mock, es := setupTestEngine(t)

	bookA := plausibleBook("BOOK_A", "9780000000001")
	bookB := plausibleBook("BOOK_B", "9780000000001")

	// Wire a fake index where built=false.
	fakeIdx := &fakeISBNIndexStore{built: false}
	engine.SetISBNIndexStore(fakeIdx)

	getAllCallCount := 0
	mock.GetAllBooksFunc = func(limit, offset int) ([]database.Book, error) {
		getAllCallCount++
		if offset == 0 {
			return []database.Book{*bookA, *bookB}, nil
		}
		return nil, nil
	}

	err := engine.checkExactISBN(bookA)
	require.NoError(t, err)

	assert.Greater(t, getAllCallCount, 0, "GetAllBooks must be called on the fallback path")
	assert.Equal(t, 0, fakeIdx.getCallCount, "GetBookIDsByISBNASIN must not be called when index not built")

	// bookB shares the ISBN13 and is plausible — candidate should be emitted.
	_, total, err := es.ListCandidates(database.CandidateFilter{EntityType: "book"})
	require.NoError(t, err)
	assert.Equal(t, 1, total, "expected 1 candidate from scan-path ISBN match")
}

// TestCheckExactISBN_FallbackWhenNoIndexStore verifies that when ISBNIndexStore
// is not wired at all (nil), GetAllBooks is called.
func TestCheckExactISBN_FallbackWhenNoIndexStore(t *testing.T) {
	engine, mock, _ := setupTestEngine(t)
	// Do NOT call engine.SetISBNIndexStore — leave it nil.

	bookA := plausibleBook("BOOK_A", "9780000000001")

	getAllCallCount := 0
	mock.GetAllBooksFunc = func(limit, offset int) ([]database.Book, error) {
		getAllCallCount++
		return nil, nil
	}

	err := engine.checkExactISBN(bookA)
	require.NoError(t, err)

	assert.Greater(t, getAllCallCount, 0, "GetAllBooks must be called when ISBNIndexStore is nil")
}

// TestCheckExactISBN_SkipsSelf verifies that the indexed path does not emit a
// candidate when the only matching book is the anchor book itself.
func TestCheckExactISBN_SkipsSelf(t *testing.T) {
	engine, mock, es := setupTestEngine(t)

	bookA := plausibleBook("BOOK_A", "9780000000001")

	// Index returns BOOK_A itself (self-match).
	fakeIdx := &fakeISBNIndexStore{
		built:     true,
		returnIDs: []string{"BOOK_A"},
	}
	engine.SetISBNIndexStore(fakeIdx)

	mock.GetBookByIDFunc = func(id string) (*database.Book, error) {
		if id == "BOOK_A" {
			return bookA, nil
		}
		return nil, nil
	}

	err := engine.checkExactISBN(bookA)
	require.NoError(t, err)

	_, total, err := es.ListCandidates(database.CandidateFilter{EntityType: "book"})
	require.NoError(t, err)
	assert.Equal(t, 0, total, "self-match must not produce a candidate")
}

// TestCheckExactISBN_SkipsMarkedForDeletion verifies that the indexed path
// does not emit a candidate when the matched book is soft-deleted.
func TestCheckExactISBN_SkipsMarkedForDeletion(t *testing.T) {
	engine, mock, es := setupTestEngine(t)

	bookA := plausibleBook("BOOK_A", "9780000000001")

	// bookB exists but is soft-deleted.
	bookB := plausibleBook("BOOK_B", "9780000000001")
	markedTrue := true
	bookB.MarkedForDeletion = &markedTrue

	fakeIdx := &fakeISBNIndexStore{
		built:     true,
		returnIDs: []string{"BOOK_B"},
	}
	engine.SetISBNIndexStore(fakeIdx)

	mock.GetBookByIDFunc = func(id string) (*database.Book, error) {
		if id == "BOOK_B" {
			return bookB, nil
		}
		return nil, nil
	}

	err := engine.checkExactISBN(bookA)
	require.NoError(t, err)

	_, total, err := es.ListCandidates(database.CandidateFilter{EntityType: "book"})
	require.NoError(t, err)
	assert.Equal(t, 0, total, "soft-deleted book must not produce a candidate")
}

// TestCheckExactISBN_SkipsImplausibleAudio verifies that the indexed path does
// not emit a candidate when the matched book has no plausible audio (hasPlausibleAudio
// returns false — e.g. a stub entry with no duration).
func TestCheckExactISBN_SkipsImplausibleAudio(t *testing.T) {
	engine, mock, es := setupTestEngine(t)

	bookA := plausibleBook("BOOK_A", "9780000000001")

	// bookB has no duration — not plausible audio.
	marked := false
	bookB := &database.Book{
		ID:                "BOOK_B",
		Title:             "Book B",
		FilePath:          "/audio/BOOK_B.m4b",
		ISBN13:            strPtr("9780000000001"),
		MarkedForDeletion: &marked,
		// Duration intentionally left nil → hasPlausibleAudio = false
	}

	fakeIdx := &fakeISBNIndexStore{
		built:     true,
		returnIDs: []string{"BOOK_B"},
	}
	engine.SetISBNIndexStore(fakeIdx)

	mock.GetBookByIDFunc = func(id string) (*database.Book, error) {
		if id == "BOOK_B" {
			return bookB, nil
		}
		return nil, nil
	}

	err := engine.checkExactISBN(bookA)
	require.NoError(t, err)

	_, total, err := es.ListCandidates(database.CandidateFilter{EntityType: "book"})
	require.NoError(t, err)
	assert.Equal(t, 0, total, "book with no plausible audio must not produce a candidate")
}

// TestCheckExactISBN_AnchorImplausibleAudio verifies that if the anchor book
// itself has no plausible audio, checkExactISBN returns early without touching
// the index or GetAllBooks.
func TestCheckExactISBN_AnchorImplausibleAudio(t *testing.T) {
	engine, mock, _ := setupTestEngine(t)

	// Anchor has ISBN but no audio.
	marked := false
	bookA := &database.Book{
		ID:                "BOOK_A",
		Title:             "Book A",
		ISBN13:            strPtr("9780000000001"),
		MarkedForDeletion: &marked,
		// Duration nil → not plausible
	}

	fakeIdx := &fakeISBNIndexStore{built: true}
	engine.SetISBNIndexStore(fakeIdx)

	getAllCallCount := 0
	mock.GetAllBooksFunc = func(limit, offset int) ([]database.Book, error) {
		getAllCallCount++
		return nil, nil
	}

	err := engine.checkExactISBN(bookA)
	require.NoError(t, err)

	assert.Equal(t, 0, fakeIdx.getCallCount, "GetBookIDsByISBNASIN must not be called for implausible anchor")
	assert.Equal(t, 0, getAllCallCount, "GetAllBooks must not be called for implausible anchor")
}
