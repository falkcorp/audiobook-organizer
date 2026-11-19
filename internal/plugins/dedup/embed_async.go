// file: internal/plugins/dedup/embed_async.go
// version: 2.3.0
// guid: b1c2d3e4-f5a6-7890-bcde-f01234567890
// last-edited: 2026-11-18

// T018: embed_async.go is a thin wrapper that delegates to runEmbedScanMode
// (in embed_scan.go) with async=true.
//
// Deprecated (T018): Both op IDs remain registered for one release so that
// existing callers that trigger "dedup.embed-async" by ID continue to work.
// New callers should use dedup.embed-scan with {"async": true}.

package dedup

import (
	"context"
	"encoding/json"
	"time"

	"github.com/falkcorp/mediacore/tasks"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// embedAsyncKind is rule R3's native kind for dedup.embed-async.
const embedAsyncKind = tasks.Kind("books.dedup.embed-async")

// embedAsyncHelp is the definition's description, HEAD's and the native kind's
// Help (rule R5: verbatim).
const embedAsyncHelp = "Deprecated: delegates to dedup.embed-scan with async=true. Submits all un-embedded books to the OpenAI Batch API. Results arrive within 24 hours and are ingested automatically."

func (p *Plugin) embedAsyncDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:              "dedup.embed-async",
		Liveness:        sdk.LivenessRunItems,
		Plugin:          "dedup",
		DisplayName:     "Embed books async (batch API) [deprecated — use embed-scan with async:true]",
		Description:     embedAsyncHelp,
		ResumePolicy:    sdk.ResumeRequeue,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "dedup.embed-async",
		Cancellable:     false,
		Isolate:         false,
		Timeout:         10 * time.Minute,
		Schedule:        nil, // manual invocation only — nightly cron retired; OpenAI Batch API is quota-dead under the Ollama cutover, see docs/status/2026-07-02-local-cutover-and-matching.md
		Run:             p.runEmbedAsync,
	}
}

// runEmbedAsync is the v2 shell: embedAsyncCore (the skip check, then
// embedScanCore with async=true) over HEAD's env. Keeping this as a named
// method (not an inline closure) preserves the method expression stored in
// OperationDef.Run so sdk.Registry can identify and invoke it correctly.
func (p *Plugin) runEmbedAsync(ctx context.Context, _ json.RawMessage, reporter sdk.Reporter) error {
	_, err := embedAsyncCore(ctx, p.headEmbedScanEnv(true, reporter))
	return err
}

// embedAsyncCore is the body shared by both shells. Skip if the backend is not
// OpenAI — the OpenAI Batch API is quota-dead now that Ollama/bge-m3 is the
// primary embedding backend — before anything else, the engine check included.
func embedAsyncCore(ctx context.Context, env embedScanEnv) (embedScanResult, error) {
	if config.AppConfig.Embedding.BaseURL != "" || config.AppConfig.OpenAIAPIKey == "" {
		logger.WarnCtx(ctx, "dedup.embed-async skipped: no OpenAI backend configured (Ollama/local embedding is primary)", "base_url", config.AppConfig.Embedding.BaseURL)
		return embedScanResult{}, nil
	}
	env.Async = true
	return embedScanCore(ctx, env)
}
