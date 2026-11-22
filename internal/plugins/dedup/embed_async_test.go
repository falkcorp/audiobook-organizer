// file: internal/plugins/dedup/embed_async_test.go
// version: 1.0.1
// guid: c1d2e3f4-a5b6-7890-cdef-012345678901
// last-edited: 2026-07-03

// Tests for the dedup.embed-async op retirement.
//
// Verifies that:
// 1. The op's nightly cron schedule has been removed (Schedule is nil).
// 2. The op is a no-op (returns nil) when backend is not OpenAI.

package dedup

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEmbedAsyncDef_ScheduleIsNil(t *testing.T) {
	t.Parallel()
	p := &Plugin{}
	def := p.embedAsyncDef()

	assert.Nil(t, def.Schedule,
		"embedAsyncDef().Schedule must be nil — nightly cron should be retired")
}

func TestRunEmbedAsync_SkipsWhenOllamaBaseURLConfigured(t *testing.T) {
	// Save and restore config
	origConfig := config.Snapshot()
	t.Cleanup(func() {
		config.ReplaceForTest(origConfig)
	})

	// Configure non-empty BaseURL (Ollama) — signals non-OpenAI backend
	config.SetForTest(func(c *config.Config) {
		c.Embedding.BaseURL = "http://localhost:11434"
		c.OpenAIAPIKey = "sk-test-key"
	})

	p := &Plugin{} // engine is nil; runEmbedAsync should not attempt to use it
	err := p.runEmbedAsync(context.Background(), json.RawMessage(`{}`), &mockReporter{})

	require.NoError(t, err, "runEmbedAsync should return nil when BaseURL is configured (non-OpenAI backend)")
}

func TestRunEmbedAsync_SkipsWhenNoOpenAIAPIKey(t *testing.T) {
	// Save and restore config
	origConfig := config.Snapshot()
	t.Cleanup(func() {
		config.ReplaceForTest(origConfig)
	})

	// Configure empty OpenAI key and empty BaseURL — signals non-OpenAI backend
	config.SetForTest(func(c *config.Config) {
		c.Embedding.BaseURL = ""
		c.OpenAIAPIKey = ""
	})

	p := &Plugin{} // engine is nil; runEmbedAsync should not attempt to use it
	err := p.runEmbedAsync(context.Background(), json.RawMessage(`{}`), &mockReporter{})

	require.NoError(t, err, "runEmbedAsync should return nil when OpenAIAPIKey is empty (non-OpenAI backend)")
}

func TestRunEmbedAsync_SkipsWhenBothConditionsMet(t *testing.T) {
	// Save and restore config
	origConfig := config.Snapshot()
	t.Cleanup(func() {
		config.ReplaceForTest(origConfig)
	})

	// Configure both conditions that signal non-OpenAI backend
	config.SetForTest(func(c *config.Config) {
		c.Embedding.BaseURL = "http://localhost:11434"
		c.OpenAIAPIKey = ""
	})

	p := &Plugin{} // engine is nil; runEmbedAsync should not attempt to use it
	err := p.runEmbedAsync(context.Background(), json.RawMessage(`{}`), &mockReporter{})

	require.NoError(t, err, "runEmbedAsync should return nil when both BaseURL and OpenAIAPIKey indicate non-OpenAI backend")
}
