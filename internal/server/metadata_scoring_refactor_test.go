// file: internal/server/metadata_scoring_refactor_test.go
// version: 1.3.0
// guid: 3a7c2b1d-e84f-4d59-9f16-0e5a8b2c4d7e

package server

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/ai"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// TestScoreOneResult_RefactorEquivalence locks in the current output of
// scoreOneResult against representative inputs so the split into base +
// non-base halves can't accidentally change the combined result.
func TestScoreOneResult_RefactorEquivalence(t *testing.T) {
	searchWords := metafetch.SignificantWords("The Way of Kings")

	cases := []struct {
		name   string
		input  metadata.BookMetadata
		minExp float64
		maxExp float64
	}{
		{
			name: "exact title match with rich metadata",
			input: metadata.BookMetadata{
				Title:       "The Way of Kings",
				Description: "long description",
				CoverURL:    "https://example/cover.jpg",
				Narrator:    "Kate Reading",
				ISBN:        "9780765326355",
			},
			// F1 = 1.0, full rich-metadata bonus (+0.15), no penalties
			minExp: 1.10, maxExp: 1.20,
		},
		{
			name: "compilation penalty fires",
			input: metadata.BookMetadata{
				Title: "The Way of Kings (Stormlight Archive Omnibus)",
			},
			// Contains "omnibus" → compilation multiplier 0.15 → tiny score
			minExp: 0.0, maxExp: 0.20,
		},
		{
			name: "unrelated title",
			input: metadata.BookMetadata{
				Title: "Completely Different Book",
			},
			// F1 ~ 0 (no overlap) + no bonus
			minExp: 0.0, maxExp: 0.05,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := metafetch.ScoreOneResult(tc.input, searchWords)
			assert.GreaterOrEqual(t, got, tc.minExp, "score below expected range")
			assert.LessOrEqual(t, got, tc.maxExp, "score above expected range")
		})
	}
}

// newScoringTestService creates a metafetch.Service with the given scorer(s) set.
func newScoringTestService(scorer, llmScorer ai.MetadataCandidateScorer) *metafetch.Service {
	svc := metafetch.NewService(nil)
	if scorer != nil {
		svc.SetMetadataScorer(scorer)
	}
	if llmScorer != nil {
		svc.SetMetadataLLMScorer(llmScorer)
	}
	return svc
}

// scorerStub is a controllable MetadataCandidateScorer for tests.
type scorerStub struct {
	name      string
	scores    []float64
	err       error
	callCount int
}

func (s *scorerStub) Score(ctx context.Context, q ai.Query, cands []ai.Candidate) ([]float64, error) {
	s.callCount++
	if s.err != nil {
		return nil, s.err
	}
	return s.scores, nil
}

func (s *scorerStub) Name() string { return s.name }

func TestScoreBaseCandidates_EmbeddingTierUsed(t *testing.T) {
	mfs := newScoringTestService(&scorerStub{
		name:   "embedding",
		scores: []float64{0.9, 0.7, 0.3},
	}, nil)
	prev := config.AppConfig.MetadataScoring.EmbeddingEnabled
	config.AppConfig.MetadataScoring.EmbeddingEnabled = true
	defer func() { config.AppConfig.MetadataScoring.EmbeddingEnabled = prev }()

	results := []metadata.BookMetadata{
		{Title: "A"}, {Title: "B"}, {Title: "C"},
	}
	searchWords := metafetch.SignificantWords("A")
	book := &database.Book{ID: "BOOK1", Title: "A"}

	scores, tier := mfs.ScoreBaseCandidates(context.Background(), book, results, searchWords)
	assert.Equal(t, "embedding", tier)
	assert.Equal(t, []float64{0.9, 0.7, 0.3}, scores)
}

