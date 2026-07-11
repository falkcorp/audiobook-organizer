// file: internal/models/dedup_score.go
// version: 1.0.0
// guid: 59926af2-f1cb-4823-bbfd-65a11750bce6
// last-edited: 2026-07-11

package models

import "time"

// SignalKind is the identifier for a single evidence signal in the dedup
// pipeline. Kinds are categorised as PRIMARY (contribute to the noisy-OR
// product) or SUPPORTING (add bounded boosts after the product is computed).
// See SPEC 1 §3 and ComposeScore for the exact categorisation logic.
type SignalKind string

// Signal is a single piece of evidence from one collector for one candidate
// pair. Signals from all collectors are passed to ComposeScore together.
type Signal struct {
	// Kind identifies which collector produced this signal.
	Kind SignalKind `json:"kind"`

	// Raw is the unscaled measurement (e.g. cosine similarity 0.961,
	// Hamming similarity 0.93, absolute duration delta as a fraction 0.004).
	Raw float64 `json:"raw"`

	// Confidence is the calibrated probability (0..1) that this signal
	// alone indicates a duplicate. ComposeScore reads this field; Raw is
	// stored for human auditing and re-calibration.
	Confidence float64 `json:"confidence"`

	// Evidence is a human-readable description for UI display and audit
	// logs (e.g. "whole-file hash 9af3… both sides",
	// "cosine 0.961 via embedding_high collector").
	Evidence string `json:"evidence"`

	// FPVersion is the fingerprint-algorithm version that produced the
	// underlying acoustic data, stored for provenance and invalidation.
	// Empty for non-acoustic signals.
	FPVersion string `json:"fp_version,omitempty"`
}

// UnifiedDedupScore is the composite output of ComposeScore for one candidate
// pair. It is stored as DedupCandidate.ScoreBreakdown (JSON).
type UnifiedDedupScore struct {
	// Pair holds the two book IDs in canonical order (aID < bID).
	Pair [2]string `json:"pair"`

	// Score is the noisy-OR composite score on a 0–100 scale, capped at 100.
	// Consumers should use Band for thresholding rather than raw Score,
	// since calibration changes the meaning of any absolute number.
	Score float64 `json:"score"`

	// Band is the persistence band derived from Score:
	//   CERTAIN ≥ 97   — auto-merge eligible
	//   HIGH    90–96.99 — suggest-merge
	//   MEDIUM  75–89.99 — review queue
	//   REVIEW  60–74.99 — LLM phase / manual
	//   (below 60 is not persisted)
	Band string `json:"band"`

	// Signals is the full per-signal breakdown, always stored.
	// Supports re-scoring without re-collection when calibration changes.
	Signals []Signal `json:"signals"`

	// Suppressors lists the negative-guard identifiers that were fired
	// before scoring (e.g. "series_volume_differs", "same_dir_multi_file").
	// Non-empty means the pair was dropped before ComposeScore was called;
	// the score will be 0 if this ever reaches persistence.
	Suppressors []string `json:"suppressors"`

	// Formula is the scoring algorithm version tag used to compute this
	// score. Enables corpus-wide re-score detection after formula upgrades.
	Formula string `json:"formula"`

	// ComputedAt is the wall-clock time when this score was computed.
	ComputedAt time.Time `json:"computed_at"`
}
