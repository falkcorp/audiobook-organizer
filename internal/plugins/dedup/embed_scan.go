// file: internal/plugins/dedup/embed_scan.go
// version: 2.6.1
// guid: e2f3a4b5-c6d7-8901-bcde-f12345678901
// last-edited: 2026-11-19

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

	"github.com/falkcorp/mediacore/tasks"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	dedupengine "github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// embedScanConcurrency bounds the number of concurrent EmbedBook calls made
// by the dedup.embed-scan synchronous path (CONC-5) when embeddings are NOT
// pool-routed. This loop is network-bound against a rate-limited embedding
// backend (OpenAI or a local Ollama server), not CPU-bound, so concurrency is
// a small FIXED knob here — deliberately NOT runtime.NumCPU() — to avoid
// tripping the backend's own rate limiting or overwhelming a single-GPU local
// server. With ai_endpoints_routing on, the scan instead uses the pool's
// routed capacity (Engine.EmbedConcurrency): the sum of the embed endpoints'
// own concurrency caps, so every host gets work and none is overloaded.
const embedScanConcurrency = 4

// embedScanKind is rule R3's native kind for dedup.embed-scan.
const embedScanKind = tasks.Kind("books.dedup.embed-scan")

// embedScanHelp is the definition's description, HEAD's and the native kind's
// Help (rule R5: verbatim).
const embedScanHelp = "Re-embeds every primary book that lacks a fresh embedding. Pass {\"async\":true} to use the OpenAI Batch API instead of synchronous per-book calls."

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
		Liveness:        sdk.LivenessRunItems,
		Plugin:          "dedup",
		DisplayName:     "Embed all books",
		Description:     embedScanHelp,
		ResumePolicy:    sdk.ResumeRequeue,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "dedup.embed-scan",
		Cancellable:     true,
		Isolate:         false,
		Timeout:         120 * time.Minute,
		Run:             p.runEmbedScan,
	}
}

// embedScanStore is the one read the synchronous path takes (rule R10): the
// book list the per-book loop walks.
type embedScanStore interface {
	GetAllBooksCore(limit, offset int) ([]database.BookCore, error)
}

// embedScanEngine is the engine surface the embed cores take: every method is
// one the HEAD body called on p.engine, with its signature. The engine writes
// the embedding rows through its own store, which in a native run is built over
// the run's gated writer by EmbeddingKindDeps.NewEngine (seams.d/F5-04.md).
type embedScanEngine interface {
	EmbedBook(ctx context.Context, bookID string) (dedupengine.EmbedStatus, error)
	EmbedBooksAsync(ctx context.Context) (batchID string, count int, err error)
	EmbedConcurrency(fallback int) int
}

// embedScanEnv is what the embed-scan and embed-async cores take from their
// shell (rules R2 and R10). Async is HEAD's async param (embed-async: always
// true); the core still downgrades it to the synchronous path under a non-OpenAI
// embedding backend.
type embedScanEnv struct {
	kindEnv
	Async  bool
	Store  embedScanStore
	Engine embedScanEngine
}

// embedScanResult is the native run's result: what the synchronous pass
// counted, or what the async submission sent. HEAD stored no result, so its
// shell drops it.
type embedScanResult struct {
	Async     bool   `json:"async"`
	BatchID   string `json:"batch_id,omitempty"`
	Submitted int    `json:"submitted"`
	Total     int    `json:"total"`
	Embedded  int    `json:"embedded"`
	Cached    int    `json:"cached"`
	Skipped   int    `json:"skipped"`
	Errors    int    `json:"errors"`
}

