// file: internal/ai/ai_metrics_models.go
// version: 1.0.0
// guid: 5d720f86-7e84-4490-9507-66a96a2fc61a
// last-edited: 2026-10-11

package ai

import (
	"maps"
	"slices"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/config"
)

// configuredModelPairs lists the (provider, model) pairs the settings can make
// an AI call send, which is what the model label names (see modelLabelSet). It
// is a pure function of cfg: the same settings give the same list in the same
// order, so a restart names the same models whatever call arrives first.
//
// It mirrors how each call site resolves its model and provider rather than
// listing every model-looking field: a pair that no call can produce would
// take one of the maxModelLabels slots for nothing. The provider of a pair is
// the one providerForBaseURL derives from the base URL that call site is built
// with, which is how observe labels it.
//
// The order is the priority when the settings hold more pairs than fit, and is
// fixed: chat models for OpenAI, chat models for the local backend, embedding
// models, then the endpoint pool's rows in the order the settings store them.
// Within a row, the chat model, the embed model, then the per-capability
// overrides by capability id (a map, so sorted). A pair repeated by several
// settings keeps its first position.
func configuredModelPairs(cfg *config.Config) []modelPair {
	var out []modelPair
	add := func(provider, model string) {
		if !modelFitsLabel(model) {
			return
		}
		p := modelPair{provider, model}
		if !slices.Contains(out, p) {
			out = append(out, p)
		}
	}

	// Chat models. OpenAIParser resolves a call's model from the per-feature
	// field and, when that is empty, from the model its constructor was given:
	// defaultModel for the OpenAI parser, local_llm_model for the local one.
	// The fields apply to both, which is why a local parser can label a
	// per-feature model name.
	feature := func(fallback string) []string {
		var models []string
		for _, m := range []string{cfg.FilenameParseModel, cfg.MetadataReviewModel, cfg.CoverArtModel} {
			if m == "" {
				m = fallback
			}
			models = append(models, m)
		}
		return models
	}

	if cfg.OpenAIAPIKey != "" {
		provider := providerForBaseURL(cfg.OpenAIBaseURL)
		for _, m := range feature(defaultModel) {
			add(provider, m)
		}
		// The dedup engine's review model; its own default is the parser's.
		reviewModel := cfg.Dedup.ReviewModel
		if reviewModel == "" {
			reviewModel = defaultModel
		}
		add(provider, reviewModel)
	}

	llmMode := cfg.EffectiveLLMMode()
	localBase := cfg.AIBackend.LocalBaseURL
	if localBase == "" {
		localBase = cfg.Embedding.BaseURL
	}
	if localBase != "" && (llmMode == config.AIBackendModeLocal || llmMode == config.AIBackendModeOpenAIFallbackLocal) {
		provider := providerForBaseURL(localBase)
		for _, m := range feature(cfg.AIBackend.LocalLLMModel) {
			add(provider, m)
		}
	}

	// Embedding. The local client uses local_embedding_model, then
	// embedding.model; the OpenAI one embedding.model; both fall back to
	// defaultEmbeddingModel inside the client.
	embedModel := cfg.Embedding.Model
	if embedModel == "" {
		embedModel = defaultEmbeddingModel
	}
	switch cfg.EffectiveEmbeddingMode() {
	case config.AIBackendModeDisabled:
	case config.AIBackendModeLocal:
		model := cfg.AIBackend.LocalEmbeddingModel
		if model == "" {
			model = embedModel
		}
		add(providerForBaseURL(localBase), model)
	default: // openai, openai-fallback-local
		if cfg.OpenAIAPIKey != "" {
			base := cfg.Embedding.BaseURL
			if base == "" {
				base = cfg.OpenAIBaseURL
			}
			add(providerForBaseURL(base), embedModel)
		}
	}

	// The endpoint pool. Rows only reach a metric when routing is on and the
	// row is enabled; a disabled row, or any row with routing off, would name a
	// model nothing sends.
	if cfg.AIEndpointsRouting {
		for _, row := range cfg.AIEndpoints {
			if !row.Enabled {
				continue
			}
			provider := providerForBaseURL(row.URL)
			add(provider, row.ChatModel)
			add(provider, row.EmbedModel)
			for _, capID := range slices.Sorted(maps.Keys(row.CapabilityModels)) {
				add(provider, row.CapabilityModels[capID])
			}
		}
	}
	return out
}

// modelFitsLabel reports whether a model string can be a label value at all:
// the same test normalized applies before admission, so a name that would fold
// to "other" anyway never takes a slot.
func modelFitsLabel(model string) bool {
	return model != "" && len(model) <= maxModelLabelLen && !strings.ContainsAny(model, " \t\r\n")
}
