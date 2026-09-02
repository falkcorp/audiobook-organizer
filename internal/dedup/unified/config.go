// file: internal/dedup/unified/config.go
// version: 2.1.0
// guid: d8a383db-5083-4257-be54-686ac2e72d32
// last-edited: 2026-09-02

package unified

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/spf13/viper"
)

// KindConfig holds per-signal-kind calibration values. All fields have
// spec-mandated defaults (SPEC 1 §3) and can be overridden in config.yaml
// under dedup.signals.<kind>.*
type KindConfig struct {
	// Base is the default confidence value used when the signal emits at
	// the midpoint of its range. Collectors that produce a single fixed
	// confidence (e.g. SigExactFile always 1.0) ignore Scale.
	Base float64 `mapstructure:"base" json:"base"`

	// Scale is a linear multiplier applied to the raw measurement when
	// computing the final per-signal Confidence in the collector. Stored
	// here so consumers can understand the calibration even when the
	// collector already computed the confidence.
	Scale float64 `mapstructure:"scale" json:"scale"`

	// MinConfidence is the lower bound for confidence values emitted by
	// this kind. Values below this floor are clamped (collector-side).
	MinConfidence float64 `mapstructure:"min_confidence" json:"min_confidence"`

	// MaxConfidence is the upper bound for confidence values emitted by
	// this kind. Values above this ceiling are clamped (collector-side).
	MaxConfidence float64 `mapstructure:"max_confidence" json:"max_confidence"`

	// Boost is the additive score points added when this supporting signal
	// is present. Only meaningful for supporting kinds (SigDuration,
	// SigFolderPath); ignored for primary kinds.
	Boost float64 `mapstructure:"boost" json:"boost"`
}

// ScoreConfig is the top-level config for the unified scoring package,
// loaded from config.yaml under dedup.signals.*. All fields have
// SPEC-mandated defaults applied by DefaultScoreConfig.
type ScoreConfig struct {
	// Signals maps each SignalKind string to its per-kind calibration.
	Signals map[string]KindConfig `mapstructure:"signals" json:"signals"`

	// BandCertainMin is the minimum score to be classified as CERTAIN (default 97).
	BandCertainMin float64 `mapstructure:"band_certain_min" json:"band_certain_min"`
	// BandHighMin is the minimum score to be classified as HIGH (default 90).
	BandHighMin float64 `mapstructure:"band_high_min" json:"band_high_min"`
	// BandMediumMin is the minimum score to be classified as MEDIUM (default 75).
	BandMediumMin float64 `mapstructure:"band_medium_min" json:"band_medium_min"`
	// BandReviewMin is the minimum score to be classified as REVIEW (default 60).
	// Scores below this are not persisted.
	BandReviewMin float64 `mapstructure:"band_review_min" json:"band_review_min"`
}

