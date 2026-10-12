// file: internal/dedup/unified/compose.go
// version: 1.1.0
// guid: 73a952d9-e850-493a-ae72-a7eb16b0e168
// last-edited: 2026-10-11

package unified

import "time"

// FormulaVersion is the version tag embedded in every UnifiedDedupScore.
// Changing this constant causes the corpus-wide re-score detector to
// identify all candidates computed under the old formula.
//
// !!! BUMPING THIS MAKES EVERY STORED PAIR STALE. !!!
//
// History: "noisy-or-v1" until 02-PR8 (2026-10-11), which added the supporting
// kind SigWindowAcoustID; the signal set changed, so the stamp moved to
// "noisy-or-v2". Nothing rescans on the bump: pairs keep their v1 stamp and
// score until the OWNER schedules the corpus rescore (dedup.rescore) in a
// window with no library scan running (owner decision D33). Do not trigger or
// schedule one from code. 02-PR13 is meant to ride this same version, so the
// corpus is rescored once for both.
const FormulaVersion = "noisy-or-v2"

// ComposeScore combines a set of evidence signals into a single
// UnifiedDedupScore following SPEC 1 §4.
//
// Algorithm:
//
//  1. Separate signals into PRIMARY (feed noisy-OR) and SUPPORTING
//     (duration, folder_path — add bounded boosts only).
//     WHY: supporting signals alone are not strong enough evidence of
//     duplication — two books in the same folder could be sequels, not
//     duplicates. Excluding them from the noisy-OR product prevents
//     weak corroborating signals from compounding into false positives.
//
//  2. Compute the noisy-OR probability over independent primary signals:
//     P_dup = 1 - Π(1 - Confidence(s))  for all primary signals s
//     score  = 100 * P_dup
//     WHY: noisy-OR is the standard model for combining independent
//     boolean-ish evidence. Unlike a simple max(), it correctly
//     accumulates mid-strength signals: embedding 0.90 + metadata_fuzzy 0.80
//     → 1 - (0.10 * 0.20) = 0.98, which satisfies the spec's requirement
//     that "high in combination" weak signals reach a strong composite.
//
//  3. Add bounded supporting boosts (capped at their config values, not
//     unlimited). Boosts can push a borderline score into a higher band
//     but cannot create a candidate from supporting signals alone.
//
//  4. Cap the score at 100.
//
//  5. Assign a band based on capped score.
//
// The pair [2]string and suppressors []string are passed through unchanged
// (not computed here — the caller owns suppressor evaluation).
//
// An empty signals slice (no evidence at all) returns score 0 and no band.
func ComposeScore(signals []Signal, suppressors []string, cfg ScoreConfig, pair [2]string) UnifiedDedupScore {
	var primarySignals []Signal
	var supportingSignals []Signal

	for _, s := range signals {
		if isSupportingKind(s.Kind) {
			supportingSignals = append(supportingSignals, s)
		} else {
			primarySignals = append(primarySignals, s)
		}
	}

	// Step 1 + 2: noisy-OR over primary signals.
	// WHY noisy-OR rather than max: noisy-OR correctly handles the case
	// where multiple independent mid-confidence signals compound. A single
	// strong signal (confidence=1.0) drives the product to 0, yielding
	// P_dup=1.0 — correct for exact-file matches.
	notDup := 1.0
	for _, s := range primarySignals {
		// (1 - Confidence) is the probability this signal is NOT a duplicate.
		notDup *= (1.0 - s.Confidence)
	}
	pDup := 1.0 - notDup
	score := 100.0 * pDup

	// Step 3: add bounded supporting boosts.
	// Each boost is the configured additive value for that kind.
	// Boosts are config-driven, not free-form — see SPEC 1 §4.
	for _, s := range supportingSignals {
		score += cfg.BoostFor(s.Kind)
	}

	// Step 4: cap at 100.
	if score > 100.0 {
		score = 100.0
	}

	// Step 5: determine band.
	band := bandFor(score, cfg)

	return UnifiedDedupScore{
		Pair:        pair,
		Score:       score,
		Band:        band,
		Signals:     signals,
		Suppressors: suppressors,
		Formula:     FormulaVersion,
		ComputedAt:  time.Now().UTC(),
	}
}

// bandFor maps a capped score to its persistence band using the thresholds
// from cfg. Returns "" for scores below cfg.BandReviewMin (not persisted).
func bandFor(score float64, cfg ScoreConfig) string {
	switch {
	case score >= cfg.BandCertainMin:
		return BandCertain
	case score >= cfg.BandHighMin:
		return BandHigh
	case score >= cfg.BandMediumMin:
		return BandMedium
	case score >= cfg.BandReviewMin:
		return BandReview
	default:
		return ""
	}
}
