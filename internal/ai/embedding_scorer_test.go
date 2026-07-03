// file: internal/ai/embedding_scorer_test.go
// version: 2.1.0
// guid: 9c3e5b17-6f8a-4d2e-b091-5a7c8d4e2f6a
// last-edited: 2026-07-03

package ai

import (
	"context"
	"errors"
	"testing"

	"github.com/cockroachdb/pebble/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// fakeEmbedAPI is an in-process stand-in for the OpenAI embeddings endpoint.
// Tests install a textToVec function that maps a text to a deterministic
// vector, so cosine math is predictable without real API calls.
type fakeEmbedAPI struct {
	textToVec  func(string) []float32
	embedOne   int // call counts for assertions
	embedBatch int
	failNext   error
	model      string // Model() return value; set explicitly when a test relies on the store fast-path
}

// Model implements embeddingAPI.
func (f *fakeEmbedAPI) Model() string { return f.model }

func (f *fakeEmbedAPI) EmbedOne(ctx context.Context, text string) ([]float32, error) {
	f.embedOne++
	if f.failNext != nil {
		err := f.failNext
		f.failNext = nil
		return nil, err
	}
	return f.textToVec(text), nil
}

func (f *fakeEmbedAPI) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	f.embedBatch++
	if f.failNext != nil {
		err := f.failNext
		f.failNext = nil
		return nil, err
	}
	out := make([][]float32, len(texts))
	for i, t := range texts {
		out[i] = f.textToVec(t)
	}
	return out, nil
}

// oneHotByPrefix returns a 4-dim vector where the hot index depends on the
// first character of the text. Two texts sharing a first letter get identical
// vectors (cosine = 1.0), different letters get orthogonal vectors
// (cosine = 0.0). This makes the test assertions trivial to read.
func oneHotByPrefix(text string) []float32 {
	if text == "" {
		return []float32{0, 0, 0, 0}
	}
	switch text[0] {
	case 'a', 'A':
		return []float32{1, 0, 0, 0}
	case 'b', 'B':
		return []float32{0, 1, 0, 0}
	case 'c', 'C':
		return []float32{0, 0, 1, 0}
	default:
		return []float32{0, 0, 0, 1}
	}
}

func newFakeScorer(t *testing.T) (*EmbeddingScorer, *fakeEmbedAPI) {
	t.Helper()
	api := &fakeEmbedAPI{textToVec: oneHotByPrefix}
	scorer := NewEmbeddingScorerWithAPI(api, nil)
	return scorer, api
}

func TestEmbeddingScorer_Name(t *testing.T) {
	scorer, _ := newFakeScorer(t)
	assert.Equal(t, "embedding", scorer.Name())
}

func TestEmbeddingScorer_EmptyCandidates(t *testing.T) {
	scorer, api := newFakeScorer(t)
	scores, err := scorer.Score(context.Background(), Query{Title: "Dune"}, nil)
	require.NoError(t, err)
	assert.Nil(t, scores)
	assert.Equal(t, 0, api.embedOne, "empty candidates should not trigger query embedding")
	assert.Equal(t, 0, api.embedBatch, "empty candidates should not trigger candidate batch")
}

func TestEmbeddingScorer_CosineRanking(t *testing.T) {
	scorer, api := newFakeScorer(t)

	// Query title "Dune" starts with 'd' → hot index 3 (the default branch).
	// Candidates use known prefixes that give orthogonal or identical vectors.
	scores, err := scorer.Score(context.Background(), Query{Title: "Dune by Frank Herbert"}, []Candidate{
		{Title: "abyss", Author: "X"},     // different prefix → cosine 0
		{Title: "different", Author: "X"}, // 'd' prefix → same vector as query → cosine 1
		{Title: "boring", Author: "X"},    // different prefix → cosine 0
	})
	require.NoError(t, err)
	require.Len(t, scores, 3)
	assert.InDelta(t, 0.0, scores[0], 0.01, "candidate 0 should be orthogonal to query")
	assert.InDelta(t, 1.0, scores[1], 0.01, "candidate 1 should match query perfectly")
	assert.InDelta(t, 0.0, scores[2], 0.01, "candidate 2 should be orthogonal to query")

	assert.Equal(t, 1, api.embedOne, "query should be embedded once")
	assert.Equal(t, 1, api.embedBatch, "candidates should be batch-embedded once")
}

