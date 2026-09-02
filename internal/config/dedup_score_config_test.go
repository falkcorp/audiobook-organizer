// file: internal/config/dedup_score_config_test.go
// version: 1.0.0
// guid: 3f8b2c71-9d4e-4a56-b1c0-7e2d5f6a8b9c
// last-edited: 2026-09-02

package config

import (
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/dedup/unified"
)

// TestDedupSignalConfig_ScoreConfig_PersistedValuesWin: every persisted band
// and the per-kind confidence override reach the effective ScoreConfig, and
// differ from unified.DefaultScoreConfig()'s 97/90/75/60 so the test can tell
// "configured" from "default" apart. BandCertainMin=99.9 is the operator move
// the original bug made inert.
func TestDedupSignalConfig_ScoreConfig_PersistedValuesWin(t *testing.T) {
	sigs := DedupSignalConfig{
		BandCertainMin: 99.9,
		BandHighMin:    93.0,
		BandMediumMin:  80.0,
		BandReviewMin:  65.0,
		Confidence: map[string]DedupKindConfidence{
			string(unified.SigEmbedMedium): {MinConfidence: 0.72, MaxConfidence: 0.83},
		},
	}
	got, err := sigs.ScoreConfig()
	if err != nil {
		t.Fatalf("ScoreConfig: %v", err)
	}
	if got.BandCertainMin != 99.9 || got.BandHighMin != 93 || got.BandMediumMin != 80 || got.BandReviewMin != 65 {
		t.Fatalf("persisted bands did not reach the ScoreConfig: %+v", got)
	}
	if kc := got.Signals[string(unified.SigEmbedMedium)]; kc.MinConfidence != 0.72 || kc.MaxConfidence != 0.83 {
		t.Fatalf("persisted confidence override dropped: %+v", kc)
	}
	// Untouched kinds keep their compiled-in bounds.
	if kc := got.Signals[string(unified.SigEmbedHigh)]; kc.MinConfidence != unified.DefaultScoreConfig().Signals[string(unified.SigEmbedHigh)].MinConfidence {
		t.Fatalf("an unrelated kind's bound changed: %+v", kc)
	}
}

// TestDedupSignalConfig_ScoreConfig_ZeroValueIsDefaults: the zero value (a
// blob with no dedup.signals block) converts to the defaults, not an error —
// otherwise every install that never touched the ladder would refuse to start.
func TestDedupSignalConfig_ScoreConfig_ZeroValueIsDefaults(t *testing.T) {
	got, err := DedupSignalConfig{}.ScoreConfig()
	if err != nil {
		t.Fatalf("zero DedupSignalConfig must convert cleanly, got: %v", err)
	}
	def := unified.DefaultScoreConfig()
	if got.BandCertainMin != def.BandCertainMin || got.BandReviewMin != def.BandReviewMin {
		t.Fatalf("zero value should yield defaults, got %+v", got)
	}
}

// TestDedupSignalConfig_ScoreConfig_InvalidLadderIsAnError: a ladder that
// fails unified.Validate is an ERROR from the conversion, prefixed so the
// operator can see which config block it came from — never a silent fallback
// to defaults.
func TestDedupSignalConfig_ScoreConfig_InvalidLadderIsAnError(t *testing.T) {
	_, err := DedupSignalConfig{BandCertainMin: 50.0}.ScoreConfig()
	if err == nil {
		t.Fatal("expected error for band_certain_min=50 below band_high_min=90, got nil")
	}
	if !strings.HasPrefix(err.Error(), "dedup.signals:") {
		t.Errorf("error should be prefixed with the config block name, got: %v", err)
	}
	if _, err := (DedupSignalConfig{BandCertainMin: 100.5}).ScoreConfig(); err == nil {
		t.Fatal("band_certain_min above the 100 score cap must be rejected")
	}
	if _, err := (DedupSignalConfig{Confidence: map[string]DedupKindConfidence{"nope": {MinConfidence: 0.5}}}).ScoreConfig(); err == nil {
		t.Fatal("an unknown confidence kind must be rejected")
	}
}
