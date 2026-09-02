// file: internal/server/registry_wire_dedup_score_config_test.go
// version: 2.0.0
// guid: a9b707f0-a8bf-408c-80ca-6476753a15ba
// last-edited: 2026-09-02

package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/ai"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/internal/dedup/unified"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/serviceregistry"
)

// dedupWiring is what buildDedupEngineFromContainer hands back: the engine the
// production ServiceDef built, the config UpdateService the SAME container
// built (the registry's own configupdate def, resolved through the dedup
// def's Needs edge — not a test-constructed one), and the Pebble store both
// persist to, so a test can read the config blob back.
type dedupWiring struct {
	engine *dedup.Engine
	update *config.UpdateService
	pebble *database.PebbleStore
}

// buildDedupEngineFromContainer runs the production "dedup" ServiceDef (the
// Build closure in registry_wire.go) against a container whose every
// dependency is overridden with a test double, and returns the built engine
// or the Build error. This exercises the EXACT wiring line the bug lived on
// — not a re-implementation of it in the test.
//
// KeyStore is a real Pebble store (not MockStore) so that the configupdate
// service the container resolves transitively persists a real config_blob the
// tests can assert on, and so the engine's rescore writes to real rows.
func buildDedupEngineFromContainer(t *testing.T, cfg *config.Config) (dedupWiring, error) {
	t.Helper()
	pebble, err := database.NewPebbleStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	t.Cleanup(func() { _ = pebble.Close() })

	// "dedupplugin" + "opregistry" are Included because the dedup-score sink
	// is installed by the PLUGIN's PostInit (internal/plugins/dedup/register.go),
	// not by the dedup ServiceDef — it needs the ops registry to queue the
	// dedup.rescore op. Building only the engine would test a wiring that no
	// longer exists.
	c := serviceregistry.NewContainer().
		Include(serviceregistry.KeyDedup, "dedupplugin", "opregistry").
		Override(serviceregistry.KeyConfig, cfg).
		Override(serviceregistry.KeyStore, pebble).
		Override(serviceregistry.KeyEmbeddingStore, database.NewEmbeddingStore(pebble.DB())).
		Override("embedclient", ai.NewEmbeddingClientWithOptions("test-key", "test-model", "")).
		Override("llmparser", (*ai.OpenAIParser)(nil)).
		Override(serviceregistry.KeyMerge, merge.NewService(pebble))
	if err := c.Build(context.Background()); err != nil {
		return dedupWiring{}, err
	}
	if err := c.PostInit(context.Background()); err != nil {
		return dedupWiring{}, err
	}
	eng, ok := serviceregistry.TryGet[*dedup.Engine](c, serviceregistry.KeyDedup)
	if !ok || eng == nil {
		t.Fatalf("dedup engine not built (ok=%v) — the embedding-mode gate short-circuited the fixture", ok)
	}
	upd, ok := serviceregistry.TryGet[*config.UpdateService](c, serviceregistry.KeyConfigUpdate)
	if !ok || upd == nil {
		t.Fatalf("configupdate service not built (ok=%v) — the dedup def must pull it in via Needs", ok)
	}
	return dedupWiring{engine: eng, update: upd, pebble: pebble}, nil
}

// restoreGlobalConfig snapshots the process-wide config.AppConfig and puts it
// back when the test ends; UpdateConfig mutates the global, not the container's
// *Config, so tests that PUT must clean up after themselves.
func restoreGlobalConfig(t *testing.T) {
	t.Helper()
	orig := config.Snapshot()
	t.Cleanup(func() { config.Mutate(func(c *config.Config) { *c = orig }) })
}

// persistedSignals reads dedup.signals back out of the config_blob the
// UpdateService wrote to Pebble. ok=false when no blob has been written.
func persistedSignals(t *testing.T, pebble *database.PebbleStore) (config.DedupSignalConfig, bool) {
	t.Helper()
	setting, err := pebble.GetSetting("config_blob")
	if errors.Is(err, database.ErrSettingNotFound) {
		return config.DedupSignalConfig{}, false
	}
	if err != nil {
		t.Fatalf("GetSetting(config_blob): %v", err)
	}
	if setting == nil || setting.Value == "" {
		return config.DedupSignalConfig{}, false
	}
	var blob struct {
		Dedup struct {
			Signals config.DedupSignalConfig `json:"signals"`
		} `json:"dedup"`
	}
	if err := json.Unmarshal([]byte(setting.Value), &blob); err != nil {
		t.Fatalf("config_blob is not JSON: %v", err)
	}
	return blob.Dedup.Signals, true
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
// config.DedupSignalConfig.ScoreConfig derives from those same settings.
//
// Mutation check: delete the ScoreConfig() / scoreCfg hand-off in
// registry_wire.go's Build (pass unified.DefaultScoreConfig() instead) and
// the engine reads 97 here.
func TestRegistryWire_DedupEngineScoresOnPersistedSignals(t *testing.T) {
	sigs := config.DedupSignalConfig{
		BandCertainMin: 99.9,
		BandHighMin:    93.0,
		BandMediumMin:  80.0,
		BandReviewMin:  65.0,
		Confidence: map[string]config.DedupKindConfidence{
			string(unified.SigEmbedMedium): {MinConfidence: 0.72, MaxConfidence: 0.83},
		},
	}
	w, err := buildDedupEngineFromContainer(t, dedupWiringConfig(sigs))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	want, err := sigs.ScoreConfig()
	if err != nil {
		t.Fatalf("ScoreConfig: %v", err)
	}
	got := w.engine.ScoreConfig()
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
	w, err := buildDedupEngineFromContainer(t, dedupWiringConfig(sigs))
	if err == nil {
		t.Fatalf("expected Build to fail on an invalid dedup.signals ladder; got engine with certain=%.2f",
			w.engine.ScoreConfig().BandCertainMin)
	}
	for _, want := range []string{"dedup.signals", "band_certain_min", "PUT /api/v1/config", "persisted"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("startup error must contain %q so the operator knows WHAT is wrong and WHERE it lives (the persisted blob, not just config.yaml); got: %v", want, err)
		}
	}
}

