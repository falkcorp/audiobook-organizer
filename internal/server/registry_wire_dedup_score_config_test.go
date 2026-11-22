// file: internal/server/registry_wire_dedup_score_config_test.go
// version: 2.1.2
// guid: a9b707f0-a8bf-408c-80ca-6476753a15ba
// last-edited: 2026-11-09

package server

import (
	"context"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/ai"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/internal/dedup/unified"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/serviceregistry"
)

// dedupWiring is what buildDedupEngineFromContainer hands back: the engine the
// production ServiceDef built. A changed ladder reaching the live engine is the
// dedup plugin's Facade UpdateHook (internal/plugins/dedup/score_hook_test.go).
type dedupWiring struct {
	engine *dedup.Engine
	// merge is the service handed to serviceDefs, which the dedup def must
	// close over (R3-05a: it is constructed, no longer registry-built).
	merge *merge.Service
}

// buildDedupEngineFromContainer runs the production "dedup" ServiceDef (the
// Build closure in registry_wire.go) against a container whose every
// dependency is overridden with a test double, and returns the built engine
// or the Build error. This exercises the EXACT wiring line the bug lived on
// — not a re-implementation of it in the test.
//
// KeyStore is a real Pebble store (not MockStore) so the engine's rescore
// writes to real rows.
func buildDedupEngineFromContainer(t *testing.T, cfg *config.Config) (dedupWiring, error) {
	t.Helper()
	pebble, err := database.NewPebbleStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	t.Cleanup(func() { _ = pebble.Close() })

	// "dedupplugin" is Included and "opregistry" Overridden because the
	// dedup-score sink is installed by the PLUGIN's PostInit
	// (internal/plugins/dedup/register.go), not by the dedup ServiceDef — it
	// needs the ops registry to queue the dedup.rescore op. Building only the
	// engine would test a wiring that no longer exists. The ops registry is
	// constructed explicitly, as NewServer does (R1-15 removed its init()
	// registration).
	opReg, err := opsregistry.NewService(pebble, nil, opsregistry.NewEventHub())
	if err != nil {
		t.Fatalf("ops registry: %v", err)
	}
	overrides := map[string]any{
		"opregistry":                      opReg,
		serviceregistry.KeyConfig:         cfg,
		serviceregistry.KeyStore:          pebble,
		serviceregistry.KeyEmbeddingStore: database.NewEmbeddingStore(pebble.DB()),
		"embedclient":                     ai.NewEmbeddingClientWithOptions("test-key", "test-model", ""),
		"llmparser":                       (*ai.OpenAIParser)(nil),
	}
	mergeSvc := merge.NewService(pebble)
	// The production list's defs reachable from the two roots, minus every
	// name overridden: what the old include-by-name container built.
	c := serviceregistry.NewContainerFrom(defsReachableFrom(serviceDefs(false, mergeSvc), overrides, serviceregistry.KeyDedup, "dedupplugin")...)
	for name, instance := range overrides {
		c.Override(name, instance)
	}
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
	return dedupWiring{engine: eng, merge: mergeSvc}, nil
}

// headOrderWithDatabase is the order the production container resolved its 45
// names, less configupdate (F3-07 deleted config.UpdateService, so 44 now), at R1-34's base (R1-15's head, fc21cd0ca) with DatabasePath set. It is
// Container.Names() unsorted, so it pins the build, start and stop order as well
// as the set. Captured by resolving that base's group-based container (the
// core, ai, scheduler and plugins groups, plus activity) with NewServer's seven
// overrides, from a checkout of the base and before any R1-34 edit, three times
// with identical output (the order is a topological sort with a lexically sorted
// ready queue, so it does not depend on the order of the list). An earlier
// capture at b91bfec17 gave the same 45 names, so R1-15's review fixes and R1-20
// changed neither services nor overrides.
//
// R3-05a removed eight names (audiobook, batch, importpath, merge, organize,
// quarantine, scan, work: constructed by domainServices) and dedup no longer
// Needs merge, so dedup, acoustidplugin and dedupplugin resolve ahead of
// filesystem instead of behind it. filesystem declares no PostInit, Start or
// Stop, so what they now cross has no lifecycle method to reorder.
var headOrderWithDatabase = []string{
	"appversion",
	"config",
	"chromemstore",
	"eventbus",
	"llmparser",
	"metadatallmscorer",
	"olservice",
	"ophub",
	"opregistry",
	"searchindex",
	"store",
	"aijobsstore",
	"aiscanstore",
	"dashboard",
	"delugeplugin",
	"embeddingstore",
	"embedclient",
	"dedup",
	"acoustidplugin",
	"dedupplugin",
	"filesystem",
	"metadatascorer",
	"metadatastate",
	"metafetch",
	"itunes",
	"itunesplugin",
	"metafetchplugin",
	"metricsstore",
	"pebble-activitystore",
	"activitystore",
	"activity",
	"activitywriter",
	"pipelinemanager",
	"system",
	"updater",
	"updatescheduler",
}

