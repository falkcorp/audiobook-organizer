// file: internal/plugins/dedup/reembed_embeddings.go
// version: 1.0.0
// guid: 9d8c7b6a-5e4f-3a2b-1c0d-9e8f7a6b5c4d
// last-edited: 2026-06-14

// Package dedup — op dedup.reembed-embeddings.
//
// Re-embeds the book corpus when the configured embedding model changes
// (e.g. switching from OpenAI text-embedding-3-large [3072-dim] to a local
// Ollama bge-m3 [1024-dim]). A model switch invalidates every stored vector:
// the dimensions differ and the vector spaces are not comparable, so the old
// vectors must be replaced before the embedding-based dedup layer is re-enabled.
//
// # Why a dedicated op (not just EmbedBooks)
//
// EmbedBooks/prepBookEmbed short-circuit on a TextHash cache hit — they do NOT
// compare the stored vector's model. A book whose title/author text is
// unchanged would therefore keep its OLD-model vector (wrong dimension) and
// even mirror it into the in-memory chromem ANN store. This op deletes the
// stale entity embedding first, forcing a genuine re-embed at the new model.
//
// # Resumability
//
// Stored entity embeddings are tagged with the model that produced them
// (EmbedBooks records de.embedClient.Model()). The scan phase skips any book
// already at the target model, so a re-run after interruption only processes
// the remainder. ResumePolicy is ResumeRequeue.
//
// # Cutover sequence (operational)
//
//	1. PUT /config {embedding_model, embedding_dimensions, embedding_base_url,
//	   dedup_embeddings_enabled:false}   ← Layer 2 OFF during re-embed
//	2. restart (chromem rebuilds at the new dim; embed client points at backend)
//	3. POST /operations/v2 {def_id:"dedup.reembed-embeddings"}              # dry-run
//	4. POST /operations/v2 {def_id:"dedup.reembed-embeddings",params:{apply:true}}
//	5. restart (hydrate chromem from the fresh vectors) → set dedup_embeddings_enabled:true
//
// Usage:
//
//	POST /api/v1/operations/v2  {"def_id":"dedup.reembed-embeddings"}                       # dry-run
//	POST /api/v1/operations/v2  {"def_id":"dedup.reembed-embeddings","params":{"apply":true}}  # execute
//
// Scope: books only. Author embeddings are a separate, much smaller set and
// live in a distinct chromem collection ("author"); re-embedding them is left
// to a follow-up.

package dedup

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	dedupengine "github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

const (
	// defaultReembedBatchSize is the number of books embedded per backend call.
	// One batch = one /v1/embeddings request; bge-m3 on Ollama handles batched
	// input arrays. Kept modest so a single failed request loses little work.
	defaultReembedBatchSize = 64
	maxReembedBatchSize     = 512
)

// reembedEmbeddingsParams are the JSON parameters accepted by the op.
type reembedEmbeddingsParams struct {
	// Apply, if true, deletes stale vectors and writes fresh ones. Default
	// false (dry-run) — the op counts what it would re-embed and returns.
	Apply bool `json:"apply"`
	// BatchSize overrides the per-request book count (1..maxReembedBatchSize).
	// Zero uses defaultReembedBatchSize.
	BatchSize int `json:"batch_size"`
}

// reembedEmbeddingsDef returns the OperationDef for dedup.reembed-embeddings.
func (p *Plugin) reembedEmbeddingsDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:          "dedup.reembed-embeddings",
		Plugin:      "dedup",
		DisplayName: "Re-embed corpus (embedding model change)",
		Description: "Re-embeds every book whose stored embedding was produced by a " +
			"different model than the one currently configured. Required after switching " +
			"embedding models (e.g. OpenAI 3072-dim → local bge-m3 1024-dim) because the " +
			"vector spaces are incompatible. Dry-run by default (pass apply=true to execute). " +
			"Resumable — re-running skips books already at the target model.",
		ResumePolicy:    sdk.ResumeRequeue,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "dedup.reembed-embeddings",
		Cancellable:     true,
		Isolate:         false,
		// Re-embedding the full corpus through a CPU-bound local backend can run
		// for hours; allow generous headroom. Cancellation + resume cover the
		// case where it genuinely needs longer.
		Timeout: 12 * time.Hour,
		Capabilities: []sdk.Capability{
			sdk.CapLibraryRead,
			sdk.CapLibraryWrite,
		},
		Run: p.runReembedEmbeddings,
	}
}

