// file: internal/ai/pool_routing_locality_test.go
// version: 1.0.0
// guid: 5d0c2b8e-3f71-4a6e-9c24-8e1b7a3f6d95
// last-edited: 2026-09-19

package ai

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/aidispatch"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// mapSettings is a minimal database.SettingsStore so the test can drive the
// REAL LoadConfigFromDatabase migration path.
type mapSettings struct {
	mu sync.Mutex
	m  map[string]database.Setting
}

func (s *mapSettings) GetSetting(k string) (*database.Setting, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if v, ok := s.m[k]; ok {
		return &v, nil
	}
	return nil, nil
}
func (s *mapSettings) SetSetting(k, v, typ string, secret bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[k] = database.Setting{Key: k, Value: v, Type: typ, IsSecret: secret}
	return nil
}
func (s *mapSettings) GetAllSettings() ([]database.Setting, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]database.Setting, 0, len(s.m))
	for _, v := range s.m {
		out = append(out, v)
	}
	return out, nil
}
func (s *mapSettings) DeleteSetting(k string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, k)
	return nil
}

// migratedLocalModeRows runs the real ai_endpoints migration from a legacy
// config: llm_mode=local at localURL, with an OpenAI key whose base URL is
// cloudURL. It returns the migrated rows as the dispatcher sees them.
func migratedLocalModeRows(t *testing.T, localURL, cloudURL string) []aidispatch.Endpoint {
	t.Helper()
	prev := config.Snapshot()
	t.Cleanup(func() { config.Mutate(func(c *config.Config) { *c = prev }) })
	config.Mutate(func(c *config.Config) {
		c.AIEndpoints = nil
		c.OpenAIAPIKey = "sk-test"
		c.OpenAIBaseURL = cloudURL
	})
	blob := `{"enable_ai_parsing":true,"openai_base_url":"` + cloudURL + `",` +
		`"ai_backend":{"llm_mode":"local","embedding_mode":"local","local_base_url":"` + localURL + `",` +
		`"local_llm_model":"qwen2.5:7b-instruct","local_embedding_model":"bge-m3"}}`
	store := &mapSettings{m: map[string]database.Setting{
		"config_blob":    {Key: "config_blob", Value: blob, Type: "json"},
		"openai_api_key": {Key: "openai_api_key", Value: "sk-test", Type: "string"},
	}}
	if err := config.LoadConfigFromDatabase(store); err != nil {
		t.Fatal(err)
	}
	rows := config.Snapshot().AIEndpoints
	var sawCloud bool
	for _, r := range rows {
		if r.ID == config.AIEndpointIDOpenAI {
			sawCloud = true
			if r.URL != cloudURL {
				t.Fatalf("migrated openai row URL = %q, want the cloud fake %q", r.URL, cloudURL)
			}
		}
	}
	if !sawCloud {
		t.Fatalf("migration produced no openai row: %+v", rows)
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
