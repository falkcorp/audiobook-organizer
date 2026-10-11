// file: internal/ai/ai_metrics_models_test.go
// version: 1.1.0
// guid: 3c6e1b94-52a7-4d0f-8e13-9b4a7f2d6c58
// last-edited: 2026-10-11

package ai

import (
	"slices"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/aidispatch"
	"github.com/falkcorp/audiobook-organizer/internal/config"
)

// Model names here are synthetic stand-ins shaped like a routing-on install:
// a local chat/embedding host, a second local host that does vision, and the
// migrated OpenAI row. Hostnames are single-label on purpose: aidispatch
// classifies a TEST-NET address as cloud (only private ranges are local).
const (
	testLocalURL  = "http://llm-local:11434/v1"
	testLLM1URL   = "http://llm1:11434/v1"
	testOpenAIURL = "https://api.openai.com/v1"
)

func caps(c ...aidispatch.Capability) []string {
	out := make([]string, len(c))
	for i, x := range c {
		out[i] = x.ID()
	}
	return out
}

// probeRows is the endpoint pool of a routing-on install: the migrated local
// row (chat + embedding), a vision-capable second local host, and the
// migrated OpenAI row with per-capability models. The OpenAI row's priority is
// the migration's: 5 in openai-fallback-local (cloud first), 20 otherwise.
func probeRows(openAIPriority int) []config.AIEndpoint {
	return []config.AIEndpoint{
		{
			ID: config.AIEndpointIDLocalLLM, Protocol: string(aidispatch.ProtocolOpenAICompat), URL: testLocalURL,
			ChatModel: "qwen3:8b", EmbedModel: "bge-m3", Priority: 10, Enabled: true,
			Capabilities: caps(aidispatch.LLMFilenameParse, aidispatch.LLMAuthorReview, aidispatch.LLMAuthorDiscovery,
				aidispatch.LLMMetadataRerank, aidispatch.EmbedText),
		},
		{
			ID: "llm1", Protocol: string(aidispatch.ProtocolOpenAICompat), URL: testLLM1URL,
			ChatModel: "qwen3:14b", Priority: 11, Enabled: true,
			CapabilityModels: map[string]string{aidispatch.LLMCoverArtVision.ID(): "qwen2.5vl:7b"},
			Features:         []string{aidispatch.FeatureVision},
			Capabilities:     caps(aidispatch.LLMFilenameParse, aidispatch.LLMCoverArtVision),
		},
		{
			ID: config.AIEndpointIDOpenAI, Protocol: string(aidispatch.ProtocolOpenAICompat), URL: testOpenAIURL,
			ChatModel: "gpt-5-mini", Priority: openAIPriority, Enabled: true, AuthRef: "openai_api_key",
			CapabilityModels: map[string]string{
				aidispatch.LLMFilenameParse.ID():  "gpt-5-nano",
				aidispatch.LLMAudiobookParse.ID(): "gpt-5-nano",
				aidispatch.LLMCoverArtVision.ID(): "gpt-5",
				aidispatch.LLMDedupReview.ID():    "gpt-5-mini",
			},
			Features: []string{aidispatch.FeatureBatchAPI, aidispatch.FeatureVision},
			Capabilities: caps(aidispatch.LLMFilenameParse, aidispatch.LLMAudiobookParse, aidispatch.LLMCoverArtVision,
				aidispatch.LLMAuthorReview, aidispatch.LLMDedupReview),
		},
	}
}

