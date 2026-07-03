// file: internal/dedup/embed_async_gate_test.go
// version: 1.0.0
// last-edited: 2026-07-03

package dedup

import (
	"context"
	"errors"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/ai"
	"github.com/falkcorp/audiobook-organizer/internal/config"
)

// TestEmbedBooksAsync_HardGatedToOpenAI locks TOGGLE-2: the OpenAI Batch API
// path must reject any non-openai effective embedding mode with
// ai.ErrBatchUnsupported, so a local/disabled backend never submits a batch.
func TestEmbedBooksAsync_HardGatedToOpenAI(t *testing.T) {
	engine, _, _ := setupTestEngine(t)
	// A client must exist so the gate (not the nil-client guard) is what fires.
	engine.embedClient = ai.NewEmbeddingClientWithOptions("ollama", "bge-m3", "http://192.168.0.20:11434/v1")

	saved := config.AppConfig
	t.Cleanup(func() { config.AppConfig = saved })

	for _, mode := range []string{
		config.AIBackendModeLocal,
		config.AIBackendModeDisabled,
		config.AIBackendModeOpenAIFallbackLocal,
	} {
		config.AppConfig = config.Config{}
		config.AppConfig.AIBackend.EmbeddingMode = mode

		_, _, err := engine.EmbedBooksAsync(context.Background())
		if !errors.Is(err, ai.ErrBatchUnsupported) {
			t.Errorf("mode %q: EmbedBooksAsync err = %v, want ai.ErrBatchUnsupported", mode, err)
		}
	}
}
