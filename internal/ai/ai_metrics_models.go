// file: internal/ai/ai_metrics_models.go
// version: 1.1.1
// guid: 5d720f86-7e84-4490-9507-66a96a2fc61a
// last-edited: 2026-10-11

package ai

import (
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/aidispatch"
	"github.com/falkcorp/audiobook-organizer/internal/config"
)

// configuredModelPairs lists the (provider, model) pairs the settings make an
// AI call send, which is what the model label names (see modelLabelSet). It is
// a pure function of cfg: the same settings give the same list in the same
// order, so a restart names the same models whatever call arrives first.
//
// It mirrors what each call site really sends rather than listing every
// model-looking field: a pair no call can produce would take one of the
// maxModelLabels slots for nothing, and a slot is never given back. The
// provider of a pair is the one providerForBaseURL derives from the base URL
// the call is built with, which is how observe labels it.
//
// The order is the priority when the settings hold more pairs than fit, and is
// fixed. It follows how much traffic a pair carries and how certain it is
// that the pair is sent:
//
//  1. The embedding model. Every embed call sends it, which is more calls than
//     any chat task makes. With routing on and embedding_mode local that is
//     the pool rows that serve embed.text; otherwise the legacy client's
//     model.
//  2. With routing on, the models the pool really serves, in the order the
//     dispatcher would try the rows (priority, then stored order): the
//     llm.filename_parse rows the scanner routes to (llm_mode local or
//     openai-fallback-local), then the llm.cover_art_vision rows cover-text
//     reading routes to, local rows before cloud ones as ReadCoverText does.
//  3. The legacy OpenAI chat models (per-feature fields, then the parser
//     default).
//  4. The legacy local chat models that something still sends.
//
// A pair repeated by several settings keeps its first position.
//
// Pool rows are not listed by their fields. A row's chat_model, embed_model and
// capability_models entries are named only for a capability that has a routed
// call site (llm.filename_parse, embed.text and llm.cover_art_vision: routing
// a further capability means adding it below, with the dispatcher options its
// call site builds) and only when the dispatcher would hand that row the
// call: enabled, ticked for the capability (default-deny), protocol and
// required features, GPU label, a model for the kind, locality for the call,
// and a secret for its auth_ref. Anything else on a row is never sent.
//
// Not derived from llm_mode alone and not gated on enable_ai_parsing: several
// callers build their parser enabled whatever that flag says (handlers/ai.go),
// and a settings toggle of the flag is as ordinary as a model change, so
// gating would make the named set depend on the flag's state at the first use.
func configuredModelPairs(cfg *config.Config) []modelPair {
	var out []modelPair
	add := func(provider, model string) {
		if !modelFitsLabel(model) {
			return
		}
		p := modelPair{provider, model}
		for _, have := range out {
			if have == p {
				return
			}
		}
		out = append(out, p)
	}
	addAll := func(pairs []modelPair) {
		for _, p := range pairs {
			add(p.provider, p.model)
		}
	}
	first := func(models ...string) string {
		for _, m := range models {
			if m != "" {
				return m
			}
		}
		return ""
	}

	llmMode := cfg.EffectiveLLMMode()
	routed := cfg.AIEndpointsRouting
	localLLM := llmMode == config.AIBackendModeLocal
	fallbackLocalLLM := llmMode == config.AIBackendModeOpenAIFallbackLocal
	// The scanner's newAIParser sends filename parsing through the pool in
	// exactly these two modes; explicit openai mode is never routed.
	filenameRouted := routed && (localLLM || fallbackLocalLLM)

	localBase := cfg.AIBackend.LocalBaseURL
	if localBase == "" {
		localBase = cfg.Embedding.BaseURL
	}

	// 1. Embedding. The client's model is local_embedding_model, then
	// embedding.model, then defaultEmbeddingModel inside the constructor; the
	// OpenAI client uses embedding.model.
	embedModel := cfg.Embedding.Model
	if embedModel == "" {
		embedModel = defaultEmbeddingModel
	}
	switch cfg.EffectiveEmbeddingMode() {
	case config.AIBackendModeDisabled:
	case config.AIBackendModeLocal:
		model := first(cfg.AIBackend.LocalEmbeddingModel, embedModel)
		if routed {
			// WithPoolRouting is installed in this mode: with the switch on
			// the legacy path is not used at all, and the call is pinned to
			// the client's model and local-only.
			addAll(poolTargets(cfg, aidispatch.EmbedText,
				aidispatch.WithPinnedModel(aidispatch.EmbedText, model), aidispatch.WithLocalOnly()))
		} else {
			add(providerForBaseURL(localBase), model)
		}
	default: // openai, openai-fallback-local: never routed
		if cfg.OpenAIAPIKey != "" {
			base := cfg.Embedding.BaseURL
			if base == "" {
				base = cfg.OpenAIBaseURL
			}
			add(providerForBaseURL(base), embedModel)
		}
	}

	// 2. The pool, with routing on.
	if routed {
		if filenameRouted {
			var opts []aidispatch.Option
			if localLLM {
				// RoutedFilenameParser: only the two cloud-capable modes may
				// reach a cloud row.
				opts = append(opts, aidispatch.WithLocalOnly())
			}
			addAll(poolTargets(cfg, aidispatch.LLMFilenameParse, opts...))
		}
		// RoutedCoverTextReader, in every llm_mode: local rows first, the
		// cloud rows only as a fallback.
		addAll(poolTargets(cfg, aidispatch.LLMCoverArtVision, aidispatch.WithLocalOnly()))
		addAll(poolTargets(cfg, aidispatch.LLMCoverArtVision, aidispatch.WithCloudOnly()))
	}

	// 3. Legacy OpenAI chat. Handlers and ops build their OpenAI parser from
	// the key whatever llm_mode is, and OpenAIParser resolves a call's model
	// from the per-feature field, then the model its constructor was given
	// (defaultModel for the OpenAI parser). The dedup review model is not
	// here: dedup submits its review through the Batch API (aijobs), which no
	// ai_* metric observes.
	if cfg.OpenAIAPIKey != "" {
		provider := providerForBaseURL(cfg.OpenAIBaseURL)
		for _, m := range []string{cfg.FilenameParseModel, cfg.MetadataReviewModel, cfg.CoverArtModel} {
			add(provider, first(m, defaultModel))
		}
	}

	// 4. Legacy local chat. Only what a local parser is still asked for:
	//   - llm_mode local, routing off: the scanner's local parser sends the
	//     filename_parse_model (batch parse), and the llmparser service
	//     (author review/discovery, metadata rerank) the metadata_review_model.
	//     Each falls back to local_llm_model and, when that is empty too, to
	//     OpenAIParser's own default: gpt-5-mini is really sent to the local
	//     backend then, so that pair is named.
	//   - llm_mode local, routing on: the scanner goes through the pool, so
	//     only the llmparser's metadata_review_model remains.
	//   - llm_mode openai-fallback-local, routing off: the scanner's local rung
	//     batch-parses filenames, and is skipped when local_llm_model is empty.
	//     llmparser is the OpenAI parser in this mode.
	//   - openai-fallback-local with routing on: nothing local is sent
	//     outside the pool.
	// The cover_art_model is never sent to a local parser: ParseCoverArt is
	// only called on OpenAI parsers, and cover text is the pool's.
	if localBase != "" {
		provider := providerForBaseURL(localBase)
		localModel := cfg.AIBackend.LocalLLMModel
		switch {
		case localLLM:
			if !routed {
				add(provider, first(cfg.FilenameParseModel, localModel, defaultModel))
			}
			add(provider, first(cfg.MetadataReviewModel, localModel, defaultModel))
		case fallbackLocalLLM:
			if !routed && localModel != "" {
				add(provider, first(cfg.FilenameParseModel, localModel))
			}
		}
	}
	return out
}