// probeConfig is the realistic routing-on config: an OpenAI key, local
// embedding, routing on, per-feature OpenAI models and the pool above.
func probeConfig(llmMode string) *config.Config {
	prio := 20
	if llmMode == config.AIBackendModeOpenAIFallbackLocal {
		prio = 5
	}
	return &config.Config{
		OpenAIAPIKey: "sk-test",
		AIBackend: config.AIBackendConfig{
			LLMMode: llmMode, EmbeddingMode: config.AIBackendModeLocal,
			LocalBaseURL: testLocalURL, LocalLLMModel: "qwen3:8b", LocalEmbeddingModel: "bge-m3",
		},
		FilenameParseModel: "gpt-5-nano",
		CoverArtModel:      "gpt-5",
		Embedding:          config.EmbeddingConfig{Enabled: true},
		AIEndpointsRouting: true,
		AIEndpoints:        probeRows(prio),
	}
}

func labelsFor(cfg *config.Config, pairs ...modelPair) map[modelPair]string {
	s := newModelLabelSet()
	s.source.Store(&modelSource{generation: func() uint64 { return 1 }, configured: func() []modelPair { return configuredModelPairs(cfg) }})
	got := map[modelPair]string{}
	for _, p := range pairs {
		got[p] = s.admit(p.provider, p.model)
	}
	return got
}

// The routing-on, prod-shaped config: the models real calls send must come
// first, whatever else the settings hold. With llm_mode local, scanner
// filename parsing goes through the pool (local rows only), every embed call
// sends the embedding model, and cover text reads on the local vision row
// before the cloud one; the legacy OpenAI per-feature models follow. With
// openai-fallback-local the cloud row is the first filename-parse target.
func TestConfiguredModelPairs_RoutingOnProdShape(t *testing.T) {
	for _, c := range []struct {
		mode string
		want []modelPair
	}{
		{config.AIBackendModeLocal, []modelPair{
			{providerLocal, "bge-m3"},       // every embed call
			{providerLocal, "qwen3:8b"},     // routed filename parse, local-llm row
			{providerLocal, "qwen3:14b"},    // routed filename parse, llm1 row
			{providerLocal, "qwen2.5vl:7b"}, // cover text, local vision row
			{providerOpenAI, "gpt-5"},       // cover text cloud fallback, and the legacy cover-art model
			{providerOpenAI, "gpt-5-nano"},  // legacy filename_parse_model
			{providerOpenAI, "gpt-5-mini"},  // legacy metadata-review default
		}},
		{config.AIBackendModeOpenAIFallbackLocal, []modelPair{
			{providerLocal, "bge-m3"},
			{providerOpenAI, "gpt-5-nano"}, // the cloud row (priority 5) is tried first
			{providerLocal, "qwen3:8b"},
			{providerLocal, "qwen3:14b"},
			{providerLocal, "qwen2.5vl:7b"},
			{providerOpenAI, "gpt-5"},
			{providerOpenAI, "gpt-5-mini"},
		}},
	} {
		cfg := probeConfig(c.mode)
		got := configuredModelPairs(cfg)
		if !slices.Equal(got, c.want) {
			t.Errorf("llm_mode %s: configuredModelPairs =\n %v\nwant\n %v", c.mode, got, c.want)
			continue
		}
		// What the label set then names is the first maxModelLabels of them;
		// each of the four pairs the old order folded to "other" is named.
		labels := labelsFor(cfg, c.want...)
		for i, p := range c.want {
			want := modelOther
			if i < maxModelLabels {
				want = p.model
			}
			if labels[p] != want {
				t.Errorf("llm_mode %s: %v labelled %q, want %q", c.mode, p, labels[p], want)
			}
		}
		for _, p := range []modelPair{{providerLocal, "bge-m3"}, {providerLocal, "qwen3:14b"}, {providerLocal, "qwen2.5vl:7b"}} {
			if labels[p] != p.model {
				t.Errorf("llm_mode %s: %v is sent by real traffic but labelled %q", c.mode, p, labels[p])
			}
		}
	}
}

