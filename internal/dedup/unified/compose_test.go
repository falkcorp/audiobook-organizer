// file: internal/dedup/unified/compose_test.go
// version: 1.0.1
// guid: e1fb3967-2f53-41fd-ac9d-0cbcb3b03668
// last-edited: 2026-08-20

package unified

import (
	"math"
	"testing"
)

// defaultPair is a canonical pair used across tests.
var defaultPair = [2]string{"aaa", "bbb"}

// helper: build a Signal with a given kind and confidence.
func sig(kind SignalKind, conf float64) Signal {
	return Signal{
		Kind:       kind,
		Confidence: conf,
		Raw:        conf,
		Evidence:   string(kind),
	}
}

// ─── SPEC §4 Worked Examples ──────────────────────────────────────────────────
//
// These three tests are EXACT assertions from SPEC 1 §4. They are normative:
// any change to the scoring formula must be reflected here first.

// TestComposeScore_WorkedExample1_ExactFileOnly verifies:
//
//	exact_file only: 1 − (1 − 1.0) → 100 → CERTAIN
func TestComposeScore_WorkedExample1_ExactFileOnly(t *testing.T) {
	cfg := DefaultScoreConfig()
	signals := []Signal{sig(SigExactFile, 1.0)}

	result := ComposeScore(signals, nil, cfg, defaultPair)

	if result.Score != 100.0 {
		t.Errorf("expected score 100.0, got %.6f", result.Score)
	}
	if result.Band != BandCertain {
		t.Errorf("expected band CERTAIN, got %q", result.Band)
	}
	if result.Formula != FormulaVersion {
		t.Errorf("expected formula %q, got %q", FormulaVersion, result.Formula)
	}
}

// TestComposeScore_WorkedExample2_EmbeddingPlusDuration verifies:
//
//	embedding 0.93 cos (conf 0.78) + duration match: 78 + 4 = 82 → MEDIUM
func TestComposeScore_WorkedExample2_EmbeddingPlusDuration(t *testing.T) {
	cfg := DefaultScoreConfig()
	signals := []Signal{
		sig(SigEmbedHigh, 0.78),
		sig(SigDuration, 0.0), // duration: supporting signal; confidence unused in noisy-OR
	}

	result := ComposeScore(signals, nil, cfg, defaultPair)

	// noisy-OR over primaries: 100 * (1 - (1 - 0.78)) = 78.0
	// duration boost: +4  →  82.0
	const expected = 82.0
	if result.Score != expected {
		t.Errorf("expected score %.1f, got %.6f", expected, result.Score)
	}
	if result.Band != BandMedium {
		t.Errorf("expected band MEDIUM, got %q", result.Band)
	}
}

// TestComposeScore_WorkedExample3_LSHPlusMetaFuzzy verifies:
//
//	lsh_acoustid hamming 0.95 (conf 0.94) + metadata_fuzzy 0.80 (conf 0.78):
//	1 − (0.06 · 0.22) = 0.9868 → 98.68 → CERTAIN (98.7 ± 0.1)
func TestComposeScore_WorkedExample3_LSHPlusMetaFuzzy(t *testing.T) {
	cfg := DefaultScoreConfig()
	signals := []Signal{
		sig(SigLSHAcoustID, 0.94),
		sig(SigMetaFuzzy, 0.78),
	}

	result := ComposeScore(signals, nil, cfg, defaultPair)

	// noisy-OR: 1 - (1-0.94)*(1-0.78) = 1 - 0.06*0.22 = 1 - 0.0132 = 0.9868
	// score = 98.68
	const expected = 98.68
	const tolerance = 0.1
	if math.Abs(result.Score-expected) > tolerance {
		t.Errorf("expected score %.2f ± %.1f, got %.6f", expected, tolerance, result.Score)
	}
	if result.Band != BandCertain {
		t.Errorf("expected band CERTAIN, got %q", result.Band)
	}
}

// ─── Band boundary tests ──────────────────────────────────────────────────────

