// file: internal/dedup/hydrate_chromem_test.go
// version: 1.2.0
// guid: 3c4d5e6f-7a8b-9c0d-1e2f-3a4b5c6d7e8f
// last-edited: 2026-08-29

package dedup

import (
	"context"
	"errors"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/ai"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// fakeVectorANNStore is a minimal in-memory database.VectorANNStore double
// that records every Upsert call so tests can assert which entities were (or
// were not) mirrored during HydrateChromem.
type fakeVectorANNStore struct {
	upserted map[string]bool // "entityType/entityID" -> seen
}

func newFakeVectorANNStore() *fakeVectorANNStore {
	return &fakeVectorANNStore{upserted: map[string]bool{}}
}

func (f *fakeVectorANNStore) Upsert(_ context.Context, entityType, entityID string, _ []float32, _ map[string]string) error {
	f.upserted[entityType+"/"+entityID] = true
	return nil
}
func (f *fakeVectorANNStore) Get(_ context.Context, _, _ string) (map[string]string, error) {
	return nil, nil
}
func (f *fakeVectorANNStore) Delete(_ context.Context, _, _ string) error { return nil }
func (f *fakeVectorANNStore) FindSimilar(_ context.Context, _ string, _ []float32, _ int, _ map[string]string) ([]database.ChromemSimilarityResult, error) {
	return nil, nil
}
func (f *fakeVectorANNStore) CountByType(_ context.Context, _ string) (int, error) { return 0, nil }
func (f *fakeVectorANNStore) Close() error                                         { return nil }

// TestHydrateChromem_SkipsStaleModelRows verifies the guard added after the
// bge-m3 cutover: a row stamped with a different model than the currently
// wired embed client is skipped instead of being mirrored into the ANN
// store, where it would only fail the store's dimension check and log a
// warning. Covers both the book and author loops, and both the "stale but
// re-embeddable" case (a book/author that still exists) and the "orphaned"
// case (an author ID that no longer resolves via GetAuthorByID, e.g. after a
// merge) — HydrateChromem should skip the stale row before ever needing to
// look the entity up.
func TestHydrateChromem_SkipsStaleModelRows(t *testing.T) {
	engine, mock, es := setupTestEngine(t)
	fake := newFakeVectorANNStore()
	engine.SetChromemStore(fake)
	engine.embedClient = ai.NewEmbeddingClientWithOptions("k", "bge-m3", "")

	primary := true
	currentBook := &database.Book{ID: "BOOK_CURRENT", Title: "Current Model Book", IsPrimaryVersion: &primary}
	mock.GetBookByIDFunc = func(id string) (*database.Book, error) {
		if id == "BOOK_CURRENT" {
			return currentBook, nil
		}
		// BOOK_STALE deliberately still resolves — the row must be skipped
		// on the model check alone, without ever reaching the book lookup.
		return &database.Book{ID: id, Title: "Stale Model Book", IsPrimaryVersion: &primary}, nil
	}

	if err := es.Upsert(database.Embedding{EntityType: "book", EntityID: "BOOK_CURRENT", Vector: []float32{1, 2, 3, 4}, Model: "bge-m3"}); err != nil {
		t.Fatalf("seed current-model book: %v", err)
	}
	if err := es.Upsert(database.Embedding{EntityType: "book", EntityID: "BOOK_STALE", Vector: []float32{1, 2, 3}, Model: "text-embedding-3-large"}); err != nil {
		t.Fatalf("seed stale-model book: %v", err)
	}

	// AUTHOR_ORPHAN mimics the real prod scenario: an embedding row exists
	// for an author ID that no longer exists (merged/deleted), so it isn't
	// in the entity table at all. The guard must skip it purely on model
	// mismatch, never needing to resolve the author.
	if err := es.Upsert(database.Embedding{EntityType: "author", EntityID: "9999", Vector: []float32{5, 6, 7, 8}, Model: "bge-m3"}); err != nil {
		t.Fatalf("seed current-model author: %v", err)
	}
	if err := es.Upsert(database.Embedding{EntityType: "author", EntityID: "AUTHOR_ORPHAN", Vector: []float32{5, 6, 7}, Model: "text-embedding-3-large"}); err != nil {
		t.Fatalf("seed orphaned stale-model author: %v", err)
	}

	stats, err := engine.HydrateChromem(context.Background())
	if err != nil {
		t.Fatalf("HydrateChromem: %v", err)
	}

	if stats.BooksHydrated != 1 {
		t.Errorf("BooksHydrated = %d, want 1", stats.BooksHydrated)
	}
	if stats.AuthorsHydrated != 1 {
		t.Errorf("AuthorsHydrated = %d, want 1", stats.AuthorsHydrated)
	}
	if stats.BooksStaleModel != 1 {
		t.Errorf("BooksStaleModel = %d, want 1", stats.BooksStaleModel)
	}
	if stats.AuthorsStaleModel != 1 {
		t.Errorf("AuthorsStaleModel = %d, want 1", stats.AuthorsStaleModel)
	}
	if !fake.upserted["book/BOOK_CURRENT"] {
		t.Error("expected current-model book to be mirrored into the ANN store")
	}
	if fake.upserted["book/BOOK_STALE"] {
		t.Error("stale-model book should NOT have been mirrored into the ANN store")
	}
	if !fake.upserted["author/9999"] {
		t.Error("expected current-model author to be mirrored into the ANN store")
	}
	if fake.upserted["author/AUTHOR_ORPHAN"] {
		t.Error("orphaned stale-model author should NOT have been mirrored into the ANN store")
	}
}

// TestHydrateChromem_AccountsForEverySkipPath is the fixture that makes the
// 2026-08-29 defect observable. Production read 39,658 book embedding rows and
// indexed 17,706 of them; 21,952 disappeared and exactly one (a stale-model
// row) was reported anywhere, because three of the four book skip paths
// incremented no counter and logged nothing.
//
// The fixture deliberately contains ONE row of EVERY book skip category plus
// two rows that must survive, so each counter can be asserted individually
// rather than inferred from a total:
//
//	BOOK_OK           primary=true             -> BooksHydrated
//	BOOK_NIL_PRIMARY  IsPrimaryVersion==nil    -> BooksHydrated (the DEFAULT
//	                                             case in production; a nil
//	                                             pointer is not "non-primary")
//	BOOK_EMPTY        zero-length vector       -> BooksEmptyVector
//	BOOK_STALE        model=text-embedding-3-* -> BooksStaleModel
//	BOOK_ORPHAN       GetBookByID -> (nil,nil) -> BooksOrphaned
//	BOOK_LOOKUP_ERR   GetBookByID -> (nil,err) -> BooksLookupError
//	BOOK_NONPRIMARY   primary=false            -> BooksNonPrimary
//
// plus, on the author side, one row per author skip path and one that lands.
//
// The counts are asserted one bucket at a time AND as the arithmetic identity
// BookRows == BooksAccounted(). The identity is what catches a future skip
// path added without a counter — the exact defect class this test exists for —
// while the individual assertions catch a counter incremented into the wrong
// bucket, which the identity alone would not notice.
func TestHydrateChromem_AccountsForEverySkipPath(t *testing.T) {
	engine, mock, es := setupTestEngine(t)
	fake := newFakeVectorANNStore()
	engine.SetChromemStore(fake)
	engine.embedClient = ai.NewEmbeddingClientWithOptions("k", "bge-m3", "")

	primary, nonPrimary := true, false
	lookupErr := errors.New("pebble: simulated read fault")

	// Keyed explicitly — a catch-all returning a book for any unrecognised ID
	// (as the sibling test above does) would silently reclassify the orphan
	// row as hydrated and this test would prove nothing.
	mock.GetBookByIDFunc = func(id string) (*database.Book, error) {
		switch id {
		case "BOOK_OK":
			return &database.Book{ID: id, Title: "Good", IsPrimaryVersion: &primary}, nil
		case "BOOK_NIL_PRIMARY":
			// IsPrimaryVersion left nil on purpose: nil serializes as ABSENT
			// and is the default across most of the production library.
			return &database.Book{ID: id, Title: "Unset Primary Flag"}, nil
		case "BOOK_NONPRIMARY":
			return &database.Book{ID: id, Title: "Alt Version", IsPrimaryVersion: &nonPrimary}, nil
		case "BOOK_LOOKUP_ERR":
			return nil, lookupErr
		case "BOOK_ORPHAN":
			// PebbleStore.GetBookByID maps pebble.ErrNotFound to a bare
			// (nil, nil) — no sentinel — so this is production's real
			// not-found shape, not a mock-only convenience.
			return nil, nil
		}
		t.Errorf("unexpected GetBookByID(%q) — the stale-model and empty-vector rows must be skipped before the lookup", id)
		return nil, nil
	}

	seed := func(entityType, entityID string, vec []float32, model string) {
		t.Helper()
		if err := es.Upsert(database.Embedding{EntityType: entityType, EntityID: entityID, Vector: vec, Model: model}); err != nil {
			t.Fatalf("seed %s/%s: %v", entityType, entityID, err)
		}
	}

	vec := []float32{1, 2, 3, 4}
	seed("book", "BOOK_OK", vec, "bge-m3")
	seed("book", "BOOK_NIL_PRIMARY", vec, "bge-m3")
	seed("book", "BOOK_EMPTY", nil, "bge-m3")
	seed("book", "BOOK_STALE", vec, "text-embedding-3-large")
	seed("book", "BOOK_ORPHAN", vec, "bge-m3")
	seed("book", "BOOK_LOOKUP_ERR", vec, "bge-m3")
	seed("book", "BOOK_NONPRIMARY", vec, "bge-m3")

	seed("author", "100", vec, "bge-m3")
	seed("author", "101", nil, "bge-m3")
	seed("author", "102", vec, "text-embedding-3-large")

	// Confirm the fixture can actually observe each bucket before trusting the
	// counters: an empty-vector row has to survive the encode/decode round
	// trip as zero-length, or that bucket is untestable through this seam.
	rows, err := es.ListByType("book")
	if err != nil {
		t.Fatalf("ListByType(book): %v", err)
	}
	if len(rows) != 7 {
		t.Fatalf("fixture seeded %d book rows, want 7", len(rows))
	}
	for _, r := range rows {
		if r.EntityID == "BOOK_EMPTY" && len(r.Vector) != 0 {
			t.Fatalf("fixture cannot observe the empty-vector bucket: BOOK_EMPTY round-tripped with %d elements", len(r.Vector))
		}
	}

	stats, err := engine.HydrateChromem(context.Background())
	if err != nil {
		t.Fatalf("HydrateChromem: %v", err)
	}

	// --- every bucket, individually ---
	for _, tc := range []struct {
		name string
		got  int
		want int
	}{
		{"BookRows", stats.BookRows, 7},
		{"BooksHydrated", stats.BooksHydrated, 2},
		{"BooksEmptyVector", stats.BooksEmptyVector, 1},
		{"BooksStaleModel", stats.BooksStaleModel, 1},
		{"BooksOrphaned", stats.BooksOrphaned, 1},
		{"BooksLookupError", stats.BooksLookupError, 1},
		{"BooksNonPrimary", stats.BooksNonPrimary, 1},
		{"AuthorRows", stats.AuthorRows, 3},
		{"AuthorsHydrated", stats.AuthorsHydrated, 1},
		{"AuthorsEmptyVector", stats.AuthorsEmptyVector, 1},
		{"AuthorsStaleModel", stats.AuthorsStaleModel, 1},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %d, want %d", tc.name, tc.got, tc.want)
		}
	}

	// --- the arithmetic identity: nothing may vanish unnamed ---
	if stats.BooksAccounted() != stats.BookRows {
		t.Errorf("book buckets do not account for every row: BooksAccounted()=%d, BookRows=%d (%d rows vanished into an uncounted skip path)",
			stats.BooksAccounted(), stats.BookRows, stats.BookRows-stats.BooksAccounted())
	}
	if stats.AuthorsAccounted() != stats.AuthorRows {
		t.Errorf("author buckets do not account for every row: AuthorsAccounted()=%d, AuthorRows=%d (%d rows vanished into an uncounted skip path)",
			stats.AuthorsAccounted(), stats.AuthorRows, stats.AuthorRows-stats.AuthorsAccounted())
	}
	if got, want := stats.BooksSkipped(), 5; got != want {
		t.Errorf("BooksSkipped() = %d, want %d", got, want)
	}
	if got, want := stats.AuthorsSkipped(), 2; got != want {
		t.Errorf("AuthorsSkipped() = %d, want %d", got, want)
	}

	// --- a bare count gives nobody anything to debug; keep the sample ---
	if stats.FirstBookLookupError == nil {
		t.Error("FirstBookLookupError = nil, want the sampled lookup fault")
	} else if !errors.Is(stats.FirstBookLookupError, lookupErr) {
		t.Errorf("FirstBookLookupError = %v, want it to wrap %v", stats.FirstBookLookupError, lookupErr)
	}

	// --- and the counters must describe what actually reached the index ---
	for _, id := range []string{"book/BOOK_OK", "book/BOOK_NIL_PRIMARY", "author/100"} {
		if !fake.upserted[id] {
			t.Errorf("expected %s to be mirrored into the ANN store", id)
		}
	}
	for _, id := range []string{
		"book/BOOK_EMPTY", "book/BOOK_STALE", "book/BOOK_ORPHAN",
		"book/BOOK_LOOKUP_ERR", "book/BOOK_NONPRIMARY",
		"author/101", "author/102",
	} {
		if fake.upserted[id] {
			t.Errorf("%s should NOT have been mirrored into the ANN store", id)
		}
	}
}
