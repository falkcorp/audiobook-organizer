// file: internal/ai/register_test.go
// version: 2.0.0
// last-edited: 2026-07-03

package ai

import (
	"context"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/serviceregistry"
)

// buildEmbedClient builds only the embedclient service against the given config,
// with a nil embedding store override so no cache is wired.
func buildEmbedClient(t *testing.T, cfg *config.Config) *EmbeddingClient {
	t.Helper()
	c := serviceregistry.NewContainer().
		Override(serviceregistry.KeyConfig, cfg).
		Override(serviceregistry.KeyEmbeddingStore, (*database.EmbeddingStore)(nil)).
		Include("embedclient")
	if err := c.Build(context.Background()); err != nil {
		t.Fatalf("build embedclient: %v", err)
	}
	client, _ := serviceregistry.TryGet[*EmbeddingClient](c, "embedclient")
	return client
}

// buildLLMParser builds only the llmparser service against the given config.
func buildLLMParser(t *testing.T, cfg *config.Config) *OpenAIParser {
	t.Helper()
	c := serviceregistry.NewContainer().
		Override(serviceregistry.KeyConfig, cfg).
		Include("llmparser")
	if err := c.Build(context.Background()); err != nil {
		t.Fatalf("build llmparser: %v", err)
	}
	parser, _ := serviceregistry.TryGet[*OpenAIParser](c, "llmparser")
	return parser
}

// TestEmbedClientBuild_ModeGated verifies embedclient construction keys off
// EffectiveEmbeddingMode: nil in disabled mode, a local-model client in local
// mode (built with a dummy key), and a real-key client in openai mode.
func TestEmbedClientBuild_ModeGated(t *testing.T) {
	t.Run("disabled mode -> nil client", func(t *testing.T) {
		cfg := &config.Config{}
		cfg.Embedding.Enabled = false // no explicit mode; derives to disabled
		if got := buildEmbedClient(t, cfg); got != nil {
			t.Fatalf("expected nil client in disabled mode, got %#v (model=%q)", got, got.Model())
		}
	})

	t.Run("local mode -> dummy-key client with local model", func(t *testing.T) {
		cfg := &config.Config{}
		cfg.AIBackend.EmbeddingMode = config.AIBackendModeLocal
		cfg.AIBackend.LocalBaseURL = "http://192.168.0.20:11434/v1"
		cfg.AIBackend.LocalEmbeddingModel = "bge-m3"
		got := buildEmbedClient(t, cfg)
		if got == nil {
			t.Fatal("expected non-nil client in local mode")
		}
		if got.Model() != "bge-m3" {
			t.Fatalf("local client model = %q, want bge-m3", got.Model())
		}
	})

	t.Run("openai mode requires a key", func(t *testing.T) {
		cfg := &config.Config{}
		cfg.Embedding.Enabled = true
		cfg.Embedding.Model = "text-embedding-3-large"
		cfg.OpenAIAPIKey = "" // openai mode derivation needs a key; none -> disabled
		if got := buildEmbedClient(t, cfg); got != nil {
			t.Fatalf("expected nil client when openai mode lacks a key, got model=%q", got.Model())
		}

		cfg.OpenAIAPIKey = "sk-real"
		got := buildEmbedClient(t, cfg)
		if got == nil {
			t.Fatal("expected non-nil client in openai mode with a key")
		}
		if got.Model() != "text-embedding-3-large" {
			t.Fatalf("openai client model = %q, want text-embedding-3-large", got.Model())
		}
	})
}

// TestLLMParserBuild_ModeGated verifies llmparser construction keys off
// EffectiveLLMMode: nil in disabled mode, a constructed parser in local mode
// with the local model as fallback, and a constructed parser in openai mode.
func TestLLMParserBuild_ModeGated(t *testing.T) {
	t.Run("disabled mode -> nil parser", func(t *testing.T) {
		cfg := &config.Config{} // no key, nothing enabled -> disabled
		if got := buildLLMParser(t, cfg); got != nil {
			t.Fatalf("expected nil parser in disabled LLM mode, got %#v", got)
		}
	})

	t.Run("local mode -> parser using local model fallback", func(t *testing.T) {
		cfg := &config.Config{}
		cfg.AIBackend.LLMMode = config.AIBackendModeLocal
		cfg.AIBackend.LocalBaseURL = "http://192.168.0.20:11434/v1"
		cfg.AIBackend.LocalLLMModel = "qwen2.5:7b-instruct"
		cfg.EnableAIParsing = true
		got := buildLLMParser(t, cfg)
		if got == nil {
			t.Fatal("expected non-nil parser in local LLM mode")
		}
		if m := got.fallbackModel(); m != "qwen2.5:7b-instruct" {
			t.Fatalf("local parser fallback model = %q, want qwen2.5:7b-instruct", m)
		}
	})

	t.Run("openai mode -> parser constructed", func(t *testing.T) {
		cfg := &config.Config{}
		cfg.OpenAIAPIKey = "sk-real"
		cfg.EnableAIParsing = true
		got := buildLLMParser(t, cfg)
		if got == nil {
			t.Fatal("expected non-nil parser in openai LLM mode")
		}
		if !got.IsEnabled() {
			t.Fatal("expected enabled parser when EnableAIParsing=true and key set")
		}
	})
}
