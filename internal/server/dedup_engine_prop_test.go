// file: internal/server/dedup_engine_prop_test.go
// version: 2.0.0
// guid: e6425d8b-3ab4-4e0c-86fd-71ece563085e

package server

import (
	"context"
	"fmt"
	"math"
	"testing"

	"github.com/cockroachdb/pebble/v2"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"pgregory.net/rapid"
)

// Property-based tests for the dedup engine's similarity math and
// FindSimilar plumbing (plan item 4.5 task 4).
//
// Dimensions are kept small (8–16) because the chromem backend's
// persistence and HNSW index build are the hot path here — larger
// dimensions multiply the per-iteration cost without exercising
// any additional invariant. `pgregory.net/rapid`'s default
// iteration count (100) is left alone; the small vectors keep the
// whole file under a second on a cold cache.

const propVectorDim = 8

// genVector draws a random float32 vector of the given dimension with
// each coordinate in [-1, 1]. rapid.Float32Range accepts a closed
// range and its shrink logic favours zero.
func genVector(t *rapid.T, dim int, label string) []float32 {
	return rapid.SliceOfN(rapid.Float32Range(-1, 1), dim, dim).Draw(t, label)
}

// genNonZeroVector draws a random float32 vector whose L2 norm is
// demonstrably above zero. Needed for self-similarity and range
// properties where the zero vector short-circuits CosineSimilarity
// to 0 (which is a separate, explicitly tested property).
func genNonZeroVector(t *rapid.T, dim int, label string) []float32 {
	return rapid.Custom(func(t *rapid.T) []float32 {
		v := rapid.SliceOfN(rapid.Float32Range(-1, 1), dim, dim).Draw(t, label)
		var norm float64
		for _, x := range v {
			norm += float64(x) * float64(x)
		}
		if norm < 1e-8 {
			// Force a non-zero component so cosine similarity is defined.
			v[0] = 1
		}
		return v
	}).Draw(t, label+"_nonzero")
}

// TestProp_CosineSimilaritySymmetry asserts CosineSimilarity(a, b)
// equals CosineSimilarity(b, a) for arbitrary float32 vectors. The
// implementation is order-independent by construction (the dot
// product and norms are symmetric), so any asymmetry would indicate
// a numerical or refactor bug.
func TestProp_CosineSimilaritySymmetry(t *testing.T) {
	if testing.Short() {
		t.Skip("slow property test; run without -short")
	}
	rapid.Check(t, func(t *rapid.T) {
		a := genVector(t, propVectorDim, "a")
		b := genVector(t, propVectorDim, "b")

		ab := database.CosineSimilarity(a, b)
		ba := database.CosineSimilarity(b, a)

		if ab != ba {
			t.Fatalf("asymmetric: CosineSimilarity(a,b)=%v, (b,a)=%v", ab, ba)
		}
	})
}

// TestProp_CosineSelfSimilarity asserts that any non-zero vector is
// (approximately) maximally similar to itself. Float32 rounding in
// the norm computation means the result can drift below 1.0 by a
// few ULPs, so we allow a 1e-5 tolerance rather than requiring
// exact equality.
func TestProp_CosineSelfSimilarity(t *testing.T) {
	if testing.Short() {
		t.Skip("slow property test; run without -short")
	}
	rapid.Check(t, func(t *rapid.T) {
		v := genNonZeroVector(t, propVectorDim, "v")
		sim := database.CosineSimilarity(v, v)
		diff := math.Abs(float64(sim) - 1.0)
		if diff > 1e-5 {
			t.Fatalf("self-similarity drift: got %v, want ≈1.0 (diff=%v)", sim, diff)
		}
	})
}

// TestProp_CosineRange asserts cosine similarity is always in
// [-1, 1]. A tiny epsilon guard accommodates float32 rounding at
// the boundaries (e.g. a vector compared with itself might return
// 1.0000001 under pathological floats). Anything outside
// [-1-eps, 1+eps] is a real violation.
func TestProp_CosineRange(t *testing.T) {
	if testing.Short() {
		t.Skip("slow property test; run without -short")
	}
	rapid.Check(t, func(t *rapid.T) {
		a := genVector(t, propVectorDim, "a")
		b := genVector(t, propVectorDim, "b")
		sim := database.CosineSimilarity(a, b)
		const eps = 1e-5
		if float64(sim) < -1.0-eps || float64(sim) > 1.0+eps {
			t.Fatalf("cosine out of range: got %v", sim)
		}
	})
}

// TestProp_CosineZeroVector asserts that comparing the zero vector
// against anything returns exactly 0 — CosineSimilarity short-
// circuits when either norm is zero to avoid a NaN divide.
func TestProp_CosineZeroVector(t *testing.T) {
	if testing.Short() {
		t.Skip("slow property test; run without -short")
	}
	rapid.Check(t, func(t *rapid.T) {
		v := genVector(t, propVectorDim, "v")
		zero := make([]float32, propVectorDim)
		if sim := database.CosineSimilarity(zero, v); sim != 0 {
			t.Fatalf("zero×v: got %v, want 0", sim)
		}
		if sim := database.CosineSimilarity(v, zero); sim != 0 {
			t.Fatalf("v×zero: got %v, want 0", sim)
		}
	})
}

