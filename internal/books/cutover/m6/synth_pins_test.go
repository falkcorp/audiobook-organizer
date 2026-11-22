// file: internal/books/cutover/m6/synth_pins_test.go
// version: 0.1.0
// guid: c41e6b27-9d03-4f58-b2a1-6e7f30d5a984
// last-edited: 2026-11-22

package m6

import (
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/aidispatch"
)

// moduleRoot is the repository root above the test's working directory. It is
// found with os.Getwd: CI builds with -trimpath, which empties runtime.Caller.
func moduleRoot(t testing.TB) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			if _, err := os.Stat(filepath.Join(dir, "internal", "aidispatch")); err == nil {
				return dir
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no module root above the test's directory")
		}
		dir = parent
	}
}

func readRepoFile(t testing.TB, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(moduleRoot(t), filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("a HEAD source file the pin reads has moved: %v", err)
	}
	return string(b)
}

// legacyKeySite is one HEAD call site that reads the scalar OpenAI key: where it
// is, the lines that show its gate and the parser call it makes, and the
// capability that call is (aidispatch's call-site table: the parser method names
// the capability by its description).
type legacyKeySite struct {
	file       string
	gate       []string // the key and enable_ai_parsing gate
	call       []string // the parser calls the site makes
	capability string
}

// legacyKeySites are the sites legacyKeyCapabilities is derived from. The file
// the call is made in is not always the one that holds the gate, so a site lists
// both.
var legacyKeySites = []legacyKeySite{
	{file: "internal/server/ai_ops.go",
		gate:       []string{"newAIParser(config.AppConfig.OpenAIAPIKey, config.AppConfig.EnableAIParsing)", "handlers.AIReviewGroupsMode"},
		call:       []string{"handlers.AIReviewGroupsMode"},
		capability: "llm.author_review"},
	{file: "internal/server/handlers/ai.go",
		gate:       []string{"func AIReviewGroupsMode"},
		call:       []string{"parser.ReviewAuthorDuplicates(ctx, inputs)"},
		capability: "llm.author_review"},
	{file: "internal/server/ai_ops.go",
		gate:       []string{"newAIParser(config.AppConfig.OpenAIAPIKey, config.AppConfig.EnableAIParsing)", "handlers.AIReviewFullMode"},
		call:       []string{"handlers.AIReviewFullMode"},
		capability: "llm.author_discovery"},
	{file: "internal/server/handlers/ai.go",
		gate:       []string{"func AIReviewFullMode"},
		call:       []string{"parser.DiscoverAuthorDuplicates(ctx, inputs)"},
		capability: "llm.author_discovery"},
	{file: "internal/plugins/maintenance/dedup_ops.go",
		gate:       []string{"if !p.deps.HasAIParsing()", "ai.NewOpenAIParser(&cfg, cfg.OpenAIAPIKey, cfg.EnableAIParsing)", "if !parser.IsEnabled()"},
		call:       []string{"SubmitAuthorDedupJob("},
		capability: "llm.author_dedup_batch"},
	{file: "internal/server/diagnostics_ai_ops.go",
		gate:       []string{"config.AppConfig.OpenAIAPIKey == \"\" || !config.AppConfig.EnableAIParsing"},
		call:       []string{"parser.UploadBatchFile(", "parser.CreateBatchWithMetadata(ctx, fileID, \"diagnostics\", nil)"},
		capability: "llm.diagnostics"},
}

