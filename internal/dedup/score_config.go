// file: internal/dedup/score_config.go
// version: 1.0.0
// guid: 165676d2-22e4-41ce-8001-c2fef028a4bc
// last-edited: 2026-09-02

package dedup

import (
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/dedup/unified"
)

// LoadScoreConfig builds the unified ScoreConfig the engine must score with
// from the persisted dedup.signals settings (config.yaml merged with the DB
// config blob — the same struct config.AppConfig.Dedup.Signals holds), layered
// over Viper and unified.DefaultScoreConfig.
//
// This is the ONE conversion from config.DedupSignalConfig to
// unified.ScoreOverrides. Both places that feed the live engine go through
// it: internal/server/registry_wire.go at startup (into NewEngine) and
// dedup.calibrate-composite's apply path (into Engine.SetScoreConfig after
// it persisted new bands). It lives in this package because unified cannot
// import internal/config (unified→config would be circular) and this package
// already does.
//
// A returned error means the effective config failed unified.Validate
// (non-monotonic bands, a confidence outside (0,1]). Callers MUST fail — the
// server refuses to start, the op returns the error — never fall back to
// defaults: a fallback is exactly how configured thresholds were silently
// ignored for the lifetime of the unified scorer up to 2026-09-02.
func LoadScoreConfig(sigs config.DedupSignalConfig) (unified.ScoreConfig, error) {
	ov := unified.ScoreOverrides{
		BandCertainMin: sigs.BandCertainMin,
		BandHighMin:    sigs.BandHighMin,
		BandMediumMin:  sigs.BandMediumMin,
		BandReviewMin:  sigs.BandReviewMin,
	}
	if len(sigs.Confidence) > 0 {
		ov.Confidence = make(map[string]unified.KindConfidenceOverride, len(sigs.Confidence))
		for kind, kc := range sigs.Confidence {
			ov.Confidence[kind] = unified.KindConfidenceOverride{
				MinConfidence: kc.MinConfidence,
				MaxConfidence: kc.MaxConfidence,
			}
		}
	}
	return unified.LoadScoreConfig(ov)
}