func TestComposeScore_BandBoundaries(t *testing.T) {
	cfg := DefaultScoreConfig()

	tests := []struct {
		name     string
		score    float64 // approximate input confidence to get near a boundary
		wantBand string
	}{
		// To hit exactly 97: single primary with conf = 0.97 → score=97 → CERTAIN
		{"certain boundary", 0.97, BandCertain},
		// Single primary conf=0.90 → score=90 → HIGH
		{"high lower boundary", 0.90, BandHigh},
		// Single primary conf=0.75 → score=75 → MEDIUM
		{"medium lower boundary", 0.75, BandMedium},
		// Single primary conf=0.60 → score=60 → REVIEW
		{"review lower boundary", 0.60, BandReview},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			signals := []Signal{sig(SigEmbedHigh, tc.score)}
			result := ComposeScore(signals, nil, cfg, defaultPair)
			if result.Band != tc.wantBand {
				t.Errorf("conf=%.2f: expected band %q, got %q (score=%.4f)", tc.score, tc.wantBand, result.Band, result.Score)
			}
		})
	}
}

func TestComposeScore_BelowThresholdNoBand(t *testing.T) {
	cfg := DefaultScoreConfig()
	// conf=0.50 → score=50 → below review_min=60 → no band
	signals := []Signal{sig(SigEmbedMedium, 0.50)}
	result := ComposeScore(signals, nil, cfg, defaultPair)
	if result.Band != "" {
		t.Errorf("expected empty band for score below 60, got %q (score=%.2f)", result.Band, result.Score)
	}
}

// ─── Property tests ───────────────────────────────────────────────────────────

// TestComposeScore_MonotonicityPrimarySignals verifies that adding a primary
// signal never lowers the composite score (noisy-OR is monotone increasing).
func TestComposeScore_MonotonicityPrimarySignals(t *testing.T) {
	cfg := DefaultScoreConfig()

	base := []Signal{sig(SigEmbedMedium, 0.70)}
	baseScore := ComposeScore(base, nil, cfg, defaultPair).Score

	addedSignals := []Signal{
		sig(SigMetaFuzzy, 0.75),
		sig(SigLSHAcoustID, 0.90),
		sig(SigMetaSrcHash, 0.97),
	}

	// Add signals one at a time and check monotone increase.
	current := base
	prev := baseScore
	for _, extra := range addedSignals {
		current = append(current, extra)
		result := ComposeScore(current, nil, cfg, defaultPair)
		if result.Score < prev {
			t.Errorf("adding primary signal %q lowered score: prev=%.4f, now=%.4f", extra.Kind, prev, result.Score)
		}
		prev = result.Score
	}
}

// TestComposeScore_CapRespected verifies that the score never exceeds 100
// even when many high-confidence signals are present.
func TestComposeScore_CapRespected(t *testing.T) {
	cfg := DefaultScoreConfig()

	// Multiple signals plus supporting boosts — should never exceed 100.
	signals := []Signal{
		sig(SigExactFile, 1.0),
		sig(SigExactAcoustID, 0.99),
		sig(SigISBNASIN, 0.98),
		sig(SigMetaSrcHash, 0.97),
		sig(SigDuration, 0.0),   // supporting: +4
		sig(SigFolderPath, 0.0), // supporting: +3
	}

	result := ComposeScore(signals, nil, cfg, defaultPair)
	if result.Score > 100.0 {
		t.Errorf("score %.6f exceeds 100.0 cap", result.Score)
	}
	if result.Score != 100.0 {
		t.Errorf("expected capped score 100.0, got %.6f", result.Score)
	}
}

// TestComposeScore_SupportingOnlyNeverEligible verifies that a set of
// ONLY supporting signals never produces a candidate-eligible score (≥ 60).
// This enforces the SPEC constraint that supporting signals cannot create
// a candidate on their own.
func TestComposeScore_SupportingOnlyNeverEligible(t *testing.T) {
	cfg := DefaultScoreConfig()

	// Maximum possible supporting-only score: +4 (duration) + +3 (folder) = 7,
	// which is well below the 60-point persistence threshold.
	signals := []Signal{
		sig(SigDuration, 0.0),
		sig(SigFolderPath, 0.0),
	}

	result := ComposeScore(signals, nil, cfg, defaultPair)
	if result.Score >= 60.0 {
		t.Errorf("supporting-only signals produced candidate-eligible score %.2f (must be < 60)", result.Score)
	}
	if result.Band != "" {
		t.Errorf("supporting-only signals should produce no band, got %q", result.Band)
	}
}