// TestRegistryWire_PutConfigReachesLiveEngine (review-round H2): a
// PUT /api/v1/config body carrying a new dedup.signals ladder must reach the
// running engine through the sink registry_wire.go registers on the
// container's UpdateService — the same service the HTTP handler calls. Before
// this round the PUT persisted the blob and the engine kept scoring on the
// ladder it was built with until a restart.
//
// Mutation check: delete the SetDedupScoreConfigSink call in registry_wire.go
// and the engine still reads the 97/90/75/60 it was built with here.
func TestRegistryWire_PutConfigReachesLiveEngine(t *testing.T) {
	restoreGlobalConfig(t)
	w, err := buildDedupEngineFromContainer(t, dedupWiringConfig(config.DedupSignalConfig{}))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if got := w.engine.ScoreConfig().BandCertainMin; got != 97 {
		t.Fatalf("fixture: engine should start on the default 97, got %.2f", got)
	}

	status, resp := w.update.UpdateConfig(context.Background(), map[string]any{
		"dedup": map[string]any{
			"signals": map[string]any{
				"band_certain_min": 98.5,
				"band_high_min":    91.0,
				"band_medium_min":  76.0,
				"band_review_min":  61.0,
			},
		},
	})
	if status != http.StatusOK {
		t.Fatalf("UpdateConfig status = %d, resp = %v", status, resp)
	}

	live := w.engine.ScoreConfig()
	if live.BandCertainMin != 98.5 || live.BandHighMin != 91 || live.BandMediumMin != 76 || live.BandReviewMin != 61 {
		t.Fatalf("PUT /api/v1/config did not reach the live engine: engine ladder = %.2f/%.2f/%.2f/%.2f, want 98.5/91/76/61",
			live.BandCertainMin, live.BandHighMin, live.BandMediumMin, live.BandReviewMin)
	}
	persisted, ok := persistedSignals(t, w.pebble)
	if !ok {
		t.Fatalf("PUT succeeded but no config_blob was persisted")
	}
	if persisted.BandCertainMin != 98.5 {
		t.Fatalf("persisted band_certain_min = %.2f, want 98.5", persisted.BandCertainMin)
	}

	// D4: the stored-candidate re-band is QUEUED as dedup.rescore, not run
	// inside the PUT. The response carries the op id, and a matching queued
	// row exists in the ops store.
	opID, _ := resp["dedup_rescore_op_id"].(string)
	if opID == "" {
		t.Fatalf("PUT response carries no dedup_rescore_op_id; the ladder changed but no re-band was queued: %v", resp)
	}
	ops, err := w.pebble.ListActiveOperationsV2()
	if err != nil {
		t.Fatalf("ListActiveOperationsV2: %v", err)
	}
	found := false
	for _, op := range ops {
		if op.ID == opID {
			found = true
			if op.DefID != "dedup.rescore" {
				t.Errorf("queued op %s has def_id %q, want dedup.rescore", opID, op.DefID)
			}
		}
	}
	if !found {
		t.Errorf("no queued operation with id %s; active ops = %+v", opID, ops)
	}
}

// TestRegistryWire_PutInvalidLadderChangesNothing (review-round H1a): an
// invalid ladder on PUT is rejected with 400 BEFORE anything is persisted, and
// neither the live engine nor the in-memory config moves. This is what makes
// the fail-closed startup above safe: a ladder that would refuse to boot can
// no longer be saved through the API in the first place.
func TestRegistryWire_PutInvalidLadderChangesNothing(t *testing.T) {
	restoreGlobalConfig(t)
	w, err := buildDedupEngineFromContainer(t, dedupWiringConfig(config.DedupSignalConfig{}))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	before := config.Snapshot().Dedup.Signals

	status, resp := w.update.UpdateConfig(context.Background(), map[string]any{
		"dedup": map[string]any{
			"signals": map[string]any{
				"band_certain_min": 1.0, // below every other band
			},
		},
	})
	if status != http.StatusBadRequest {
		t.Fatalf("UpdateConfig status = %d, want 400; resp = %v", status, resp)
	}
	msg, _ := resp["error"].(string)
	if !strings.Contains(msg, "band_certain_min") || !strings.Contains(msg, "nothing was saved") {
		t.Errorf("400 body must name the offending field and say nothing was saved, got: %q", msg)
	}
	if got := w.engine.ScoreConfig(); !reflect.DeepEqual(got, unified.DefaultScoreConfig()) {
		t.Errorf("rejected PUT must not touch the live engine; engine ladder now %+v", got)
	}
	if after := config.Snapshot().Dedup.Signals; !reflect.DeepEqual(before, after) {
		t.Errorf("rejected PUT mutated in-memory config: before=%+v after=%+v", before, after)
	}
	if _, ok := persistedSignals(t, w.pebble); ok {
		t.Errorf("rejected PUT must persist nothing, but a config_blob was written")
	}
}
