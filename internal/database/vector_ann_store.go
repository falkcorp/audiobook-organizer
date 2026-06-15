// file: internal/database/vector_ann_store.go
// version: 1.0.0
// guid: 5e6f7a8b-9c0d-1e2f-3a4b-5c6d7e8f9a0b
// last-edited: 2026-06-14

package database

import "context"

// VectorANNStore is the approximate-nearest-neighbor index used by the dedup
// engine's embedding layer. It abstracts the concrete vector backend so the
// engine can run against either the brute-force chromem-go store or the
// HNSW-graph store (coder/hnsw) without code changes.
//
// Both implementations are DERIVED, in-memory indexes hydrated from the
// authoritative per-entity vectors in the PebbleDB EmbeddingStore (emb:v:);
// neither is a source of truth. Method set and the ChromemSimilarityResult
// return type are kept identical to the original chromem store so callers are
// backend-agnostic.
//
// Implementations:
//   - *ChromemEmbeddingStore — brute-force O(n) cosine scan (default).
//   - *HNSWEmbeddingStore     — coder/hnsw graph, sub-linear search.
type VectorANNStore interface {
	// Upsert stores or replaces the vector + metadata for an entity.
	Upsert(ctx context.Context, entityType, entityID string, vec []float32, meta map[string]string) error
	// Get returns an entity's stored metadata, or (nil, nil) if absent.
	Get(ctx context.Context, entityType, entityID string) (map[string]string, error)
	// Delete removes an entity's vector + metadata. Absent keys are a no-op.
	Delete(ctx context.Context, entityType, entityID string) error
	// FindSimilar returns up to maxResults nearest neighbors by cosine
	// similarity, restricted to entities whose metadata matches every key/value
	// in filter (nil filter = no restriction), sorted by descending similarity.
	FindSimilar(ctx context.Context, entityType string, query []float32, maxResults int, filter map[string]string) ([]ChromemSimilarityResult, error)
	// CountByType returns the number of indexed entities of the given type.
	CountByType(ctx context.Context, entityType string) (int, error)
	// Close releases any resources held by the store.
	Close() error
}

// Compile-time assertion: the chromem store satisfies the interface.
var _ VectorANNStore = (*ChromemEmbeddingStore)(nil)
