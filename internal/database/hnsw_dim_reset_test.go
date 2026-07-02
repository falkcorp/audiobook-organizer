// file: internal/database/hnsw_dim_reset_test.go
// version: 1.0.0
// guid: 2f7b8c19-4d3e-4a60-9b1c-5e8f0a2d6b74
// last-edited: 2026-07-02

package database

import (
	"context"
	"testing"
)

// TestHNSWImport_DiscardsStaleDimSnapshot locks the embedding-backend cutover
// fix: a persisted HNSW snapshot whose dimension no longer matches the store's
// configured dimension (e.g. OpenAI 3072 -> local bge-m3 1024) must be
// discarded on Import, not loaded — otherwise coder/hnsw panics the moment a
// new-dimension vector is added. The index rebuilds empty at the new dimension.
func TestHNSWImport_DiscardsStaleDimSnapshot(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	// Snapshot written by a 3-dim store (stand-in for the old 3072-dim index).
	old := NewHNSWEmbeddingStore(3)
	if err := old.Upsert(ctx, "book", "B1", []float32{1, 0, 0}, nil); err != nil {
		t.Fatalf("upsert into old store: %v", err)
	}
	if err := old.Export(dir); err != nil {
		t.Fatalf("export: %v", err)
	}

	// A new store configured for a DIFFERENT dimension imports that snapshot.
	newStore := NewHNSWEmbeddingStore(4)
	if err := newStore.Import(dir); err != nil {
		t.Fatalf("import: %v", err)
	}

	// The stale 3-dim book graph must have been discarded.
	n, err := newStore.CountByType(ctx, "book")
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Fatalf("expected stale-dim snapshot discarded (count 0), got %d", n)
	}

	// And a correctly-dimensioned vector must now insert without panicking.
	if err := newStore.Upsert(ctx, "book", "B2", []float32{1, 0, 0, 0}, nil); err != nil {
		t.Fatalf("upsert new-dim vector after discard: %v", err)
	}
	if n, _ := newStore.CountByType(ctx, "book"); n != 1 {
		t.Fatalf("expected 1 after new-dim upsert, got %d", n)
	}
}

// TestHNSWImport_KeepsMatchingDimSnapshot ensures a same-dimension snapshot is
// still loaded normally (no regression to the happy path).
func TestHNSWImport_KeepsMatchingDimSnapshot(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	old := NewHNSWEmbeddingStore(3)
	if err := old.Upsert(ctx, "book", "B1", []float32{1, 0, 0}, nil); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := old.Export(dir); err != nil {
		t.Fatalf("export: %v", err)
	}

	same := NewHNSWEmbeddingStore(3)
	if err := same.Import(dir); err != nil {
		t.Fatalf("import: %v", err)
	}
	if n, _ := same.CountByType(ctx, "book"); n != 1 {
		t.Fatalf("matching-dim snapshot should load (count 1), got %d", n)
	}
}
