// file: internal/config/dedup_score_config.go
// version: 1.0.0
// guid: 165676d2-22e4-41ce-8001-c2fef028a4bc
// last-edited: 2026-09-02

package config

import (
	"fmt"

	"github.com/falkcorp/audiobook-organizer/internal/dedup/unified"
)

// ScoreConfig builds the effective unified.ScoreConfig the dedup engine must
// score with from this persisted dedup.signals block (config.yaml merged with
// the DB config blob — the struct AppConfig.Dedup.Signals holds), layered over
// Viper and unified.DefaultScoreConfig.
//
// This is the ONE conversion from DedupSignalConfig to unified.ScoreOverrides,
// and every consumer of the ladder goes through it:
//
//   - Config.Validate (startup, cmd/root.go) — an invalid ladder refuses to
//     start the server before any engine is built.
//   - UpdateService.UpdateConfig (PUT /api/v1/config, the Settings page, and
//     dedup.calibrate-composite apply) — validated BEFORE the blob is written,
//     so a bad ladder can never be persisted and then brick the next restart.
//   - internal/server/registry_wire.go — hands the result to dedup.NewEngine at
//     construction and registers the engine as the UpdateService's sink so a
//     later PUT reloads it.
//
// It lives in this package, not in internal/dedup, because the write boundary
// (UpdateConfig) is here and config → unified is a legal import (unified pulls
// in only viper and internal/models); the reverse, unified → config, would be a
// cycle, which is why the conversion cannot live in unified itself.
//
// A returned error means the effective config failed unified.Validate
// (non-monotonic bands, a band above the 100-point score cap, a confidence
// outside (0,1], an unknown signal kind). Callers MUST fail — refuse to start,
// reject the update, fail the op — never fall back to defaults: a silent
// fallback is exactly how configured thresholds were ignored for the lifetime
// of the unified scorer up to 2026-09-02.
func (s DedupSignalConfig) ScoreConfig() (unified.ScoreConfig, error) {
	ov := unified.ScoreOverrides{
		BandCertainMin: s.BandCertainMin,
		BandHighMin:    s.BandHighMin,
		BandMediumMin:  s.BandMediumMin,
		BandReviewMin:  s.BandReviewMin,
	}
	if len(s.Confidence) > 0 {
		ov.Confidence = make(map[string]unified.KindConfidenceOverride, len(s.Confidence))
		for kind, kc := range s.Confidence {
			ov.Confidence[kind] = unified.KindConfidenceOverride{
				MinConfidence: kc.MinConfidence,
				MaxConfidence: kc.MaxConfidence,
			}
		}
	}
	cfg, err := unified.LoadScoreConfig(ov)
	if err != nil {
		return unified.ScoreConfig{}, fmt.Errorf("dedup.signals: %w", err)
	}
	return cfg, nil
}
