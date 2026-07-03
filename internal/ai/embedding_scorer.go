// file: internal/ai/embedding_scorer.go
// version: 1.1.0
// guid: f7a2c841-3b5e-4d9f-82c6-1e0d7f3a9b4c
// last-edited: 2026-07-03

package ai

import (
	"context"
	"fmt"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// embeddingAPI is the minimal surface EmbeddingScorer needs from an embedding
// client. It exists purely so tests can inject a fake without spinning up the
// real OpenAI client. Production code always wires a real *EmbeddingClient
// here via NewEmbeddingScorer.
type embeddingAPI interface {
	EmbedOne(ctx context.Context, text string) ([]float32, error)
	EmbedBatch(ctx context.Context, texts []string) ([][]float32, error)
	Model() string
}

// EmbeddingScorer ranks metadata candidates by cosine similarity between the
// query book's embedding and each candidate's embedding. When a BookID is
// supplied and the EmbeddingStore has a cached vector for that book, the
// query embedding step is skipped entirely — this is the common case in
// production since all library books are embedded by the initial backfill.
type EmbeddingScorer struct {
	api   embeddingAPI
	store *database.EmbeddingStore // optional; enables BookID fast-path
}

// NewEmbeddingScorer wraps a real *EmbeddingClient for production use.
// A nil store is allowed and disables the BookID fast-path — the scorer
// will always embed the query text on the fly.
func NewEmbeddingScorer(client *EmbeddingClient, store *database.EmbeddingStore) *EmbeddingScorer {
	return &EmbeddingScorer{api: client, store: store}
}

// NewEmbeddingScorerWithAPI is the test seam. Do not call this from
// production code.
func NewEmbeddingScorerWithAPI(api embeddingAPI, store *database.EmbeddingStore) *EmbeddingScorer {
	return &EmbeddingScorer{api: api, store: store}
}

// Name implements MetadataCandidateScorer.
func (s *EmbeddingScorer) Name() string { return "embedding" }

// Score implements MetadataCandidateScorer.
func (s *EmbeddingScorer) Score(ctx context.Context, q Query, cands []Candidate) ([]float64, error) {
	if len(cands) == 0 {
		return nil, nil
	}
	if s.api == nil {
		return nil, fmt.Errorf("embedding scorer: no embedding API configured")
	}

	qVec, err := s.queryVector(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("embedding scorer: query vector: %w", err)
	}

	texts := make([]string, len(cands))
	for i, c := range cands {
		texts[i] = BuildEmbeddingText("book", c.Title, c.Author, c.Narrator)
	}

	candVecs, err := s.api.EmbedBatch(ctx, texts)
	if err != nil {
		return nil, fmt.Errorf("embedding scorer: candidate batch: %w", err)
	}
	if len(candVecs) != len(cands) {
		return nil, fmt.Errorf("embedding scorer: batch returned %d vectors for %d candidates",
			len(candVecs), len(cands))
	}

	scores := make([]float64, len(cands))
	for i, cv := range candVecs {
		cos := database.CosineSimilarity(qVec, cv)
		if cos < 0 {
			cos = 0
		}
		scores[i] = float64(cos)
	}
	return scores, nil
}

// queryVector returns the vector for the query book, preferring the
// EmbeddingStore fast-path when a BookID is set and a cached vector exists,
// and falling back to a live API embed otherwise.
func (s *EmbeddingScorer) queryVector(ctx context.Context, q Query) ([]float32, error) {
	if q.BookID != "" && s.store != nil {
		if existing, err := s.store.Get("book", q.BookID); err == nil && existing != nil &&
			len(existing.Vector) > 0 && s.modelMatches(existing.Model) {
			return existing.Vector, nil
		}
	}
	text := BuildEmbeddingText("book", q.Title, q.Author, q.Narrator)
	return s.api.EmbedOne(ctx, text)
}

// modelMatches reports whether a stored embedding's model matches the
// scorer's live API model. A mismatch — e.g. after switching the embedding
// backend from OpenAI (text-embedding-3-large, 3072-dim) to a local model
// (bge-m3, 1024-dim) — must force a live re-embed even though a cached
// vector exists; otherwise CosineSimilarity silently returns 0 against the
// mismatched vector. Empty stored model (pre-model-tagging rows) counts as
// a mismatch so those are re-embedded too. Mirrors
// internal/dedup/engine.go's embeddingModelMatches.
func (s *EmbeddingScorer) modelMatches(storedModel string) bool {
	if storedModel == "" {
		return false
	}
	return storedModel == s.api.Model()
}