// TestComposeScore_EmptySignals returns score 0 with no band.
func TestComposeScore_EmptySignals(t *testing.T) {
	cfg := DefaultScoreConfig()
	result := ComposeScore(nil, nil, cfg, defaultPair)

	if result.Score != 0.0 {
		t.Errorf("empty signals: expected score 0, got %.4f", result.Score)
	}
	if result.Band != "" {
		t.Errorf("empty signals: expected no band, got %q", result.Band)
	}
}

// TestComposeScore_SuppressorsPassedThrough verifies that suppressor strings
// are stored verbatim on the result without affecting the score.
func TestComposeScore_SuppressorsPassedThrough(t *testing.T) {
	cfg := DefaultScoreConfig()
	suppressors := []string{"series_volume_differs", "same_dir_multi_file"}
	signals := []Signal{sig(SigExactFile, 1.0)}

	result := ComposeScore(signals, suppressors, cfg, defaultPair)
	if len(result.Suppressors) != 2 {
		t.Errorf("expected 2 suppressors, got %d", len(result.Suppressors))
	}
	if result.Score != 100.0 {
		t.Errorf("suppressors should not affect score; expected 100, got %.2f", result.Score)
	}
}

// TestComposeScore_PairPassedThrough verifies canonical pair ordering is
// preserved verbatim (ordering is the caller's responsibility).
func TestComposeScore_PairPassedThrough(t *testing.T) {
	cfg := DefaultScoreConfig()
	pair := [2]string{"book-a", "book-b"}
	result := ComposeScore(nil, nil, cfg, pair)
	if result.Pair != pair {
		t.Errorf("expected pair %v, got %v", pair, result.Pair)
	}
}

// TestComposeScore_FormulaVersion checks that the formula version tag is
// always set to the expected constant.
func TestComposeScore_FormulaVersion(t *testing.T) {
	cfg := DefaultScoreConfig()
	result := ComposeScore(nil, nil, cfg, defaultPair)
	if result.Formula != "noisy-or-v1" {
		t.Errorf("expected formula %q, got %q", "noisy-or-v1", result.Formula)
	}
}

// TestComposeScore_ComputedAtSet checks that ComputedAt is non-zero.
func TestComposeScore_ComputedAtSet(t *testing.T) {
	cfg := DefaultScoreConfig()
	result := ComposeScore(nil, nil, cfg, defaultPair)
	if result.ComputedAt.IsZero() {
		t.Error("ComputedAt should not be zero")
	}
}

// TestComposeScore_DurationBoostApplied verifies that the duration boost
// is applied exactly as configured (default +4).
func TestComposeScore_DurationBoostApplied(t *testing.T) {
	cfg := DefaultScoreConfig()

	// A single primary signal giving score=78, then +4 duration = 82.
	signals := []Signal{
		sig(SigEmbedHigh, 0.78),
		sig(SigDuration, 0.0),
	}
	result := ComposeScore(signals, nil, cfg, defaultPair)
	if result.Score != 82.0 {
		t.Errorf("expected 82.0 (78 + 4 duration boost), got %.4f", result.Score)
	}
}

// TestComposeScore_FolderPathBoostApplied verifies the folder path boost (+3).
func TestComposeScore_FolderPathBoostApplied(t *testing.T) {
	cfg := DefaultScoreConfig()

	// Single primary giving 78, then +3 folder = 81.
	signals := []Signal{
		sig(SigEmbedHigh, 0.78),
		sig(SigFolderPath, 0.0),
	}
	result := ComposeScore(signals, nil, cfg, defaultPair)
	if result.Score != 81.0 {
		t.Errorf("expected 81.0 (78 + 3 folder boost), got %.4f", result.Score)
	}
}

// TestComposeScore_BothBoostsTogether verifies duration +4 and folder +3 together.
func TestComposeScore_BothBoostsTogether(t *testing.T) {
	cfg := DefaultScoreConfig()

	signals := []Signal{
		sig(SigEmbedHigh, 0.78),
		sig(SigDuration, 0.0),
		sig(SigFolderPath, 0.0),
	}
	result := ComposeScore(signals, nil, cfg, defaultPair)
	// 78 + 4 + 3 = 85
	if result.Score != 85.0 {
		t.Errorf("expected 85.0, got %.4f", result.Score)
	}
}