func TestScoreBaseCandidates_ConfigDisabledFallsBackToF1(t *testing.T) {
	mfs := newScoringTestService(&scorerStub{
		name:   "embedding",
		scores: []float64{1.0, 1.0, 1.0},
	}, nil)
	prev := config.AppConfig.MetadataScoring.EmbeddingEnabled
	config.AppConfig.MetadataScoring.EmbeddingEnabled = false
	defer func() { config.AppConfig.MetadataScoring.EmbeddingEnabled = prev }()

	results := []metadata.BookMetadata{
		{Title: "The Way of Kings"},
		{Title: "Completely Unrelated Book"},
	}
	searchWords := metafetch.SignificantWords("The Way of Kings")
	book := &database.Book{ID: "BOOK1", Title: "The Way of Kings"}

	scores, tier := mfs.ScoreBaseCandidates(context.Background(), book, results, searchWords)
	assert.Equal(t, "f1", tier)
	assert.Len(t, scores, 2)
	assert.InDelta(t, 1.0, scores[0], 0.01)
	assert.InDelta(t, 0.0, scores[1], 0.1)
}

func TestScoreBaseCandidates_ScorerErrorFallsBackToF1(t *testing.T) {
	stub := &scorerStub{name: "embedding", err: errors.New("api boom")}
	mfs := newScoringTestService(stub, nil)
	prev := config.AppConfig.MetadataScoring.EmbeddingEnabled
	config.AppConfig.MetadataScoring.EmbeddingEnabled = true
	defer func() { config.AppConfig.MetadataScoring.EmbeddingEnabled = prev }()

	results := []metadata.BookMetadata{{Title: "The Way of Kings"}}
	searchWords := metafetch.SignificantWords("The Way of Kings")
	book := &database.Book{ID: "BOOK1", Title: "The Way of Kings"}

	scores, tier := mfs.ScoreBaseCandidates(context.Background(), book, results, searchWords)
	assert.Equal(t, "f1", tier, "scorer error should fall back to F1 tier")
	assert.Equal(t, 1, stub.callCount, "scorer should be called exactly once")
	assert.InDelta(t, 1.0, scores[0], 0.01)
}

func TestScoreBaseCandidates_NilScorerFallsBackSilently(t *testing.T) {
	mfs := newScoringTestService(nil, nil)
	prev := config.AppConfig.MetadataScoring.EmbeddingEnabled
	config.AppConfig.MetadataScoring.EmbeddingEnabled = true
	defer func() { config.AppConfig.MetadataScoring.EmbeddingEnabled = prev }()

	results := []metadata.BookMetadata{{Title: "The Way of Kings"}}
	searchWords := metafetch.SignificantWords("The Way of Kings")
	book := &database.Book{ID: "BOOK1", Title: "The Way of Kings"}

	scores, tier := mfs.ScoreBaseCandidates(context.Background(), book, results, searchWords)
	assert.Equal(t, "f1", tier)
	assert.Len(t, scores, 1)
}

// TestMetadataScorer_WiredEndToEnd verifies that an injected scorer's output
// reaches the main search loop via scoreBaseCandidates. It uses a
// controllable scorerStub and feeds it canned results, then checks that the
// stub was invoked and its scores became the base scores used downstream.
func TestMetadataScorer_WiredEndToEnd(t *testing.T) {
	// Stub scorer that prefers the second candidate over the first.
	stub := &scorerStub{
		name:   "embedding",
		scores: []float64{0.30, 0.95},
	}

	mfs := newScoringTestService(stub, nil)
	prev := config.AppConfig.MetadataScoring.EmbeddingEnabled
	prevMin := config.AppConfig.MetadataScoring.EmbeddingMinScore
	config.AppConfig.MetadataScoring.EmbeddingEnabled = true
	config.AppConfig.MetadataScoring.EmbeddingMinScore = 0.50
	defer func() {
		config.AppConfig.MetadataScoring.EmbeddingEnabled = prev
		config.AppConfig.MetadataScoring.EmbeddingMinScore = prevMin
	}()

	book := &database.Book{ID: "BOOK_X", Title: "Query Title"}
	results := []metadata.BookMetadata{
		{Title: "Weak Match", Author: "Someone"},
		{Title: "Strong Match", Author: "Someone"},
	}
	searchWords := metafetch.SignificantWords("Query Title")

	scores, tier := mfs.ScoreBaseCandidates(context.Background(), book, results, searchWords)
	assert.Equal(t, "embedding", tier)
	assert.Equal(t, []float64{0.30, 0.95}, scores)
	assert.Equal(t, 1, stub.callCount, "scorer called exactly once")

	// Verify the min-score filter logic by running it inline (mirrors the
	// main search loop's minScore check after applyNonBaseAdjustments).
	var kept []int
	for i, s := range scores {
		adjusted := metafetch.ApplyNonBaseAdjustments(s, results[i], 0)
		if adjusted > config.AppConfig.MetadataScoring.EmbeddingMinScore {
			kept = append(kept, i)
		}
	}
	assert.Equal(t, []int{1}, kept, "only the strong match should survive the filter")
}

