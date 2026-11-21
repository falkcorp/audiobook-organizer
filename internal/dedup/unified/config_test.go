// file: internal/dedup/unified/config_test.go
// version: 2.1.0
// guid: 4f7a2c9b-8e3d-4b6f-a1c5-9e7b3d2f6a8c
// last-edited: 2026-09-02

package unified

import (
	"strings"
	"testing"
)

// TestLoadScoreConfig_DefaultsWithNoOverrides verifies that LoadScoreConfig
// returns the SPEC defaults when no overrides are given.
func TestLoadScoreConfig_DefaultsWithNoOverrides(t *testing.T) {
	cfg, err := LoadScoreConfig(ScoreOverrides{})
	if err != nil {
		t.Fatalf("LoadScoreConfig with no overrides failed: %v", err)
	}

	if cfg.BandCertainMin != 97.0 {
		t.Errorf("default BandCertainMin: expected 97.0, got %.2f", cfg.BandCertainMin)
	}
	if cfg.BandHighMin != 90.0 {
		t.Errorf("default BandHighMin: expected 90.0, got %.2f", cfg.BandHighMin)
	}
	if cfg.BandMediumMin != 75.0 {
		t.Errorf("default BandMediumMin: expected 75.0, got %.2f", cfg.BandMediumMin)
	}
	if cfg.BandReviewMin != 60.0 {
		t.Errorf("default BandReviewMin: expected 60.0, got %.2f", cfg.BandReviewMin)
	}

	// Spot-check a few per-kind defaults.
	exactFile, ok := cfg.Signals[string(SigExactFile)]
	if !ok {
		t.Fatal("expected SigExactFile in default config")
	}
	if exactFile.Base != 1.0 {
		t.Errorf("SigExactFile base: expected 1.0, got %.4f", exactFile.Base)
	}
}

// TestLoadScoreConfig_InvalidConfidenceReturnError verifies that a
// per-kind confidence override outside (0, 1] returns an error.
func TestLoadScoreConfig_InvalidConfidenceReturnError(t *testing.T) {
	// SigExactFile max_confidence of 1.5 is invalid.
	_, err := LoadScoreConfig(ScoreOverrides{Confidence: map[string]KindConfidenceOverride{
		string(SigExactFile): {MinConfidence: 0.5, MaxConfidence: 1.5},
	}})
	if err == nil {
		t.Error("expected error for confidence > 1, got nil")
	}
}

// TestScoreConfig_BoostForMissingKind verifies that BoostFor returns 0
// when the kind is not present in the config map.
func TestScoreConfig_BoostForMissingKind(t *testing.T) {
	cfg := DefaultScoreConfig()
	// Delete a kind from the map.
	delete(cfg.Signals, string(SigDuration))

	if b := cfg.BoostFor(SigDuration); b != 0.0 {
		t.Errorf("BoostFor missing kind: expected 0, got %.2f", b)
	}
}

// TestValidate_NegativeReviewMin verifies that a negative BandReviewMin
// returns a validation error.
func TestValidate_NegativeReviewMin(t *testing.T) {
	cfg := DefaultScoreConfig()
	cfg.BandReviewMin = -1.0

	if err := cfg.Validate(); err == nil {
		t.Error("expected error for negative BandReviewMin, got nil")
	}
}

// TestValidate_MediumHighOrderingError verifies band_high_min > band_medium_min.
func TestValidate_MediumHighOrderingError(t *testing.T) {
	cfg := DefaultScoreConfig()
	cfg.BandHighMin = 70.0 // lower than medium
	cfg.BandMediumMin = 80.0
	// Now band_high < band_medium — invalid.

	if err := cfg.Validate(); err == nil {
		t.Error("expected error for high < medium band ordering, got nil")
	}
}

// TestValidate_MediumReviewOrderingError verifies band_medium_min > band_review_min.
func TestValidate_MediumReviewOrderingError(t *testing.T) {
	cfg := DefaultScoreConfig()
	cfg.BandMediumMin = 55.0 // lower than review
	cfg.BandReviewMin = 60.0

	if err := cfg.Validate(); err == nil {
		t.Error("expected error for medium < review band ordering, got nil")
	}
}