// TestComposeScore_AllPrimaryKinds verifies that all primary kinds contribute
// to noisy-OR correctly (each with 0.5 confidence; 8 signals → high score).
func TestComposeScore_AllPrimaryKinds(t *testing.T) {
	cfg := DefaultScoreConfig()
	signals := []Signal{
		sig(SigExactFile, 0.5),
		sig(SigExactAcoustID, 0.5),
		sig(SigISBNASIN, 0.5),
		sig(SigLSHAcoustID, 0.5),
		sig(SigEmbedHigh, 0.5),
		sig(SigMetaSrcHash, 0.5),
		sig(SigMetaFuzzy, 0.5),
		sig(SigEmbedMedium, 0.5),
	}

	result := ComposeScore(signals, nil, cfg, defaultPair)
	// noisy-OR with 8 signals each 0.5: 1 - 0.5^8 = 1 - 1/256 ≈ 0.99609 → 99.61
	expected := (1 - math.Pow(0.5, 8)) * 100
	if math.Abs(result.Score-expected) > 0.001 {
		t.Errorf("expected %.4f, got %.4f", expected, result.Score)
	}
	if result.Band != BandCertain {
		t.Errorf("expected CERTAIN, got %q", result.Band)
	}
}

// ─── ScoreConfig validation tests ────────────────────────────────────────────

func TestScoreConfig_ValidateDefault(t *testing.T) {
	cfg := DefaultScoreConfig()
	if err := cfg.Validate(); err != nil {
		t.Errorf("default config should be valid: %v", err)
	}
}

func TestScoreConfig_ValidateBandOrderingEnforced(t *testing.T) {
	cfg := DefaultScoreConfig()
	// Invert two thresholds — validation must reject.
	cfg.BandCertainMin = 90.0
	cfg.BandHighMin = 97.0 // now HIGH > CERTAIN — invalid

	if err := cfg.Validate(); err == nil {
		t.Error("expected validation error for inverted band thresholds, got nil")
	}
}

func TestScoreConfig_ValidateConfidenceOutOfRange(t *testing.T) {
	cfg := DefaultScoreConfig()
	kc := cfg.Signals[string(SigExactFile)]
	kc.MinConfidence = 0.0 // zero is excluded: must be in (0, 1]
	cfg.Signals[string(SigExactFile)] = kc

	if err := cfg.Validate(); err == nil {
		t.Error("expected validation error for confidence = 0, got nil")
	}
}

func TestScoreConfig_ValidateConfidenceAboveOne(t *testing.T) {
	cfg := DefaultScoreConfig()
	kc := cfg.Signals[string(SigEmbedHigh)]
	kc.MaxConfidence = 1.1 // above 1 — invalid
	cfg.Signals[string(SigEmbedHigh)] = kc

	if err := cfg.Validate(); err == nil {
		t.Error("expected validation error for confidence > 1, got nil")
	}
}

func TestScoreConfig_ValidateMinGTMax(t *testing.T) {
	cfg := DefaultScoreConfig()
	kc := cfg.Signals[string(SigEmbedMedium)]
	kc.MinConfidence = 0.90
	kc.MaxConfidence = 0.80 // min > max — invalid
	cfg.Signals[string(SigEmbedMedium)] = kc

	if err := cfg.Validate(); err == nil {
		t.Error("expected validation error for min_confidence > max_confidence, got nil")
	}
}

// ─── isSupportingKind tests ───────────────────────────────────────────────────

func TestIsSupportingKind(t *testing.T) {
	supporting := []SignalKind{SigDuration, SigFolderPath}
	primary := []SignalKind{
		SigExactFile, SigExactAcoustID, SigISBNASIN,
		SigLSHAcoustID, SigEmbedHigh, SigMetaSrcHash,
		SigMetaFuzzy, SigEmbedMedium,
	}

	for _, k := range supporting {
		if !isSupportingKind(k) {
			t.Errorf("expected %q to be a supporting kind", k)
		}
	}
	for _, k := range primary {
		if isSupportingKind(k) {
			t.Errorf("expected %q to be a primary kind (not supporting)", k)
		}
	}
}