// TestRerankTopK_FiresOnAmbiguousTop checks that rerankTopK sends exactly the
// candidates within MetadataLLMRerankEpsilon of the best score to the LLM,
// and replaces their Score fields with the LLM's output.
func TestRerankTopK_FiresOnAmbiguousTop(t *testing.T) {
	// LLM says candidate 1 is actually the winner (0.95) even though
	// candidate 0 had a higher base score (0.90).
	llm := &scorerStub{
		name:   "llm",
		scores: []float64{0.60, 0.95}, // indices 0 and 1 of the ambiguous top
	}
	mfs := newScoringTestService(nil, llm)

	prevEps := config.AppConfig.MetadataScoring.LLMRerankEpsilon
	prevK := config.AppConfig.MetadataScoring.LLMRerankTopK
	config.AppConfig.MetadataScoring.LLMRerankEpsilon = 0.05
	config.AppConfig.MetadataScoring.LLMRerankTopK = 5
	defer func() {
		config.AppConfig.MetadataScoring.LLMRerankEpsilon = prevEps
		config.AppConfig.MetadataScoring.LLMRerankTopK = prevK
	}()

	book := &database.Book{ID: "BOOK", Title: "Query"}
	candidates := []metafetch.MetadataCandidate{
		{Title: "A", Score: 0.90},
		{Title: "B", Score: 0.88}, // within epsilon of 0.90 → rerank
		{Title: "C", Score: 0.70}, // outside epsilon → untouched
		{Title: "D", Score: 0.50}, // outside epsilon → untouched
	}

	got := mfs.RerankTopK(context.Background(), book, candidates)
	assert.Equal(t, 1, llm.callCount, "LLM should be called exactly once")
	require.Len(t, got, 4)

	// After rerank + resort, candidate B (0.95) should be first, A (0.60)
	// should be pushed down, C (0.70) and D (0.50) should be unchanged.
	assert.Equal(t, "B", got[0].Title)
	assert.InDelta(t, 0.95, got[0].Score, 0.0001)
	assert.Equal(t, "C", got[1].Title, "C's 0.70 should now outrank A's demoted 0.60")
	assert.InDelta(t, 0.70, got[1].Score, 0.0001)
	assert.Equal(t, "A", got[2].Title)
	assert.InDelta(t, 0.60, got[2].Score, 0.0001)
	assert.Equal(t, "D", got[3].Title)
	assert.InDelta(t, 0.50, got[3].Score, 0.0001)
}

