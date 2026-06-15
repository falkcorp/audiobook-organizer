// file: internal/database/hnsw_embedding_store_test.go
// version: 1.0.0
// guid: 7a8b9c0d-1e2f-3a4b-5c6d-7e8f9a0b1c2d
// last-edited: 2026-06-14

package database

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"sync"
	"testing"
)

// unitVec returns a deterministic pseudo-random L2-normalized vector.
func unitVec(rng *rand.Rand, dim int) []float32 {
	v := make([]float32, dim)
	var norm float64
	for i := range v {
		x := float32(rng.NormFloat64())
		v[i] = x
		norm += float64(x) * float64(x)
	}
	norm = math.Sqrt(norm)
	if norm == 0 {
		v[0] = 1
		return v
	}
	for i := range v {
		v[i] = float32(float64(v[i]) / norm)
	}
	return v
}

func TestHNSW_UpsertGetDeleteCount(t *testing.T) {
	ctx := context.Background()
	s := NewHNSWEmbeddingStore(4)

	if n, _ := s.CountByType(ctx, "book"); n != 0 {
		t.Fatalf("empty store count = %d, want 0", n)
	}

	if err := s.Upsert(ctx, "book", "B1", []float32{1, 0, 0, 0}, map[string]string{"is_primary_version": "true"}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if n, _ := s.CountByType(ctx, "book"); n != 1 {
		t.Fatalf("count after upsert = %d, want 1", n)
	}

	meta, err := s.Get(ctx, "book", "B1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if meta["is_primary_version"] != "true" {
		t.Errorf("meta = %v, want is_primary_version=true", meta)
	}

	// Get on a missing id returns (nil, nil).
	if m, err := s.Get(ctx, "book", "NOPE"); err != nil || m != nil {
		t.Errorf("Get(missing) = (%v, %v), want (nil, nil)", m, err)
	}

	if err := s.Delete(ctx, "book", "B1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if n, _ := s.CountByType(ctx, "book"); n != 0 {
		t.Fatalf("count after delete = %d, want 0", n)
	}
	// Delete of a missing id is a no-op.
	if err := s.Delete(ctx, "book", "GONE"); err != nil {
		t.Errorf("Delete(missing) = %v, want nil", err)
	}
}

func TestHNSW_UpsertValidation(t *testing.T) {
	ctx := context.Background()
	s := NewHNSWEmbeddingStore(4)
	if err := s.Upsert(ctx, "book", "", []float32{1, 0, 0, 0}, nil); err == nil {
		t.Error("empty entityID should error")
	}
	if err := s.Upsert(ctx, "book", "B1", nil, nil); err == nil {
		t.Error("empty vector should error")
	}
	if err := s.Upsert(ctx, "book", "B1", []float32{1, 0, 0}, nil); err == nil {
		t.Error("dimension mismatch (3 != 4) should error")
	}
}

func TestHNSW_FindSimilar_NearestFirst(t *testing.T) {
	ctx := context.Background()
	s := NewHNSWEmbeddingStore(3)
	// query will be {1,0,0}; A is identical, B near, C orthogonal.
	_ = s.Upsert(ctx, "book", "A", []float32{1, 0, 0}, nil)
	_ = s.Upsert(ctx, "book", "B", []float32{0.9, 0.1, 0}, nil)
	_ = s.Upsert(ctx, "book", "C", []float32{0, 1, 0}, nil)

	res, err := s.FindSimilar(ctx, "book", []float32{1, 0, 0}, 3, nil)
	if err != nil {
		t.Fatalf("FindSimilar: %v", err)
	}
	if len(res) == 0 {
		t.Fatal("expected results")
	}
	if res[0].EntityID != "A" {
		t.Errorf("nearest = %q, want A", res[0].EntityID)
	}
	// Scores must be descending and A's ~1.0.
	if res[0].Similarity < 0.99 {
		t.Errorf("A similarity = %f, want ~1.0", res[0].Similarity)
	}
	for i := 1; i < len(res); i++ {
		if res[i].Similarity > res[i-1].Similarity+1e-6 {
			t.Errorf("results not descending: %v", res)
		}
	}
}

func TestHNSW_FindSimilar_MetadataFilter(t *testing.T) {
	ctx := context.Background()
	s := NewHNSWEmbeddingStore(3)
	_ = s.Upsert(ctx, "book", "PRIMARY", []float32{1, 0, 0}, map[string]string{"is_primary_version": "true"})
	_ = s.Upsert(ctx, "book", "VERSION", []float32{1, 0, 0}, map[string]string{"is_primary_version": "false"})

	res, err := s.FindSimilar(ctx, "book", []float32{1, 0, 0}, 5, map[string]string{"is_primary_version": "true"})
	if err != nil {
		t.Fatalf("FindSimilar: %v", err)
	}
	if len(res) != 1 || res[0].EntityID != "PRIMARY" {
		t.Errorf("filtered results = %v, want only PRIMARY", res)
	}
}

func TestHNSW_FindSimilar_EmptyGraph(t *testing.T) {
	ctx := context.Background()
	s := NewHNSWEmbeddingStore(3)
	res, err := s.FindSimilar(ctx, "book", []float32{1, 0, 0}, 5, nil)
	if err != nil || res != nil {
		t.Errorf("empty graph FindSimilar = (%v, %v), want (nil, nil)", res, err)
	}
}

// TestHNSW_ConcurrentAddSearch exercises the RWMutex under -race.
func TestHNSW_ConcurrentAddSearch(t *testing.T) {
	ctx := context.Background()
	s := NewHNSWEmbeddingStore(16)
	rng := rand.New(rand.NewSource(42))

	// Seed some entries so searches have something to traverse.
	for i := 0; i < 50; i++ {
		_ = s.Upsert(ctx, "book", fmt.Sprintf("seed-%d", i), unitVec(rng, 16), nil)
	}

	var wg sync.WaitGroup
	// Writers.
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			r := rand.New(rand.NewSource(int64(w)))
			for i := 0; i < 100; i++ {
				_ = s.Upsert(ctx, "book", fmt.Sprintf("w%d-%d", w, i), unitVec(r, 16), nil)
			}
		}(w)
	}
	// Readers.
	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func(r int) {
			defer wg.Done()
			rr := rand.New(rand.NewSource(int64(100 + r)))
			for i := 0; i < 100; i++ {
				_, _ = s.FindSimilar(ctx, "book", unitVec(rr, 16), 10, nil)
				_, _ = s.CountByType(ctx, "book")
			}
		}(r)
	}
	wg.Wait()
}

