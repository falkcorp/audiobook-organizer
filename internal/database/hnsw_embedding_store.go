// file: internal/database/hnsw_embedding_store.go
// version: 1.0.0
// guid: 6f7a8b9c-0d1e-2f3a-4b5c-6d7e8f9a0b1c
// last-edited: 2026-06-14

// HNSW-graph vector store (coder/hnsw) — a sub-linear ANN index alternative to
// the brute-force chromem store. Selected via config.VectorIndexBackend="hnsw".
//
// # Why
//
// chromem-go performs an exhaustive O(n·d) cosine scan per query. At ~68K
// vectors × 1024 dims a dedup full-scan (one query per book) is hours of CPU.
// HNSW gives ~O(log n) search; the dependency is pure Go (zero CGo —
// viterin/vek uses Go assembly), satisfying the project's embedded-DB
// constraint.
//
// # Design
//
// coder/hnsw's Graph stores vectors keyed by a comparable key (we use the
// string entityID) with NO metadata and NO internal locking, and its v0.6.1
// Search returns nodes without distances. This store therefore adds three
// things around it:
//
//   - one *hnsw.Graph per entityType ("book", "author"), lazily created;
//   - a metadata sidecar (entityType → id → meta) for filtered search + Get;
//   - a sync.RWMutex (Search under RLock, Add/Delete under Lock), because the
//     dedup engine mirrors writes while querying.
//
// FindSimilar over-fetches limit*overFetchFactor candidates, recomputes cosine
// similarity per node (1 - CosineDistance), applies the metadata filter, then
// returns the top `limit` — the over-fetch compensates for candidates dropped
// by the filter (e.g. non-primary versions).
//
// Like chromem, this is a DERIVED in-memory index hydrated from the PebbleDB
// EmbeddingStore on boot; it is not a source of truth. (On-disk persistence via
// Graph.Export/Import is a documented follow-up.)

package database

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/coder/hnsw"
)

const (
	// hnswM is the max neighbors per node. The library default (16) gives only
	// ~72% recall@10 on our data; 32 lifts it materially. Higher M = denser
	// graph = better recall at more memory (~M edges/node/layer). 32 is a good
	// balance for a dedup index where missing a duplicate matters.
	hnswM = 32
	// hnswEfSearch is the candidate-list (beam) size during search; higher =
	// better recall at more CPU. It MUST be ≥ the number of neighbors requested
	// from Search, otherwise the beam is narrower than the result set and recall
	// collapses. FindSimilar requests limit*overFetch neighbors (≤80 in the
	// common limit≤20 case); 200 covers that with ample headroom for recall.
	hnswEfSearch = 200
	// hnswOverFetchFactor multiplies the requested limit when searching, so the
	// metadata post-filter still has enough survivors to fill `limit`. The graph
	// has no native filtering, so non-matching neighbors must be fetched then
	// dropped. Kept modest (most books are primary versions, so few are filtered)
	// to keep the search k under EfSearch.
	hnswOverFetchFactor = 4
)

// HNSWEmbeddingStore is a coder/hnsw-backed VectorANNStore.
type HNSWEmbeddingStore struct {
	mu     sync.RWMutex
	graphs map[string]*hnsw.Graph[string]          // entityType → graph
	meta   map[string]map[string]map[string]string // entityType → id → metadata
	dims   int
}

// NewHNSWEmbeddingStore creates an empty HNSW store sized for `dims`-dimensional
// vectors. dims is advisory (used to reject mismatched inserts early); the graph
// itself infers dimensionality from the first vector.
func NewHNSWEmbeddingStore(dims int) *HNSWEmbeddingStore {
	return &HNSWEmbeddingStore{
		graphs: make(map[string]*hnsw.Graph[string]),
		meta:   make(map[string]map[string]map[string]string),
		dims:   dims,
	}
}

// graphFor returns the graph for entityType, creating it if needed.
// Caller must hold s.mu (write lock).
func (s *HNSWEmbeddingStore) graphFor(entityType string) *hnsw.Graph[string] {
	g, ok := s.graphs[entityType]
	if !ok {
		g = hnsw.NewGraph[string]()
		g.Distance = hnsw.CosineDistance
		g.M = hnswM
		g.EfSearch = hnswEfSearch
		s.graphs[entityType] = g
		s.meta[entityType] = make(map[string]map[string]string)
	}
	return g
}

