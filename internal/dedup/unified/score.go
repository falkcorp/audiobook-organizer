// file: internal/dedup/unified/score.go
// version: 1.6.1
// guid: e12361d1-96ea-4301-919d-3fdb51e12f8f
// last-edited: 2026-11-09

// Package unified provides the scoring core for the unified deduplication
// pipeline (SPEC 1, fable5). It is intentionally pure: no I/O, no storage
// access, no engine changes. This makes it trivially unit-testable and
// re-runnable over stored signal sets for offline auditing and re-scoring
// after calibration changes.
package unified

import "time"

// SignalKind is the identifier for a single evidence signal in the dedup
// pipeline. Kinds are categorised as PRIMARY (contribute to the noisy-OR
// product) or SUPPORTING (add bounded boosts after the product is computed).
// See SPEC 1 §3 and ComposeScore for the exact categorisation logic.
type SignalKind string

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

	// SigChapterStructure is a per-chapter boundary match: the same chapter
	// COUNT and every boundary within a tolerance. Confidence 0.85–0.93,
	// scaled by how tightly the boundaries align (owner decision 2026-09-22).
	//
	// Ranked above SigEmbedMedium and beneath SigLSHAcoustID on purpose: a
	// matching chapter table is structural evidence of the same EDITION, which
	// is stronger than text similarity but weaker than matching audio. Two
	// different editions of a book essentially never share an exact chapter
	// table, because the boundaries come from the recording, not the text.
	//
	// Chapter data has been collected per book since chapters were persisted
	// and read by nobody in dedup until now.
	SigChapterStructure SignalKind = "chapter_structure"

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

	// SigWindowAcoustID is a window-print match (02-PR8, owner decisions D14a
	// and D34): the fpwinidx: band index proposed the candidate file and
	// fingerprint.WindowSetSimilarity verified the two files' window prints
	// (Raw = the median per-slot similarity). SUPPORTING on purpose: it adds
	// KindConfig.Boost (default +6, 0 disables it) after the noisy-OR and can
	// never make a candidate on its own. Window prints sample three 120 s
	// spans of a file, so agreement says "the same recording at these spots",
	// which corroborates other evidence but is not, alone, proof of the same
	// edition. One signal per candidate BOOK per scored pair, however many of
	// the book's files matched (the collector and engine dedupe by book).
	SigWindowAcoustID SignalKind = "window_acoustid"

	// SigSamePath is a non-scoring, informational signal (CHAPTER-SUBFOLDER-
	// NN-ROWS, 2026-09-25): both books resolve to the SAME cleaned
	// file-system path. It carries no configured boost (BoostFor returns 0
	// for an unregistered kind) and its Confidence is always 0, so it never
	// changes a score. Its sole purpose is to carry the "same_path" evidence
	// string into the persisted candidate so the review UI can show why the
	// pair is flagged — owner decision: this pair shape goes to the review
	// queue only, and no automated path may merge it.
	SigSamePath SignalKind = "same_path"

	// SigCoverText is the text a vision model read off a book's cover image
	// (title, subtitle, authors, narrators, series and number, publisher and
	// any other printed line), stored per image hash by internal/covertext
	// and read with covertext.ForBook. Registered 2026-09-26 as EVIDENCE for
	// the identification and dedup code to read; it is deliberately
	// non-scoring for now, exactly like SigSamePath: no configured boost,
	// Confidence 0, and it is a supporting kind, so it can never make or
	// change a candidate. Scoring it (agreement between two books' cover
	// text, or between cover text and a candidate's metadata) is a tracked
	// TODO, not an omission.
	SigCoverText SignalKind = "cover_text"

	// SigExactRule is a non-scoring PROVENANCE signal (2026-09-27): it names
	// the exact-layer rule that emitted a candidate (Signal.Rule —
	// "file_hash", "isbn_asin", "title_author", ...) and the values that rule
	// matched on (Signal.Evidence). Confidence 0, no configured boost, so it
	// never changes a score; it exists so every exact candidate can say WHY it
	// exists. Before it, every exact-layer row carried no breakdown at all and
	// the review panel said "No score breakdown recorded." for all of them.
	SigExactRule SignalKind = "exact_rule"
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
	return IsSupportingKind(k)
}

// IsSupportingKind is isSupportingKind for callers outside this package (the
// drain's content-evidence check) that must agree with the scorer on which
// kinds can never be the reason a pair exists.
func IsSupportingKind(k SignalKind) bool {
	return k == SigDuration || k == SigFolderPath || k == SigSamePath || k == SigCoverText || k == SigExactRule ||
		k == SigWindowAcoustID
}