// poolTargets returns the (provider, model) pairs a call for c would send to
// the pool described by cfg, in the order the dispatcher would try the rows.
// opts are the options the real call site builds its dispatcher with.
//
// It asks the dispatcher itself, not a copy of its filters, so the answer
// follows the dispatcher's rules: a fresh Health and Slots make it a pure
// function of cfg (no cooldown or in-flight depth from runtime state), and
// the pool's own priority order is the result's order. A row whose auth_ref
// has no secret is skipped, as RoutedFilenameParser/EmbeddingClient do (an
// endpoint failure, never an unauthenticated request).
func poolTargets(cfg *config.Config, c aidispatch.Capability, opts ...aidispatch.Option) []modelPair {
	opts = append([]aidispatch.Option{
		aidispatch.WithHealth(aidispatch.NewHealth()),
		aidispatch.WithSlots(aidispatch.NewSlots()),
	}, opts...)
	d := aidispatch.New(config.DispatchEndpoints(cfg.AIEndpoints), opts...)
	eps, _, err := d.Candidates(c)
	if err != nil {
		return nil
	}
	var out []modelPair
	for _, ep := range eps {
		if ep.AuthRef != "" && poolSecret(cfg, ep.AuthRef) == "" {
			continue
		}
		model, ok := aidispatch.ModelFor(ep, c)
		if !ok {
			continue
		}
		out = append(out, modelPair{providerForBaseURL(ep.URL), model})
	}
	return out
}

// modelFitsLabel reports whether a model string can be a label value at all:
// the same test normalized applies before admission, so a name that would fold
// to "other" anyway never takes a slot.
func modelFitsLabel(model string) bool {
	return model != "" && len(model) <= maxModelLabelLen && !strings.ContainsAny(model, " \t\r\n")
}