// addNoise returns base perturbed by a small random vector and re-normalized,
// producing a point near base on the unit sphere (mimics how real embeddings
// of similar items cluster, unlike uniform-random vectors for which ANN recall
// is pathologically low because all neighbors are near-equidistant).
func addNoise(rng *rand.Rand, base []float32, scale float64) []float32 {
	out := make([]float32, len(base))
	var norm float64
	for i := range base {
		x := float64(base[i]) + rng.NormFloat64()*scale
		out[i] = float32(x)
		norm += x * x
	}
	norm = math.Sqrt(norm)
	if norm == 0 {
		return base
	}
	for i := range out {
		out[i] = float32(float64(out[i]) / norm)
	}
	return out
}

// TestHNSW_RecallVsChromem asserts HNSW finds the same nearest neighbors as the
// exact brute-force chromem store on CLUSTERED data (the realistic case — real
// embeddings of similar items cluster). HNSW is approximate, so we assert
// recall@10 ≥ 0.8, not identity.
func TestHNSW_RecallVsChromem(t *testing.T) {
	ctx := context.Background()
	const (
		dim      = 64
		clusters = 80
		// perCluster == k and clusters are well-separated, so the exact top-k for
		// a query near a centroid is EXACTLY that cluster's members — no
		// far-field near-ties to dilute the metric, no within-cluster ambiguity.
		perCluster = 10
		k          = 10
		noiseScale = 0.05 // cluster-mates close; clusters orthogonal (random unit centroids)
		queryNoise = 0.02
	)
	rng := rand.New(rand.NewSource(7))
	h := NewHNSWEmbeddingStore(dim)
	c := NewInMemoryChromemStore(dim)

	centroids := make([][]float32, clusters)
	for ci := 0; ci < clusters; ci++ {
		centroids[ci] = unitVec(rng, dim)
		for pi := 0; pi < perCluster; pi++ {
			id := fmt.Sprintf("c%d-p%d", ci, pi)
			v := addNoise(rng, centroids[ci], noiseScale)
			if err := h.Upsert(ctx, "book", id, v, nil); err != nil {
				t.Fatalf("hnsw upsert: %v", err)
			}
			if err := c.Upsert(ctx, "book", id, v, nil); err != nil {
				t.Fatalf("chromem upsert: %v", err)
			}
		}
	}

	var totalRecall float64
	const queries = 40
	qrng := rand.New(rand.NewSource(99))
	for q := 0; q < queries; q++ {
		query := addNoise(qrng, centroids[q%clusters], queryNoise)
		hres, err := h.FindSimilar(ctx, "book", query, k, nil)
		if err != nil {
			t.Fatalf("hnsw find: %v", err)
		}
		cres, err := c.FindSimilar(ctx, "book", query, k, nil)
		if err != nil {
			t.Fatalf("chromem find: %v", err)
		}
		exact := make(map[string]bool, len(cres))
		for _, r := range cres {
			exact[r.EntityID] = true
		}
		hit := 0
		for _, r := range hres {
			if exact[r.EntityID] {
				hit++
			}
		}
		if len(cres) > 0 {
			totalRecall += float64(hit) / float64(len(cres))
		}
	}
	recall := totalRecall / float64(queries)
	if recall < 0.8 {
		t.Errorf("recall@%d vs exact = %.3f, want ≥ 0.80", k, recall)
	}
	t.Logf("HNSW recall@%d vs exact chromem = %.3f", k, recall)
}
