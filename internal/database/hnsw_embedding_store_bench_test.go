// file: internal/database/hnsw_embedding_store_bench_test.go
// version: 1.0.0
// guid: 8b9c0d1e-2f3a-4b5c-6d7e-8f9a0b1c2d3e
// last-edited: 2026-06-14

// Benchmarks proving the HNSW speedup over chromem's brute-force scan.
// Run: go test ./internal/database/ -run '^$' -bench 'FindSimilar' -benchmem
//
// Expectation at production scale (50K × 1024-dim): HNSW FindSimilar is
// dramatically faster per query than chromem's O(n·d) cosine scan — that gap
// is the entire reason for this backend. The benchmark records both so the
// PR can cite real numbers on the dev box.

package database

import (
	"context"
	"fmt"
	"math/rand"
	"testing"
)

func benchPopulate(b *testing.B, s VectorANNStore, n, dim int) []float32 {
	b.Helper()
	ctx := context.Background()
	rng := rand.New(rand.NewSource(1))
	for i := 0; i < n; i++ {
		if err := s.Upsert(ctx, "book", fmt.Sprintf("v%d", i), unitVec(rng, dim), map[string]string{"is_primary_version": "true"}); err != nil {
			b.Fatalf("upsert: %v", err)
		}
	}
	return unitVec(rng, dim) // query vector
}

func benchFindSimilar(b *testing.B, newStore func(dim int) VectorANNStore, n, dim int) {
	s := newStore(dim)
	query := benchPopulate(b, s, n, dim)
	ctx := context.Background()
	filter := map[string]string{"is_primary_version": "true"}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := s.FindSimilar(ctx, "book", query, 20, filter); err != nil {
			b.Fatalf("find: %v", err)
		}
	}
}

func newChromem(dim int) VectorANNStore { return NewInMemoryChromemStore(dim) }
func newHNSW(dim int) VectorANNStore    { return NewHNSWEmbeddingStore(dim) }

// Production-scale dims (bge-m3 = 1024). Sizes scaled to keep population time
// reasonable; the per-query asymptotics are what matter.
func BenchmarkFindSimilar_Chromem_10K(b *testing.B) { benchFindSimilar(b, newChromem, 10_000, 1024) }
func BenchmarkFindSimilar_HNSW_10K(b *testing.B)    { benchFindSimilar(b, newHNSW, 10_000, 1024) }
func BenchmarkFindSimilar_Chromem_50K(b *testing.B) { benchFindSimilar(b, newChromem, 50_000, 1024) }
func BenchmarkFindSimilar_HNSW_50K(b *testing.B)    { benchFindSimilar(b, newHNSW, 50_000, 1024) }