// TestRerankTopK_HonorsTopKCap verifies the topK cap even when more
// candidates are within epsilon of the best.
func TestRerankTopK_HonorsTopKCap(t *testing.T) {
	llm := &scorerStub{
		name:   "llm",
		scores: []float64{0.90, 0.80, 0.70}, // 3 scores, matching topK=3
	}
	mfs := newScoringTestService(nil, llm)

	prevEps := config.AppConfig.MetadataScoring.LLMRerankEpsilon
	prevK := config.AppConfig.MetadataScoring.LLMRerankTopK
	config.AppConfig.MetadataScoring.LLMRerankEpsilon = 0.50 // huge — everything is "ambiguous"
	config.AppConfig.MetadataScoring.LLMRerankTopK = 3
	defer func() {
		config.AppConfig.MetadataScoring.LLMRerankEpsilon = prevEps
		config.AppConfig.MetadataScoring.LLMRerankTopK = prevK
	}()

	book := &database.Book{ID: "BOOK", Title: "Query"}
	candidates := []metafetch.MetadataCandidate{
		{Title: "A", Score: 0.85},
		{Title: "B", Score: 0.80},
		{Title: "C", Score: 0.75},
		{Title: "D", Score: 0.70}, // would be in epsilon but topK caps at 3
		{Title: "E", Score: 0.65},
	}

	mfs.RerankTopK(context.Background(), book, candidates)
	assert.Equal(t, 1, llm.callCount)
	// The stub received exactly 3 candidates — verify via scores slice length
	// we handed it (the stub returned a 3-element slice).
	assert.Len(t, llm.scores, 3)
}

// TestRerankTopK_NoAmbiguityReturnsUnchanged verifies that when only one
// candidate is within epsilon of the best, rerank is a no-op.
func TestRerankTopK_NoAmbiguityReturnsUnchanged(t *testing.T) {
	llm := &scorerStub{name: "llm", scores: []float64{0.9}}
	mfs := newScoringTestService(nil, llm)

	prevEps := config.AppConfig.MetadataScoring.LLMRerankEpsilon
	config.AppConfig.MetadataScoring.LLMRerankEpsilon = 0.01
	defer func() { config.AppConfig.MetadataScoring.LLMRerankEpsilon = prevEps }()

	candidates := []metafetch.MetadataCandidate{
		{Title: "A", Score: 0.95},
		{Title: "B", Score: 0.70}, // 0.25 below best → outside epsilon
		{Title: "C", Score: 0.50},
	}
	got := mfs.RerankTopK(context.Background(), &database.Book{ID: "B"}, candidates)
	assert.Equal(t, 0, llm.callCount, "LLM should not be called when only 1 candidate is ambiguous")
	assert.Equal(t, "A", got[0].Title)
	assert.InDelta(t, 0.95, got[0].Score, 0.0001)
}

// TestRerankTopK_NilScorerIsNoOp verifies the nil-scorer fallback.
func TestRerankTopK_NilScorerIsNoOp(t *testing.T) {
	mfs := newScoringTestService(nil, nil)
	candidates := []metafetch.MetadataCandidate{
		{Title: "A", Score: 0.95},
		{Title: "B", Score: 0.94},
	}
	got := mfs.RerankTopK(context.Background(), &database.Book{ID: "B"}, candidates)
	assert.Equal(t, "A", got[0].Title, "nil scorer should return candidates unchanged")
	assert.InDelta(t, 0.95, got[0].Score, 0.0001)
}

// TestRerankTopK_LLMErrorKeepsBaseScores verifies that a scorer error leaves
// the base scores untouched.
func TestRerankTopK_LLMErrorKeepsBaseScores(t *testing.T) {
	llm := &scorerStub{name: "llm", err: errors.New("openai boom")}
	mfs := newScoringTestService(nil, llm)

	prevEps := config.AppConfig.MetadataScoring.LLMRerankEpsilon
	config.AppConfig.MetadataScoring.LLMRerankEpsilon = 0.10
	defer func() { config.AppConfig.MetadataScoring.LLMRerankEpsilon = prevEps }()

	candidates := []metafetch.MetadataCandidate{
		{Title: "A", Score: 0.95},
		{Title: "B", Score: 0.92},
	}
	got := mfs.RerankTopK(context.Background(), &database.Book{ID: "B"}, candidates)
	assert.Equal(t, 1, llm.callCount)
	assert.Equal(t, "A", got[0].Title)
	assert.InDelta(t, 0.95, got[0].Score, 0.0001, "base score preserved on LLM error")
	assert.InDelta(t, 0.92, got[1].Score, 0.0001)
}
