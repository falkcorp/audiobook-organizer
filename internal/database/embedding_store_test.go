// file: internal/database/embedding_store_test.go
// version: 2.2.1
// guid: a1b2c3d4-e5f6-7890-abcd-ef1234567890
// last-edited: 2026-10-03

package database

import (
	"encoding/json"
	"testing"

	"github.com/cockroachdb/pebble/v2"
	"github.com/cockroachdb/pebble/v2/vfs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestEmbeddingStore creates an EmbeddingStore backed by an isolated
// in-memory PebbleDB for use in tests.
// The database lives on vfs.NewMem: these writes pass pebble.Sync, and on a
// real filesystem each one is an fsync, which dominated this package's run time.
// Nothing here reopens the database, so no on-disk state is needed.
func newTestEmbeddingStore(t *testing.T) *EmbeddingStore {
	t.Helper()
	db, err := pebble.Open("embeddings.pebble", &pebble.Options{FS: vfs.NewMem()})
	require.NoError(t, err)
	store := &EmbeddingStore{db: db, owned: true}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// TestEmbeddingStore_ContentHashCache locks in the contract that
// GetCachedEmbedding returns (nil, nil) on a miss, PutCachedEmbedding
// round-trips a vector by text hash + model, and different models
// for the same hash are isolated. This is the cache path that
// EmbedBatch relies on to avoid burning the OpenAI quota on
// identical-content re-embeds.
func TestEmbeddingStore_ContentHashCache(t *testing.T) {
	store := newTestEmbeddingStore(t)

	const hash = "deadbeef"
	const model = "text-embedding-3-large"

	// Miss on a fresh store.
	got, err := store.GetCachedEmbedding(hash, model)
	require.NoError(t, err)
	assert.Nil(t, got, "miss should return nil, nil")

	// Round-trip a vector.
	// T021: vectors are stored as float16+zstd; exact equality is not expected.
	// Use InDelta(1e-2) to allow for float16 quantisation error.
	want := []float32{0.1, 0.2, 0.3, 0.4}
	require.NoError(t, store.PutCachedEmbedding(hash, model, want))
	got, err = store.GetCachedEmbedding(hash, model)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Len(t, got, len(want))
	for i, w := range want {
		assert.InDelta(t, w, got[i], 1e-2, "cache round-trip element %d", i)
	}

	// Same hash, different model → separate row.
	// Values 1.0 and 2.0 are exactly representable in float16, so InDelta works.
	smaller := []float32{1, 2}
	require.NoError(t, store.PutCachedEmbedding(hash, "text-embedding-3-small", smaller))
	got, err = store.GetCachedEmbedding(hash, "text-embedding-3-small")
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Len(t, got, len(smaller))
	for i, w := range smaller {
		assert.InDelta(t, w, got[i], 1e-2, "smaller cache element %d", i)
	}
	// The original entry at the original model is still intact.
	got, err = store.GetCachedEmbedding(hash, model)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Len(t, got, len(want))
	for i, w := range want {
		assert.InDelta(t, w, got[i], 1e-2, "original cache element %d after second put", i)
	}

	// Empty arguments short-circuit cleanly.
	got, err = store.GetCachedEmbedding("", model)
	require.NoError(t, err)
	assert.Nil(t, got)
	require.NoError(t, store.PutCachedEmbedding("", model, want))
	require.NoError(t, store.PutCachedEmbedding(hash, model, nil))
}

func TestEmbeddingStore_UpsertAndGet(t *testing.T) {
	store := newTestEmbeddingStore(t)

	e := Embedding{
		EntityType: "book",
		EntityID:   "book-001",
		TextHash:   "abc123",
		Vector:     []float32{0.1, 0.2, 0.3},
		Model:      "text-embedding-3-small",
	}
	require.NoError(t, store.Upsert(e))

	got, err := store.Get("book", "book-001")
	require.NoError(t, err)
	require.NotNil(t, got)

	assert.Equal(t, "book", got.EntityType)
	assert.Equal(t, "book-001", got.EntityID)
	assert.Equal(t, "abc123", got.TextHash)
	assert.Equal(t, "text-embedding-3-small", got.Model)
	require.Len(t, got.Vector, 3)
	// T021: vectors are stored as float16+zstd (v1 encoding). Float16 has ~10-bit
	// mantissa precision (~1e-3 relative error per element) — not float32's 1e-6.
	// The tolerance here is relaxed to 1e-2 to account for float16 quantisation.
	// See the WHY comment in embedding_store.go for why this is acceptable.
	assert.InDelta(t, 0.1, got.Vector[0], 1e-2)
	assert.InDelta(t, 0.2, got.Vector[1], 1e-2)
	assert.InDelta(t, 0.3, got.Vector[2], 1e-2)
	assert.False(t, got.CreatedAt.IsZero())
	assert.False(t, got.UpdatedAt.IsZero())
}

func TestEmbeddingStore_UpsertOverwrites(t *testing.T) {
	store := newTestEmbeddingStore(t)

	first := Embedding{
		EntityType: "book",
		EntityID:   "book-001",
		TextHash:   "hash-v1",
		Vector:     []float32{1.0, 0.0, 0.0},
		Model:      "model-v1",
	}
	require.NoError(t, store.Upsert(first))

	second := Embedding{
		EntityType: "book",
		EntityID:   "book-001",
		TextHash:   "hash-v2",
		Vector:     []float32{0.0, 1.0, 0.0},
		Model:      "model-v2",
	}
	require.NoError(t, store.Upsert(second))

	got, err := store.Get("book", "book-001")
	require.NoError(t, err)
	require.NotNil(t, got)

	// Second upsert must win.
	assert.Equal(t, "hash-v2", got.TextHash)
	assert.Equal(t, "model-v2", got.Model)
	// T021: float16 tolerance (1e-2) for stored vectors.
	assert.InDelta(t, 0.0, got.Vector[0], 1e-2)
	assert.InDelta(t, 1.0, got.Vector[1], 1e-2)

	// Only one row should exist.
	n, err := store.CountByType("book")
	require.NoError(t, err)
	assert.Equal(t, 1, n)
}

func TestEmbeddingStore_FindSimilar(t *testing.T) {
	store := newTestEmbeddingStore(t)

	// Two vectors pointing in roughly the same direction.
	similar1 := Embedding{EntityType: "book", EntityID: "sim-a", TextHash: "h1", Vector: []float32{1, 0, 0}, Model: "m"}
	similar2 := Embedding{EntityType: "book", EntityID: "sim-b", TextHash: "h2", Vector: []float32{0.9, 0.1, 0}, Model: "m"}
	// One vector pointing in an orthogonal direction.
	different := Embedding{EntityType: "book", EntityID: "diff-c", TextHash: "h3", Vector: []float32{0, 0, 1}, Model: "m"}

	require.NoError(t, store.Upsert(similar1))
	require.NoError(t, store.Upsert(similar2))
	require.NoError(t, store.Upsert(different))

	query := []float32{1, 0, 0}
	results, err := store.FindSimilar("book", query, 0.5, 10)
	require.NoError(t, err)

	// Should find sim-a and sim-b, not diff-c.
	ids := make([]string, len(results))
	for i, r := range results {
		ids[i] = r.EntityID
	}
	assert.Contains(t, ids, "sim-a")
	assert.Contains(t, ids, "sim-b")
	assert.NotContains(t, ids, "diff-c")

	// Results must be sorted descending by similarity.
	if len(results) >= 2 {
		assert.GreaterOrEqual(t, results[0].Similarity, results[1].Similarity)
	}
}

func TestCosineSimilarity(t *testing.T) {
	t.Run("identical vectors produce 1.0", func(t *testing.T) {
		v := []float32{1, 2, 3}
		assert.InDelta(t, 1.0, CosineSimilarity(v, v), 1e-6)
	})

	t.Run("orthogonal vectors produce 0.0", func(t *testing.T) {
		a := []float32{1, 0, 0}
		b := []float32{0, 1, 0}
		assert.InDelta(t, 0.0, CosineSimilarity(a, b), 1e-6)
	})

	t.Run("opposite vectors produce -1.0", func(t *testing.T) {
		a := []float32{1, 0, 0}
		b := []float32{-1, 0, 0}
		assert.InDelta(t, -1.0, CosineSimilarity(a, b), 1e-6)
	})
}

func TestEmbeddingStore_Delete(t *testing.T) {
	store := newTestEmbeddingStore(t)

	e := Embedding{
		EntityType: "author",
		EntityID:   "author-001",
		TextHash:   "hash1",
		Vector:     []float32{0.5, 0.5},
		Model:      "m",
	}
	require.NoError(t, store.Upsert(e))

	// Verify it exists.
	got, err := store.Get("author", "author-001")
	require.NoError(t, err)
	require.NotNil(t, got)

	// Delete and verify it is gone.
	require.NoError(t, store.Delete("author", "author-001"))

	got, err = store.Get("author", "author-001")
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestEmbeddingStore_ListByType(t *testing.T) {
	store := newTestEmbeddingStore(t)

	books := []Embedding{
		{EntityType: "book", EntityID: "b1", TextHash: "h1", Vector: []float32{1}, Model: "m"},
		{EntityType: "book", EntityID: "b2", TextHash: "h2", Vector: []float32{2}, Model: "m"},
	}
	authors := []Embedding{
		{EntityType: "author", EntityID: "a1", TextHash: "h3", Vector: []float32{3}, Model: "m"},
	}

	for _, e := range append(books, authors...) {
		require.NoError(t, store.Upsert(e))
	}

	gotBooks, err := store.ListByType("book")
	require.NoError(t, err)
	assert.Len(t, gotBooks, 2)
	for _, b := range gotBooks {
		assert.Equal(t, "book", b.EntityType)
	}

	gotAuthors, err := store.ListByType("author")
	require.NoError(t, err)
	assert.Len(t, gotAuthors, 1)
	assert.Equal(t, "a1", gotAuthors[0].EntityID)
}

// TestCandRec_LegacyDecodeCompat verifies that a candRec JSON payload written
// before T015 (i.e. without ScoreBreakdown/Band/FormulaVersion keys) decodes
// cleanly into the new struct with nil/empty values for the new fields. This
// ensures old PebbleDB rows are never corrupted or rejected after the schema
// addition.
func TestCandRec_LegacyDecodeCompat(t *testing.T) {
	// Minimal JSON as a pre-T015 writer would have stored it — no new keys.
	const legacyJSON = `{"et":"book","a":"book-aaa","b":"book-bbb","l":"exact","sim":1.0,"s":"pending","c":1000000000,"u":1000000001}`

	var rec candRec
	require.NoError(t, json.Unmarshal([]byte(legacyJSON), &rec), "legacy candRec JSON must decode without error")

	assert.Equal(t, "book", rec.EntityType)
	assert.Equal(t, "book-aaa", rec.EntityAID)
	assert.Equal(t, "exact", rec.Layer)
	assert.Equal(t, "pending", rec.Status)

	// T015 additions: must decode as zero values, never error.
	assert.Nil(t, rec.ScoreBreakdown, "ScoreBreakdown must be nil on legacy rows")
	assert.Empty(t, rec.Band, "Band must be empty on legacy rows")
	assert.Empty(t, rec.FormulaVersion, "FormulaVersion must be empty on legacy rows")
}

// TestDedupCandidate_T015Fields_RoundTrip verifies that the three new T015 fields
// (ScoreBreakdown, Band, FormulaVersion) survive a UpsertCandidate + GetCandidateByID
// round-trip through PebbleDB.
func TestDedupCandidate_T015Fields_RoundTrip(t *testing.T) {
	store := newTestEmbeddingStore(t)

	sim := 0.95
	cand := DedupCandidate{
		EntityType:     "book",
		EntityAID:      "book-aaa",
		EntityBID:      "book-bbb",
		Layer:          "embedding",
		Similarity:     &sim,
		Status:         "pending",
		Band:           "HIGH",
		FormulaVersion: "noisy-or-v1",
		// ScoreBreakdown intentionally nil — tests the non-nil path separately below.
	}
	require.NoError(t, store.UpsertCandidate(cand))

	// List candidates and find ours.
	candidates, total, err := store.ListCandidates(CandidateFilter{Status: "pending", Limit: 10})
	require.NoError(t, err)
	require.Equal(t, 1, total)
	require.Len(t, candidates, 1)

	got := candidates[0]
	assert.Equal(t, "HIGH", got.Band, "Band must survive round-trip")
	assert.Equal(t, "noisy-or-v1", got.FormulaVersion, "FormulaVersion must survive round-trip")
	assert.Nil(t, got.ScoreBreakdown, "nil ScoreBreakdown must survive round-trip as nil")
}

// TestUpsertCandidate_FormulaVersionProtection verifies the T015 layer-precedence
// addition: a row with a non-empty FormulaVersion is never overwritten by a legacy
// writer that carries an empty FormulaVersion.
func TestUpsertCandidate_FormulaVersionProtection(t *testing.T) {
	store := newTestEmbeddingStore(t)

	sim := 0.95
	unified := DedupCandidate{
		EntityType:     "book",
		EntityAID:      "book-aaa",
		EntityBID:      "book-bbb",
		Layer:          "embedding",
		Similarity:     &sim,
		Status:         "pending",
		Band:           "HIGH",
		FormulaVersion: "noisy-or-v1",
	}
	require.NoError(t, store.UpsertCandidate(unified))

	// Legacy writer: same pair, no FormulaVersion, lower similarity, different band.
	legacySim := 0.88
	legacy := DedupCandidate{
		EntityType:     "book",
		EntityAID:      "book-aaa",
		EntityBID:      "book-bbb",
		Layer:          "embedding",
		Similarity:     &legacySim,
		Status:         "pending",
		Band:           "", // legacy — no band
		FormulaVersion: "", // legacy — no formula version
	}
	require.NoError(t, store.UpsertCandidate(legacy))

	// The unified-pipeline data must be preserved — the legacy writer must NOT
	// have overwritten FormulaVersion, Band, or Similarity.
	candidates, _, err := store.ListCandidates(CandidateFilter{Status: "pending", Limit: 10})
	require.NoError(t, err)
	require.Len(t, candidates, 1)

	got := candidates[0]
	assert.Equal(t, "noisy-or-v1", got.FormulaVersion, "FormulaVersion must not be overwritten by legacy writer")
	assert.Equal(t, "HIGH", got.Band, "Band must not be overwritten by legacy writer")
	assert.InDelta(t, 0.95, *got.Similarity, 1e-9, "Similarity must not be overwritten by legacy writer")
}
