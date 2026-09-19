// file: internal/dedup/embed_concurrency_test.go
// version: 1.0.0
// guid: 4e7a1c93-6b25-4f8d-a0c9-3d5b82e6f17a
// last-edited: 2026-09-19

package dedup

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/ai"
	"github.com/falkcorp/audiobook-organizer/internal/aidispatch"
	"github.com/falkcorp/audiobook-organizer/internal/dedup/unified"
)

func embedEndpoint(id, url string, conc int) aidispatch.Endpoint {
	return aidispatch.Endpoint{ID: id, Protocol: aidispatch.ProtocolOpenAICompat, URL: url,
		EmbedModel: "bge-m3", Priority: 10, Concurrency: conc, Enabled: true,
		Capabilities: []string{aidispatch.EmbedText.ID()}}
}

// With routing on, the embed fan-out is sized from the pool (3+2 slots), not
// the legacy fixed knob, so the second host gets work.
func TestEngine_EmbedConcurrency(t *testing.T) {
	active := true
	pool := &ai.PoolSource{
		Active: func() bool { return active },
		Endpoints: func() []aidispatch.Endpoint {
			return []aidispatch.Endpoint{embedEndpoint("m1max", "http://127.0.0.1:1/v1", 3), embedEndpoint("llm1", "http://127.0.0.1:2/v1", 2)}
		},
		Secret:  func(string) string { return "" },
		Options: []aidispatch.Option{aidispatch.WithSlots(aidispatch.NewSlots())},
	}
	client := ai.NewEmbeddingClientWithOptions("ollama", "bge-m3", "").WithPoolRouting(pool)
	eng, err := NewEngine(nil, nil, client, nil, nil, unified.DefaultScoreConfig())
	if err != nil {
		t.Fatal(err)
	}
	if got := eng.EmbedConcurrency(4); got != 5 {
		t.Fatalf("routing on: EmbedConcurrency = %d, want 5", got)
	}
	active = false
	if got := eng.EmbedConcurrency(4); got != 4 {
		t.Fatalf("routing off: EmbedConcurrency = %d, want the fallback 4", got)
	}
	noClient, err := NewEngine(nil, nil, nil, nil, nil, unified.DefaultScoreConfig())
	if err != nil {
		t.Fatal(err)
	}
	if got := noClient.EmbedConcurrency(4); got != 4 {
		t.Fatalf("no embed client: EmbedConcurrency = %d, want 4", got)
	}
}
