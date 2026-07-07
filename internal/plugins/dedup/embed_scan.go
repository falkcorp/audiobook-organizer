// file: internal/plugins/dedup/embed_scan.go
// version: 2.3.0
// guid: e2f3a4b5-c6d7-8901-bcde-f12345678901
// last-edited: 2026-07-07

// T018: embed_scan.go is the canonical implementation for both
// dedup.embed-scan (sync) and dedup.embed-async (async/batch API).
//
// dedup.embed-async delegates here with async=true; dedup.embed-scan
// delegates here with async=false (derived from the JSON params).
//
// EmbedScanParams allows callers to opt into the async batch-API path
// by setting {"async": true}. Omitting the field or passing null defaults
// to false (synchronous per-book embedding).

package dedup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	dedupengine "github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// embedScanConcurrency bounds the number of concurrent EmbedBook calls made
// by the dedup.embed-scan synchronous path (CONC-5). This loop is
// network-bound against a rate-limited embedding backend (OpenAI or a local
// Ollama server), not CPU-bound, so concurrency is a small FIXED knob here —
// deliberately NOT runtime.NumCPU() — to avoid tripping the backend's own
// rate limiting or overwhelming a single-GPU local server.
const embedScanConcurrency = 4

// EmbedScanParams are the JSON parameters accepted by dedup.embed-scan.
// Omitting the struct or leaving the async field absent defaults to async=false.
type EmbedScanParams struct {
	// Async instructs the op to submit books to the OpenAI Batch API instead
	// of embedding them synchronously. Results arrive within 24 hours and are
	// ingested automatically by the batch poller. Default: false.
	Async bool `json:"async"`
}

func (p *Plugin) embedScanDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:              "dedup.embed-scan",
		Plugin:          "dedup",
		DisplayName:     "Embed all books",
		Description:     "Re-embeds every primary book that lacks a fresh embedding. Pass {\"async\":true} to use the OpenAI Batch API instead of synchronous per-book calls.",
		ResumePolicy:    sdk.ResumeRequeue,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "dedup.embed-scan",
		Cancellable:     true,
		Isolate:         false,
		Timeout:         120 * time.Minute,
		Run:             p.runEmbedScan,
	}
}

func (p *Plugin) runEmbedScan(ctx context.Context, raw json.RawMessage, reporter sdk.Reporter) error {
	var params EmbedScanParams
	if len(raw) > 0 {
		// Unmarshal errors are non-fatal: unknown keys are ignored and missing
		// fields default to zero-values (async=false).
		_ = json.Unmarshal(raw, &params)
	}
	return p.runEmbedScanMode(ctx, params.Async, reporter)
}

// runEmbedScanMode is the shared runner for both dedup.embed-scan and
// dedup.embed-async. When async=false it embeds books synchronously via the
// per-book EmbedBook path; when async=true it submits all un-embedded books to
// the OpenAI Batch API and returns immediately.
func (p *Plugin) runEmbedScanMode(ctx context.Context, async bool, reporter sdk.Reporter) error {
	if p.engine == nil {
		return errors.New("dedup engine not available (embedding may be disabled or API key not configured)")
	}

	// The OpenAI Batch API path is openai-only (TOGGLE-2). Under a local or
	// disabled embedding backend, EmbedBooksAsync returns ai.ErrBatchUnsupported;
	// downgrade to the synchronous per-book path here so the op still makes
	// progress instead of erroring out.
	if async {
		if mode := config.AppConfig.EffectiveEmbeddingMode(); mode != config.AIBackendModeOpenAI {
			reporter.Logger().Warn("dedup embed-scan: async batch requested but effective embedding backend is not openai — downgrading to synchronous embedding",
				"mode", mode)
			async = false
		}
	}

	if async {
		collectProg := sdk.NewProgress(reporter, 0)
		collectProg.Start("Collecting un-embedded books...")

		batchID, count, err := p.engine.EmbedBooksAsync(ctx)
		if err != nil {
			return fmt.Errorf("submit embedding batch: %w", err)
		}
		if count == 0 {
			collectProg.Done("All books already embedded — nothing to submit")
			return nil
		}

		prog := sdk.NewProgress(reporter, count)
		prog.Start(fmt.Sprintf("Submitting %d books to batch API...", count))
		prog.StepN(count, fmt.Sprintf("Submitted %d / %d books", count, count))
		prog.Finalize("writing results...")
		prog.Done(fmt.Sprintf("Submitted %d books to OpenAI Batch API (batch_id=%s). Results will be ingested automatically within 24h.", count, batchID))
		return nil
	}

	// Synchronous path: embed books one by one (batched by engine.EmbedBook).
	loadProg := sdk.NewProgress(reporter, 0)
	loadProg.Start("Loading books for embedding...")

	books, err := p.store.GetAllBooksCore(0, 0)
	if err != nil {
		return fmt.Errorf("load books: %w", err)
	}
	total := len(books)
	if total == 0 {
		loadProg.Done("No books to embed")
		return nil
	}

	prog := sdk.NewProgress(reporter, total)
	prog.Start(fmt.Sprintf("Embedding books: 0 / %d", total))

	// Counters are mutated from up to embedScanConcurrency worker goroutines
	// (registry.RunItems fans the per-book closure out over a bounded pool),
	// so they must be atomic rather than plain ints.
	var embedded, cached, skipped, errs atomic.Int64

	runErr := registry.RunItems(ctx, reporter, books, func(ctx context.Context, book database.BookCore) error {
		status, embedErr := p.engine.EmbedBook(ctx, book.ID)
		if embedErr != nil {
			reporter.Logger().Error("embed error", "book_id", book.ID, "error", embedErr)
			errs.Add(1)
		} else {
			switch status {
			case dedupengine.EmbedStatusEmbedded:
				embedded.Add(1)
			case dedupengine.EmbedStatusCached:
				cached.Add(1)
			default:
				skipped.Add(1)
			}
		}
		return nil
	}, registry.RunItemsOptions{
		Concurrency: embedScanConcurrency,
		Label: func(i, t int) string {
			return fmt.Sprintf("Embedding books: %d / %d (new=%d cached=%d skipped=%d errors=%d)",
				i+1, t, embedded.Load(), cached.Load(), skipped.Load(), errs.Load())
		},
	})
	if runErr != nil {
		return runErr
	}

	prog.Finalize("writing results...")
	prog.Done(fmt.Sprintf("Embedding complete — %d new, %d cached, %d skipped, %d errors (of %d books)",
		embedded.Load(), cached.Load(), skipped.Load(), errs.Load(), total))
	return nil
}
