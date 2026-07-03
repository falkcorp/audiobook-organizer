// file: internal/dedup/collectors_embedding.go
// version: 1.1.0
// guid: d0e1f2a3-b4c5-4d6e-9f0a-1b2c3d4e5f6a
// last-edited: 2026-07-03

// Package dedup — embedding collector (fable5 T014).
//
// # Design
//
// CollectEmbedding wraps the findSimilarBooks similarity search into a
// []unified.Signal emitter.  The business logic is UNCHANGED — only the return
// shape changes.  Guards (version-group, series-volume, same-dir) have been
// extracted to PairEligibility in eligibility.go; the collector itself no
// longer re-evaluates them.  Callers must call PairEligibility first.
//
// Signal tier mapping (SPEC 1 §3):
//
//	cosine ≥ 0.95     → SigEmbedHigh   confidence 0.88 + (cos − 0.95) / 0.05 * 0.07  → 0.88–0.95
//	0.85 ≤ cos < 0.95 → SigEmbedMedium confidence 0.65 + (cos − 0.85) / 0.10 * 0.15  → 0.65–0.80
//
// WHY two tiers: a cosine of 0.96 is qualitatively different from 0.87 — two
// books with near-identical embeddings are much more likely to be duplicates
// than two books that are merely "in the same part of the vector space".
// Mapping each tier to its own SignalKind lets ComposeScore treat them as
// independent primary signals; a HIGH + MEDIUM combination compounds
// (noisy-OR) to a higher composite than either alone.

package dedup

import (
	"context"
	"fmt"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/dedup/unified"
)

// EmbeddingStore is the subset of database.EmbeddingStore required by
// CollectEmbedding.
type EmbeddingStore interface {
	Get(entityType, entityID string) (*database.Embedding, error)
	FindSimilar(entityType string, vector []float32, minSim float32, limit int) ([]database.SimilarityResult, error)
}

// ChromemStore is the subset of database.ChromemEmbeddingStore required by
// CollectEmbedding.
type ChromemStore interface {
	FindSimilar(ctx context.Context, entityType string, vector []float32, limit int, filter map[string]string) ([]database.ChromemSimilarityResult, error)
}

// EmbeddingCollectorConfig controls the embedding collector thresholds.
// Defaults match the Engine's BookHighThreshold and BookLowThreshold fields.
type EmbeddingCollectorConfig struct {
	// HighThreshold is the minimum cosine similarity to emit SigEmbedHigh.
	// Default 0.95.
	HighThreshold float64

	// LowThreshold is the minimum cosine similarity to emit SigEmbedMedium.
	// Candidates with similarity below this are not emitted.
	// Default 0.85.
	LowThreshold float64

	// TopK is the maximum number of candidate results to retrieve from the
	// vector store.  Default 20.
	TopK int
}

// DefaultEmbeddingCollectorConfig returns SPEC 1 §3 defaults.
func DefaultEmbeddingCollectorConfig() EmbeddingCollectorConfig {
	return EmbeddingCollectorConfig{
		HighThreshold: 0.95,
		LowThreshold:  0.85,
		TopK:          20,
	}
}

// embedHighConfidence maps a cosine similarity in [high, 1.00] to a confidence
// in [0.88, 0.95] using linear interpolation.
//
// DEDUP-2/3: the lower cut-point (`high`) is now a parameter derived from the
// active per-model threshold, not a hard-coded 0.95 literal — a model whose
// true-duplicate cosines cluster lower (e.g. bge-m3) reshapes the ramp so the
// confidence interpolation stays consistent with the similarity comparison. The
// confidence endpoints (0.88..0.95) are intentionally NOT parameterized: they
// express how much trust a high-tier embedding match earns, independent of
// which model produced the cosine.
//
// WHY the 0.88 floor: embeddings can be tricked by stylistic similarity (same
// author, different series). The 0.95 ceiling stays below 1.0 to keep the
// noisy-OR product separable from exact-file (1.00).
func embedHighConfidence(cos float32, high float64) float64 {
	const (
		cosMax  = 1.00
		confMin = 0.88
		confMax = 0.95
	)
	cosMin := high
	if float64(cos) <= cosMin {
		return confMin
	}
	if float64(cos) >= cosMax {
		return confMax
	}
	frac := (float64(cos) - cosMin) / (cosMax - cosMin)
	return confMin + frac*(confMax-confMin)
}