// With routing off the pool is not read: embedding first (it carries the most
// calls), then the legacy OpenAI models, then the local parsers' models. The
// cover-art model is never sent to a local parser, and the dedup review model
// is never an ai_* metric model at all (it goes through the Batch API).
func TestConfiguredModelPairs_RoutingOff(t *testing.T) {
	cfg := probeConfig(config.AIBackendModeLocal)
	cfg.AIEndpointsRouting = false
	cfg.Dedup.ReviewModel = "dedup-batch-model"
	want := []modelPair{
		{providerLocal, "bge-m3"},
		{providerOpenAI, "gpt-5-nano"}, {providerOpenAI, "gpt-5-mini"}, {providerOpenAI, "gpt-5"},
		// llm_mode local, routing off: the scanner's local parser (filename
		// model) and the llmparser (metadata review falls back to local_llm_model).
		{providerLocal, "gpt-5-nano"}, {providerLocal, "qwen3:8b"},
	}
	if got := configuredModelPairs(cfg); !slices.Equal(got, want) {
		t.Errorf("llm_mode local, routing off:\n got %v\nwant %v", got, want)
	}

	// openai-fallback-local, routing off: the scanner's local rung batch-parses
	// filenames and nothing else, and is skipped when local_llm_model is empty.
	cfg = probeConfig(config.AIBackendModeOpenAIFallbackLocal)
	cfg.AIEndpointsRouting = false
	want = []modelPair{
		{providerLocal, "bge-m3"},
		{providerOpenAI, "gpt-5-nano"}, {providerOpenAI, "gpt-5-mini"}, {providerOpenAI, "gpt-5"},
		{providerLocal, "gpt-5-nano"}, // the local rung sends filename_parse_model
	}
	if got := configuredModelPairs(cfg); !slices.Equal(got, want) {
		t.Errorf("llm_mode openai-fallback-local, routing off:\n got %v\nwant %v", got, want)
	}
	cfg.AIBackend.LocalLLMModel = ""
	if got := configuredModelPairs(cfg); slices.Contains(got, modelPair{providerLocal, "gpt-5-nano"}) {
		t.Errorf("a skipped local rung (no local_llm_model) named a local pair: %v", got)
	}
}

// dedup.review_model is submitted through ai.SubmitDedupReviewJob (aijobs'
// Batch API), a path that records no ai_* metric, so it never takes a slot.
func TestConfiguredModelPairs_DedupReviewModelIsNotNamed(t *testing.T) {
	cfg := probeConfig(config.AIBackendModeLocal)
	cfg.Dedup.ReviewModel = "only-the-dedup-engine-sends-this"
	for _, routing := range []bool{true, false} {
		cfg.AIEndpointsRouting = routing
		for _, p := range configuredModelPairs(cfg) {
			if p.model == cfg.Dedup.ReviewModel {
				t.Errorf("routing=%v: dedup.review_model named as %v", routing, p)
			}
		}
	}
}

// llm_mode local with no local_llm_model and no per-feature model: the local
// OpenAIParser's constructor gets an empty model, so OpenAIParser.fallbackModel
// returns defaultModel and gpt-5-mini really is sent to the local backend.
func TestConfiguredModelPairs_LocalModeWithNoModelSendsTheParserDefault(t *testing.T) {
	cfg := &config.Config{
		AIBackend: config.AIBackendConfig{LLMMode: config.AIBackendModeLocal, LocalBaseURL: testLocalURL},
	}
	want := []modelPair{{providerLocal, defaultModel}}
	if got := configuredModelPairs(cfg); !slices.Equal(got, want) {
		t.Errorf("configuredModelPairs = %v, want %v", got, want)
	}
}

