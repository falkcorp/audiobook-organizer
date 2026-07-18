// file: internal/dedup/unified/config_test.go
// version: 1.1.0
// guid: 4f7a2c9b-8e3d-4b6f-a1c5-9e7b3d2f6a8c
// last-edited: 2026-07-18

package unified

import (
	"testing"

	"github.com/spf13/viper"
)

// resetViper clears all Viper state so tests don't bleed into each other.
func resetViper(t *testing.T) {
	t.Helper()
	viper.Reset()
}

// TestLoadScoreConfig_DefaultsWithNoOverrides verifies that LoadScoreConfig
// returns the SPEC defaults when no Viper keys are set.
func TestLoadScoreConfig_DefaultsWithNoOverrides(t *testing.T) {
	resetViper(t)

	cfg, err := LoadScoreConfig()
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

// TestLoadScoreConfig_BandOverrides verifies that band thresholds can be
// overridden individually via Viper.
func TestLoadScoreConfig_BandOverrides(t *testing.T) {
	resetViper(t)

	viper.Set("dedup.signals.band_certain_min", 98.0)
	viper.Set("dedup.signals.band_high_min", 92.0)
	viper.Set("dedup.signals.band_medium_min", 80.0)
	viper.Set("dedup.signals.band_review_min", 65.0)

	cfg, err := LoadScoreConfig()
	if err != nil {
		t.Fatalf("LoadScoreConfig with band overrides failed: %v", err)
	}

	if cfg.BandCertainMin != 98.0 {
		t.Errorf("BandCertainMin: expected 98.0, got %.2f", cfg.BandCertainMin)
	}
	if cfg.BandHighMin != 92.0 {
		t.Errorf("BandHighMin: expected 92.0, got %.2f", cfg.BandHighMin)
	}
	if cfg.BandMediumMin != 80.0 {
		t.Errorf("BandMediumMin: expected 80.0, got %.2f", cfg.BandMediumMin)
	}
	if cfg.BandReviewMin != 65.0 {
		t.Errorf("BandReviewMin: expected 65.0, got %.2f", cfg.BandReviewMin)
	}
}

// TestLoadScoreConfig_PerKindBoostOverride verifies that per-kind boost
// values can be overridden via Viper.
func TestLoadScoreConfig_PerKindBoostOverride(t *testing.T) {
	resetViper(t)

	// Override the duration boost to 8.0.
	viper.Set("dedup.signals."+string(SigDuration), true) // marks it as "set"
	viper.Set("dedup.signals."+string(SigDuration)+".boost", 8.0)

	cfg, err := LoadScoreConfig()
	if err != nil {
		t.Fatalf("LoadScoreConfig with kind override failed: %v", err)
	}

	if cfg.BoostFor(SigDuration) != 8.0 {
		t.Errorf("duration boost after override: expected 8.0, got %.2f", cfg.BoostFor(SigDuration))
	}
	// folder_path should be unchanged.
	if cfg.BoostFor(SigFolderPath) != 3.0 {
		t.Errorf("folder_path boost should be unchanged: expected 3.0, got %.2f", cfg.BoostFor(SigFolderPath))
	}
}

// TestLoadScoreConfig_PerKindBaseOverride verifies that per-kind base
// values can be overridden via Viper.
func TestLoadScoreConfig_PerKindBaseOverride(t *testing.T) {
	resetViper(t)

	viper.Set("dedup.signals."+string(SigMetaFuzzy), true)
	viper.Set("dedup.signals."+string(SigMetaFuzzy)+".base", 0.75)
	viper.Set("dedup.signals."+string(SigMetaFuzzy)+".min_confidence", 0.75)
	viper.Set("dedup.signals."+string(SigMetaFuzzy)+".max_confidence", 0.90)

	cfg, err := LoadScoreConfig()
	if err != nil {
		t.Fatalf("LoadScoreConfig with base override failed: %v", err)
	}

	kc := cfg.Signals[string(SigMetaFuzzy)]
	if kc.Base != 0.75 {
		t.Errorf("SigMetaFuzzy base: expected 0.75, got %.4f", kc.Base)
	}
	if kc.MinConfidence != 0.75 {
		t.Errorf("SigMetaFuzzy min_confidence: expected 0.75, got %.4f", kc.MinConfidence)
	}
	if kc.MaxConfidence != 0.90 {
		t.Errorf("SigMetaFuzzy max_confidence: expected 0.90, got %.4f", kc.MaxConfidence)
	}
}

// TestLoadScoreConfig_ScaleOverride verifies scale override.
func TestLoadScoreConfig_ScaleOverride(t *testing.T) {
	resetViper(t)

	viper.Set("dedup.signals."+string(SigEmbedHigh), true)
	viper.Set("dedup.signals."+string(SigEmbedHigh)+".scale", 2.0)

	cfg, err := LoadScoreConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	kc := cfg.Signals[string(SigEmbedHigh)]
	if kc.Scale != 2.0 {
		t.Errorf("SigEmbedHigh scale: expected 2.0, got %.4f", kc.Scale)
	}
}

// TestLoadScoreConfig_InvalidBandsReturnError verifies that LoadScoreConfig
// returns an error when band overrides produce an invalid ordering.
func TestLoadScoreConfig_InvalidBandsReturnError(t *testing.T) {
	resetViper(t)

	// Set CERTAIN lower than HIGH — invalid ordering.
	viper.Set("dedup.signals.band_certain_min", 80.0)
	viper.Set("dedup.signals.band_high_min", 90.0)

	_, err := LoadScoreConfig()
	if err == nil {
		t.Error("expected error for invalid band ordering, got nil")
	}
}

// TestLoadScoreConfig_InvalidConfidenceReturnError verifies that a
// per-kind confidence override outside (0, 1] returns an error.
func TestLoadScoreConfig_InvalidConfidenceReturnError(t *testing.T) {
	resetViper(t)

	// Set SigExactFile max_confidence to 1.5 — invalid.
	viper.Set("dedup.signals."+string(SigExactFile), true)
	viper.Set("dedup.signals."+string(SigExactFile)+".min_confidence", 0.5)
	viper.Set("dedup.signals."+string(SigExactFile)+".max_confidence", 1.5)

	_, err := LoadScoreConfig()
	if err == nil {
		t.Error("expected error for confidence > 1, got nil")
	}
}

// TestLoadScoreConfig_AllKindsCanBeOverridden exercises the loop over all
// signal kinds in LoadScoreConfig, touching the non-set path for each.
func TestLoadScoreConfig_AllKindsCanBeOverridden(t *testing.T) {
	resetViper(t)

	// Set a known-valid override for every kind via the kind-level key.
	// This exercises each branch of the per-kind loop in LoadScoreConfig.
	allKinds := []SignalKind{
		SigExactFile, SigExactAcoustID, SigISBNASIN, SigLSHAcoustID,
		SigEmbedHigh, SigMetaSrcHash, SigMetaFuzzy, SigEmbedMedium,
		SigDuration, SigFolderPath,
	}
	for _, k := range allKinds {
		key := "dedup.signals." + string(k)
		viper.Set(key, true) // marks the key as set so the branch executes
	}

	cfg, err := LoadScoreConfig()
	if err != nil {
		t.Fatalf("LoadScoreConfig with all kinds set: %v", err)
	}

	// Just verify all kinds are present with valid defaults.
	for _, k := range allKinds {
		if _, ok := cfg.Signals[string(k)]; !ok {
			t.Errorf("missing kind %q after override", k)
		}
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
	cfg.BandHighMin = 70.0  // lower than medium
	cfg.BandMediumMin = 80.0
	// Now band_high < band_medium — invalid.

	if err := cfg.Validate(); err == nil {
		t.Error("expected error for high < medium band ordering, got nil")
	}
}

// TestValidate_MediumReviewOrderingError verifies band_medium_min > band_review_min.
func TestValidate_MediumReviewOrderingError(t *testing.T) {
	cfg := DefaultScoreConfig()
	cfg.BandMediumMin = 55.0  // lower than review
	cfg.BandReviewMin = 60.0

	if err := cfg.Validate(); err == nil {
		t.Error("expected error for medium < review band ordering, got nil")
	}
}

// TestLoadScoreConfig_PerKindMinConfidenceOverride exercises the min_confidence branch.
func TestLoadScoreConfig_PerKindMinConfidenceOverride(t *testing.T) {
	resetViper(t)

	viper.Set("dedup.signals."+string(SigEmbedMedium), true)
	viper.Set("dedup.signals."+string(SigEmbedMedium)+".min_confidence", 0.70)

	cfg, err := LoadScoreConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	kc := cfg.Signals[string(SigEmbedMedium)]
	if kc.MinConfidence != 0.70 {
		t.Errorf("min_confidence override: expected 0.70, got %.4f", kc.MinConfidence)
	}
}

// b6ResetConfidenceOverrides clears the package-level DB-persisted per-kind
// confidence override state (SetKindConfidenceOverrides) before the test body
// runs and again on cleanup, so TestSetKindConfidenceOverrides_* tests never
// bleed into each other or into unrelated tests in this package (mirrors
// resetViper's role for the Viper-backed state). Name is task-scoped (b6xxx,
// INIT-1 T05 follow-up / TODO item 6) to avoid colliding with any other
// worktree's helper of the same shape.
func b6ResetConfidenceOverrides(t *testing.T) {
	t.Helper()
	SetKindConfidenceOverrides(nil)
	t.Cleanup(func() { SetKindConfidenceOverrides(nil) })
}

// TestSetKindConfidenceOverrides_NoOverridesIsANoOp verifies that a nil/unset
// override map (the default, and what SetKindConfidenceOverrides(nil) means)
// leaves LoadScoreConfig's per-kind confidence bounds at their Viper/default
// values — i.e. existing behaviour (and existing configs) are unaffected by
// this feature existing at all.
func TestSetKindConfidenceOverrides_NoOverridesIsANoOp(t *testing.T) {
	resetViper(t)
	b6ResetConfidenceOverrides(t)

	cfg, err := LoadScoreConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	kc := cfg.Signals[string(SigEmbedMedium)]
	if kc.MinConfidence != 0.65 || kc.MaxConfidence != 0.80 {
		t.Fatalf("expected untouched defaults 0.65/0.80, got %.4f/%.4f", kc.MinConfidence, kc.MaxConfidence)
	}
}

// TestSetKindConfidenceOverrides_OverridesOnlyThatKind verifies that a
// DB-persisted override for one kind changes only that kind's bounds in the
// resulting ScoreConfig — every other kind (spot-checked here) keeps its
// compiled-in default. This is the "(a) a per-kind confidence overrides the
// default for that kind only" requirement.
func TestSetKindConfidenceOverrides_OverridesOnlyThatKind(t *testing.T) {
	resetViper(t)
	b6ResetConfidenceOverrides(t)

	SetKindConfidenceOverrides(map[string]KindConfidenceOverride{
		string(SigEmbedMedium): {MinConfidence: 0.72, MaxConfidence: 0.83},
	})

	cfg, err := LoadScoreConfig()
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

// TestSetKindConfidenceOverrides_UnsetBoundFallsBack verifies that an
// override entry which sets ONLY one bound (the other left at its zero
// value) leaves the other bound at whatever Viper/default would otherwise
// produce — a per-field "not set" fallback, not an all-or-nothing entry.
// This is the "(b) an unset per-kind value falls back to the prior
// behaviour" requirement.
func TestSetKindConfidenceOverrides_UnsetBoundFallsBack(t *testing.T) {
	resetViper(t)
	b6ResetConfidenceOverrides(t)

	SetKindConfidenceOverrides(map[string]KindConfidenceOverride{
		// MaxConfidence deliberately left at zero == "not set".
		string(SigEmbedMedium): {MinConfidence: 0.72},
	})

	cfg, err := LoadScoreConfig()
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

// TestSetKindConfidenceOverrides_PrecedenceOverViper verifies the documented
// precedence order (DB override > Viper > defaults): a Viper-set value AND a
// DB-persisted override for the same kind/bound results in the DB value
// winning, exactly like SetBandThresholds already wins over Viper for bands.
func TestSetKindConfidenceOverrides_PrecedenceOverViper(t *testing.T) {
	resetViper(t)
	b6ResetConfidenceOverrides(t)

	viper.Set("dedup.signals."+string(SigEmbedMedium), true)
	viper.Set("dedup.signals."+string(SigEmbedMedium)+".min_confidence", 0.66)

	SetKindConfidenceOverrides(map[string]KindConfidenceOverride{
		string(SigEmbedMedium): {MinConfidence: 0.74},
	})

	cfg, err := LoadScoreConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	kc := cfg.Signals[string(SigEmbedMedium)]
	if kc.MinConfidence != 0.74 {
		t.Errorf("expected DB override 0.74 to win over Viper's 0.66, got %.4f", kc.MinConfidence)
	}
}

// TestSetKindConfidenceOverrides_ConsumedByLoadScoreConfig is requirement (c):
// the per-kind value is reflected by the config the rest of the package
// consumes. As of this change (see docs/plans/DECISIONS-PENDING.md row 10),
// ComposeScore itself does not yet clamp against these bounds — the wiring
// stops at LoadScoreConfig's returned ScoreConfig, which is what
// dedup.calibrate-composite and the dedup engine both read cfg.Signals from.
// This test exercises exactly that boundary rather than asserting a
// ComposeScore behaviour change that was deliberately not made here.
func TestSetKindConfidenceOverrides_ConsumedByLoadScoreConfig(t *testing.T) {
	resetViper(t)
	b6ResetConfidenceOverrides(t)

	SetKindConfidenceOverrides(map[string]KindConfidenceOverride{
		string(SigLSHAcoustID): {MinConfidence: 0.91, MaxConfidence: 0.96},
	})

	cfg, err := LoadScoreConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	kc, ok := cfg.Signals[string(SigLSHAcoustID)]
	if !ok {
		t.Fatal("expected SigLSHAcoustID entry in cfg.Signals")
	}
	if kc.MinConfidence != 0.91 || kc.MaxConfidence != 0.96 {
		t.Errorf("expected 0.91/0.96, got %.4f/%.4f", kc.MinConfidence, kc.MaxConfidence)
	}
}

// TestSetKindConfidenceOverrides_UnknownKindIsSkipped verifies that an
// override for a kind string not present in cfg.Signals (shouldn't happen in
// practice — DefaultScoreConfig seeds every known kind — but a stale/typo'd
// persisted key must fail closed, not panic or create a partial entry) is
// silently ignored rather than propagated.
func TestSetKindConfidenceOverrides_UnknownKindIsSkipped(t *testing.T) {
	resetViper(t)
	b6ResetConfidenceOverrides(t)

	SetKindConfidenceOverrides(map[string]KindConfidenceOverride{
		"not_a_real_signal_kind": {MinConfidence: 0.5, MaxConfidence: 0.9},
	})

	cfg, err := LoadScoreConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := cfg.Signals["not_a_real_signal_kind"]; ok {
		t.Error("unknown kind should not be created in cfg.Signals")
	}
}