// embedMediumConfidence maps a cosine similarity in [low, high) to a confidence
// in [0.65, 0.80]. Like embedHighConfidence, the band cut-points (`low`, `high`)
// derive from the active per-model thresholds; the confidence endpoints
// (0.65..0.80) are fixed (DEDUP-2/3).
func embedMediumConfidence(cos float32, low, high float64) float64 {
	const (
		confMin = 0.65
		confMax = 0.80
	)
	cosMin := low
	cosMax := high
	if float64(cos) <= cosMin {
		return confMin
	}
	if float64(cos) >= cosMax {
		return confMax
	}
	frac := (float64(cos) - cosMin) / (cosMax - cosMin)
	return confMin + frac*(confMax-confMin)
}

// CollectEmbedding performs the vector-similarity search for bookID and returns
// SigEmbedHigh / SigEmbedMedium signals for each candidate that passes the
// similarity threshold.
//
// The caller is responsible for:
//  1. Ensuring the embedding for bookID exists (via EmbedBook/EmbedBooks).
//  2. Running PairEligibility(queryBook, otherBook) before consuming signals —
//     the eligibility filter is NOT re-run inside this collector.
//
// Implementation mirrors findSimilarBooks (engine.go:822-936) with the
// guards removed (moved to PairEligibility) and UpsertCandidate replaced by
// signal emission.
func CollectEmbedding(
	ctx context.Context,
	embedStore EmbeddingStore,
	chromemStore ChromemStore, // may be nil — falls back to embedStore
	bookID string,
	cfg EmbeddingCollectorConfig,
) ([]unified.Signal, error) {
	emb, err := embedStore.Get("book", bookID)
	if err != nil || emb == nil {
		// No embedding available — not an error; caller can log if needed.
		return nil, nil
	}

	// Attempt chromem ANN first (fast, in-memory ANN).
	var results []database.SimilarityResult
	if chromemStore != nil {
		filter := map[string]string{"is_primary_version": "true"}
		chromemResults, cErr := chromemStore.FindSimilar(ctx, "book", emb.Vector, cfg.TopK, filter)
		if cErr != nil {
			return nil, fmt.Errorf("CollectEmbedding chromem: %w", cErr)
		}
		for _, cr := range chromemResults {
			if float64(cr.Similarity) >= cfg.LowThreshold {
				results = append(results, database.SimilarityResult{
					EntityID:   cr.EntityID,
					Similarity: cr.Similarity,
				})
			}
		}
	}

	// SQLite linear-scan fallback when chromem is absent or empty.
	// See findSimilarBooks rationale: ~50-200ms per query for 42K books;
	// acceptable for infrequent dedup runs.
	if len(results) == 0 && embedStore != nil {
		fallback, fErr := embedStore.FindSimilar("book", emb.Vector, float32(cfg.LowThreshold), cfg.TopK)
		if fErr != nil {
			return nil, fmt.Errorf("CollectEmbedding fallback: %w", fErr)
		}
		results = fallback
	}

	var signals []unified.Signal
	for _, r := range results {
		if r.EntityID == bookID {
			continue
		}

		// Classify into HIGH or MEDIUM tier based on cosine similarity.
		// Note: eligibility guards (version-group, series-volume, same-dir)
		// are NOT applied here — they must have been evaluated by the caller
		// via PairEligibility before processing returned signals.
		var sig unified.Signal
		if float64(r.Similarity) >= cfg.HighThreshold {
			sig = unified.Signal{
				Kind:       unified.SigEmbedHigh,
				Raw:        float64(r.Similarity),
				Confidence: embedHighConfidence(r.Similarity, cfg.HighThreshold),
				Evidence: fmt.Sprintf(
					"embedding cosine %.4f (high tier): book %s ↔ %s",
					r.Similarity, bookID, r.EntityID,
				),
			}
		} else {
			sig = unified.Signal{
				Kind:       unified.SigEmbedMedium,
				Raw:        float64(r.Similarity),
				Confidence: embedMediumConfidence(r.Similarity, cfg.LowThreshold, cfg.HighThreshold),
				Evidence: fmt.Sprintf(
					"embedding cosine %.4f (medium tier): book %s ↔ %s",
					r.Similarity, bookID, r.EntityID,
				),
			}
		}
		signals = append(signals, sig)
	}

	return signals, nil
}
