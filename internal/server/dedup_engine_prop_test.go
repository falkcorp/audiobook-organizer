// file: internal/server/dedup_engine_prop_test.go
// version: 2.1.0
// guid: e6425d8b-3ab4-4e0c-86fd-71ece563085e
// last-edited: 2026-10-10

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
// Dimensions are kept small (8) because per-iteration store setup is
// the hot path here — larger dimensions multiply the per-iteration
// cost without exercising any additional invariant. `pgregory.net/rapid`'s default
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

// fatalfer is the slice of *testing.T and *rapid.T that propDB's factory
// needs, so pinned table tests and rapid.Check bodies share one store.
type fatalfer interface {
	Fatalf(format string, args ...any)
}

// propDB opens one PebbleDB for the lifetime of the outer *testing.T and
// returns a factory that produces a fresh EmbeddingStore per rapid iteration
// (or per pinned table case)
// by deleting all emb:/dedup: keys between iterations. This avoids opening
// hundreds of PebbleDB instances (one per rapid iteration) which leaks
// internal goroutines and file descriptors.
func propDB(t *testing.T) func(fatalfer) *database.EmbeddingStore {
	t.Helper()
	db, err := pebble.Open(t.TempDir(), &pebble.Options{})
	if err != nil {
		t.Fatalf("pebble.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	es := database.NewEmbeddingStore(db)

	return func(rt fatalfer) *database.EmbeddingStore {
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

// TestProp_ChromemMatchesSqlite asserts that, for the same inputs, the
// chromem backend and the embedding store's linear-scan FindSimilar
// (Pebble-backed today; the "Sqlite" in the name predates the move) both
// return exactly the set of entity IDs whose cosine similarity to the
// query is at or above the threshold.
//
// chromem-go v0.7.0 is an exhaustive brute-force scan, not HNSW (see
// internal/database/chromem_embedding_store.go), so there is no ANN
// approximation to make room for: outside the numerical guard band
// described on checkChromemMatchesSqlite the two sets must be equal, and
// each must equal an independent float64 reference. An earlier version
// of this test claimed HNSW and only required 50% overlap, which still
// failed whenever a single score sat on the threshold (minOverlap(1) == 1).
func TestProp_ChromemMatchesSqlite(t *testing.T) {
	if testing.Short() {
		t.Skip("slow property test; run without -short")
	}
	getStore := propDB(t)
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(10, 20).Draw(t, "n")
		vecs := make([][]float32, n)
		for i := range n {
			vecs[i] = genNonZeroVector(t, propVectorDim, fmt.Sprintf("vec_%d", i))
		}
		query := genNonZeroVector(t, propVectorDim, "query")
		if _, err := checkChromemMatchesSqlite(getStore(t), vecs, query); err != nil {
			t.Fatalf("%v", err)
		}
	})
}

// TestChromemMatchesSqlite_PinnedCounterexamples replays the two inputs
// on which TestProp_ChromemMatchesSqlite failed in CI under its old
// zero-tolerance oracle, and asserts the culprit document is the one the
// guard band classifies as on-the-threshold. Both are the shrunk draws
// rapid printed; the seeds are in the case names.
//
//   - 2026-10-10 (PR #3924, seed 3835638638717310857): b19 scores
//     0.500041 against the float32 vector chromem holds, and 0.499969
//     against the float16 vector the embedding store persists
//     (-0.30240512 is stored as -0.30249023). chromem includes it, the
//     store does not: "chromem→sqlite overlap too low: 0 of 1".
//   - 2026-10-09 (main run 37995734374, seed 10234930014058505839): b1's
//     exact cosine is 0.4999999928. The store computes it in float64 and
//     rounds to float32, landing exactly on 0.5 (included). chromem's
//     float32 normalise-and-dot gives 0x3effffff (0.49999997, excluded)
//     on amd64 but 0.5 on arm64, where Go fuses the multiply-adds, so
//     this case only fails on amd64: "sqlite→chromem overlap too low:
//     0 of 1".
//
// The denormal and signed-zero components in these draws contribute
// nothing to either score (float16 flushes them to zero and their
// squares underflow float32); they are rapid's generator, not the cause.
func TestChromemMatchesSqlite_PinnedCounterexamples(t *testing.T) {
	getStore := propDB(t)
	for _, tc := range chromemSqliteCounterexamples {
		t.Run(tc.name, func(t *testing.T) {
			onThreshold, err := checkChromemMatchesSqlite(getStore(t), tc.vecs, tc.query)
			if err != nil {
				t.Fatalf("%v", err)
			}
			if _, ok := onThreshold[tc.culprit]; !ok {
				t.Fatalf("culprit %s not classified as on-threshold (got %v); the pinned case no longer exercises the guard band",
					tc.culprit, onThreshold)
			}
		})
	}
}

const (
	chromemSqliteThreshold float32 = 0.5
	chromemSqliteMaxK              = 20 // >= the largest n drawn, so neither backend truncates

	// propScoreTolerance bounds the float32 arithmetic error of either
	// backend's score relative to a float64 reference on the same vector.
	// chromem normalises both vectors in float32 (8-term sum of squares,
	// sqrt, divide) and takes an 8-term float32 dot product, which bounds
	// its error at roughly (2*8+3) * 2^-24 ≈ 1.1e-6; measured over 200,000
	// draws from genNonZeroVector the worst case was 1.07e-6 on amd64 and
	// 8.9e-7 on arm64. The embedding store accumulates in float64 and
	// rounds once to float32, so its error is at most 2^-25 ≈ 3e-8. 1e-5
	// leaves about 10x headroom over the worst observed value.
	//
	// It does NOT cover float16 storage drift (measured up to 3e-4 for this
	// generator): that is modelled exactly instead, by scoring the store's
	// reference against the same encode/decode round trip the store applies.
	propScoreTolerance = 1e-5
)

// propCosineRef is the float64 reference cosine similarity. It returns 0
// for a zero-norm side, mirroring database.CosineSimilarity.
func propCosineRef(a, b []float32) float64 {
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	denom := math.Sqrt(na) * math.Sqrt(nb)
	if denom == 0 {
		return 0
	}
	return dot / denom
}

// checkChromemMatchesSqlite loads vecs (as b0..bN-1) into a fresh chromem
// store and into es, queries both, and checks each against a float64
// reference computed on the vector that backend actually scores:
//
//   - chromem keeps the float32 vector it was given;
//   - the embedding store persists float16 (database.EncodeVectorExported)
//     and scores the decoded vector, which can move a cosine by ~1e-4.
//
// A document is "on the threshold" when either reference is within
// propScoreTolerance of it, or the two references fall on opposite sides
// of it. Which way such a document goes is decided by rounding, not by
// similarity, so it is excluded from the set comparison and returned in
// onThreshold. Every other document must be in both result sets or in
// neither, exactly as the reference says, and every returned score must be
// within propScoreTolerance of its reference.
func checkChromemMatchesSqlite(es *database.EmbeddingStore, vecs [][]float32, query []float32) (onThreshold map[string]struct{}, err error) {
	ctx := context.Background()
	cs := database.NewInMemoryChromemStore(propVectorDim)

	t64 := float64(chromemSqliteThreshold)
	refChromem := make(map[string]float64, len(vecs))
	refStore := make(map[string]float64, len(vecs))
	want := make(map[string]bool, len(vecs))
	onThreshold = make(map[string]struct{})
	for i, v := range vecs {
		id := fmt.Sprintf("b%d", i)
		if err := es.Upsert(database.Embedding{
			EntityType: "book",
			EntityID:   id,
			TextHash:   fmt.Sprintf("hash_%d", i),
			Vector:     v,
			Model:      "test",
		}); err != nil {
			return nil, fmt.Errorf("embedding store upsert %s: %w", id, err)
		}
		if err := cs.Upsert(ctx, "book", id, v, nil); err != nil {
			return nil, fmt.Errorf("chromem upsert %s: %w", id, err)
		}

		rc := propCosineRef(query, v)
		rs := propCosineRef(query, database.DecodeVectorExported(database.EncodeVectorExported(v)))
		refChromem[id], refStore[id] = rc, rs
		if math.Abs(rc-t64) <= propScoreTolerance || math.Abs(rs-t64) <= propScoreTolerance ||
			(rc >= t64) != (rs >= t64) {
			onThreshold[id] = struct{}{}
			continue
		}
		want[id] = rc >= t64
	}

	storeResults, err := es.FindSimilar("book", query, chromemSqliteThreshold, chromemSqliteMaxK)
	if err != nil {
		return nil, fmt.Errorf("embedding store FindSimilar: %w", err)
	}
	chromemResults, err := cs.FindSimilar(ctx, "book", query, chromemSqliteMaxK, nil)
	if err != nil {
		return nil, fmt.Errorf("chromem FindSimilar: %w", err)
	}
	if len(chromemResults) != len(vecs) {
		return nil, fmt.Errorf("chromem returned %d results for %d documents with maxK=%d",
			len(chromemResults), len(vecs), chromemSqliteMaxK)
	}

	storeSet := make(map[string]struct{}, len(storeResults))
	for _, r := range storeResults {
		if d := math.Abs(float64(r.Similarity) - refStore[r.EntityID]); d > propScoreTolerance {
			return nil, fmt.Errorf("embedding store score for %s = %v, reference %v (|diff| %g > %g)",
				r.EntityID, r.Similarity, refStore[r.EntityID], d, propScoreTolerance)
		}
		storeSet[r.EntityID] = struct{}{}
	}
	chromemSet := make(map[string]struct{}, len(chromemResults))
	for _, r := range chromemResults {
		if d := math.Abs(float64(r.Similarity) - refChromem[r.EntityID]); d > propScoreTolerance {
			return nil, fmt.Errorf("chromem score for %s = %v, reference %v (|diff| %g > %g)",
				r.EntityID, r.Similarity, refChromem[r.EntityID], d, propScoreTolerance)
		}
		if r.Similarity >= chromemSqliteThreshold {
			chromemSet[r.EntityID] = struct{}{}
		}
	}

	for i := range vecs {
		id := fmt.Sprintf("b%d", i)
		w, decided := want[id]
		if !decided {
			continue
		}
		_, inStore := storeSet[id]
		_, inChromem := chromemSet[id]
		if inStore != w || inChromem != w {
			return nil, fmt.Errorf("%s: reference says above-threshold=%v (chromem ref %.9f, store ref %.9f) but embedding store=%v, chromem=%v",
				id, w, refChromem[id], refStore[id], inStore, inChromem)
		}
	}
	return onThreshold, nil
}

// chromemSqliteCounterexamples are the CI failures of the old oracle; see
// TestChromemMatchesSqlite_PinnedCounterexamples for what each one shows.
type chromemSqliteCase struct {
	name    string
	culprit string // the document whose score straddles the threshold
	vecs    [][]float32
	query   []float32
}

var chromemSqliteCounterexamples = []chromemSqliteCase{
	{
		name:    "2026-10-10_seed3835638638717310857_f16_drift_below_threshold",
		culprit: "b19",
		vecs: [][]float32{
			{1, 1, 1, 1, 1, 1, 1, 1},
			{1, 1, 1, 1, 1, 1, 1, 1},
			{1, 1, 1, 1, 1, 1, 1, 1},
			{1, 1, 1, 1, 1, 1, 1, 1},
			{1, 1, 1, 1, 1, 1, 1, 1},
			{1, 1, 1, 1, 1, 1, 1, 1},
			{1, 1, 1, 1, 1, 1, 1, 1},
			{1, 1, 1, 1, 1, 1, 1, 1},
			{1, 1, 1, 1, 1, 1, 1, 1},
			{1, 1, 1, 1, 1, 1, 1, 1},
			{1, 1, 1, 1, 1, 1, 0.75, -9.926574e-39},
			{1.6155871e-27, -0.06186676, 1.8299983e-11, 1, -8.6351065e-08, 0.0012142259, 0.60251564, 0.022766573},
			{4.656613e-10, float32(math.Copysign(0, -1)), -0.14212637, -1.6799531e-06, -6.148035e-25, 0.012911899, -1, 2.0224829e-23},
			{-1, 6.2785466e-06, -4.0745363e-10, 0.00021011099, 0.421875, 0.0002403339, -0.625, 0.31550956},
			{-0.4581089, 6.118957e-05, -0.00048828125, 3.8997625e-13, -0.8286133, 0.18310547, -1, 7.4569096e-16},
			{-1, -0.85388803, -1.8204759e-14, -4.262759e-10, 7.052966e-37, -0.8051169, 0.7746582, 2.8646299e-22},
			{3.1230884e-15, -4.656613e-10, -0.0024414062, 0.375, -8.399015e-12, -0.0078125, -0.7110596, -1.7370793e-20},
			{-1, 4.2351647e-22, -1.9073486e-06, 1.455796e-29, -1, -1.5501832e-37, -3.344254e-35, 1},
			{0.5184674, 1.9426645e-30, -0.027347593, -3.1272018e-28, -9.700761e-22, -2.4864896e-26, -0.19216919, 3.2243365e-16},
			{-0.30240512, 0, 7.483862e-14, 0.0037841797, 5.684342e-14, -0.21043532, -0.0004314962, -1.1588666e-06},
		},
		query: []float32{-1.4305115e-06, -0.00012207031, 0, -0.09375, 2.997855e-38, -1, 1.9032055e-38, 0.53988266},
	},
	{
		name:    "2026-10-09_seed10234930014058505839_f32_rounding_at_threshold",
		culprit: "b1",
		vecs: [][]float32{
			{1, 1, 1, 1, 0.125, 1, 1, 1},
			{1, 6.1035156e-05, 1.4901161e-08, 1.5258789e-05, 1, 2.9802322e-08, 2.6054498e-25, -0.00012207031},
			{-8.832194e-34, -3.83929e-13, 5.8233515e-22, 0.75878906, 1.1426275e-33, -2.1598391e-14, -7.853424e-14, 7.6293945e-05},
			{-0.095308445, -0.49450684, 9.536709e-31, 0.8639034, 1, -1.4222998e-14, -5.9604645e-08, 0.640625},
			{-3.1475884e-07, 0.01171875, 0, 0.39001465, 3.9790393e-12, 4.0308805e-08, -2.1684043e-19, -0.625},
			{1.9855229e-12, -0.0008713409, 2.259241e-29, -0.56512696, -5.639404e-13, 5.9639454e-28, -0.0025728457, -4.622232e-33},
			{2.7755576e-17, -4.7763063e-32, -0.75192475, -3.615283e-14, 0, 0.26757812, float32(math.Copysign(0, -1)), -1.6283541e-31},
			{4.7683716e-07, -0.0016710758, 1.05098e-30, -0.0006135013, 0.00070913136, -4.774598e-28, -3.4279528e-23, 1.162497e-25},
			{-0.38642883, -4.9867313e-29, -0.026365787, 1.06174116e-17, 0.016074095, 2.4092708e-09, float32(math.Copysign(0, -1)), 0},
			{0.00024414062, 5.026029e-18, 1.1963127e-14, 1, -8.599857e-31, 3.0450183e-29, -0.48784992, 8.56204e-13},
		},
		query: []float32{6.2430505e-35, 5.376467e-18, 1, -3.1590462e-06, 1, 7.608571e-38, 0.00031266455, 4.0817127e-11},
	},
}
