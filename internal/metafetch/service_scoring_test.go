// file: internal/metafetch/service_scoring_test.go
// version: 1.0.0
// guid: 7e3f6a1c-9b4d-4e2a-8c1f-6d5b3a2e9f70
// last-edited: 2026-07-03

package metafetch

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/ai"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

// fakeCandidateScorer is a minimal ai.MetadataCandidateScorer test double
// that returns a fixed score slice (or error) regardless of input, so tests
// can drive ScoreBaseCandidates' acceptance logic directly.
type fakeCandidateScorer struct {
	name   string
	scores []float64
	err    error
}

func (f *fakeCandidateScorer) Score(ctx context.Context, q ai.Query, cands []ai.Candidate) ([]float64, error) {
	return f.scores, f.err
}
func (f *fakeCandidateScorer) Name() string {
	if f.name == "" {
		return "embedding"
	}
	return f.name
}

// withEmbeddingScoringEnabled sets config.AppConfig.MetadataScoring.EmbeddingEnabled
// for the duration of the test and restores the prior value on cleanup.
func withEmbeddingScoringEnabled(t *testing.T, enabled bool) {
	t.Helper()
	prev := config.AppConfig.MetadataScoring.EmbeddingEnabled
	config.AppConfig.MetadataScoring.EmbeddingEnabled = enabled
	t.Cleanup(func() {
		config.AppConfig.MetadataScoring.EmbeddingEnabled = prev
	})
}

// ---------------------------------------------------------------------------
// ScoreBaseCandidates — degenerate all-zero scorer result falls back to F1
// ---------------------------------------------------------------------------

func TestScoreBaseCandidates_DegenerateAllZeroFallsBackToF1(t *testing.T) {
	withEmbeddingScoringEnabled(t, true)

	mock := &database.MockStore{}
	svc := NewService(mock)
	svc.SetMetadataScorer(&fakeCandidateScorer{
		scores: []float64{0, 0},
	})

	book := &database.Book{ID: "b1", Title: "Mistborn"}
	results := []metadata.BookMetadata{
		{Title: "Mistborn"},
		{Title: "Completely Different Book"},
	}
	searchWords := SignificantWords("Mistborn")

	scores, tier := svc.ScoreBaseCandidates(context.Background(), book, results, searchWords)

	require.Equal(t, "f1", tier, "an all-zero scorer result must be treated as failure and fall back to F1")
	require.Len(t, scores, 2)
	for i, r := range results {
		assert.Equal(t, computeF1Base(r, searchWords), scores[i], "fallback scores should match computeF1Base directly")
	}
}

// TestScoreBaseCandidates_NonDegenerateStaysOnEmbeddingTier proves the fix
// doesn't over-trigger the fallback: a scorer returning genuine non-zero
// scores must still win the "embedding" tier.
func TestScoreBaseCandidates_NonDegenerateStaysOnEmbeddingTier(t *testing.T) {
	withEmbeddingScoringEnabled(t, true)

	mock := &database.MockStore{}
	svc := NewService(mock)
	svc.SetMetadataScorer(&fakeCandidateScorer{
		scores: []float64{0.9, 0.1},
	})

	book := &database.Book{ID: "b1", Title: "Mistborn"}
	results := []metadata.BookMetadata{
		{Title: "Mistborn"},
		{Title: "Completely Different Book"},
	}
	searchWords := SignificantWords("Mistborn")

	scores, tier := svc.ScoreBaseCandidates(context.Background(), book, results, searchWords)

	require.Equal(t, "embedding", tier, "a genuine non-zero scorer result must not be discarded")
	require.Equal(t, []float64{0.9, 0.1}, scores)
}

// ---------------------------------------------------------------------------
// End-to-end: a degenerate embedding tier must not zero out search results
// (MATCH-1/BUG-1/QUAL-4 regression, service_search.go's threshold filter).
// ---------------------------------------------------------------------------

func TestSearchMetadataForBookWithOptions_DegenerateEmbeddingFallsBackToF1Results(t *testing.T) {
	withEmbeddingScoringEnabled(t, true)

	mock := &database.MockStore{
		GetBookByIDFunc: func(id string) (*database.Book, error) {
			return &database.Book{ID: id, Title: "Mistborn"}, nil
		},
	}
	svc := NewService(mock)
	svc.SetOverrideSources([]metadata.MetadataSource{
		&mockMetadataSource{
			name: "test-source",
			results: []metadata.BookMetadata{
				{Title: "Mistborn", Author: "Brandon Sanderson"},
				{Title: "Mistborn: The Final Empire", Author: "Brandon Sanderson"},
			},
		},
	})
	// Simulate the degenerate case: every candidate scored exactly 0 by the
	// embedding tier (e.g. a stale cross-model vector comparison). Before the
	// fix, ScoreBaseCandidates would have accepted this as tier="embedding"
	// and the threshold filter (minScore = EmbeddingMinScore, default 0.82)
	// would have dropped every candidate, returning zero results.
	svc.SetMetadataScorer(&fakeCandidateScorer{
		scores: []float64{0, 0},
	})

	resp, err := svc.SearchMetadataForBookWithOptions("b1", "Mistborn", "", "", "", SearchOptions{})
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.NotEmpty(t, resp.Results, "F1 fallback should surface non-empty results even though the embedding tier degenerated to all-zero scores")
}
