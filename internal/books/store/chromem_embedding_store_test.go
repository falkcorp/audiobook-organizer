// file: internal/books/store/chromem_embedding_store_test.go
// version: 1.0.1
// guid: 3e1f2a0b-4c5d-4a70-b8c5-3d7e0f1b9a99

package store

import (
	"context"
	"math"
	"testing"
)

func TestChromem_UpsertAndQuery(t *testing.T) {
	store := NewInMemoryChromemStore(4)
	ctx := context.Background()

	vec1 := []float32{1, 0, 0, 0}
	vec2 := []float32{0, 1, 0, 0}
	vec3 := []float32{0.9, 0.1, 0, 0}

	if err := store.Upsert(ctx, "book", "b1", vec1, map[string]string{"is_primary": "true"}); err != nil {
		t.Fatalf("upsert b1: %v", err)
	}
	if err := store.Upsert(ctx, "book", "b2", vec2, map[string]string{"is_primary": "true"}); err != nil {
		t.Fatalf("upsert b2: %v", err)
	}
	if err := store.Upsert(ctx, "book", "b3", vec3, map[string]string{"is_primary": "false"}); err != nil {
		t.Fatalf("upsert b3: %v", err)
	}

	// Query for vectors similar to vec1 — should rank b3 (0.9 similarity) above b2 (0.0).
	results, err := store.FindSimilar(ctx, "book", vec1, 10, nil)
	if err != nil {
		t.Fatalf("find similar: %v", err)
	}
	if len(results) < 2 {
		t.Fatalf("expected at least 2 results, got %d", len(results))
	}
	// b1 is the query itself (similarity ~1.0), b3 should be next.
	found := false
	for _, r := range results {
		if r.EntityID == "b3" {
			found = true
			if r.Similarity < 0.8 {
				t.Errorf("b3 similarity = %f, want >= 0.8", r.Similarity)
			}
		}
	}
	if !found {
		t.Error("b3 not in results")
	}
}

func TestChromem_MetadataFilter(t *testing.T) {
	store := NewInMemoryChromemStore(4)
	ctx := context.Background()

	vec := []float32{1, 0, 0, 0}
	if err := store.Upsert(ctx, "book", "b1", vec, map[string]string{"is_primary": "true"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Upsert(ctx, "book", "b2", vec, map[string]string{"is_primary": "false"}); err != nil {
		t.Fatal(err)
	}

	results, err := store.FindSimilar(ctx, "book", vec, 10, map[string]string{"is_primary": "true"})
	if err != nil {
		t.Fatalf("query with filter: %v", err)
	}
	for _, r := range results {
		if r.EntityID == "b2" {
			t.Error("b2 (is_primary=false) should have been filtered out")
		}
	}
}

func TestChromem_Delete(t *testing.T) {
	store := NewInMemoryChromemStore(4)
	ctx := context.Background()

	vec := []float32{1, 0, 0, 0}
	_ = store.Upsert(ctx, "book", "b1", vec, nil)

	if err := store.Delete(ctx, "book", "b1"); err != nil {
		t.Fatalf("delete: %v", err)
	}

	meta, err := store.Get(ctx, "book", "b1")
	if err != nil {
		t.Fatalf("get after delete: %v", err)
	}
	if meta != nil {
		t.Error("expected nil after delete")
	}
}

func TestChromem_CountByType(t *testing.T) {
	store := NewInMemoryChromemStore(4)
	ctx := context.Background()

	vec := []float32{1, 0, 0, 0}
	_ = store.Upsert(ctx, "book", "b1", vec, nil)
	_ = store.Upsert(ctx, "book", "b2", vec, nil)
	_ = store.Upsert(ctx, "author", "a1", vec, nil)

	bookCount, _ := store.CountByType(ctx, "book")
	authorCount, _ := store.CountByType(ctx, "author")

	if bookCount != 2 {
		t.Errorf("book count = %d, want 2", bookCount)
	}
	if authorCount != 1 {
		t.Errorf("author count = %d, want 1", authorCount)
	}
}

// Silence unused import.
var _ = math.Abs
