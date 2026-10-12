// file: internal/dedup/unified/window_kind_test.go
// version: 1.0.0
// guid: 4a8d2e61-07b3-4c9f-8e15-6f2b9c7d0a34
// last-edited: 2026-10-11

package unified

import (
	"strings"
	"testing"

	"github.com/spf13/viper"
)

// 02-PR8: SigWindowAcoustID is a supporting kind with a configurable boost.

func TestFormulaVersionIsV2(t *testing.T) {
	if FormulaVersion != "noisy-or-v2" {
		t.Fatalf("FormulaVersion = %q, want noisy-or-v2 (02-PR8 changed the signal set)", FormulaVersion)
	}
	if got := ComposeScore([]Signal{{Kind: SigWindowAcoustID, Raw: 0.97}}, nil, DefaultScoreConfig(), defaultPair).Formula; got != "noisy-or-v2" {
		t.Fatalf("composed Formula = %q, want noisy-or-v2", got)
	}
}

func TestSupportingKind_WindowIsSupportingWithDefaultBoost(t *testing.T) {
	if !IsSupportingKind(SigWindowAcoustID) {
		t.Fatal("window_acoustid must be a supporting kind")
	}
	cfg := DefaultScoreConfig()
	if got := cfg.BoostFor(SigWindowAcoustID); got != 6 {
		t.Fatalf("default boost = %v, want 6", got)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("default config with the new kind must validate: %v", err)
	}
	// Every supporting kind at once is still below the review floor: the
	// window signal can never make a candidate alone (the engine passes at
	// most one per pair, see CollectWindowAcoustIDForBook).
	got := ComposeScore([]Signal{
		{Kind: SigWindowAcoustID, Raw: 1, Confidence: 0.97},
		{Kind: SigDuration},
		{Kind: SigFolderPath},
	}, nil, cfg, defaultPair)
	if got.Score >= cfg.BandReviewMin || got.Band != "" {
		t.Fatalf("supporting-only score %.1f band %q: must stay below %.0f with no band", got.Score, got.Band, cfg.BandReviewMin)
	}
	if got.Score != 13 {
		t.Fatalf("supporting-only score = %.1f, want 4+3+6 = 13", got.Score)
	}
	// On top of a primary signal it adds exactly its boost.
	base := ComposeScore([]Signal{{Kind: SigEmbedMedium, Confidence: 0.70}}, nil, cfg, defaultPair)
	with := ComposeScore([]Signal{{Kind: SigEmbedMedium, Confidence: 0.70}, {Kind: SigWindowAcoustID, Confidence: 0.97}}, nil, cfg, defaultPair)
	if d := with.Score - base.Score; d < 5.999 || d > 6.001 {
		t.Fatalf("window boost on a primary score = %.3f, want 6", d)
	}
}

// TestLoadScoreConfig_WindowBoostZeroDisables is the rollback lever: a
// config.yaml boost of 0 for the kind makes the signal inert.
func TestLoadScoreConfig_WindowBoostZeroDisables(t *testing.T) {
	resetViper(t)
	t.Cleanup(viper.Reset)
	viper.SetConfigType("yaml")
	if err := viper.ReadConfig(strings.NewReader("dedup:\n  signals:\n    window_acoustid:\n      boost: 0\n")); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadScoreConfig(ScoreOverrides{})
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.BoostFor(SigWindowAcoustID); got != 0 {
		t.Fatalf("boost after config.yaml 0 = %v, want 0", got)
	}
	base := ComposeScore([]Signal{{Kind: SigEmbedMedium, Confidence: 0.70}}, nil, cfg, defaultPair)
	with := ComposeScore([]Signal{{Kind: SigEmbedMedium, Confidence: 0.70}, {Kind: SigWindowAcoustID, Confidence: 0.97}}, nil, cfg, defaultPair)
	if with.Score != base.Score {
		t.Fatalf("with boost 0 the window signal changed the score: %.3f vs %.3f", with.Score, base.Score)
	}
}