// TestLoadScoreConfig_BandOverrideLeavesOthersAtDefaults verifies that a
// ScoreOverrides value for one band applies and a band left at zero keeps its
// default.
func TestLoadScoreConfig_BandOverrideLeavesOthersAtDefaults(t *testing.T) {
	cfg, err := LoadScoreConfig(ScoreOverrides{BandCertainMin: 99.5})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.BandCertainMin != 99.5 {
		t.Errorf("BandCertainMin: expected override 99.5, got %.2f", cfg.BandCertainMin)
	}
	if cfg.BandHighMin != 90.0 {
		t.Errorf("BandHighMin: expected default 90.0 (override unset), got %.2f", cfg.BandHighMin)
	}
	if cfg.BandMediumMin != 75.0 || cfg.BandReviewMin != 60.0 {
		t.Errorf("untouched bands: expected 75/60, got %.2f/%.2f", cfg.BandMediumMin, cfg.BandReviewMin)
	}
}

// TestLoadScoreConfig_InvalidOverridesReturnError verifies that a persisted
// override producing a non-monotonic band ladder is an ERROR, not a silent
// fallback to defaults. An operator who mistypes band_certain_min below
// band_high_min must hear about it at startup, not have auto-resolve keep
// running on 97/90/75/60.
func TestLoadScoreConfig_InvalidOverridesReturnError(t *testing.T) {
	_, err := LoadScoreConfig(ScoreOverrides{BandCertainMin: 80.0})
	if err == nil {
		t.Fatal("expected error for band_certain_min (80) < band_high_min (90), got nil")
	}
}

