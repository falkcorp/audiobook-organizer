// file: internal/ai/register.go
// version: 1.3.0
// last-edited: 2026-07-03

// Service registry registrations for the AI cluster (W4).
//
// These services are all optional and conditional on config. Each Build
// returns (nil, nil) when its preconditions aren't met so the container
// can complete Build without error, and downstream consumers TryGet
// instead of Get.
//
// For consumers to be safe, they MUST nil-check the value returned
// from TryGet. Wiring in NewServer remains inline for now; W7 cleanup
// flips construction over to the container.

package ai

import (
	"log/slog"
	"os"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/serviceregistry"
)

// resolveAIEndpointKey decides whether an OpenAI-compatible client should be
// constructed and what API key to hand it. When a real apiKey is configured
// it's always used. When apiKey is empty but an explicit baseURL is
// configured (a local OpenAI-compatible backend, e.g. Ollama, which ignores
// the Authorization header), a dummy bearer is substituted so construction
// proceeds. When neither is set, construction is skipped — the real-OpenAI
// path (no baseURL) still requires a real key.
func resolveAIEndpointKey(apiKey, baseURL string) (resolvedKey string, ok bool) {
	if apiKey != "" {
		return apiKey, true
	}
	if baseURL != "" {
		return "ollama", true
	}
	return "", false
}

func init() {
	// embedclient — OpenAI embedding client with optional cache.
	// Conditional on: OpenAIAPIKey set AND EmbeddingEnabled true.
	serviceregistry.Register(serviceregistry.ServiceDef{
		Name:   "embedclient",
		Needs:  []string{serviceregistry.KeyConfig, serviceregistry.KeyEmbeddingStore},
		Groups: []string{"ai"},
		Build: func(c *serviceregistry.Container) (any, error) {
			cfg := serviceregistry.Get[*config.Config](c, serviceregistry.KeyConfig)
			if !cfg.Embedding.Enabled {
				return (*EmbeddingClient)(nil), nil
			}
			// Base URL is scoped to the embedding client ONLY (see
			// NewEmbeddingClientWithOptions): cfg.Embedding.BaseURL points
			// embeddings at a local OpenAI-compatible backend (e.g. Ollama)
			// without touching the LLM / metadata clients. Fall back to the
			// OPENAI_BASE_URL env when the config field is empty for backward
			// compatibility with env-based setups.
			baseURL := cfg.Embedding.BaseURL
			if baseURL == "" {
				baseURL = os.Getenv("OPENAI_BASE_URL")
			}
			resolvedKey, ok := resolveAIEndpointKey(cfg.OpenAIAPIKey, baseURL)
			if !ok {
				return (*EmbeddingClient)(nil), nil
			}
			if cfg.OpenAIAPIKey == "" {
				slog.Warn("embedclient: constructing with keyless/local backend (no OpenAIAPIKey, using explicit base URL)", "baseURL", baseURL)
			}
			embStore, _ := serviceregistry.TryGet[*database.EmbeddingStore](c, serviceregistry.KeyEmbeddingStore)
			client := NewEmbeddingClientWithOptions(resolvedKey, cfg.Embedding.Model, baseURL)
			if embStore != nil {
				client = client.WithCache(embStore)
			}
			return client, nil
		},
	})

	// llmparser — OpenAIParser used by dedup Layer 3 review + metadata
	// LLM reranker. Conditional on OpenAIAPIKey set.
	serviceregistry.Register(serviceregistry.ServiceDef{
		Name:   "llmparser",
		Needs:  []string{serviceregistry.KeyConfig},
		Groups: []string{"ai"},
		Build: func(c *serviceregistry.Container) (any, error) {
			cfg := serviceregistry.Get[*config.Config](c, serviceregistry.KeyConfig)
			baseURL := os.Getenv("OPENAI_BASE_URL")
			resolvedKey, ok := resolveAIEndpointKey(cfg.OpenAIAPIKey, baseURL)
			if !ok {
				return (*OpenAIParser)(nil), nil
			}
			if cfg.OpenAIAPIKey == "" {
				slog.Warn("llmparser: constructing with keyless/local backend (no OpenAIAPIKey, using OPENAI_BASE_URL env)", "baseURL", baseURL)
			}
			return NewOpenAIParser(cfg, resolvedKey, cfg.EnableAIParsing), nil
		},
	})

	// metadatascorer — embedding-based metadata candidate scorer.
	// Conditional on embedclient + embeddingstore both being available.
	serviceregistry.Register(serviceregistry.ServiceDef{
		Name:   "metadatascorer",
		Needs:  []string{serviceregistry.KeyConfig, "embedclient", serviceregistry.KeyEmbeddingStore},
		Groups: []string{"ai"},
		Build: func(c *serviceregistry.Container) (any, error) {
			cfg := serviceregistry.Get[*config.Config](c, serviceregistry.KeyConfig)
			if !cfg.MetadataScoring.EmbeddingEnabled {
				return (*EmbeddingScorer)(nil), nil
			}
			client, _ := serviceregistry.TryGet[*EmbeddingClient](c, "embedclient")
			store, _ := serviceregistry.TryGet[*database.EmbeddingStore](c, serviceregistry.KeyEmbeddingStore)
			if client == nil || store == nil {
				return (*EmbeddingScorer)(nil), nil
			}
			return NewEmbeddingScorer(client, store), nil
		},
	})

	// metadatallmscorer — LLM-based metadata candidate rerank scorer.
	// Conditional on llmparser being available.
	serviceregistry.Register(serviceregistry.ServiceDef{
		Name:   "metadatallmscorer",
		Needs:  []string{"llmparser"},
		Groups: []string{"ai"},
		Build: func(c *serviceregistry.Container) (any, error) {
			parser, _ := serviceregistry.TryGet[*OpenAIParser](c, "llmparser")
			if parser == nil {
				return (*LLMScorer)(nil), nil
			}
			return NewLLMScorer(parser), nil
		},
	})
}