// TestSynthesisedOpenAI_TicksMatchHEADsLegacySites pins what the ai/openai row M6
// synthesises is ticked with to HEAD's source, so it cannot be left tickless (the
// row health-probed with the key and served nothing: r2 m-a) or ticked past what
// HEAD served. The tick set is derived from the call sites, then held to:
//   - the sites' gate and calls, read from HEAD's files, so a moved or regated
//     site fails here and is looked at;
//   - the pool: no tick is a capability the pool routes, because a tick would let
//     the router send OpenAI the scan, embedding and vision calls HEAD sent only
//     where a row was ticked (the routed set is read from internal/ai);
//   - aidispatch: every tick is registered, and the synthesised row carries every
//     feature the capability requires;
//   - what the row is written with.
func TestSynthesisedOpenAI_TicksMatchHEADsLegacySites(t *testing.T) {
	derived := map[string]bool{}
	for _, s := range legacyKeySites {
		src := readRepoFile(t, s.file)
		for _, want := range append(slices.Clone(s.gate), s.call...) {
			if !strings.Contains(src, want) {
				t.Errorf("%s no longer holds %q: the legacy site for %s has moved or changed its gate", s.file, want, s.capability)
			}
		}
		derived[s.capability] = true
	}
	if got := slices.Sorted(maps.Keys(derived)); !slices.Equal(got, slices.Sorted(slices.Values(legacyKeyCapabilities))) {
		t.Errorf("legacyKeyCapabilities %v, the sites derive %v", legacyKeyCapabilities, got)
	}

	// What the pool routes, from the source: every aidispatch capability a
	// non-test file of internal/ai names.
	routedNames := map[string]bool{}
	re := regexp.MustCompile(`aidispatch\.((?:LLM|Embed|Transcribe)[A-Za-z]*)\b`)
	entries, err := os.ReadDir(filepath.Join(moduleRoot(t), "internal", "ai"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		for _, m := range re.FindAllStringSubmatch(readRepoFile(t, "internal/ai/"+e.Name()), -1) {
			routedNames[m[1]] = true
		}
	}
	idOf := map[string]string{
		"LLMFilenameParse": aidispatch.LLMFilenameParse.ID(), "EmbedText": aidispatch.EmbedText.ID(),
		"LLMCoverArtVision": aidispatch.LLMCoverArtVision.ID(),
	}
	routed := map[string]bool{}
	for name := range routedNames {
		id, ok := idOf[name]
		if !ok {
			t.Fatalf("internal/ai routes %s through the pool, which this pin has not been told about: decide whether the synthesised row may be ticked for it", name)
		}
		routed[id] = true
	}
	if len(routed) != len(idOf) {
		t.Errorf("the pool routes %v, the pin expects %v", routed, idOf)
	}

	ticks, dropped := mapCaps(legacyKeyCapabilities)
	if len(dropped) != 0 || len(ticks) != len(legacyKeyCapabilities) {
		t.Fatalf("legacyKeyCapabilities %v maps to %v (dropped %v)", legacyKeyCapabilities, ticks, dropped)
	}
	p, ok := (&Migration{}).openAIInstance("", secretSet{}, true)
	if !ok {
		t.Fatal("the synthesised row was not proven")
	}
	rowCaps, _ := p.inst.Config.Settings["capabilities"].([]string)
	if !slices.Equal(rowCaps, ticks) {
		t.Errorf("the row is ticked %v, want %v", rowCaps, ticks)
	}
	feats, _ := p.inst.Config.Settings["features"].([]string)
	for _, id := range legacyKeyCapabilities {
		if routed[id] {
			t.Errorf("%s is routed through the pool: a tick on the synthesised row would widen what HEAD sent to OpenAI", id)
		}
		spec, ok := aidispatchSpec(id)
		if !ok {
			t.Errorf("%s is no aidispatch capability", id)
			continue
		}
		for _, f := range spec.RequiredFeatures {
			if !slices.Contains(feats, f) {
				t.Errorf("%s requires feature %q, which the synthesised row lacks (%v): it would be ticked and never selected", id, f, feats)
			}
		}
	}
	if err := endpointValidator(t).ValidateEndpoint(p.inst.Config.Settings, nil); err != nil {
		t.Errorf("core refuses the synthesised row: %v", err)
	}
}

func aidispatchSpec(id string) (aidispatch.Spec, bool) {
	for _, s := range aidispatch.Registry() {
		if s.Capability.ID() == id {
			return s, true
		}
	}
	return aidispatch.Spec{}, false
}
