// file: internal/dedup/hydrate_chromem_test.go
// version: 1.0.0
// guid: 3c4d5e6f-7a8b-9c0d-1e2f-3a4b5c6d7e8f
// last-edited: 2026-07-08

package dedup

import (
	"context"
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
func (f *fakeVectorANNStore) Close() error                                        { return nil }

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

	booksHydrated, authorsHydrated, err := engine.HydrateChromem(context.Background())
	if err != nil {
		t.Fatalf("HydrateChromem: %v", err)
	}

	if booksHydrated != 1 {
		t.Errorf("booksHydrated = %d, want 1", booksHydrated)
	}
	if authorsHydrated != 1 {
		t.Errorf("authorsHydrated = %d, want 1", authorsHydrated)
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