// runReembedEmbeddings implements the reembed-embeddings op.
func (p *Plugin) runReembedEmbeddings(ctx context.Context, rawParams json.RawMessage, reporter sdk.Reporter) error {
	if p.engine == nil {
		return fmt.Errorf("dedup engine not available (embeddings disabled?)")
	}
	if p.embeddingStore == nil {
		return fmt.Errorf("embedding store not available")
	}
	if p.store == nil {
		return fmt.Errorf("main store not available")
	}

	// The target model is whatever the wired embedding client is pinned to —
	// the authoritative value EmbedBooks will tag fresh vectors with.
	targetModel := p.engine.EmbeddingModel()
	if targetModel == "" {
		return fmt.Errorf("no embedding client configured (need OpenAIAPIKey + EmbeddingEnabled; " +
			"for a local backend also set embedding_base_url + embedding_model)")
	}

	// --- Parse params ---
	var params reembedEmbeddingsParams
	if len(rawParams) > 0 {
		if err := json.Unmarshal(rawParams, &params); err != nil {
			return fmt.Errorf("parse params: %w", err)
		}
	}
	batchSize := params.BatchSize
	if batchSize <= 0 {
		batchSize = defaultReembedBatchSize
	}
	if batchSize > maxReembedBatchSize {
		batchSize = maxReembedBatchSize
	}

	reporter.Logger().Info("reembed-embeddings start",
		"target_model", targetModel, "apply", params.Apply, "batch_size", batchSize)

	// --- Phase 1: scan, partition books by stored model ---
	_ = reporter.UpdateProgress(0, 2, "Scanning books for stale-model embeddings…")

	const scanBatch = 500
	var (
		totalBooks int
		current    int // already at target model
		toReembed  []string
	)
	offset := 0
	for {
		if reporter.IsCanceled() {
			return context.Canceled
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		batch, err := p.store.GetAllBooks(scanBatch, offset)
		if err != nil {
			return fmt.Errorf("get all books at offset %d: %w", offset, err)
		}
		if len(batch) == 0 {
			break
		}
		for i := range batch {
			b := &batch[i]
			totalBooks++
			existing, getErr := p.embeddingStore.Get("book", b.ID)
			if getErr == nil && existing != nil && existing.Model == targetModel {
				current++
				continue
			}
			toReembed = append(toReembed, b.ID)
		}
		if len(batch) < scanBatch {
			break
		}
		offset += scanBatch
	}

	needCount := len(toReembed)
	summary := fmt.Sprintf("%d books total, %d already at %q, %d need re-embedding",
		totalBooks, current, targetModel, needCount)
	reporter.Logger().Info("reembed-embeddings: scan complete",
		"total_books", totalBooks, "already_current", current,
		"need_reembed", needCount, "target_model", targetModel)

	if !params.Apply {
		_ = reporter.UpdateProgress(2, 2, fmt.Sprintf(
			"Dry-run — %d books would be re-embedded with %q (%d already current). Pass apply=true to execute.",
			needCount, targetModel, current))
		reporter.Logger().Info("reembed-embeddings: dry-run only; no changes written",
			"would_reembed", needCount)
		return nil
	}

	// --- Phase 2: delete stale vectors + re-embed, in batches ---
	_ = reporter.UpdateProgress(1, 2, fmt.Sprintf("Re-embedding %d books with %q…", needCount, targetModel))

	var embedded, skipped int
	for start := 0; start < needCount; start += batchSize {
		if reporter.IsCanceled() {
			reporter.Logger().Warn("reembed-embeddings: canceled mid-run; re-run to continue",
				"embedded_so_far", embedded)
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		end := start + batchSize
		if end > needCount {
			end = needCount
		}
		chunk := toReembed[start:end]

		// Delete the stale entity embedding first so prepBookEmbed cannot
		// TextHash-cache-hit the old-model vector and skip the re-embed.
		for _, id := range chunk {
			if err := p.embeddingStore.Delete("book", id); err != nil {
				reporter.Logger().Warn("reembed-embeddings: delete stale embedding",
					"book_id", id, "err", err)
			}
		}

		results, err := p.engine.EmbedBooks(ctx, chunk)
		if err != nil {
			// Whole-batch failure (e.g. backend unreachable). Fail visibly so the
			// operator fixes the backend and re-runs; already-embedded books are
			// skipped on the re-run (they now carry the target model).
			reporter.Logger().Error("reembed-embeddings: embed batch failed",
				"chunk_start", start, "size", len(chunk), "err", err)
			return fmt.Errorf("embed batch at offset %d (%d books): %w", start, len(chunk), err)
		}
		for _, id := range chunk {
			if results[id] == dedupengine.EmbedStatusEmbedded {
				embedded++
			} else {
				// Cached (shouldn't happen post-delete), SkippedNonPrimary,
				// SkippedEmptyTitle, or omitted due to a per-book error.
				skipped++
			}
		}

		_ = reporter.UpdateProgress(1, 2, fmt.Sprintf(
			"Re-embedded %d/%d (%d embedded, %d skipped)…", end, needCount, embedded, skipped))
	}

	_ = reporter.UpdateProgress(2, 2, fmt.Sprintf(
		"Complete — %d embedded, %d skipped of %d candidates (target model %q). %s",
		embedded, skipped, needCount, targetModel, summary))
	reporter.Logger().Info("reembed-embeddings: complete",
		"embedded", embedded, "skipped", skipped, "candidates", needCount,
		"target_model", targetModel)
	return nil
}