// headOrderWithoutDatabase is the same capture with DatabasePath unset: the
// four activity defs are absent. It is not headOrderWithDatabase minus those
// names, because the sort is over the remaining graph.
var headOrderWithoutDatabase = []string{
	"appversion",
	"config",
	"chromemstore",
	"eventbus",
	"llmparser",
	"metadatallmscorer",
	"olservice",
	"ophub",
	"opregistry",
	"searchindex",
	"store",
	"aijobsstore",
	"aiscanstore",
	"dashboard",
	"delugeplugin",
	"embeddingstore",
	"embedclient",
	"dedup",
	"acoustidplugin",
	"dedupplugin",
	"filesystem",
	"metadatascorer",
	"metadatastate",
	"metafetch",
	"itunes",
	"itunesplugin",
	"metafetchplugin",
	"metricsstore",
	"pipelinemanager",
	"system",
	"updater",
	"updatescheduler",
}

// TestServiceDefs_BuildsHEADSet pins what the production container builds:
// serviceDefs(true) resolves to HEAD's 45 names in HEAD's order (less R3-05a's
// eight constructed clusters, see headOrderWithDatabase) and
// serviceDefs(false) to HEAD's 41, each less configupdate (44 and 40), with no def listed twice. The container is
// made the way NewServer makes it, with the same seven overrides, so the names
// include them.
func TestServiceDefs_BuildsHEADSet(t *testing.T) {
	for _, tc := range []struct {
		name            string
		databasePathSet bool
		want            []string
	}{
		{"database path set", true, headOrderWithDatabase},
		{"database path unset", false, headOrderWithoutDatabase},
	} {
		t.Run(tc.name, func(t *testing.T) {
			defs := serviceDefs(tc.databasePathSet, (*merge.Service)(nil))
			seen := map[string]bool{}
			var dups []string
			for _, d := range defs {
				if seen[d.Name] {
					dups = append(dups, d.Name)
				}
				seen[d.Name] = true
			}
			// NewContainerFrom panics on a duplicate, which would take the whole
			// internal/server test binary down; fail this test instead.
			if len(dups) > 0 {
				t.Fatalf("serviceDefs lists %q more than once", dups)
			}
			c := serviceregistry.NewContainerFrom(defs...).
				Override("store", struct{}{}).
				Override("config", struct{}{}).
				Override("appversion", "test").
				Override(serviceregistry.KeyOpHub, struct{}{}).
				Override("opregistry", struct{}{}).
				Override(serviceregistry.KeyUpdater, struct{}{}).
				Override("updatescheduler", struct{}{})
			if err := c.Resolve(); err != nil {
				t.Fatalf("resolve: %v", err)
			}
			if got := c.Names(); !slices.Equal(got, tc.want) {
				t.Errorf("container resolves, in order,\n  %q\nwant HEAD's\n  %q", got, tc.want)
			}
			if seen["batchpoller"] {
				t.Error("batchpoller is in the list: HEAD never built it (it carries no Groups)")
			}
		})
	}
}

// defsReachableFrom returns the defs in all that the roots reach through
// Needs, leaving out any name in leaves (the caller overrides those). It lets a
// test build part of the production list in isolation: every def handed to
// NewContainerFrom is built, so the closure the include-by-name container used
// to compute has to be cut from the list first.
func defsReachableFrom(all []serviceregistry.ServiceDef, leaves map[string]any, roots ...string) []serviceregistry.ServiceDef {
	byName := make(map[string]serviceregistry.ServiceDef, len(all))
	for _, d := range all {
		byName[d.Name] = d
	}
	seen := map[string]bool{}
	var out []serviceregistry.ServiceDef
	var walk func(name string)
	walk = func(name string) {
		if _, isLeaf := leaves[name]; isLeaf || seen[name] {
			return
		}
		seen[name] = true
		d, ok := byName[name]
		if !ok {
			return
		}
		out = append(out, d)
		for _, dep := range d.Needs {
			walk(dep)
		}
	}
	for _, r := range roots {
		walk(r)
	}
	return out
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
