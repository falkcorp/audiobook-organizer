// file: internal/dedup/unified/score.go
// version: 1.1.1
// guid: e12361d1-96ea-4301-919d-3fdb51e12f8f
// last-edited: 2026-07-11

// Package unified provides the scoring core for the unified deduplication
// pipeline (SPEC 1, fable5). It is intentionally pure: no I/O, no storage
// access, no engine changes. This makes it trivially unit-testable and
// re-runnable over stored signal sets for offline auditing and re-scoring
// after calibration changes.
package unified

import "github.com/falkcorp/audiobook-organizer/internal/models"

// SignalKind is the identifier for a single evidence signal in the dedup
// pipeline. Kinds are categorised as PRIMARY (contribute to the noisy-OR
// product) or SUPPORTING (add bounded boosts after the product is computed).
// See SPEC 1 §3 and ComposeScore for the exact categorisation logic.
//
// Moved to internal/models/dedup_score.go (TASK-03, SDKGUARD-VIOLATION #1795)
// so pkg/plugin/sdk's dependency tree no longer has to pull in this package;
// aliased here so all existing unified.SignalKind call sites keep compiling.
type SignalKind = models.SignalKind

const (
	// Primary signals — these are independent evidence sources that feed
	// directly into the noisy-OR product 1 - Π(1 - Confidence(s)).

	// SigExactFile is a whole-file hash match (certainty 1.00).
	SigExactFile SignalKind = "exact_file"

	// SigExactAcoustID is an exact-match from the book_file_acoustid: index (0.99).
	SigExactAcoustID SignalKind = "exact_acoustid"

	// SigISBNASIN is an ISBN or ASIN match from external ID data (0.98).
	SigISBNASIN SignalKind = "isbn_asin"

	// SigLSHAcoustID is an LSH fingerprint band-hit followed by Hamming
	// refinement — confidence is Hamming-scaled 0.90–0.97.
	SigLSHAcoustID SignalKind = "lsh_acoustid"

	// SigEmbedHigh is a high-cosine embedding match (cos ≥ 0.95);
	// confidence 0.88–0.95.
	SigEmbedHigh SignalKind = "embedding_high"

	// SigMetaSrcHash is a same-external-record metadata source hash match
	// (same Audible/Google record applied to both; confidence 0.97).
	SigMetaSrcHash SignalKind = "metadata_hash"

	// SigMetaFuzzy is a normalized title+author Levenshtein similarity
	// (NEW collector in T014); confidence 0.70–0.85.
	SigMetaFuzzy SignalKind = "metadata_fuzzy"

	// SigEmbedMedium is a medium-cosine embedding match (0.85 ≤ cos < 0.95);
	// confidence 0.65–0.80.
	SigEmbedMedium SignalKind = "embedding_med"

	// Supporting signals — these are NOT included in the noisy-OR product.
	// They add bounded additive boosts AFTER the primary product is computed.
	// A set of supporting-only signals can never reach a candidate-eligible
	// score of ≥ 60, preventing false positives from weak corroborating
	// evidence alone (see ComposeScore for the enforcement).

	// SigDuration is a duration-match supporting signal (±2% window).
	// Adds a bounded boost of +4 (config.DurationBoost).
	SigDuration SignalKind = "duration"

	// SigFolderPath is a matching-folder-path supporting signal.
	// Adds a bounded boost of +3 (config.FolderBoost).
	SigFolderPath SignalKind = "folder_path"
)

// Signal is a single piece of evidence from one collector for one candidate
// pair. Signals from all collectors are passed to ComposeScore together.
//
// Moved to internal/models/dedup_score.go (TASK-03, SDKGUARD-VIOLATION #1795);
// aliased here so all existing unified.Signal call sites keep compiling.
type Signal = models.Signal

// UnifiedDedupScore is the composite output of ComposeScore for one candidate
// pair. It is stored as DedupCandidate.ScoreBreakdown (JSON).
//
// Moved to internal/models/dedup_score.go (TASK-03, SDKGUARD-VIOLATION #1795)
// so internal/database can reference it without pulling in this whole
// package (and, transitively, pkg/plugin/sdk with it); aliased here so all
// existing unified.UnifiedDedupScore call sites keep compiling.
type UnifiedDedupScore = models.UnifiedDedupScore

// Band constants — match exactly the thresholds in ComposeScore.
const (
	BandCertain = "CERTAIN" // score ≥ 97
	BandHigh    = "HIGH"    // 90 ≤ score < 97
	BandMedium  = "MEDIUM"  // 75 ≤ score < 90
	BandReview  = "REVIEW"  // 60 ≤ score < 75
	// Scores < 60 are not persisted; there is intentionally no BandBelow constant.
)

// isSupportingKind returns true for signal kinds that are excluded from
// the noisy-OR product and contribute only bounded additive boosts.
// These signals must never be the sole reason a candidate reaches
// persistence (score ≥ 60), which is enforced by ComposeScore.
func isSupportingKind(k SignalKind) bool {
	return k == SigDuration || k == SigFolderPath
}
