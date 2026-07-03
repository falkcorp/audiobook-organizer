// file: internal/dedup/collectors_composite_test.go
// version: 1.0.1
// guid: c3d4e5f6-a7b8-4c9d-8e0f-1a2b3c4d5e6f
// last-edited: 2026-07-03

package dedup

// Tests that verify the acceptance criteria from TASK-014:
//
//  "A pair matched by embedding+duration produces a composed score
//   with 2-signal breakdown persisted on the candidate."
//
// These tests do NOT touch engine.go integration paths — they test
// the collector + ComposeScore pipeline in isolation.

import (
	"context"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/dedup/unified"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ─── EmbeddingStore mock ──────────────────────────────────────────────────────

// stubEmbedStore implements EmbeddingStore for tests.
type stubEmbedStore struct {
	GetFunc  func(entityType, entityID string) (*database.Embedding, error)
	FindFunc func(entityType string, vector []float32, minSim float32, limit int) ([]database.SimilarityResult, error)
}

func (s *stubEmbedStore) Get(entityType, entityID string) (*database.Embedding, error) {
	if s.GetFunc != nil {
		return s.GetFunc(entityType, entityID)
	}
	return nil, nil
}

func (s *stubEmbedStore) FindSimilar(entityType string, vector []float32, minSim float32, limit int) ([]database.SimilarityResult, error) {
	if s.FindFunc != nil {
		return s.FindFunc(entityType, vector, minSim, limit)
	}
	return nil, nil
}

// ─── acceptance criterion test ────────────────────────────────────────────────

// TestCompositeScore_EmbeddingPlusDuration verifies the acceptance criterion:
// "A pair matched by embedding+duration produces a composed score with
//
//	2-signal breakdown."
//
// The test constructs signals directly (skipping collector I/O) and feeds them
// to ComposeScore, then checks the resulting UnifiedDedupScore fields.
func TestCompositeScore_EmbeddingPlusDuration(t *testing.T) {
	// Simulate embedding_high (cosine 0.97) + duration match (SigDuration).
	cosine := float32(0.97)
	signals := []unified.Signal{
		{
			Kind:       unified.SigEmbedHigh,
			Raw:        float64(cosine),
			Confidence: embedHighConfidence(cosine, 0.95), // should be ~0.894
			Evidence:   "embedding cosine 0.9700 (high tier): book A ↔ book B",
		},
		{
			Kind:       unified.SigDuration,
			Raw:        0.99, // 1% difference, very close
			Confidence: 0,    // supporting signal — ComposeScore uses Boost
			Evidence:   "duration match 1.00% difference: book A (3600s) ↔ book B (3564s)",
		},
	}

	cfg := unified.DefaultScoreConfig()
	pair := [2]string{"book_A", "book_B"}
	composed := unified.ComposeScore(signals, nil, cfg, pair)

	// Should have exactly 2 signals in the breakdown.
	require.Len(t, composed.Signals, 2, "expected 2 signals in breakdown")

	// Score: noisy-OR of the primary signal only (SigEmbedHigh).
	// embedHighConfidence(0.97) = 0.88 + (0.97-0.95)/(1.00-0.95)*0.07 = 0.88 + 0.028 = 0.908
	// P_dup = 0.908 → score = 90.8
	// + DurationBoost = +4  → 94.8 → HIGH band.
	assert.Greater(t, composed.Score, 90.0, "score should exceed 90 with embedding_high + duration boost")
	assert.LessOrEqual(t, composed.Score, 100.0, "score should be capped at 100")

	// Band should be HIGH or CERTAIN (≥ 90 after boost).
	assert.True(t, composed.Band == unified.BandHigh || composed.Band == unified.BandCertain,
		"expected HIGH or CERTAIN band, got %s (score=%.2f)", composed.Band, composed.Score)

	// FormulaVersion must be set.
	assert.Equal(t, unified.FormulaVersion, composed.Formula, "formula version must match constant")

	// Pair must be the canonical pair we passed.
	assert.Equal(t, pair, composed.Pair)

	// Suppressors must be nil (we didn't pass any).
	assert.Empty(t, composed.Suppressors)
}

// TestCompositeScore_EmbeddingMediumPlusMetaFuzzy verifies that a medium-cosine
// embedding + metadata fuzzy combination produces a HIGH score via noisy-OR.
func TestCompositeScore_EmbeddingMediumPlusMetaFuzzy(t *testing.T) {
	signals := []unified.Signal{
		{
			Kind:       unified.SigEmbedMedium,
			Raw:        0.90,
			Confidence: embedMediumConfidence(0.90, 0.85, 0.95), // 0.65 + (0.90-0.85)/(0.95-0.85)*0.15 = 0.65+0.075 = 0.725
			Evidence:   "embedding cosine 0.9000 (medium tier): book A ↔ book B",
		},
		{
			Kind:       unified.SigMetaFuzzy,
			Raw:        0.80,
			Confidence: metaFuzzyConfidence(0.80, DefaultMetaFuzzyConfig()),
			Evidence:   "metadata fuzzy title+author sim 0.8000: book A ↔ book B",
		},
	}

	cfg := unified.DefaultScoreConfig()
	pair := [2]string{"book_A", "book_B"}
	composed := unified.ComposeScore(signals, nil, cfg, pair)

	require.Len(t, composed.Signals, 2, "expected 2 signals in breakdown")

	// Noisy-OR: both are primary signals.
	// Approximate: 1 - (1-0.725)*(1-conf_fuzzy_0.80) where conf_fuzzy_0.80 ≈ 0.775
	// = 1 - 0.275 * 0.225 ≈ 1 - 0.0619 ≈ 0.938 → score ≈ 93.8 → HIGH
	assert.Greater(t, composed.Score, 85.0, "combined embedding_med + meta_fuzzy should be high")
	assert.LessOrEqual(t, composed.Score, 100.0)
	assert.NotEmpty(t, composed.Band, "should produce a non-empty band")
}

// TestCompositeScore_SuppressedPairLabeled verifies that suppressors passed to
// ComposeScore are stored in the resulting UnifiedDedupScore.
func TestCompositeScore_SuppressedPairLabeled(t *testing.T) {
	signals := []unified.Signal{
		{
			Kind:       unified.SigExactFile,
			Raw:        1.0,
			Confidence: 1.0,
			Evidence:   "whole-file hash match abc123: book A ↔ book B",
		},
	}
	suppressors := []string{"version_group_same"}

	cfg := unified.DefaultScoreConfig()
	pair := [2]string{"book_A", "book_B"}
	composed := unified.ComposeScore(signals, suppressors, cfg, pair)

	assert.Equal(t, unified.BandCertain, composed.Band, "exact_file alone should be CERTAIN")
	assert.Equal(t, suppressors, composed.Suppressors, "suppressors must pass through to score")
}

// TestCollectExactFileHash_EmitsSignalForSharedHash verifies that
// CollectExactFileHash emits a SigExactFile signal when two books share a
// file hash.
func TestCollectExactFileHash_EmitsSignalForSharedHash(t *testing.T) {
	bookA := &database.Book{ID: "BOOK_A", FileHash: strPtr("abc123")}
	bookB := &database.Book{ID: "BOOK_B", FileHash: strPtr("abc123")}

	mock := &database.MockStore{}
	mock.GetBookByFileHashFunc = func(hash string) (*database.Book, error) {
		if hash == "abc123" {
			return bookB, nil
		}
		return nil, nil
	}
	mock.GetBookFilesFunc = func(bookID string) ([]database.BookFile, error) {
		return nil, nil
	}

	sigs, err := CollectExactFileHash(mock, bookA)
	require.NoError(t, err)
	require.Len(t, sigs, 1, "expected one SigExactFile signal")
	assert.Equal(t, unified.SigExactFile, sigs[0].Kind)
	assert.Equal(t, 1.0, sigs[0].Confidence)
	assert.Equal(t, 1.0, sigs[0].Raw)
}

// TestCollectExactFileHash_SelfMatchSuppressed verifies that a book's own hash
// does not produce a self-match signal.
func TestCollectExactFileHash_SelfMatchSuppressed(t *testing.T) {
	bookA := &database.Book{ID: "BOOK_A", FileHash: strPtr("abc123")}

	mock := &database.MockStore{}
	mock.GetBookByFileHashFunc = func(hash string) (*database.Book, error) {
		return bookA, nil // returns self
	}
	mock.GetBookFilesFunc = func(bookID string) ([]database.BookFile, error) {
		return nil, nil
	}

	sigs, err := CollectExactFileHash(mock, bookA)
	require.NoError(t, err)
	assert.Empty(t, sigs, "self-match should produce no signals")
}

// TestCollectMetaSrcHash_EmitsSignalForSharedHash verifies that
// CollectMetaSrcHash emits a SigMetaSrcHash signal when books share the same
// metadata_source_hash.
func TestCollectMetaSrcHash_EmitsSignalForSharedHash(t *testing.T) {
	hash := "sha256:abcdef"
	bookA := &database.Book{ID: "BOOK_A", MetadataSourceHash: &hash}
	bookB := &database.Book{ID: "BOOK_B", MetadataSourceHash: &hash}

	mock := &database.MockStore{}
	mock.GetBooksByMetadataSourceHashFunc = func(h string) ([]database.Book, error) {
		if h == hash {
			return []database.Book{*bookA, *bookB}, nil
		}
		return nil, nil
	}

	sigs, err := CollectMetaSrcHash(mock, bookA)
	require.NoError(t, err)
	require.Len(t, sigs, 1, "expected one SigMetaSrcHash signal")
	assert.Equal(t, unified.SigMetaSrcHash, sigs[0].Kind)
	assert.Equal(t, 0.97, sigs[0].Confidence)
}

// TestCollectEmbedding_EmitsTwoTiers checks that CollectEmbedding correctly
// emits SigEmbedHigh for cosine ≥ 0.95 and SigEmbedMedium for 0.85 ≤ cos < 0.95.
func TestCollectEmbedding_EmitsTwoTiers(t *testing.T) {
	vec := []float32{0.1, 0.2, 0.3}
	stubES := &stubEmbedStore{
		GetFunc: func(entityType, entityID string) (*database.Embedding, error) {
			if entityType == "book" && entityID == "BOOK_A" {
				return &database.Embedding{EntityType: "book", EntityID: "BOOK_A", Vector: vec}, nil
			}
			return nil, nil
		},
		FindFunc: func(entityType string, vector []float32, minSim float32, limit int) ([]database.SimilarityResult, error) {
			return []database.SimilarityResult{
				{EntityID: "BOOK_B", Similarity: 0.97}, // HIGH
				{EntityID: "BOOK_C", Similarity: 0.87}, // MEDIUM
				{EntityID: "BOOK_A", Similarity: 0.99}, // self — must be skipped
			}, nil
		},
	}

	cfg := DefaultEmbeddingCollectorConfig()
	sigs, err := CollectEmbedding(context.Background(), stubES, nil, "BOOK_A", cfg)
	require.NoError(t, err)
	assert.Len(t, sigs, 2, "expected 2 signals (self skipped)")

	highCount, medCount := 0, 0
	for _, s := range sigs {
		switch s.Kind {
		case unified.SigEmbedHigh:
			highCount++
		case unified.SigEmbedMedium:
			medCount++
		}
	}
	assert.Equal(t, 1, highCount, "expected one SigEmbedHigh")
	assert.Equal(t, 1, medCount, "expected one SigEmbedMedium")
}

// TestCollectEmbedding_NoEmbeddingReturnsNil checks that CollectEmbedding
// returns nil when the book has no embedding.
func TestCollectEmbedding_NoEmbeddingReturnsNil(t *testing.T) {
	stubES := &stubEmbedStore{
		GetFunc: func(entityType, entityID string) (*database.Embedding, error) {
			return nil, nil // no embedding
		},
	}

	cfg := DefaultEmbeddingCollectorConfig()
	sigs, err := CollectEmbedding(context.Background(), stubES, nil, "BOOK_X", cfg)
	require.NoError(t, err)
	assert.Nil(t, sigs)
}

// TestCollectMetaFuzzy_EmitsSignalForSimilarTitles verifies that
// CollectMetaFuzzy emits a SigMetaFuzzy signal when title+author similarity
// exceeds the threshold.
func TestCollectMetaFuzzy_EmitsSignalForSimilarTitles(t *testing.T) {
	authorID := 1
	bookA := &database.Book{ID: "BOOK_A", Title: "Foundation", AuthorID: &authorID}
	bookB := &database.Book{ID: "BOOK_B", Title: "Foundation", AuthorID: &authorID}

	mock := &database.MockStore{}
	mock.GetBookByIDFunc = func(id string) (*database.Book, error) {
		if id == "BOOK_B" {
			return bookB, nil
		}
		return nil, nil
	}
	mock.GetAuthorByIDFunc = func(id int) (*database.Author, error) {
		return &database.Author{ID: 1, Name: "Isaac Asimov"}, nil
	}
	cfg := DefaultMetaFuzzyConfig()
	sigs, err := CollectMetaFuzzy(mock, bookA, "Isaac Asimov", []string{"BOOK_B"}, cfg)
	require.NoError(t, err)
	require.Len(t, sigs, 1, "expected one SigMetaFuzzy signal for identical title+author")
	assert.Equal(t, unified.SigMetaFuzzy, sigs[0].Kind)
	assert.Greater(t, sigs[0].Confidence, 0.70, "confidence should be > min (0.70)")
	assert.LessOrEqual(t, sigs[0].Confidence, 0.85, "confidence should be ≤ max (0.85)")
}

// TestCollectMetaFuzzy_NoCandidates returns nil immediately with no I/O.
func TestCollectMetaFuzzy_NoCandidates(t *testing.T) {
	bookA := &database.Book{ID: "BOOK_A", Title: "Foundation"}
	mock := &database.MockStore{}
	sigs, err := CollectMetaFuzzy(mock, bookA, "Isaac Asimov", nil, DefaultMetaFuzzyConfig())
	require.NoError(t, err)
	assert.Nil(t, sigs)
}

// TestNormalizedLevenshteinSimilarity verifies the core similarity function.
func TestNormalizedLevenshteinSimilarity(t *testing.T) {
	tests := []struct {
		a, b string
		want float64
	}{
		{"identical", "identical", 1.0},
		{"", "", 1.0},
		{"abc", "", 0.0},
		{"abc", "abc", 1.0},
		{"foundation", "foundations", 1.0 - 1.0/11.0}, // 1 char diff, max len 11
	}
	for _, tt := range tests {
		t.Run(tt.a+"_vs_"+tt.b, func(t *testing.T) {
			got := normalizedLevenshteinSimilarity(tt.a, tt.b)
			assert.InDelta(t, tt.want, got, 0.01)
		})
	}
}

// TestBestLayerFromSignals verifies the layer priority ordering.
func TestBestLayerFromSignals(t *testing.T) {
	tests := []struct {
		name      string
		signals   []unified.Signal
		wantLayer string
	}{
		{
			"exact_file wins over embedding",
			[]unified.Signal{
				{Kind: unified.SigEmbedHigh},
				{Kind: unified.SigExactFile},
			},
			"exact",
		},
		{
			"embedding wins when no exact",
			[]unified.Signal{{Kind: unified.SigEmbedHigh}},
			"embedding",
		},
		{
			"acoustid via SigExactAcoustID",
			[]unified.Signal{{Kind: unified.SigExactAcoustID}},
			"acoustid",
		},
		{
			"empty signals falls back to embedding",
			nil,
			"embedding",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := bestLayerFromSignals(tt.signals)
			assert.Equal(t, tt.wantLayer, got)
		})
	}
}

// TestCanonicalPairIDs verifies that canonicalPairIDs always returns the pair
// in sorted order.
func TestCanonicalPairIDs(t *testing.T) {
	assert.Equal(t, [2]string{"A", "B"}, canonicalPairIDs("A", "B"))
	assert.Equal(t, [2]string{"A", "B"}, canonicalPairIDs("B", "A"))
	assert.Equal(t, [2]string{"x", "y"}, canonicalPairIDs("y", "x"))
}