// DefaultScoreConfig returns a ScoreConfig populated with the SPEC 1 §3
// defaults. These values are the authoritative baseline; config.yaml
// overrides individual fields.
func DefaultScoreConfig() ScoreConfig {
	return ScoreConfig{
		BandCertainMin: 97.0,
		BandHighMin:    90.0,
		BandMediumMin:  75.0,
		BandReviewMin:  60.0,
		Signals: map[string]KindConfig{
			string(SigExactFile): {
				Base:          1.00,
				Scale:         1.00,
				MinConfidence: 1.00,
				MaxConfidence: 1.00,
				Boost:         0,
			},
			string(SigExactAcoustID): {
				Base:          0.99,
				Scale:         1.00,
				MinConfidence: 0.99,
				MaxConfidence: 0.99,
				Boost:         0,
			},
			string(SigISBNASIN): {
				Base:          0.98,
				Scale:         1.00,
				MinConfidence: 0.98,
				MaxConfidence: 0.98,
				Boost:         0,
			},
			string(SigLSHAcoustID): {
				// Hamming-scaled 0.90–0.97 over hamming similarity 0.85–1.0.
				Base:          0.90,
				Scale:         0.14, // (0.97 - 0.90) / (1.0 - 0.85) = 0.4667 — stored per-kind but actual scaling is collector-side
				MinConfidence: 0.90,
				MaxConfidence: 0.97,
				Boost:         0,
			},
			string(SigEmbedHigh): {
				// cosine ≥ 0.95, confidence 0.88–0.95.
				Base:          0.88,
				Scale:         1.00,
				MinConfidence: 0.88,
				MaxConfidence: 0.95,
				Boost:         0,
			},
			string(SigMetaSrcHash): {
				Base:          0.97,
				Scale:         1.00,
				MinConfidence: 0.97,
				MaxConfidence: 0.97,
				Boost:         0,
			},
			string(SigMetaFuzzy): {
				// Levenshtein-scaled 0.70–0.85.
				Base:          0.70,
				Scale:         1.00,
				MinConfidence: 0.70,
				MaxConfidence: 0.85,
				Boost:         0,
			},
			string(SigEmbedMedium): {
				// 0.85 ≤ cos < 0.95, confidence 0.65–0.80.
				Base:          0.65,
				Scale:         1.00,
				MinConfidence: 0.65,
				MaxConfidence: 0.80,
				Boost:         0,
			},
			// Supporting kinds — boosts only, never in noisy-OR product.
			string(SigDuration): {
				Base:          0,
				Scale:         0,
				MinConfidence: 0,
				MaxConfidence: 0,
				Boost:         4.0, // SPEC 1 §4: +4 for duration match within ±2%
			},
			string(SigFolderPath): {
				Base:          0,
				Scale:         0,
				MinConfidence: 0,
				MaxConfidence: 0,
				Boost:         3.0, // SPEC 1 §4: +3 for matching folder path
			},
		},
	}
}

// KindConfidenceOverride is a per-signal-kind confidence-bound override
// value. It mirrors config.DedupKindConfidence's two fields without this
// package importing internal/config (unified→config would be a circular
// import). Zero on either field means "not set" for that bound.
type KindConfidenceOverride struct {
	MinConfidence float64
	MaxConfidence float64
}

// ScoreOverrides carries the DB-persisted (config-blob) scoring values the
// caller wants layered on top of Viper and the compiled-in defaults. It is
// the ONLY way persisted values reach LoadScoreConfig.
//
// WHY an explicit parameter and not package-level setters: until 2026-09-02
// these values were injected through two package globals (SetBandThresholds /
// SetKindConfidenceOverrides) that registry_wire.go wrote at startup and that
// only LoadScoreConfig read — and the live dedup engine never called
// LoadScoreConfig, so config.yaml `dedup.signals.*`, the persisted calibrated
// bands, and the calibrate op's apply=true output were all inert while the
// engine scored on the hard-coded 97/90/75/60. Passing the overrides in the
// call makes the dependency visible at the ONE place the engine is built and
// removes the second, disconnected way of configuring scoring.
//
// A band field at exactly zero, and a confidence entry with a bound at exactly
// zero, mean "not set" — that value falls through to Viper / DefaultScoreConfig.
// Any OTHER value, including a negative one, is an override and is checked by
// Validate; a negative band used to read as "not set" and silently take the
// default, which hid the mistake instead of rejecting it.
type ScoreOverrides struct {
	BandCertainMin float64
	BandHighMin    float64
	BandMediumMin  float64
	BandReviewMin  float64
	// Confidence is keyed by SignalKind string (e.g. "embedding_med" — the
	// SignalKind constants' values, NOT their Go names; an unknown key is an error).
	Confidence map[string]KindConfidenceOverride
}

