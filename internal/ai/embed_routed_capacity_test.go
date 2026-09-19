// file: internal/ai/embed_routed_capacity_test.go
// version: 1.0.0
// guid: 8b2d6f41-3c7e-4a9b-b5d0-2e9f71c4a6d8
// last-edited: 2026-09-19

package ai

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/aidispatch"
)

// dedup.embed-scan sized its worker pool with a fixed 4, equal to one local
// endpoint's slots, so with ai_endpoints_routing on a second embed host never
// received a request (prod 2026-09-19: llm1 idle through a 76k-book scan).
// RoutedCapacity is what the scan sizes itself from instead.
func TestEmbeddingClient_RoutedCapacity(t *testing.T) {
	a := ep("m1max", "http://127.0.0.1:1/v1", 10, et)
	a.Concurrency = 3
	b := ep("llm1", "http://127.0.0.1:2/v1", 15, et)
	b.Concurrency = 2
	other := ep("other-model", "http://127.0.0.1:3/v1", 20, et)
	other.EmbedModel = "nomic-embed-text"
	other.Concurrency = 8
	cloud := ep("cloud", "https://api.openai.com/v1", 30, et)
	cloud.Concurrency = 8

	pool := newTestPool(a, b, other, cloud)
	c := NewEmbeddingClientWithOptions("ollama", "bge-m3", "").WithPoolRouting(pool.PoolSource)

	// Only same-model, local endpoints can serve this client's calls, so
	// only their slots count: 3 + 2.
	if got := c.RoutedCapacity(); got != 5 {
		t.Fatalf("RoutedCapacity = %d, want 5 (3+2 same-model local slots)", got)
	}

	pool.active.Store(false)
	if got := c.RoutedCapacity(); got != 0 {
		t.Fatalf("routing off: RoutedCapacity = %d, want 0", got)
	}

	if got := NewEmbeddingClientWithOptions("ollama", "bge-m3", "").RoutedCapacity(); got != 0 {
		t.Fatalf("no pool installed: RoutedCapacity = %d, want 0", got)
	}
	var nilClient *EmbeddingClient
	if got := nilClient.RoutedCapacity(); got != 0 {
		t.Fatalf("nil client: RoutedCapacity = %d, want 0", got)
	}
	_ = aidispatch.EmbedText
}