// embedScanParallelism is the synchronous loop's pool (rule R4): HEAD sized it
// from Engine.EmbedConcurrency(embedScanConcurrency), a network-bound fan-out
// against rate-limited embedding endpoints, so it is a Fixed worker count, never
// CPU.
func embedScanParallelism(workers int) tasks.Parallelism {
	return tasks.Fixed(workers, "network-bound against rate-limited embedding endpoints; sized from the routed pool (Engine.EmbedConcurrency), default 4")
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

// runEmbedScanMode is the v2 shell shared by dedup.embed-scan and
// dedup.embed-async: HEAD's nil-engine refusal over embedScanCore.
func (p *Plugin) runEmbedScanMode(ctx context.Context, async bool, reporter sdk.Reporter) error {
	_, err := embedScanCore(ctx, p.headEmbedScanEnv(async, reporter))
	return err
}

// headEmbedScanEnv is HEAD's env for both embed shells. A plugin with no engine
// hands the core a nil Engine (not a typed-nil *Engine in the interface), which
// the core refuses with HEAD's message.
func (p *Plugin) headEmbedScanEnv(async bool, reporter sdk.Reporter) embedScanEnv {
	env := embedScanEnv{
		kindEnv: headEnv(reporter, true),
		Async:   async,
		Store:   headEmbeddingReads{emb: p.embeddingStore, books: p.store},
	}
	if p.engine != nil {
		env.Engine = p.engine
	}
	return env
}

// embedScanCore is the body of runEmbedScanMode, shared by both shells and by
// dedup.embed-async. When Async is false it embeds books synchronously via the
// per-book EmbedBook path; when Async is true it submits all un-embedded books
// to the OpenAI Batch API and returns immediately.
func embedScanCore(ctx context.Context, env embedScanEnv) (embedScanResult, error) {
	if env.Engine == nil {
		return embedScanResult{}, errors.New("dedup engine not available (embedding may be disabled or API key not configured)")
	}
	async := env.Async
	// The OpenAI Batch API path is openai-only (TOGGLE-2). Under a local or
	// disabled embedding backend, EmbedBooksAsync returns ai.ErrBatchUnsupported;
	// downgrade to the synchronous per-book path here so the op still makes
	// progress instead of erroring out.
	if async {
		if mode := config.AppConfig.EffectiveEmbeddingMode(); mode != config.AIBackendModeOpenAI {
			env.Logger.Warn("dedup embed-scan: async batch requested but effective embedding backend is not openai — downgrading to synchronous embedding",
				"mode", mode)
			async = false
		}
	}

	if async {
		collectProg := newProgress(env.kindEnv, 0)
		collectProg.Start("Collecting un-embedded books...")

		batchID, count, err := env.Engine.EmbedBooksAsync(ctx)
		if err != nil {
			return embedScanResult{}, fmt.Errorf("submit embedding batch: %w", err)
		}
		if count == 0 {
			collectProg.Done("All books already embedded — nothing to submit")
			return embedScanResult{Async: true}, nil
		}

		prog := newProgress(env.kindEnv, count)
		prog.Start(fmt.Sprintf("Submitting %d books to batch API...", count))
		prog.StepN(count, fmt.Sprintf("Submitted %d / %d books", count, count))
		prog.Finalize("writing results...")
		prog.Done(fmt.Sprintf("Submitted %d books to OpenAI Batch API (batch_id=%s). Results will be ingested automatically within 24h.", count, batchID))
		return embedScanResult{Async: true, BatchID: batchID, Submitted: count}, nil
	}

	// Synchronous path: embed books one by one (batched by engine.EmbedBook).
	loadProg := newProgress(env.kindEnv, 0)
	loadProg.Start("Loading books for embedding...")

	books, err := env.Store.GetAllBooksCore(0, 0)
	if err != nil {
		return embedScanResult{}, fmt.Errorf("load books: %w", err)
	}
	total := len(books)
	if total == 0 {
		loadProg.Done("No books to embed")
		return embedScanResult{}, nil
	}

	prog := newProgress(env.kindEnv, total)
	prog.Start(fmt.Sprintf("Embedding books: 0 / %d", total))

	// Counters are mutated from up to `workers` worker goroutines (the per-book
	// closure is fanned out over a bounded pool), so they must be atomic rather
	// than plain ints. The Label closure reads them, so it reads them atomically
	// too (a counter label is HEAD-only: the native pool's label is pure).
	var embedded, cached, skipped, errs atomic.Int64

	// Sized once at start from the routed pool (or the legacy knob); a pool
	// edit mid-scan applies to the next run, while the dispatcher still
	// enforces each endpoint's live cap on every call.
	workers := env.Engine.EmbedConcurrency(embedScanConcurrency)
	env.Logger.Info("embed-scan workers", "workers", workers)

	runErr := env.Items(ctx, embedScanParallelism(workers), total, kindItemsOptions{
		// HEAD's own count: it applied no upper bound, and Fixed's ceiling must not
		// narrow a routed pool above it.
		HeadWorkers: workers,
		Label: func(i, t int) string {
			return fmt.Sprintf("Embedding books: %d / %d (new=%d cached=%d skipped=%d errors=%d)",
				i+1, t, embedded.Load(), cached.Load(), skipped.Load(), errs.Load())
		},
	}, func(ctx context.Context, i int) error {
		book := books[i]
		status, embedErr := env.Engine.EmbedBook(ctx, book.ID)
		if embedErr != nil {
			env.Logger.Error("embed error", "book_id", book.ID, "error", embedErr)
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
	})
	if runErr != nil {
		return embedScanResult{}, runErr
	}

	prog.Finalize("writing results...")
	prog.Done(fmt.Sprintf("Embedding complete — %d new, %d cached, %d skipped, %d errors (of %d books)",
		embedded.Load(), cached.Load(), skipped.Load(), errs.Load(), total))
	return embedScanResult{
		Total: total, Embedded: int(embedded.Load()), Cached: int(cached.Load()),
		Skipped: int(skipped.Load()), Errors: int(errs.Load()),
	}, nil
}