// A row contributes a model only for a capability with a routed call site and
// only when the dispatcher would hand it that call. Each row below is
// ineligible in a different way; the two eligible rows are the only ones named.
func TestConfiguredModelPairs_PoolRowsNeedTicksAndDispatchEligibility(t *testing.T) {
	row := func(id, url string) config.AIEndpoint {
		return config.AIEndpoint{ID: id, Protocol: string(aidispatch.ProtocolOpenAICompat), URL: url, Enabled: true, Priority: 10}
	}
	embed := row("ok-embed", testLocalURL)
	embed.EmbedModel, embed.Capabilities = "bge-m3", caps(aidispatch.EmbedText)
	chat := row("ok-chat", testLLM1URL)
	chat.ChatModel, chat.Capabilities = "ok-chat-model", caps(aidispatch.LLMFilenameParse)

	noTicks := row("no-ticks", testLocalURL)
	noTicks.ChatModel, noTicks.EmbedModel = "no-ticks-chat", "no-ticks-embed"
	unticked := row("unticked-override", testLocalURL)
	unticked.Capabilities = caps(aidispatch.LLMAuthorReview)
	unticked.CapabilityModels = map[string]string{aidispatch.LLMFilenameParse.ID(): "unticked-override-model"}
	unrouted := row("unrouted-capability", testLocalURL)
	unrouted.ChatModel, unrouted.Capabilities = "unrouted-capability-model", caps(aidispatch.LLMAuthorReview, aidispatch.LLMAudiobookParse)
	noVision := row("vision-ticked-without-feature", testLocalURL)
	noVision.Capabilities = caps(aidispatch.LLMCoverArtVision)
	noVision.CapabilityModels = map[string]string{aidispatch.LLMCoverArtVision.ID(): "no-vision-feature-model"}
	gpu := row("needs-gpu", testLocalURL)
	gpu.ChatModel, gpu.Capabilities, gpu.RequireGPU = "needs-gpu-model", caps(aidispatch.LLMFilenameParse), true
	otherEmbed := row("other-embed-model", testLocalURL)
	otherEmbed.EmbedModel, otherEmbed.Capabilities = "not-the-pinned-embed-model", caps(aidispatch.EmbedText)
	disabled := row("disabled", testLocalURL)
	disabled.ChatModel, disabled.Capabilities, disabled.Enabled = "disabled-model", caps(aidispatch.LLMFilenameParse), false
	whisper := row("wrong-protocol", testLocalURL)
	whisper.Protocol = string(aidispatch.ProtocolWhisperServer)
	whisper.ChatModel, whisper.Capabilities = "whisper-chat-model", caps(aidispatch.LLMFilenameParse)
	noModel := row("no-chat-model", testLocalURL)
	noModel.Capabilities = caps(aidispatch.LLMFilenameParse)
	cloud := row("cloud", "https://api.example.com/v1")
	cloud.ChatModel, cloud.Capabilities = "cloud-model", caps(aidispatch.LLMFilenameParse)
	noSecret := row("no-secret", "https://api.openai.com/v1")
	noSecret.ChatModel, noSecret.Capabilities, noSecret.AuthRef = "no-secret-model", caps(aidispatch.LLMFilenameParse), "openai_api_key"

	cfg := &config.Config{
		AIBackend:          config.AIBackendConfig{LLMMode: config.AIBackendModeLocal, EmbeddingMode: config.AIBackendModeLocal, LocalEmbeddingModel: "bge-m3"},
		AIEndpointsRouting: true,
		AIEndpoints: []config.AIEndpoint{noTicks, unticked, unrouted, noVision, gpu, otherEmbed, disabled, whisper, noModel,
			cloud, noSecret, embed, chat},
	}
	want := []modelPair{{providerLocal, "bge-m3"}, {providerLocal, "ok-chat-model"}}
	if got := configuredModelPairs(cfg); !slices.Equal(got, want) {
		t.Errorf("llm_mode local:\n got %v\nwant %v", got, want)
	}

	// openai-fallback-local may reach a cloud row. Rows of equal priority keep
	// stored order. "cloud" is not an OpenAI host, so its provider label is
	// local; "no-secret" names auth_ref openai_api_key, which cannot be called
	// until the key is set.
	cfg.AIBackend.LLMMode = config.AIBackendModeOpenAIFallbackLocal
	want = []modelPair{{providerLocal, "bge-m3"}, {providerLocal, "cloud-model"}, {providerLocal, "ok-chat-model"}}
	if got := configuredModelPairs(cfg); !slices.Equal(got, want) {
		t.Errorf("llm_mode openai-fallback-local, no key:\n got %v\nwant %v", got, want)
	}
	cfg.OpenAIAPIKey = "sk-test"
	want = []modelPair{{providerLocal, "bge-m3"}, {providerLocal, "cloud-model"}, {providerOpenAI, "no-secret-model"},
		{providerLocal, "ok-chat-model"},
		{providerOpenAI, defaultModel}, // the legacy OpenAI parser's default, once a key exists
	}
	if got := configuredModelPairs(cfg); !slices.Equal(got, want) {
		t.Errorf("llm_mode openai-fallback-local, key set:\n got %v\nwant %v", got, want)
	}
}