// ─── BoostFor tests ───────────────────────────────────────────────────────────

func TestScoreConfig_BoostForSupportingKinds(t *testing.T) {
	cfg := DefaultScoreConfig()
	if cfg.BoostFor(SigDuration) != 4.0 {
		t.Errorf("expected duration boost 4.0, got %.2f", cfg.BoostFor(SigDuration))
	}
	if cfg.BoostFor(SigFolderPath) != 3.0 {
		t.Errorf("expected folder_path boost 3.0, got %.2f", cfg.BoostFor(SigFolderPath))
	}
}

func TestScoreConfig_BoostForPrimaryKindsIsZero(t *testing.T) {
	cfg := DefaultScoreConfig()
	for _, k := range []SignalKind{SigExactFile, SigEmbedHigh, SigMetaFuzzy} {
		if b := cfg.BoostFor(k); b != 0.0 {
			t.Errorf("primary kind %q should have 0 boost, got %.2f", k, b)
		}
	}
}

// ─── BandFor edge cases ───────────────────────────────────────────────────────

func TestBandFor(t *testing.T) {
	cfg := DefaultScoreConfig()

	tests := []struct {
		score    float64
		wantBand string
	}{
		{100.0, BandCertain},
		{97.0, BandCertain},
		{96.99, BandHigh},
		{90.0, BandHigh},
		{89.99, BandMedium},
		{75.0, BandMedium},
		{74.99, BandReview},
		{60.0, BandReview},
		{59.99, ""},
		{0.0, ""},
	}

	for _, tc := range tests {
		got := bandFor(tc.score, cfg)
		if got != tc.wantBand {
			t.Errorf("bandFor(%.2f) = %q, want %q", tc.score, got, tc.wantBand)
		}
	}
}

// ─── Signals stored on result ─────────────────────────────────────────────────

func TestComposeScore_SignalsStoredOnResult(t *testing.T) {
	cfg := DefaultScoreConfig()
	signals := []Signal{
		sig(SigExactFile, 1.0),
		sig(SigDuration, 0.0),
	}

	result := ComposeScore(signals, nil, cfg, defaultPair)
	if len(result.Signals) != 2 {
		t.Errorf("expected 2 signals on result, got %d", len(result.Signals))
	}
}

// ─── Custom config overrides ──────────────────────────────────────────────────

func TestComposeScore_CustomBoostFromConfig(t *testing.T) {
	cfg := DefaultScoreConfig()
	// Override duration boost to 10.
	kc := cfg.Signals[string(SigDuration)]
	kc.Boost = 10.0
	cfg.Signals[string(SigDuration)] = kc

	signals := []Signal{
		sig(SigEmbedHigh, 0.78), // primary: 78
		sig(SigDuration, 0.0),   // supporting: +10 (custom)
	}
	result := ComposeScore(signals, nil, cfg, defaultPair)
	if result.Score != 88.0 {
		t.Errorf("expected 88.0 with custom boost 10, got %.4f", result.Score)
	}
}

// TestComposeScore_HighBandBoundary verifies boundary between CERTAIN and HIGH.
func TestComposeScore_HighBandBoundary(t *testing.T) {
	cfg := DefaultScoreConfig()

	// Score of 96.99 should be HIGH, not CERTAIN.
	// Use two signals to compose a known score.
	// 1 - (1-a)*(1-b) = 0.9699 → (1-a)*(1-b) = 0.0301
	// Use a=0.94, b=0.49: (0.06)*(0.51) = 0.0306 → score ≈ 96.94 → HIGH
	signals := []Signal{
		sig(SigLSHAcoustID, 0.94),
		sig(SigMetaFuzzy, 0.49),
	}
	result := ComposeScore(signals, nil, cfg, defaultPair)
	// Compute expected
	expected := (1.0 - (1-0.94)*(1-0.49)) * 100
	if math.Abs(result.Score-expected) > 0.001 {
		t.Errorf("score mismatch: expected %.4f, got %.4f", expected, result.Score)
	}
	if result.Band != BandHigh {
		t.Errorf("expected HIGH band for score %.4f, got %q", result.Score, result.Band)
	}
}
