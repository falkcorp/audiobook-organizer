// file: internal/dedup/score_config_test.go
// version: 1.1.0
// guid: 5e38349e-2327-4736-a35a-5e24d7a580ae
// last-edited: 2026-09-02

package dedup

import (
	"context"
	"reflect"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/dedup/unified"
)

// nonDefaultSignals is a persisted dedup.signals block whose every band
// differs from unified.DefaultScoreConfig()'s 97/90/75/60, so any test that
// compares "what the engine holds" against "what was configured" can tell the
// two apart. BandCertainMin=99.9 is the operator move the bug made inert: "stop
// auto-resolve from merging anything short of near-certainty".
var nonDefaultSignals = config.DedupSignalConfig{
	BandCertainMin: 99.9,
	BandHighMin:    93.0,
	BandMediumMin:  80.0,
	BandReviewMin:  65.0,
	Confidence: map[string]config.DedupKindConfidence{
		string(unified.SigEmbedMedium): {MinConfidence: 0.72, MaxConfidence: 0.83},
	},
}

// TestNewEngine_PersistedSettingsReachTheEngine is the inverse of the probe
// that proved the bug (TestPROBE_E4_ConfiguredBandThresholdsAreInert): build
// the engine from the persisted settings and the engine's EFFECTIVE score
// config — the one CheckBook/rescore/ScorePairsForBook read via ScoreConfig()
// — equals the loaded one, not the defaults. (The conversion itself is tested
// in internal/config/dedup_score_config_test.go.)
//
// Mutation check: if NewEngine ignored its scoreCfg argument (or ScoreConfig()
// fell back to DefaultScoreConfig as getScoreConfig used to), BandCertainMin
// would read 97 and this fails.
func TestNewEngine_PersistedSettingsReachTheEngine(t *testing.T) {
	loaded, err := nonDefaultSignals.ScoreConfig()
	if err != nil {
		t.Fatalf("ScoreConfig: %v", err)
	}

	eng, err := NewEngine(nil, nil, nil, nil, nil, loaded)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	got := eng.ScoreConfig()
	if !reflect.DeepEqual(got, loaded) {
		t.Fatalf("engine effective score config != loaded\n got:  %+v\n want: %+v", got, loaded)
	}
	if def := unified.DefaultScoreConfig(); got.BandCertainMin == def.BandCertainMin {
		t.Fatalf("engine is scoring on the default band_certain_min (%.2f) — configured 99.9 is inert", got.BandCertainMin)
	}
}

// TestNewEngine_InvalidScoreConfigFailsConstruction: the engine refuses an
// invalid ScoreConfig at construction instead of building on it (or on
// defaults). registry_wire.go turns this into a startup failure.
func TestNewEngine_InvalidScoreConfigFailsConstruction(t *testing.T) {
	bad := unified.DefaultScoreConfig()
	bad.BandCertainMin = 10.0 // below HIGH/MEDIUM/REVIEW — non-monotonic ladder
	eng, err := NewEngine(nil, nil, nil, nil, nil, bad)
	if err == nil {
		t.Fatal("expected NewEngine to reject an invalid score config, got nil error")
	}
	if eng != nil {
		t.Fatalf("expected nil engine on invalid config, got %+v", eng.ScoreConfig())
	}
}

// TestSetScoreConfig_ReplacesLiveConfig: SetScoreConfig is the runtime half of
// the single channel (the calibrate op's apply path). A valid config replaces
// what every scoring site reads; an invalid one is rejected AND leaves the
// previous config in place.
func TestSetScoreConfig_ReplacesLiveConfig(t *testing.T) {
	eng, err := NewEngine(nil, nil, nil, nil, nil, unified.DefaultScoreConfig())
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	next, err := nonDefaultSignals.ScoreConfig()
	if err != nil {
		t.Fatalf("ScoreConfig: %v", err)
	}
	if err := eng.SetScoreConfig(next); err != nil {
		t.Fatalf("SetScoreConfig(valid): %v", err)
	}
	if got := eng.ScoreConfig(); !reflect.DeepEqual(got, next) {
		t.Fatalf("SetScoreConfig did not replace the live config\n got:  %+v\n want: %+v", got, next)
	}

	bad := next.Clone()
	bad.BandHighMin = bad.BandCertainMin + 1 // HIGH above CERTAIN
	if err := eng.SetScoreConfig(bad); err == nil {
		t.Fatal("expected SetScoreConfig to reject an invalid config, got nil")
	}
	if got := eng.ScoreConfig(); !reflect.DeepEqual(got, next) {
		t.Fatalf("rejected SetScoreConfig must leave the previous config live\n got:  %+v\n want: %+v", got, next)
	}
}

