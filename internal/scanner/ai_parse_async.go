// file: internal/scanner/ai_parse_async.go
// version: 1.0.0
// guid: 5c5dc851-ad6d-4624-b836-a85e38ae5d02
// last-edited: 2026-08-24

package scanner

import (
	"context"
	"errors"

	"github.com/falkcorp/audiobook-organizer/internal/ai"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

// EnqueueAIParseFn hands a batch of books that need AI filename parsing to the
// operations queue instead of parsing them inline.
//
// Why this exists: until 2026-08-24 the AI phase ran synchronously at the end of
// every folder in ProcessBooksParallel. Each batch of 20 filenames is a round
// trip to an LLM with a 2s inter-batch delay, so on a library-sized scan the AI
// phase, not the file I/O, is what the scan spends its wall clock on -- and the
// scan holds the "library.scan" ConcurrencyKey the entire time. Moving the work
// to its own queued operation lets the scan finish at disk speed while the LLM
// work drains behind it.
//
// nil by default: internal/scanner has no dependency on the operations registry
// (that would be an import cycle), so internal/server sets this at startup. When
// it is nil -- unit tests, the CLI scanner, any embedder that never wires it --
// the scan keeps the old inline behaviour and nothing changes.
var EnqueueAIParseFn func(ctx context.Context, books []Book) error

// ErrAIParseEnqueueUnavailable is returned by enqueueAIParse when no queue has
// been wired. It is a signal to run inline, not a failure.
var ErrAIParseEnqueueUnavailable = errors.New("ai parse enqueue not configured")

// aiParseEnqueueChunk is how many books ride in one queued operation.
//
// This is deliberately larger than ai_batch_phase.go's batchSize of 20 (which is
// how many filenames go in one LLM prompt). One operation carries several
// prompts' worth of work so the queue does not fill with thousands of
// nearly-empty operations, which is the failure mode that made the v1 op tables
// unreadable. 200 books is roughly 10 LLM batches, i.e. a few minutes of work
// per operation -- coarse enough to be cheap, fine enough to stay resumable and
// to show real progress in the UI.
const aiParseEnqueueChunk = 200

// enqueueAIParse hands the AI candidates to the queue in chunks.
//
// It copies the candidate books out of the caller's slice rather than passing
// indices, because by the time the operation runs the scan that produced `books`
// is long finished and its slice is gone. The copy is what gets serialized into
// the operation's params.
func enqueueAIParse(ctx context.Context, books []Book, candidates []int, scanLog logger.Logger) error {
	enqueue := EnqueueAIParseFn
	if enqueue == nil {
		return ErrAIParseEnqueueUnavailable
	}

	batch := make([]Book, 0, aiParseEnqueueChunk)
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if err := enqueue(ctx, batch); err != nil {
			return err
		}
		scanLog.Info("queued %d book(s) for background AI filename parsing", len(batch))
		batch = make([]Book, 0, aiParseEnqueueChunk)
		return nil
	}

	for _, idx := range candidates {
		if idx < 0 || idx >= len(books) {
			continue
		}
		batch = append(batch, books[idx])
		if len(batch) >= aiParseEnqueueChunk {
			if err := flush(); err != nil {
				return err
			}
		}
	}
	return flush()
}

// RunAIParseForBooks parses a queued batch of books and saves what it filled in.
//
// This is the operation handler's entry point. It builds its own parser from the
// live config rather than inheriting one from the scan, so a batch that sits in
// the queue across a config change uses the backend that is configured when it
// actually runs.
//
// Returns nil when AI parsing is disabled: a queued batch that arrives after the
// operator turned AI off is not an error, it is work that no longer needs doing.
func RunAIParseForBooks(ctx context.Context, books []Book, scanLog logger.Logger) error {
	if len(books) == 0 {
		return nil
	}
	parser, enabled := newAIParser(scanLog)
	if !enabled {
		scanLog.Info("AI parse batch of %d book(s) skipped: AI parsing is not enabled", len(books))
		return nil
	}
	candidates := make([]int, len(books))
	for i := range books {
		candidates[i] = i
	}
	runAIBatchPhase(ctx, parser, books, candidates, scanLog)
	return nil
}

// newAIParser builds the AI fallback parser from the configured LLM backend.
//
// Extracted from ProcessBooksParallel on 2026-08-24 so the inline scan path and
// the queued library.ai-parse operation construct the parser the same way. The
// hazard this guards against is the one described below: a second, divergent
// copy of the backend-routing logic.
//
// Until 2026-08-16 the inline version of this block ignored AIBackend entirely:
// it constructed the cloud OpenAI parser whenever EnableAIParsing was set,
// passing enabled=true unconditionally. The effect was that a deployment with
// ai_backend.llm_mode="disabled" and a local Ollama endpoint configured still
// sent every scan batch to api.openai.com -- which is exactly how the 2026-08-16
// rescan burned its batches against an exhausted-credit key. Routing through
// EffectiveLLMMode makes the operator's setting the thing that decides, and
// keeps this in step with the "llmparser" service in internal/ai/register.go.
func newAIParser(scanLog logger.Logger) (*ai.OpenAIParser, bool) {
	if !config.AppConfig.EnableAIParsing {
		return nil, false
	}
	cfg := &config.AppConfig
	switch cfg.EffectiveLLMMode() {
	case config.AIBackendModeDisabled:
		scanLog.Info("AI parsing skipped: llm_mode is disabled")
	case config.AIBackendModeLocal:
		baseURL := cfg.AIBackend.LocalBaseURL
		if baseURL == "" {
			baseURL = cfg.Embedding.BaseURL
		}
		if baseURL == "" {
			scanLog.Warn("AI parsing enabled with llm_mode=local but no local base URL is configured")
			return nil, false
		}
		parser := ai.NewOpenAIParserWithBaseURL(cfg, "ollama", baseURL, cfg.AIBackend.LocalLLMModel, true)
		if parser != nil && parser.IsEnabled() {
			scanLog.Info("local LLM parser initialized for filename metadata fallback (base_url=%s model=%s)",
				baseURL, cfg.AIBackend.LocalLLMModel)
			return parser, true
		}
		scanLog.Warn("failed to initialize local LLM parser, AI fallback disabled")
	default: // openai, openai-fallback-local
		if cfg.OpenAIAPIKey == "" {
			scanLog.Warn("AI parsing enabled but OpenAI API key is not configured")
			return nil, false
		}
		parser := ai.NewOpenAIParser(cfg, cfg.OpenAIAPIKey, true)
		if parser != nil && parser.IsEnabled() {
			scanLog.Debug("OpenAI parser initialized for filename metadata fallback")
			return parser, true
		}
		scanLog.Warn("failed to initialize OpenAI parser, AI fallback disabled")
	}
	return nil, false
}