// Upsert stores or replaces an entity's vector + metadata.
func (s *HNSWEmbeddingStore) Upsert(_ context.Context, entityType, entityID string, vec []float32, meta map[string]string) error {
	if entityID == "" {
		return fmt.Errorf("hnsw upsert: empty entityID")
	}
	if len(vec) == 0 {
		return fmt.Errorf("hnsw upsert %s: empty vector", entityID)
	}
	if s.dims > 0 && len(vec) != s.dims {
		return fmt.Errorf("hnsw upsert %s: vector dim %d != store dim %d", entityID, len(vec), s.dims)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	g := s.graphFor(entityType)
	// Add replaces an existing node with the same key.
	g.Add(hnsw.MakeNode(entityID, vec))
	if meta == nil {
		meta = map[string]string{}
	}
	// Store a defensive copy so the caller can't mutate our sidecar.
	cp := make(map[string]string, len(meta))
	for k, v := range meta {
		cp[k] = v
	}
	s.meta[entityType][entityID] = cp
	return nil
}

// Get returns a copy of an entity's metadata, or (nil, nil) if absent.
func (s *HNSWEmbeddingStore) Get(_ context.Context, entityType, entityID string) (map[string]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	byID, ok := s.meta[entityType]
	if !ok {
		return nil, nil
	}
	m, ok := byID[entityID]
	if !ok {
		return nil, nil
	}
	cp := make(map[string]string, len(m))
	for k, v := range m {
		cp[k] = v
	}
	return cp, nil
}

// Delete removes an entity's vector + metadata. Absent keys are a no-op.
func (s *HNSWEmbeddingStore) Delete(_ context.Context, entityType, entityID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if g, ok := s.graphs[entityType]; ok {
		g.Delete(entityID)
	}
	if byID, ok := s.meta[entityType]; ok {
		delete(byID, entityID)
	}
	return nil
}

// FindSimilar returns up to maxResults nearest neighbors by cosine similarity,
// restricted to entities whose metadata matches every key/value in filter.
func (s *HNSWEmbeddingStore) FindSimilar(
	_ context.Context,
	entityType string,
	query []float32,
	maxResults int,
	filter map[string]string,
) ([]ChromemSimilarityResult, error) {
	if maxResults <= 0 {
		maxResults = 20
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	g, ok := s.graphs[entityType]
	if !ok || g.Len() == 0 {
		return nil, nil
	}

	// Over-fetch so the metadata filter has enough survivors. Cap at graph size.
	k := maxResults * hnswOverFetchFactor
	if k > g.Len() {
		k = g.Len()
	}
	nodes := g.Search(query, k)

	byID := s.meta[entityType]
	out := make([]ChromemSimilarityResult, 0, len(nodes))
	for _, n := range nodes {
		m := byID[n.Key]
		if !metadataMatches(m, filter) {
			continue
		}
		// v0.6.1 Search returns no score — recompute cosine similarity.
		sim := 1 - hnsw.CosineDistance(query, n.Value)
		out = append(out, ChromemSimilarityResult{
			EntityID:   n.Key,
			Similarity: sim,
			Metadata:   m,
		})
	}

	// hnsw.Search already returns nearest-first, but recomputed scores + the
	// filter make an explicit sort safest. Stable so equal scores keep graph order.
	sort.SliceStable(out, func(i, j int) bool { return out[i].Similarity > out[j].Similarity })
	if len(out) > maxResults {
		out = out[:maxResults]
	}
	return out, nil
}

// CountByType returns the number of indexed entities of the given type.
func (s *HNSWEmbeddingStore) CountByType(_ context.Context, entityType string) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	g, ok := s.graphs[entityType]
	if !ok {
		return 0, nil
	}
	return g.Len(), nil
}

// Close is a no-op for the in-memory store.
func (s *HNSWEmbeddingStore) Close() error { return nil }

// metadataMatches reports whether m satisfies every key/value in filter.
// A nil/empty filter matches everything; a filter key absent from m fails.
func metadataMatches(m, filter map[string]string) bool {
	if len(filter) == 0 {
		return true
	}
	for k, want := range filter {
		if got, ok := m[k]; !ok || got != want {
			return false
		}
	}
	return true
}

// Compile-time assertion.
var _ VectorANNStore = (*HNSWEmbeddingStore)(nil)
