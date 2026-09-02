// file: internal/server/registry_wire_dedup_score_config_test.go
// version: 1.0.0
// guid: a9b707f0-a8bf-408c-80ca-6476753a15ba
// last-edited: 2026-09-02

package server

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/ai"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/serviceregistry"
)

// buildDedupEngineFromContainer runs the production "dedup" ServiceDef (the
// Build closure in registry_wire.go) against a container whose every
// dependency is overridden with a test double, and returns the built engine
// or the Build error. This exercises the EXACT wiring line the bug lived on
// — not a re-implementation of it in the test.
func buildDedupEngineFromContainer(t *testing.T, cfg *config.Config) (*dedup.Engine, error) {
	t.Helper()
	store := &database.MockStore{}
	pebble, err := database.NewPebbleStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	t.Cleanup(func() { _ = pebble.Close() })

	c := serviceregistry.NewContainer().
		Include(serviceregistry.KeyDedup).
		Override(serviceregistry.KeyConfig, cfg).
		Override(serviceregistry.KeyStore, store).
		Override(serviceregistry.KeyEmbeddingStore, database.NewEmbeddingStore(pebble.DB())).
		Override("embedclient", ai.NewEmbeddingClientWithOptions("test-key", "test-model", "")).
		Override("llmparser", (*ai.OpenAIParser)(nil)).
		Override(serviceregistry.KeyMerge, merge.NewService(store))
	if err := c.Build(context.Background()); err != nil {
		return nil, err
	}
	eng, ok := serviceregistry.TryGet[*dedup.Engine](c, serviceregistry.KeyDedup)
	if !ok || eng == nil {
		t.Fatalf("dedup engine not built (ok=%v) — the embedding-mode gate short-circuited the fixture", ok)
	}
	return eng, nil
}

// dedupWiringConfig is a Config that gets past the dedup ServiceDef's
// embedding-mode gate and carries the given persisted dedup.signals block.
func dedupWiringConfig(sigs config.DedupSignalConfig) *config.Config {
	cfg := &config.Config{}
	cfg.AIBackend.EmbeddingMode = config.AIBackendModeOpenAI
	cfg.Dedup.Signals = sigs
	return cfg
}

// TestRegistryWire_DedupEngineScoresOnPersistedSignals is the inverse of the
// probe that proved the bug (TestPROBE_E4_ConfiguredBandThresholdsAreInert):
// with dedup.signals.band_certain_min persisted at 99.9 — not the 97 default —
// the engine the production ServiceDef builds has 99.9 as its effective
// BandCertainMin, and its whole effective config equals what
// dedup.LoadScoreConfig derives from those same settings.
//
// Mutation check: delete the dedup.LoadScoreConfig / scoreCfg hand-off in
// registry_wire.go's Build (pass unified.DefaultScoreConfig() instead) and
// the engine reads 97 here.
func TestRegistryWire_DedupEngineScoresOnPersistedSignals(t *testing.T) {
	sigs := config.DedupSignalConfig{
		BandCertainMin: 99.9,
		BandHighMin:    93.0,
		BandMediumMin:  80.0,
		BandReviewMin:  65.0,
		Confidence: map[string]config.DedupKindConfidence{
			"embedding_medium": {MinConfidence: 0.72, MaxConfidence: 0.83},
		},
	}
	eng, err := buildDedupEngineFromContainer(t, dedupWiringConfig(sigs))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	want, err := dedup.LoadScoreConfig(sigs)
	if err != nil {
		t.Fatalf("LoadScoreConfig: %v", err)
	}
	got := eng.ScoreConfig()
	if got.BandCertainMin != 99.9 {
		t.Fatalf("engine band_certain_min = %.2f; the persisted 99.9 did not reach the engine", got.BandCertainMin)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("engine effective score config != loaded from persisted settings\n got:  %+v\n want: %+v", got, want)
	}
}

// TestRegistryWire_DedupEngineRefusesInvalidSignals: an invalid persisted
// band ladder fails the ServiceDef Build — and so server startup — with an
// error naming dedup.signals, instead of the engine quietly coming up on the
// compiled-in defaults.
func TestRegistryWire_DedupEngineRefusesInvalidSignals(t *testing.T) {
	sigs := config.DedupSignalConfig{BandCertainMin: 50.0} // below the 90 HIGH floor
	eng, err := buildDedupEngineFromContainer(t, dedupWiringConfig(sigs))
	if err == nil {
		t.Fatalf("expected Build to fail on an invalid dedup.signals ladder; got engine with certain=%.2f",
			eng.ScoreConfig().BandCertainMin)
	}
	if !strings.Contains(err.Error(), "dedup.signals") {
		t.Fatalf("error should name dedup.signals so the operator knows what to fix, got: %v", err)
	}
}