func TestEmbeddingScorer_ClampsNegativeCosine(t *testing.T) {
	// Force an opposite-direction vector to produce cosine = -1, verify it
	// clamps to 0.
	api := &fakeEmbedAPI{
		textToVec: func(text string) []float32 {
			if text[0] == 'q' {
				return []float32{1, 0, 0, 0}
			}
			return []float32{-1, 0, 0, 0}
		},
	}
	scorer := NewEmbeddingScorerWithAPI(api, nil)
	scores, err := scorer.Score(context.Background(), Query{Title: "query"}, []Candidate{
		{Title: "other"},
	})
	require.NoError(t, err)
	assert.Equal(t, 0.0, scores[0], "negative cosine should clamp to 0")
}

func TestEmbeddingScorer_QueryEmbedError(t *testing.T) {
	api := &fakeEmbedAPI{textToVec: oneHotByPrefix, failNext: errors.New("boom")}
	scorer := NewEmbeddingScorerWithAPI(api, nil)

	scores, err := scorer.Score(context.Background(), Query{Title: "Dune"}, []Candidate{
		{Title: "Dune"},
	})
	require.Error(t, err)
	assert.Nil(t, scores, "partial results are never returned")
}

func TestEmbeddingScorer_CandidateBatchError(t *testing.T) {
	api := &fakeEmbedAPI{textToVec: oneHotByPrefix}
	scorer := NewEmbeddingScorerWithAPI(api, nil)

	// First call succeeds (query), next call (batch) fails.
	_, _ = scorer.Score(context.Background(), Query{Title: "Dune"}, []Candidate{{Title: "Dune"}})
	api.failNext = errors.New("batch failure")

	scores, err := scorer.Score(context.Background(), Query{Title: "Dune"}, []Candidate{
		{Title: "Dune"},
		{Title: "Dune Messiah"},
	})
	require.Error(t, err)
	assert.Nil(t, scores)
}

func TestEmbeddingScorer_BookIDFastPath(t *testing.T) {
	// Spin up a real temp-dir EmbeddingStore and seed a known vector for a
	// specific book ID. Verify the scorer uses that vector instead of calling
	// EmbedOne.
	tmpDir := t.TempDir()
	db, err := pebble.Open(tmpDir, &pebble.Options{})
	require.NoError(t, err)
	store := database.NewEmbeddingStore(db)
	t.Cleanup(func() { _ = db.Close() })

	// Seed book BOOK_A with a one-hot 'a'-style vector so it matches
	// candidates whose text starts with 'a'.
	require.NoError(t, store.Upsert(database.Embedding{
		EntityType: "book",
		EntityID:   "BOOK_A",
		TextHash:   "hash-a",
		Vector:     []float32{1, 0, 0, 0},
		Model:      "text-embedding-3-large",
	}))

	api := &fakeEmbedAPI{textToVec: oneHotByPrefix, model: "text-embedding-3-large"}
	scorer := NewEmbeddingScorerWithAPI(api, store)

	scores, err := scorer.Score(context.Background(),
		Query{BookID: "BOOK_A", Title: "whatever the title is"},
		[]Candidate{
			{Title: "abyss"},     // 'a' → matches seeded vector
			{Title: "different"}, // default → orthogonal
		},
	)
	require.NoError(t, err)
	require.Len(t, scores, 2)
	assert.InDelta(t, 1.0, scores[0], 0.01)
	assert.InDelta(t, 0.0, scores[1], 0.01)

	assert.Equal(t, 0, api.embedOne, "BookID fast-path should skip query embedding")
	assert.Equal(t, 1, api.embedBatch, "candidates are still batch-embedded")
}

