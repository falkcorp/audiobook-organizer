// file: internal/ai/llm_scorer.go
// version: 1.0.1
// guid: 7c3d9f21-a4b8-4e15-92f6-0d5c8b1e6a3f
// last-edited: 2026-10-11

package ai

import (
	"context"
	"fmt"
)

// metadataLLMBackend is the minimal surface LLMScorer needs from the
// OpenAI parser. It exists so tests can inject a fake without spinning
// up the real chat client. Production code always wires the real
// *OpenAIParser here via NewLLMScorer.
type metadataLLMBackend interface {
	ScoreMetadataCandidates(
		ctx context.Context,
		query MetadataLLMQuery,
		candidates []MetadataLLMCandidate,
	) ([]MetadataLLMScore, error)
}

// LLMScorer satisfies MetadataCandidateScorer by delegating to
// OpenAIParser.ScoreMetadataCandidates. It's the third tier in the
// metadata candidate scoring stack: F1 (free) → embedding (cheap) →
// LLM (slower, more accurate, opt-in per search).
type LLMScorer struct {
	backend metadataLLMBackend
}

// NewLLMScorer wraps a real *OpenAIParser for production use. A nil
// parser yields a scorer whose Score method always returns an error,
// which falls through to the next tier in scoreBaseCandidates.
func NewLLMScorer(parser *OpenAIParser) *LLMScorer {
	if parser == nil {
		return &LLMScorer{backend: nil}
	}
	return &LLMScorer{backend: parser}
}

// Name implements MetadataCandidateScorer.
func (s *LLMScorer) Name() string { return "llm" }

// Score implements MetadataCandidateScorer.
func (s *LLMScorer) Score(ctx context.Context, q Query, cands []Candidate) ([]float64, error) {
	if len(cands) == 0 {
		return nil, nil
	}
	if s.backend == nil {
		return nil, fmt.Errorf("llm scorer: no backend configured")
	}

	query := MetadataLLMQuery{
		Title:    q.Title,
		Author:   q.Author,
		Narrator: q.Narrator,
	}
	llmCands := make([]MetadataLLMCandidate, len(cands))
	for i, c := range cands {
		llmCands[i] = MetadataLLMCandidate{
			Index:    i,
			Title:    c.Title,
			Author:   c.Author,
			Narrator: c.Narrator,
		}
	}

	raw, err := s.backend.ScoreMetadataCandidates(ctx, query, llmCands)
	if err != nil {
		return nil, fmt.Errorf("llm scorer: %w", err)
	}

	// Rehydrate scores into input order. Missing indices default to 0.0
	// (the caller should treat those as "use the base score instead").
	scores := make([]float64, len(cands))
	for _, r := range raw {
		if r.Index < 0 || r.Index >= len(scores) {
			continue
		}
		score := r.Score
		if score < 0 {
			score = 0
		}
		if score > 1 {
			score = 1
		}
		scores[r.Index] = score
	}
	return scores, nil
}