// LoadScoreConfig builds the effective ScoreConfig from, in ascending
// precedence, DefaultScoreConfig, Viper (config.yaml dedup.signals.*), and
// the caller-supplied DB-persisted overrides. Returns an error if the
// resulting config fails Validate — callers must treat that as fatal, never
// fall back to defaults (an invalid persisted config would otherwise
// silently revert auto-resolve to the compiled-in bands).
func LoadScoreConfig(ov ScoreOverrides) (ScoreConfig, error) {
	cfg := DefaultScoreConfig()

	// Merge band thresholds if overridden via Viper (config.yaml / env vars).
	if viper.IsSet("dedup.signals.band_certain_min") {
		cfg.BandCertainMin = viper.GetFloat64("dedup.signals.band_certain_min")
	}
	if viper.IsSet("dedup.signals.band_high_min") {
		cfg.BandHighMin = viper.GetFloat64("dedup.signals.band_high_min")
	}
	if viper.IsSet("dedup.signals.band_medium_min") {
		cfg.BandMediumMin = viper.GetFloat64("dedup.signals.band_medium_min")
	}
	if viper.IsSet("dedup.signals.band_review_min") {
		cfg.BandReviewMin = viper.GetFloat64("dedup.signals.band_review_min")
	}

	// Override with DB-persisted band values. Exactly zero means "not set";
	// everything else — negatives included — is an override that Validate
	// below must pass or reject.
	if ov.BandCertainMin != 0 {
		cfg.BandCertainMin = ov.BandCertainMin
	}
	if ov.BandHighMin != 0 {
		cfg.BandHighMin = ov.BandHighMin
	}
	if ov.BandMediumMin != 0 {
		cfg.BandMediumMin = ov.BandMediumMin
	}
	if ov.BandReviewMin != 0 {
		cfg.BandReviewMin = ov.BandReviewMin
	}

	// Merge per-kind overrides.
	for _, kind := range []SignalKind{
		SigExactFile, SigExactAcoustID, SigISBNASIN,
		SigLSHAcoustID, SigEmbedHigh, SigMetaSrcHash,
		SigMetaFuzzy, SigEmbedMedium, SigDuration, SigFolderPath,
	} {
		key := "dedup.signals." + string(kind)
		if !viper.IsSet(key) {
			continue
		}
		kc := cfg.Signals[string(kind)]
		if viper.IsSet(key + ".base") {
			kc.Base = viper.GetFloat64(key + ".base")
		}
		if viper.IsSet(key + ".scale") {
			kc.Scale = viper.GetFloat64(key + ".scale")
		}
		if viper.IsSet(key + ".min_confidence") {
			kc.MinConfidence = viper.GetFloat64(key + ".min_confidence")
		}
		if viper.IsSet(key + ".max_confidence") {
			kc.MaxConfidence = viper.GetFloat64(key + ".max_confidence")
		}
		if viper.IsSet(key + ".boost") {
			kc.Boost = viper.GetFloat64(key + ".boost")
		}
		cfg.Signals[string(kind)] = kc
	}

	// Merge DB-persisted per-kind confidence overrides (highest precedence,
	// mirroring the band thresholds above: DB override > Viper > defaults).
	// Within one override entry, an exactly-zero MinConfidence/MaxConfidence
	// field is "not set" and leaves that particular bound at its Viper/default
	// value — only the non-zero bound(s) in an entry are applied. The map is
	// operator-written (the calibrate op's Round-2 advisory tells them what to
	// write), so a kind DefaultScoreConfig does not seed is a typo, and a typo
	// is an error — not a silent no-op that leaves the operator believing the
	// bound took effect.
	for kindStr, kov := range ov.Confidence {
		kc, ok := cfg.Signals[kindStr]
		if !ok {
			return ScoreConfig{}, fmt.Errorf("confidence override for unknown signal kind %q (known kinds: %s)", kindStr, knownSignalKindList())
		}
		if kov.MinConfidence != 0 {
			kc.MinConfidence = kov.MinConfidence
		}
		if kov.MaxConfidence != 0 {
			kc.MaxConfidence = kov.MaxConfidence
		}
		cfg.Signals[kindStr] = kc
	}

	if err := cfg.Validate(); err != nil {
		return ScoreConfig{}, err
	}
	return cfg, nil
}

