// file: internal/ai/pool_routing_locality_test.go
// version: 1.0.1
// guid: 5d0c2b8e-3f71-4a6e-9c24-8e1b7a3f6d95
// last-edited: 2026-11-09

package ai

import (
	"context"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/aidispatch"
	"github.com/falkcorp/audiobook-organizer/internal/config"
)

// migratedLocalModeRows returns the ai_endpoints rows HEAD's first-boot
// migration wrote for a legacy llm_mode=local install (local at localURL, an
// OpenAI key whose base URL is cloudURL), as the dispatcher sees them. The
// migration left with the HEAD settings loader (F3-07); installs keep the rows
// it wrote, so the locality rules are still pinned on exactly that shape.
func migratedLocalModeRows(t *testing.T, localURL, cloudURL string) []aidispatch.Endpoint {
	t.Helper()
	ids := func(cs ...aidispatch.Capability) []string {
		out := make([]string, 0, len(cs))
		for _, c := range cs {
			out = append(out, c.ID())
		}
		return out
	}
	rows := []config.AIEndpoint{
		{
			ID: config.AIEndpointIDLocalLLM, Protocol: string(aidispatch.ProtocolOpenAICompat),
			URL: localURL, ChatModel: "qwen2.5:7b-instruct", EmbedModel: "bge-m3",
			Priority: 10, Concurrency: 4, Enabled: true,
			Capabilities: ids(aidispatch.LLMFilenameParse, aidispatch.LLMAuthorReview,
				aidispatch.LLMAuthorDiscovery, aidispatch.LLMMetadataRerank, aidispatch.EmbedText),
		},
		{
			ID: config.AIEndpointIDOpenAI, Protocol: string(aidispatch.ProtocolOpenAICompat),
			URL: cloudURL, ChatModel: config.DefaultOpenAIChatModel,
			Priority: 20, Concurrency: 4, Enabled: true,
			Features: []string{aidispatch.FeatureBatchAPI, aidispatch.FeatureVision},
			AuthRef:  "openai_api_key",
			Capabilities: ids(aidispatch.LLMFilenameParse, aidispatch.LLMAudiobookParse,
				aidispatch.LLMCoverArtVision, aidispatch.LLMAuthorReview, aidispatch.LLMAuthorDiscovery,
				aidispatch.LLMAuthorDedupBatch, aidispatch.LLMDiagnostics, aidispatch.TranscribeIntroClip),
		},
	}
	return config.DispatchEndpoints(rows)
}

func localModePool(eps []aidispatch.Endpoint, slots *aidispatch.Slots) *PoolSource {
	return &PoolSource{
		Active:    func() bool { return true },
		Endpoints: func() []aidispatch.Endpoint { return eps },
		Secret:    func(string) string { return "sk-test" },
		Options: []aidispatch.Option{aidispatch.WithSlots(slots), aidispatch.WithHealth(aidispatch.NewHealth()),
			aidispatch.WithAttribution(aidispatch.NewAttribution())},
	}
}

// FIX 1: in llm_mode=local, a saturated local endpoint must NOT spill to the
// migrated openai row. Legacy local mode never contacts OpenAI.
func TestRoutedParse_LocalModeNeverSpillsToCloud_MigratedRows(t *testing.T) {
	local, cloud := newFakeOpenAI(t), newFakeOpenAI(t)
	eps := migratedLocalModeRows(t, local.url(), cloud.url())
	slots := aidispatch.NewSlots()
	parser := NewRoutedFilenameParser(localModePool(eps, slots), config.AIBackendModeLocal)

	// Hold every local-llm slot, as concurrent embed / ai-parse work would.
	for range 4 {
		rel, err := slots.Acquire(context.Background(), config.AIEndpointIDLocalLLM, 4, aidispatch.TotalCap{})
		if err != nil {
			t.Fatal(err)
		}
		defer rel()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	_, _ = parser.ParseBatch(ctx, []string{"a", "b"})
	if n := cloud.chats.Load(); n != 0 {
		t.Fatalf("llm_mode=local sent %d request(s) to the cloud endpoint while local slots were busy", n)
	}
}

// FIX 1: a refused/unreachable local endpoint must fail, not fail over to
// the cloud row.
func TestRoutedParse_LocalModeDeadLocalNeverFailsOverToCloud(t *testing.T) {
	cloud := newFakeOpenAI(t)
	eps := migratedLocalModeRows(t, deadURL(t), cloud.url())
	parser := NewRoutedFilenameParser(localModePool(eps, aidispatch.NewSlots()), config.AIBackendModeLocal)
	if _, err := parser.ParseBatch(context.Background(), []string{"a"}); err == nil {
		t.Fatal("dead local endpoint answered")
	}
	if n := cloud.chats.Load(); n != 0 {
		t.Fatalf("llm_mode=local failed over to the cloud endpoint (%d request(s))", n)
	}
}

// Routed embeddings are installed only in local embedding mode, so they are
// always local-only: a cloud row with the same model is never used.
func TestEmbedRouted_NeverUsesCloudRow(t *testing.T) {
	cloud := newFakeOpenAI(t)
	c := ep("cloud", cloud.url(), 1, et)
	c.AuthRef = "openai_api_key"
	pool := localModePool([]aidispatch.Endpoint{c}, aidispatch.NewSlots())
	cl := NewEmbeddingClientWithOptions("ollama", "bge-m3", "").WithPoolRouting(pool)
	if _, err := cl.EmbedBatch(context.Background(), []string{"x"}); err == nil {
		t.Fatal("embed succeeded with only a cloud endpoint")
	}
	if cloud.embeds.Load() != 0 {
		t.Fatal("routed embeddings reached a cloud endpoint")
	}
}

// In openai-fallback-local mode cloud rows remain eligible (that is the mode).
func TestRoutedParse_FallbackModeMayUseCloud(t *testing.T) {
	cloud := newFakeOpenAI(t)
	c := ep("cloud", cloud.url(), 1, fp)
	c.AuthRef = "openai_api_key"
	parser := NewRoutedFilenameParser(localModePool([]aidispatch.Endpoint{c}, aidispatch.NewSlots()),
		config.AIBackendModeOpenAIFallbackLocal)
	if _, err := parser.ParseBatch(context.Background(), []string{"x"}); err != nil {
		t.Fatal(err)
	}
	if cloud.chats.Load() != 1 {
		t.Fatalf("cloud chats = %d, want 1", cloud.chats.Load())
	}
}