// TestScoreConfig_ReturnsACopy: mutating what ScoreConfig() hands out (the
// calibrate op sweeps variants off it) must not change live scoring.
func TestScoreConfig_ReturnsACopy(t *testing.T) {
	eng, err := NewEngine(nil, nil, nil, nil, nil, unified.DefaultScoreConfig())
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	got := eng.ScoreConfig()
	got.BandCertainMin = 1.0
	kc := got.Signals[string(unified.SigEmbedHigh)]
	kc.MinConfidence = 0.01
	got.Signals[string(unified.SigEmbedHigh)] = kc

	live := eng.ScoreConfig()
	if live.BandCertainMin != 97.0 {
		t.Fatalf("ScoreConfig() aliased the live struct: certain=%.2f", live.BandCertainMin)
	}
	if live.Signals[string(unified.SigEmbedHigh)].MinConfidence != 0.88 {
		t.Fatalf("ScoreConfig() aliased the live Signals map: %+v", live.Signals[string(unified.SigEmbedHigh)])
	}
}

// TestScorePairsForBook_BandFollowsConfiguredThresholds is the end-to-end
// proof that a configured band ladder drives real scoring, not just a getter:
// the SAME pair, over the SAME store, scored by two engines that differ only
// in ScoreConfig, lands in two different bands. Under defaults the fuzzy-title
// pair composes to some score s with a band; under a ladder built around s
// (CERTAIN=s+0.5 > HIGH=s-0.5 > MEDIUM=s-1 > REVIEW=s-2) the identical
// signals band as HIGH.
//
// Mutation check: were the engine still reading DefaultScoreConfig()
// regardless of what it was built with, both engines would return the same
// band and the inequality below fails.
func TestScorePairsForBook_BandFollowsConfiguredThresholds(t *testing.T) {
	eng, store := newRescoreTestEngine(t)

	author, err := store.CreateAuthor("Melville")
	if err != nil {
		t.Fatalf("CreateAuthor: %v", err)
	}
	// Near-identical titles + same author + same duration → SigMetaFuzzy plus
	// a duration boost; a mid-ladder score (< 100) so it can be re-banded.
	aID := mkBookWithHash(t, store, "Moby Dick Unabridged", "aaaa000000000001", &author.ID, 3600)
	bID := mkBookWithHash(t, store, "Moby Dick Unabridgd", "bbbb000000000002", &author.ID, 3600)
	lo, hi := aID, bID
	if lo > hi {
		lo, hi = hi, lo
	}

	scoreOne := func(e *Engine) (float64, string) {
		res, err := e.ScorePairsForBook(context.Background(), lo, []RescorePairInput{{OtherID: hi}})
		if err != nil {
			t.Fatalf("ScorePairsForBook: %v", err)
		}
		if len(res) != 1 || res[0].Score == nil {
			t.Fatalf("want 1 scored result, got %+v", res)
		}
		return res[0].Score.Score, res[0].Score.Band
	}

	defScore, defBand := scoreOne(eng)
	if defScore >= 100 || defScore < 2 {
		t.Fatalf("fixture: need a mid-ladder score to re-band, got %.2f", defScore)
	}
	if defBand == unified.BandHigh {
		t.Fatalf("fixture: default band is already HIGH (score %.2f); cannot observe a move into HIGH", defScore)
	}

	shifted := unified.DefaultScoreConfig()
	shifted.BandCertainMin = defScore + 0.5
	shifted.BandHighMin = defScore - 0.5
	shifted.BandMediumMin = defScore - 1
	shifted.BandReviewMin = defScore - 2
	es := eng.embedStore
	eng2 := newRescoreTestEngineWithConfig(t, store, es, shifted)

	newScore, newBand := scoreOne(eng2)
	if newScore != defScore {
		t.Fatalf("band thresholds must not change the composed score: %.4f vs %.4f", newScore, defScore)
	}
	if newBand != unified.BandHigh {
		t.Fatalf("configured ladder ignored: score %.2f banded %q under defaults and %q under a ladder that puts it in HIGH", defScore, defBand, newBand)
	}
}
