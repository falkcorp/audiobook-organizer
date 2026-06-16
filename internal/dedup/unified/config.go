// file: internal/dedup/unified/config.go
// version: 1.1.0
// guid: d8a383db-5083-4257-be54-686ac2e72d32
// last-edited: 2026-06-16

package unified

import (
	"errors"
	"fmt"
	"sync"

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

// bandOverride holds DB-persisted band thresholds injected by the caller
// (engine or server init) to avoid a circular import between unified→config.
// Protected by bandMu; zero values mean "use Viper / defaults".
var (
	bandMu       sync.RWMutex
	bandOverride struct {
		certainMin, highMin, mediumMin, reviewMin float64
	}
)

// SetBandThresholds injects DB-persisted band thresholds into LoadScoreConfig.
// Called by the dedup engine after AppConfig is loaded from the database.
// Values of 0 are ignored (treated as "not set").
func SetBandThresholds(certainMin, highMin, mediumMin, reviewMin float64) {
	bandMu.Lock()
	defer bandMu.Unlock()
	bandOverride.certainMin = certainMin
	bandOverride.highMin = highMin
	bandOverride.mediumMin = mediumMin
	bandOverride.reviewMin = reviewMin
}

// LoadScoreConfig reads overrides from injected band thresholds (DB-persisted,
// set via SetBandThresholds) and Viper (config.yaml dedup.signals.*) on top of
// DefaultScoreConfig.
// Precedence (highest → lowest): SetBandThresholds > Viper > defaults.
// Returns an error if the resulting config fails Validate.
func LoadScoreConfig() (ScoreConfig, error) {
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

	// Override with DB-persisted values injected via SetBandThresholds.
	// Non-zero check: zero values indicate "not set".
	bandMu.RLock()
	ov := bandOverride
	bandMu.RUnlock()
	if ov.certainMin > 0 {
		cfg.BandCertainMin = ov.certainMin
	}
	if ov.highMin > 0 {
		cfg.BandHighMin = ov.highMin
	}
	if ov.mediumMin > 0 {
		cfg.BandMediumMin = ov.mediumMin
	}
	if ov.reviewMin > 0 {
		cfg.BandReviewMin = ov.reviewMin
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

	if err := cfg.Validate(); err != nil {
		return ScoreConfig{}, err
	}
	return cfg, nil
}

// Validate returns an error if the ScoreConfig contains out-of-range values.
// Confidences must be in (0, 1] for primary kinds; band thresholds must be
// strictly decreasing (CERTAIN > HIGH > MEDIUM > REVIEW ≥ 0).
func (c ScoreConfig) Validate() error {
	var errs []error

	// Band threshold ordering.
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
