// file: internal/ai/register.go
// version: 1.4.0
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

func init() {
	// embedclient — embedding client, mode-gated by EffectiveEmbeddingMode.
	//   - disabled: no client.
	//   - local:    local OpenAI-compatible backend (Ollama) at LocalBaseURL,
	//               constructed with a dummy key the backend ignores.
	//   - openai / openai-fallback-local: real OpenAI (key required).
	serviceregistry.Register(serviceregistry.ServiceDef{
		Name:   "embedclient",
		Needs:  []string{serviceregistry.KeyConfig, serviceregistry.KeyEmbeddingStore},
		Groups: []string{"ai"},
		Build: func(c *serviceregistry.Container) (any, error) {
			cfg := serviceregistry.Get[*config.Config](c, serviceregistry.KeyConfig)
			mode := cfg.EffectiveEmbeddingMode()
			if mode == config.AIBackendModeDisabled {
				return (*EmbeddingClient)(nil), nil
			}

			var client *EmbeddingClient
			switch mode {
			case config.AIBackendModeLocal:
				// Read-time fallback keeps the getter pure: prefer the new
				// AIBackend fields, fall back to the legacy Embedding fields
				// for the migration-not-yet-applied case.
				baseURL := cfg.AIBackend.LocalBaseURL
				if baseURL == "" {
					baseURL = cfg.Embedding.BaseURL
				}
				model := cfg.AIBackend.LocalEmbeddingModel
				if model == "" {
					model = cfg.Embedding.Model
				}
				slog.Info("embedclient: local backend", "baseURL", baseURL, "model", model)
				// Dummy key "ollama" — a local OpenAI-compatible backend ignores
				// the Authorization header.
				client = NewEmbeddingClientWithOptions("ollama", model, baseURL)
			default: // openai, openai-fallback-local
				if cfg.OpenAIAPIKey == "" {
					slog.Warn("embedclient: openai embedding mode but no OpenAIAPIKey — skipping", "mode", mode)
					return (*EmbeddingClient)(nil), nil
				}
				baseURL := cfg.Embedding.BaseURL
				if baseURL == "" {
					baseURL = os.Getenv("OPENAI_BASE_URL")
				}
				client = NewEmbeddingClientWithOptions(cfg.OpenAIAPIKey, cfg.Embedding.Model, baseURL)
			}

			embStore, _ := serviceregistry.TryGet[*database.EmbeddingStore](c, serviceregistry.KeyEmbeddingStore)
			if embStore != nil {
				client = client.WithCache(embStore)
			}
			return client, nil
		},
	})

	// llmparser — OpenAIParser used by dedup Layer 3 review + metadata LLM
	// reranker, mode-gated by EffectiveLLMMode.
	//   - disabled: no parser.
	//   - local:    local OpenAI-compatible backend at LocalBaseURL with a dummy
	//               key and the local LLM model name.
	//   - openai / openai-fallback-local: real OpenAI (key required).
	serviceregistry.Register(serviceregistry.ServiceDef{
		Name:   "llmparser",
		Needs:  []string{serviceregistry.KeyConfig},
		Groups: []string{"ai"},
		Build: func(c *serviceregistry.Container) (any, error) {
			cfg := serviceregistry.Get[*config.Config](c, serviceregistry.KeyConfig)
			mode := cfg.EffectiveLLMMode()
			if mode == config.AIBackendModeDisabled {
				return (*OpenAIParser)(nil), nil
			}

			switch mode {
			case config.AIBackendModeLocal:
				baseURL := cfg.AIBackend.LocalBaseURL
				if baseURL == "" {
					baseURL = cfg.Embedding.BaseURL
				}
				slog.Info("llmparser: local backend", "baseURL", baseURL, "model", cfg.AIBackend.LocalLLMModel)
				return NewOpenAIParserWithBaseURL(cfg, "ollama", baseURL, cfg.AIBackend.LocalLLMModel, cfg.EnableAIParsing), nil
			default: // openai, openai-fallback-local
				if cfg.OpenAIAPIKey == "" {
					slog.Warn("llmparser: openai LLM mode but no OpenAIAPIKey — skipping", "mode", mode)
					return (*OpenAIParser)(nil), nil
				}
				return NewOpenAIParser(cfg, cfg.OpenAIAPIKey, cfg.EnableAIParsing), nil
			}
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
	// Conditional on llmparser being available AND the effective LLM mode not
	// being disabled (TOGGLE-6: previously it built whenever a parser existed,
	// even in a disabled-LLM configuration).
	serviceregistry.Register(serviceregistry.ServiceDef{
		Name:   "metadatallmscorer",
		Needs:  []string{serviceregistry.KeyConfig, "llmparser"},
		Groups: []string{"ai"},
		Build: func(c *serviceregistry.Container) (any, error) {
			cfg := serviceregistry.Get[*config.Config](c, serviceregistry.KeyConfig)
			if cfg.EffectiveLLMMode() == config.AIBackendModeDisabled {
				return (*LLMScorer)(nil), nil
			}
			parser, _ := serviceregistry.TryGet[*OpenAIParser](c, "llmparser")
			if parser == nil {
				return (*LLMScorer)(nil), nil
			}
			return NewLLMScorer(parser), nil
		},
	})
}