// Routing on with an embedding mode that is not local leaves embedding on the
// legacy client: WithPoolRouting is installed only for local mode.
func TestConfiguredModelPairs_EmbeddingOutsideLocalModeIsNotPooled(t *testing.T) {
	cfg := probeConfig(config.AIBackendModeLocal)
	cfg.AIBackend.EmbeddingMode = config.AIBackendModeOpenAI
	cfg.Embedding.Model = "text-embedding-3-large"
	got := configuredModelPairs(cfg)
	if len(got) == 0 || got[0] != (modelPair{providerOpenAI, "text-embedding-3-large"}) {
		t.Errorf("legacy OpenAI embedding model is not first: %v", got)
	}
	if slices.Contains(got, modelPair{providerLocal, "bge-m3"}) {
		t.Errorf("a pool embed row named although embedding is not local: %v", got)
	}
}

// Explicit openai mode is never routed by the scanner, so a pool row ticked
// for filename parsing only is not named, with routing on or off. The cover
// text rows are routed in every llm_mode and stay named.
func TestConfiguredModelPairs_OpenAIModeDoesNotNameFilenameOnlyPoolRows(t *testing.T) {
	cfg := probeConfig(config.AIBackendModeOpenAI)
	filenameOnly := config.AIEndpoint{
		ID: "filename-only", Protocol: string(aidispatch.ProtocolOpenAICompat), URL: testLocalURL,
		ChatModel: "filename-only-model", Priority: 1, Enabled: true,
		Capabilities: caps(aidispatch.LLMFilenameParse),
	}
	cfg.AIEndpoints = append([]config.AIEndpoint{filenameOnly}, cfg.AIEndpoints...)
	got := configuredModelPairs(cfg)
	for _, m := range []string{"filename-only-model", "qwen3:8b", "qwen3:14b"} {
		if slices.Contains(got, modelPair{providerLocal, m}) {
			t.Errorf("llm_mode openai named the filename-parse-only pool model %q: %v", m, got)
		}
	}
	if !slices.Contains(got, modelPair{providerLocal, "qwen2.5vl:7b"}) {
		t.Errorf("the cover-text vision row is routed in every llm_mode but was not named: %v", got)
	}
}

// The named set is a pure function of the settings: a pool row that the live
// dispatcher health has benched is still named, or a transient outage at the
// first use would decide what the process names for its whole life.
func TestConfiguredModelPairs_IgnoresLiveEndpointHealth(t *testing.T) {
	cfg := probeConfig(config.AIBackendModeLocal)
	want := configuredModelPairs(cfg)
	h := aidispatch.DefaultHealth()
	h.MarkFailure("llm1")
	h.MarkFailure(config.AIEndpointIDLocalLLM)
	t.Cleanup(func() {
		h.MarkSuccess("llm1")
		h.MarkSuccess(config.AIEndpointIDLocalLLM)
	})
	if !h.InCooldown("llm1") {
		t.Fatal("test setup: the row was not benched")
	}
	got := configuredModelPairs(cfg)
	if !slices.Equal(got, want) {
		t.Errorf("benched rows changed the derivation:\n got %v\nwant %v", got, want)
	}
	if !slices.Contains(got, modelPair{providerLocal, "qwen3:14b"}) {
		t.Errorf("a benched row's model is not named: %v", got)
	}
}