// TestLoadScoreConfig_ConfidenceOverride_NoOverridesIsANoOp verifies that a
// nil/unset override map leaves per-kind confidence bounds at their
// default values — existing configs are unaffected by this feature
// existing at all.
func TestLoadScoreConfig_ConfidenceOverride_NoOverridesIsANoOp(t *testing.T) {
	cfg, err := LoadScoreConfig(ScoreOverrides{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	kc := cfg.Signals[string(SigEmbedMedium)]
	if kc.MinConfidence != 0.65 || kc.MaxConfidence != 0.80 {
		t.Fatalf("expected untouched defaults 0.65/0.80, got %.4f/%.4f", kc.MinConfidence, kc.MaxConfidence)
	}
}

// TestLoadScoreConfig_ConfidenceOverride_OverridesOnlyThatKind verifies that a
// DB-persisted override for one kind changes only that kind's bounds — every
// other kind (spot-checked here) keeps its compiled-in default.
func TestLoadScoreConfig_ConfidenceOverride_OverridesOnlyThatKind(t *testing.T) {
	cfg, err := LoadScoreConfig(ScoreOverrides{Confidence: map[string]KindConfidenceOverride{
		string(SigEmbedMedium): {MinConfidence: 0.72, MaxConfidence: 0.83},
	}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	overridden := cfg.Signals[string(SigEmbedMedium)]
	if overridden.MinConfidence != 0.72 || overridden.MaxConfidence != 0.83 {
		t.Errorf("SigEmbedMedium: expected 0.72/0.83, got %.4f/%.4f", overridden.MinConfidence, overridden.MaxConfidence)
	}

	// Untouched kind must still be at its compiled-in default.
	untouched := cfg.Signals[string(SigEmbedHigh)]
	if untouched.MinConfidence != 0.88 || untouched.MaxConfidence != 0.95 {
		t.Errorf("SigEmbedHigh should be untouched: expected 0.88/0.95, got %.4f/%.4f", untouched.MinConfidence, untouched.MaxConfidence)
	}
}

// TestLoadScoreConfig_ConfidenceOverride_UnsetBoundFallsBack verifies that an
// override entry which sets ONLY one bound (the other left at its zero value)
// leaves the other bound at whatever the default would otherwise produce —
// a per-field "not set" fallback, not an all-or-nothing entry.
func TestLoadScoreConfig_ConfidenceOverride_UnsetBoundFallsBack(t *testing.T) {
	cfg, err := LoadScoreConfig(ScoreOverrides{Confidence: map[string]KindConfidenceOverride{
		// MaxConfidence deliberately left at zero == "not set".
		string(SigEmbedMedium): {MinConfidence: 0.72},
	}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	kc := cfg.Signals[string(SigEmbedMedium)]
	if kc.MinConfidence != 0.72 {
		t.Errorf("MinConfidence: expected overridden 0.72, got %.4f", kc.MinConfidence)
	}
	if kc.MaxConfidence != 0.80 {
		t.Errorf("MaxConfidence: expected untouched default 0.80, got %.4f", kc.MaxConfidence)
	}
}

// TestLoadScoreConfig_ConfidenceOverride_UnknownKindIsAnError verifies that
// an override for a kind string DefaultScoreConfig does not seed is rejected,
// naming the kind. The map is operator-written from the calibrate op's
// advisory output; a typo'd kind used to be silently skipped, so the operator
// saw "saved" while the bound never took effect.
func TestLoadScoreConfig_ConfidenceOverride_UnknownKindIsAnError(t *testing.T) {
	_, err := LoadScoreConfig(ScoreOverrides{Confidence: map[string]KindConfidenceOverride{
		"not_a_real_signal_kind": {MinConfidence: 0.5, MaxConfidence: 0.9},
	}})
	if err == nil {
		t.Fatal("expected an error for an unknown confidence kind, got nil")
	}
	if !strings.Contains(err.Error(), "not_a_real_signal_kind") {
		t.Errorf("error should name the unknown kind, got: %v", err)
	}
	if !strings.Contains(err.Error(), string(SigEmbedMedium)) {
		t.Errorf("error should list the known kinds so the typo is findable, got: %v", err)
	}
}

// TestValidate_CertainAboveScoreCapIsAnError: ComposeScore caps scores at
// 100, so band_certain_min = 100.5 would validate as a strictly-decreasing
// ladder while making CERTAIN (and auto-resolve) silently unreachable.
func TestValidate_CertainAboveScoreCapIsAnError(t *testing.T) {
	cfg := DefaultScoreConfig()
	cfg.BandCertainMin = 100.5
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected band_certain_min > 100 to be rejected")
	}
	if !strings.Contains(err.Error(), "100") {
		t.Errorf("error should mention the 100 cap, got: %v", err)
	}
	// Exactly 100 is a legal (if strict) floor: a score of 100 can reach it.
	cfg.BandCertainMin = 100
	if err := cfg.Validate(); err != nil {
		t.Fatalf("band_certain_min = 100 must be accepted, got: %v", err)
	}
}

// TestLoadScoreConfig_NegativeBandOverrideIsRejectedNotIgnored: a negative
// persisted band used to fall through the `> 0` "not set" check and silently
// take the default. Only an exact zero means "not set"; anything else must
// reach Validate.
func TestLoadScoreConfig_NegativeBandOverrideIsRejectedNotIgnored(t *testing.T) {
	_, err := LoadScoreConfig(ScoreOverrides{BandReviewMin: -5})
	if err == nil {
		t.Fatal("a negative band_review_min override must be rejected, not treated as unset")
	}
	if !strings.Contains(err.Error(), "band_review_min") {
		t.Errorf("error should name band_review_min, got: %v", err)
	}

	// Same for confidence bounds: a negative bound is an override, not "unset".
	_, err = LoadScoreConfig(ScoreOverrides{Confidence: map[string]KindConfidenceOverride{
		string(SigEmbedMedium): {MinConfidence: -0.1},
	}})
	if err == nil {
		t.Fatal("a negative min_confidence override must be rejected, not treated as unset")
	}
}

// TestScoreConfig_CloneDoesNotAliasSignals verifies Clone deep-copies the
// Signals map: mutating the clone's per-kind entry leaves the original
// untouched. The engine relies on this to hand out configs the calibrate op
// can sweep without changing live scoring.
func TestScoreConfig_CloneDoesNotAliasSignals(t *testing.T) {
	orig := DefaultScoreConfig()
	cl := orig.Clone()
	kc := cl.Signals[string(SigEmbedHigh)]
	kc.MinConfidence = 0.01
	cl.Signals[string(SigEmbedHigh)] = kc
	if got := orig.Signals[string(SigEmbedHigh)].MinConfidence; got != 0.88 {
		t.Fatalf("Clone aliased Signals: original SigEmbedHigh min changed to %.4f", got)
	}
}