// newPropEmbedStore builds a fresh on-disk SQLite embedding store
// for a single rapid iteration. Each Check iteration gets its own
// tmp dir so previous iterations' vectors never pollute the query.
// propDB opens one PebbleDB for the lifetime of the outer *testing.T and
// returns a factory that produces a fresh EmbeddingStore per rapid iteration
// by deleting all emb:/dedup: keys between iterations. This avoids opening
// hundreds of PebbleDB instances (one per rapid iteration) which leaks
// internal goroutines and file descriptors.
func propDB(t *testing.T) func(*rapid.T) *database.EmbeddingStore {
	t.Helper()
	db, err := pebble.Open(t.TempDir(), &pebble.Options{})
	if err != nil {
		t.Fatalf("pebble.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	es := database.NewEmbeddingStore(db)

	return func(rt *rapid.T) *database.EmbeddingStore {
		// Clear all emb: and dedup: keys from the previous iteration.
		for _, pfx := range []string{"emb:", "dedup:"} {
			lo := []byte(pfx)
			hi := make([]byte, len(lo))
			copy(hi, lo)
			for i := len(hi) - 1; i >= 0; i-- {
				hi[i]++
				if hi[i] != 0 {
					hi = hi[:i+1]
					break
				}
			}
			if err := db.DeleteRange(lo, hi, pebble.Sync); err != nil {
				rt.Fatalf("clear prefix %q: %v", pfx, err)
			}
		}
		return es
	}
}

// TestProp_FindSimilarOrdering asserts that EmbeddingStore.FindSimilar
// returns results sorted by similarity DESCENDING. The invariant
// holds regardless of input distribution because FindSimilar
// sort.Slice'es by Similarity before applying the maxResults cap.
func TestProp_FindSimilarOrdering(t *testing.T) {
	if testing.Short() {
		t.Skip("slow property test; run without -short")
	}
	getStore := propDB(t)
	rapid.Check(t, func(t *rapid.T) {
		es := getStore(t)
		n := rapid.IntRange(2, 12).Draw(t, "n")
		for i := range n {
			vec := genVector(t, propVectorDim, fmt.Sprintf("vec_%d", i))
			err := es.Upsert(database.Embedding{
				EntityType: "book",
				EntityID:   fmt.Sprintf("b%d", i),
				TextHash:   fmt.Sprintf("hash_%d", i),
				Vector:     vec,
				Model:      "test",
			})
			if err != nil {
				t.Fatalf("upsert b%d: %v", i, err)
			}
		}
		query := genNonZeroVector(t, propVectorDim, "query")
		// minSimilarity = -1 so nothing is filtered out; we want
		// to see the complete ordered list.
		results, err := es.FindSimilar("book", query, -1.0, n)
		if err != nil {
			t.Fatalf("find similar: %v", err)
		}
		for i := 1; i < len(results); i++ {
			if results[i-1].Similarity < results[i].Similarity {
				t.Fatalf("not sorted desc at i=%d: %v then %v",
					i, results[i-1].Similarity, results[i].Similarity)
			}
		}
	})
}

// TestProp_FindSimilarThreshold asserts every result from
// EmbeddingStore.FindSimilar has similarity >= minSimilarity.
// The filter happens inside the scan loop (sim >= minSimilarity)
// before sort+cap so any leak past it would be a real bug.
func TestProp_FindSimilarThreshold(t *testing.T) {
	if testing.Short() {
		t.Skip("slow property test; run without -short")
	}
	getStore := propDB(t)
	rapid.Check(t, func(t *rapid.T) {
		es := getStore(t)
		n := rapid.IntRange(2, 12).Draw(t, "n")
		for i := range n {
			vec := genVector(t, propVectorDim, fmt.Sprintf("vec_%d", i))
			if err := es.Upsert(database.Embedding{
				EntityType: "book",
				EntityID:   fmt.Sprintf("b%d", i),
				TextHash:   fmt.Sprintf("hash_%d", i),
				Vector:     vec,
				Model:      "test",
			}); err != nil {
				t.Fatalf("upsert: %v", err)
			}
		}
		query := genNonZeroVector(t, propVectorDim, "query")
		minSim := rapid.Float32Range(-1, 1).Draw(t, "minSim")
		results, err := es.FindSimilar("book", query, minSim, n)
		if err != nil {
			t.Fatalf("find similar: %v", err)
		}
		for _, r := range results {
			if r.Similarity < minSim {
				t.Fatalf("result below threshold: %v < %v", r.Similarity, minSim)
			}
		}
	})
}

// TestProp_FindSimilarMaxResults asserts
// len(results) <= maxResults (when maxResults > 0). The store also
// permits maxResults == 0 meaning "no cap", which we don't test
// because the invariant is vacuous.
func TestProp_FindSimilarMaxResults(t *testing.T) {
	if testing.Short() {
		t.Skip("slow property test; run without -short")
	}
	getStore := propDB(t)
	rapid.Check(t, func(t *rapid.T) {
		es := getStore(t)
		n := rapid.IntRange(5, 20).Draw(t, "n")
		for i := range n {
			vec := genVector(t, propVectorDim, fmt.Sprintf("vec_%d", i))
			if err := es.Upsert(database.Embedding{
				EntityType: "book",
				EntityID:   fmt.Sprintf("b%d", i),
				TextHash:   fmt.Sprintf("hash_%d", i),
				Vector:     vec,
				Model:      "test",
			}); err != nil {
				t.Fatalf("upsert: %v", err)
			}
		}
		query := genNonZeroVector(t, propVectorDim, "query")
		maxResults := rapid.IntRange(1, n).Draw(t, "maxResults")
		results, err := es.FindSimilar("book", query, -1.0, maxResults)
		if err != nil {
			t.Fatalf("find similar: %v", err)
		}
		if len(results) > maxResults {
			t.Fatalf("maxResults violated: got %d results, cap %d",
				len(results), maxResults)
		}
	})
}

// TestProp_ChromemMatchesSqlite asserts that for the same inputs the
// chromem and sqlite FindSimilar backends agree on the set of
// entity IDs whose similarity is above a chosen threshold.
//
// chromem uses HNSW — an approximate-nearest-neighbour index — so
// the relative ordering can differ from the exact linear scan the
// sqlite backend performs. The property tested here is therefore a
// Jaccard-style overlap on the top-K rather than strict set
// equality: at least half of each side's above-threshold results
// must appear in the other.
//
// With 10–20 randomly drawn, typically well-separated vectors in
// 8D the ANN structure is exact in practice, so in steady state
// the intersection is usually total. The looser bound is here to
// stop the test from going flaky the day chromem tightens its
// indexing heuristics.
func TestProp_ChromemMatchesSqlite(t *testing.T) {
	if testing.Short() {
		t.Skip("slow property test; run without -short")
	}
	getStore := propDB(t)
	rapid.Check(t, func(t *rapid.T) {
		ctx := context.Background()
		es := getStore(t)
		cs := database.NewInMemoryChromemStore(propVectorDim)

		n := rapid.IntRange(10, 20).Draw(t, "n")
		ids := make([]string, n)
		vecs := make([][]float32, n)
		for i := range n {
			ids[i] = fmt.Sprintf("b%d", i)
			vecs[i] = genNonZeroVector(t, propVectorDim, fmt.Sprintf("vec_%d", i))

			if err := es.Upsert(database.Embedding{
				EntityType: "book",
				EntityID:   ids[i],
				TextHash:   fmt.Sprintf("hash_%d", i),
				Vector:     vecs[i],
				Model:      "test",
			}); err != nil {
				t.Fatalf("sqlite upsert: %v", err)
			}
			if err := cs.Upsert(ctx, "book", ids[i], vecs[i], nil); err != nil {
				t.Fatalf("chromem upsert: %v", err)
			}
		}
		query := genNonZeroVector(t, propVectorDim, "query")
		const threshold float32 = 0.5
		const maxK = 20

		sqlResults, err := es.FindSimilar("book", query, threshold, maxK)
		if err != nil {
			t.Fatalf("sqlite FindSimilar: %v", err)
		}
		chromemResults, err := cs.FindSimilar(ctx, "book", query, maxK, nil)
		if err != nil {
			t.Fatalf("chromem FindSimilar: %v", err)
		}

		sqlSet := make(map[string]struct{}, len(sqlResults))
		for _, r := range sqlResults {
			sqlSet[r.EntityID] = struct{}{}
		}
		chromemSet := make(map[string]struct{})
		for _, r := range chromemResults {
			if r.Similarity >= threshold {
				chromemSet[r.EntityID] = struct{}{}
			}
		}

		// Special-case empty: if either side returns nothing above
		// threshold, the only violation we care about is "the other
		// side returned a ton", which would indicate a real
		// divergence. Allow total emptiness though — different
		// rounding at the boundary is expected.
		if len(sqlSet) == 0 && len(chromemSet) == 0 {
			return
		}

		overlap := 0
		for id := range sqlSet {
			if _, ok := chromemSet[id]; ok {
				overlap++
			}
		}

		// Require 50% overlap on each side. With 8D well-separated
		// random vectors and HNSW defaults the actual overlap is
		// virtually always 100%; the slack is for ANN edge cases.
		minOverlap := func(n int) int {
			if n <= 1 {
				return n
			}
			return (n + 1) / 2
		}
		if overlap < minOverlap(len(sqlSet)) {
			t.Fatalf("sqlite→chromem overlap too low: %d of %d matched (chromem set=%d)",
				overlap, len(sqlSet), len(chromemSet))
		}
		if overlap < minOverlap(len(chromemSet)) {
			t.Fatalf("chromem→sqlite overlap too low: %d of %d matched (sqlite set=%d)",
				overlap, len(chromemSet), len(sqlSet))
		}
	})
}