func TestEmbeddingScorer_BookIDMissFallsBackToEmbed(t *testing.T) {
	tmpDir := t.TempDir()
	db, err := pebble.Open(tmpDir, &pebble.Options{})
	require.NoError(t, err)
	store := database.NewEmbeddingStore(db)
	t.Cleanup(func() { _ = db.Close() })
	// Store has no entry for BOOK_MISSING.

	api := &fakeEmbedAPI{textToVec: oneHotByPrefix}
	scorer := NewEmbeddingScorerWithAPI(api, store)

	_, err = scorer.Score(context.Background(),
		Query{BookID: "BOOK_MISSING", Title: "Dune"},
		[]Candidate{{Title: "Dune"}},
	)
	require.NoError(t, err)
	assert.Equal(t, 1, api.embedOne, "store miss should fall back to EmbedOne")
}

// oneHotByPrefix1024 is oneHotByPrefix's 1024-dim analog, used to simulate a
// post-cutover live client (e.g. bge-m3) whose vector dimensionality differs
// from a stale stored OpenAI (text-embedding-3-large, 3072-dim) vector.
func oneHotByPrefix1024(text string) []float32 {
	v := make([]float32, 1024)
	if text == "" {
		return v
	}
	switch text[0] {
	case 'a', 'A':
		v[0] = 1
	case 'b', 'B':
		v[1] = 1
	case 'c', 'C':
		v[2] = 1
	default:
		v[3] = 1
	}
	return v
}

// TestEmbeddingScorer_BookIDModelMismatchFallsBackToEmbed proves MATCH-1/BUG-1:
// when the store holds a cached vector from a different embedding model (or
// dimension) than the live client, the BookID fast-path must NOT be trusted
// — it must fall through to a live re-embed rather than returning a stale
// vector that would silently score 0 against every candidate via
// CosineSimilarity's length mismatch.
func TestEmbeddingScorer_BookIDModelMismatchFallsBackToEmbed(t *testing.T) {
	tmpDir := t.TempDir()
	db, err := pebble.Open(tmpDir, &pebble.Options{})
	require.NoError(t, err)
	store := database.NewEmbeddingStore(db)
	t.Cleanup(func() { _ = db.Close() })

	// Seed a stale 3072-dim vector tagged with the old OpenAI model name.
	staleVec := make([]float32, 3072)
	staleVec[0] = 1
	staleVec[1] = 0.5
	require.NoError(t, store.Upsert(database.Embedding{
		EntityType: "book",
		EntityID:   "BOOK_A",
		TextHash:   "hash-a",
		Vector:     staleVec,
		Model:      "text-embedding-3-large",
	}))

	// Live client reports a different model (post-cutover local model) and
	// produces 1024-dim vectors.
	api := &fakeEmbedAPI{textToVec: oneHotByPrefix1024, model: "bge-m3"}
	scorer := NewEmbeddingScorerWithAPI(api, store)

	scores, err := scorer.Score(context.Background(),
		Query{BookID: "BOOK_A", Title: "whatever the title is"},
		[]Candidate{
			{Title: "abyss"},     // 'a' → matches live-embedded query vector
			{Title: "different"}, // default → orthogonal to live-embedded query vector
		},
	)
	require.NoError(t, err)
	require.Len(t, scores, 2)

	// "whatever the title is" starts with 'w' → default branch → v[3] = 1.
	// So candidate 0 ('a' → v[0]=1) is orthogonal, candidate 1 (default →
	// v[3]=1) matches.
	assert.InDelta(t, 0.0, scores[0], 0.01, "mismatched-model fast path must not produce a stale-vector score")
	assert.InDelta(t, 1.0, scores[1], 0.01, "post-fix score should be a real cosine value from a live re-embed")

	assert.Equal(t, 1, api.embedOne,
		"model mismatch must bypass the BookID fast-path and trigger a live re-embed")
}