// llm_mode local with routing on: the scanner goes through the pool, but the
// llmparser service still sends metadata_review_model, which falls back to
// local_llm_model and then to the parser default.
func TestConfiguredModelPairs_RoutedLocalMetadataReviewFallsBackToParserDefault(t *testing.T) {
	cfg := &config.Config{
		AIBackend:          config.AIBackendConfig{LLMMode: config.AIBackendModeLocal, LocalBaseURL: testLocalURL},
		AIEndpointsRouting: true,
	}
	want := []modelPair{{providerLocal, defaultModel}}
	if got := configuredModelPairs(cfg); !slices.Equal(got, want) {
		t.Errorf("configuredModelPairs = %v, want %v", got, want)
	}
}

// An empty embedding.model sends the constructor's default; the OpenAI client
// has no other source, so openai mode names it (and never reads
// local_embedding_model).
func TestConfiguredModelPairs_EmptyEmbeddingModelFallsBackToDefault(t *testing.T) {
	cfg := probeConfig(config.AIBackendModeLocal)
	cfg.AIBackend.EmbeddingMode = config.AIBackendModeOpenAI
	cfg.Embedding.Model = ""
	got := configuredModelPairs(cfg)
	if len(got) == 0 || got[0] != (modelPair{providerOpenAI, defaultEmbeddingModel}) {
		t.Errorf("empty embedding.model did not fall back to %q: %v", defaultEmbeddingModel, got)
	}
	if slices.Contains(got, modelPair{providerOpenAI, "bge-m3"}) || slices.Contains(got, modelPair{providerLocal, "bge-m3"}) {
		t.Errorf("openai embedding mode read local_embedding_model: %v", got)
	}
}

// Local embedding is local-only even with routing on: a cloud row ticked for
// embed.text with the pinned model must not be named.
func TestConfiguredModelPairs_LocalEmbeddingSkipsCloudEmbedRows(t *testing.T) {
	cfg := probeConfig(config.AIBackendModeLocal)
	cfg.AIEndpoints = append(cfg.AIEndpoints, config.AIEndpoint{
		ID: "cloud-embed", Protocol: string(aidispatch.ProtocolOpenAICompat), URL: testOpenAIURL,
		EmbedModel: "bge-m3", Priority: 1, Enabled: true, AuthRef: "openai_api_key",
		Capabilities: caps(aidispatch.EmbedText),
	})
	got := configuredModelPairs(cfg)
	if slices.Contains(got, modelPair{providerOpenAI, "bge-m3"}) {
		t.Errorf("local embedding named a cloud embed row: %v", got)
	}
	if !slices.Contains(got, modelPair{providerLocal, "bge-m3"}) {
		t.Errorf("the local embed row is not named: %v", got)
	}
}

// setConfigured must force the next label to re-sync even when the new
// source's generation equals the one the last sync ran at.
func TestModelLabelSet_SetConfiguredForcesResyncAtSameGeneration(t *testing.T) {
	s := newModelLabelSet()
	a, b := modelPair{providerOpenAI, "model-a"}, modelPair{providerOpenAI, "model-b"}
	// Generation 1 + 1 is what setConfigured's first call will produce.
	s.source.Store(&modelSource{generation: func() uint64 { return 1 }, configured: func() []modelPair { return []modelPair{a} }})
	if got := s.admit(a.provider, a.model); got != a.model {
		t.Fatalf("setup: %v labelled %q", a, got)
	}
	s.setConfigured(b)
	if got := s.admit(b.provider, b.model); got != b.model {
		t.Errorf("after setConfigured %v labelled %q, want it named", b, got)
	}
}