// Clone deep-copies the ScoreConfig, including its Signals map, so the
// copy never aliases the receiver's per-kind entries. The engine stores and
// hands out clones so a caller mutating a returned config (the calibrate
// op's coordinate-wise sweep variants) cannot change live scoring by
// accident.
func (c ScoreConfig) Clone() ScoreConfig {
	out := c
	out.Signals = make(map[string]KindConfig, len(c.Signals))
	maps.Copy(out.Signals, c.Signals)
	return out
}

// knownSignalKindList renders every kind DefaultScoreConfig seeds, sorted, for
// the unknown-kind error so the operator can see the spelling that was meant.
func knownSignalKindList() string {
	kinds := slices.Sorted(maps.Keys(DefaultScoreConfig().Signals))
	return strings.Join(kinds, ", ")
}

// Validate returns an error if the ScoreConfig contains out-of-range values.
// Confidences must be in (0, 1] for primary kinds; band thresholds must be
// strictly decreasing (100 ≥ CERTAIN > HIGH > MEDIUM > REVIEW ≥ 0).
//
// The 100 ceiling is not cosmetic: ComposeScore caps every score at 100
// (compose.go), so a band_certain_min above it validates, starts, and makes
// CERTAIN — and with it auto-resolve — silently unreachable with no error and
// no log line. Reject it here where the value is set.
func (c ScoreConfig) Validate() error {
	var errs []error

	// Band threshold ordering.
	if c.BandCertainMin > 100 {
		errs = append(errs, fmt.Errorf("band_certain_min (%.2f) must be ≤ 100 — scores are capped at 100, so a higher floor makes CERTAIN unreachable", c.BandCertainMin))
	}
	if !(c.BandCertainMin > c.BandHighMin) {
		errs = append(errs, fmt.Errorf("band_certain_min (%.2f) must be > band_high_min (%.2f)", c.BandCertainMin, c.BandHighMin))
	}
	if !(c.BandHighMin > c.BandMediumMin) {
		errs = append(errs, fmt.Errorf("band_high_min (%.2f) must be > band_medium_min (%.2f)", c.BandHighMin, c.BandMediumMin))
	}
	if !(c.BandMediumMin > c.BandReviewMin) {
		errs = append(errs, fmt.Errorf("band_medium_min (%.2f) must be > band_review_min (%.2f)", c.BandMediumMin, c.BandReviewMin))
	}
	if c.BandReviewMin < 0 {
		errs = append(errs, fmt.Errorf("band_review_min (%.2f) must be ≥ 0", c.BandReviewMin))
	}

	// Per-kind confidence range validation for primary kinds.
	for _, kind := range []SignalKind{
		SigExactFile, SigExactAcoustID, SigISBNASIN,
		SigLSHAcoustID, SigEmbedHigh, SigMetaSrcHash,
		SigMetaFuzzy, SigEmbedMedium,
	} {
		kc, ok := c.Signals[string(kind)]
		if !ok {
			continue // missing entry uses no confidence — validated at use time
		}
		// Per SPEC: confidences must be in (0, 1].
		if kc.MinConfidence <= 0 || kc.MinConfidence > 1 {
			errs = append(errs, fmt.Errorf("kind %s: min_confidence (%.4f) must be in (0,1]", kind, kc.MinConfidence))
		}
		if kc.MaxConfidence <= 0 || kc.MaxConfidence > 1 {
			errs = append(errs, fmt.Errorf("kind %s: max_confidence (%.4f) must be in (0,1]", kind, kc.MaxConfidence))
		}
		if kc.MinConfidence > kc.MaxConfidence {
			errs = append(errs, fmt.Errorf("kind %s: min_confidence (%.4f) > max_confidence (%.4f)", kind, kc.MinConfidence, kc.MaxConfidence))
		}
	}

	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

// BoostFor returns the configured additive boost for a supporting signal kind.
// Returns 0 for primary kinds (which use noisy-OR, not boosts).
func (c ScoreConfig) BoostFor(k SignalKind) float64 {
	kc, ok := c.Signals[string(k)]
	if !ok {
		return 0
	}
	return kc.Boost
}
