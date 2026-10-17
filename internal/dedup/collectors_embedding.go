// file: internal/dedup/collectors_embedding.go
// version: 1.2.0
// guid: d0e1f2a3-b4c5-4d6e-9f0a-1b2c3d4e5f6a
// last-edited: 2026-10-17

// Package dedup — embedding confidence tiers (fable5 T014).
//
// # Design
//
// The standalone CollectEmbedding vector-search emitter was removed in 01-P73
// once nothing called it; the thresholds, config and confidence curves below
// are applied by rescore.go and the engine's full-scan scorer. Guards
// (version-group, series-volume, same-dir) live in PairEligibility
// (eligibility.go), which callers must run before consuming these signals.
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
