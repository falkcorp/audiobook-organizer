// file: internal/models/dedup_score.go
// version: 1.1.0
// guid: 59926af2-f1cb-4823-bbfd-65a11750bce6
// last-edited: 2026-09-27

package models

import "time"

// SignalKind is the identifier for a single evidence signal in the dedup
// pipeline. Kinds are categorised as PRIMARY (contribute to the noisy-OR
// product) or SUPPORTING (add bounded boosts after the product is computed).
// See SPEC 1 §3 and ComposeScore for the exact categorisation logic.
type SignalKind string

// Provenance kinds. These are NON-SCORING signals: Confidence is always 0 and
// no configured boost exists for them, so they never change a score or a band.
// They record WHY a candidate row exists for the paths that write a candidate
// without composing a score (the exact-layer rules), so the review UI can
// answer "why is this pair here?" instead of "No score breakdown recorded".
// internal/dedup/unified aliases both as SigExactRule / SigSamePath.
const (
	// SignalKindExactRule names the exact-layer rule that emitted the pair
	// (Signal.Rule) and the values it matched on (Signal.Evidence).
	SignalKindExactRule SignalKind = "exact_rule"
	// SignalKindSamePath marks two book rows at the same cleaned path.
	SignalKindSamePath SignalKind = "same_path"
)

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

	// Rule is the machine-readable rule name on an exact_rule provenance
	// signal ("file_hash", "isbn_asin", "metadata_hash", "title_author",
	// "duration_title", "organize_collision"). Empty on every other kind.
	Rule string `json:"rule,omitempty"`
}

// IsProvenanceKind reports whether k is a non-scoring provenance kind.
func IsProvenanceKind(k SignalKind) bool {
	return k == SignalKindExactRule || k == SignalKindSamePath
}

// IsProvenanceOnly reports whether u records only WHY a pair exists and no
// score: at least one signal, every signal a zero-confidence provenance kind,
// and no score or band. Such a breakdown is not a scoring result — the rescore
// and breakdown-backfill paths treat it as "not yet scored", and the candidate
// store merges it into an existing breakdown instead of replacing one.
func (u *UnifiedDedupScore) IsProvenanceOnly() bool {
	if u == nil || len(u.Signals) == 0 || u.Score != 0 || u.Band != "" {
		return false
	}
	for _, s := range u.Signals {
		if !IsProvenanceKind(s.Kind) || s.Confidence != 0 {
			return false
		}
	}
	return true
}

// provenanceKey identifies one provenance fact, so re-emitting the same rule
// for the same pair replaces its evidence rather than stacking duplicates.
func provenanceKey(s Signal) string { return string(s.Kind) + "\x00" + s.Rule }

// MergeProvenance returns existing with every provenance signal from incoming
// folded in: a provenance signal with the same kind+rule is replaced (its
// evidence may have been refreshed), a new one is appended. Score, Band,
// Suppressors, Formula and every scoring signal of existing are kept exactly
// as they were, so merging never changes what a row scores.
//
// A nil existing yields a copy of incoming. Neither argument is mutated.
func MergeProvenance(existing, incoming *UnifiedDedupScore) *UnifiedDedupScore {
	if incoming == nil {
		return existing
	}
	if existing == nil {
		cp := *incoming
		cp.Signals = append([]Signal(nil), incoming.Signals...)
		return &cp
	}
	out := *existing
	out.Signals = append([]Signal(nil), existing.Signals...)
	index := make(map[string]int, len(out.Signals))
	for i, s := range out.Signals {
		if IsProvenanceKind(s.Kind) {
			index[provenanceKey(s)] = i
		}
	}
	for _, s := range incoming.Signals {
		if !IsProvenanceKind(s.Kind) {
			continue
		}
		if i, ok := index[provenanceKey(s)]; ok {
			out.Signals[i] = s
			continue
		}
		index[provenanceKey(s)] = len(out.Signals)
		out.Signals = append(out.Signals, s)
	}
	return &out
}

// CarryProvenance returns next with the provenance signals of prev that next
// lacks appended. Used when a scorer REPLACES a row's breakdown (rescore,
// breakdown backfill): the composed score is new, but the record of which
// exact rule created the row must survive it. A nil next is returned as is.
func CarryProvenance(prev, next *UnifiedDedupScore) *UnifiedDedupScore {
	if next == nil || prev == nil {
		return next
	}
	have := make(map[string]bool, len(next.Signals))
	for _, s := range next.Signals {
		if IsProvenanceKind(s.Kind) {
			have[provenanceKey(s)] = true
		}
	}
	var extra []Signal
	for _, s := range prev.Signals {
		if IsProvenanceKind(s.Kind) && !have[provenanceKey(s)] {
			extra = append(extra, s)
		}
	}
	if len(extra) == 0 {
		return next
	}
	out := *next
	out.Signals = append(append([]Signal(nil), next.Signals...), extra...)
	return &out
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
