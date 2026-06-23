// file: internal/server/batch_poller.go
// version: 1.6.0
// guid: f8a1b2c3-d4e5-6789-abcd-0123456789ab
// last-edited: 2026-06-23

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"

	"github.com/falkcorp/audiobook-organizer/internal/ai"
	"github.com/falkcorp/audiobook-organizer/internal/ai/aijobs"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logging"
)

// BatchCompletionHandler processes a completed batch.
// It receives the batch ID and the output file ID for downloading results.
type BatchCompletionHandler func(ctx context.Context, batchID string, outputFileID string) error

// BatchPoller is a unified poller that discovers completed OpenAI batches
// tagged with project metadata and routes them to the appropriate handler.
type BatchPoller struct {
	db       database.OperationStore
	parser   *ai.OpenAIParser
	handlers map[string]BatchCompletionHandler

	// processedBatches tracks batch IDs we have already handled to avoid
	// re-processing on subsequent poll cycles.
	processedBatches map[string]bool
	mu               sync.Mutex
}

// NewBatchPoller creates a new BatchPoller.
func NewBatchPoller(db database.OperationStore, parser *ai.OpenAIParser) *BatchPoller {
	return &BatchPoller{
		db:               db,
		parser:           parser,
		handlers:         make(map[string]BatchCompletionHandler),
		processedBatches: make(map[string]bool),
	}
}

// RegisterHandler registers a handler for a specific batch type.
// The type corresponds to the "type" metadata key set during batch creation.
func (bp *BatchPoller) RegisterHandler(batchType string, handler BatchCompletionHandler) {
	bp.handlers[batchType] = handler
}

// Poll queries OpenAI for all project-tagged batches, finds completed ones,
// and dispatches them to registered handlers. Returns the number of batches
// successfully processed.
func (bp *BatchPoller) Poll(ctx context.Context) (int, error) {
	batches, err := bp.parser.ListProjectBatches(ctx)
	if err != nil {
		return 0, err
	}

	processed := 0
	for _, b := range batches {
		if b.Status != "completed" {
			continue
		}

		// Skip already-processed batches
		bp.mu.Lock()
		if bp.processedBatches[b.ID] {
			bp.mu.Unlock()
			continue
		}
		bp.mu.Unlock()

		handler, ok := bp.handlers[b.Type]
		if !ok {
			logging.Warn(ctx, "batch_poller no handler for type %q (batch )", "type", b.Type, "id", b.ID)
			// Mark as processed so we don't warn repeatedly
			bp.mu.Lock()
			bp.processedBatches[b.ID] = true
			bp.mu.Unlock()
			continue
		}

		if err := handler(ctx, b.ID, b.OutputFileID); err != nil {
			logging.Error(ctx, "batch_poller handler for batch failed", "b", b.Type, "b", b.ID, "err", err)
			// Do NOT mark as processed — retry on next poll
		} else {
			bp.mu.Lock()
			bp.processedBatches[b.ID] = true
			bp.mu.Unlock()
			processed++
			logging.Info(ctx, "batch_poller processed batch", "b", b.Type, "b", b.ID)
		}
	}
	return processed, nil
}

// IsProcessed returns whether a batch ID has already been handled.
func (bp *BatchPoller) IsProcessed(batchID string) bool {
	bp.mu.Lock()
	defer bp.mu.Unlock()
	return bp.processedBatches[batchID]
}

// MarkProcessed manually marks a batch as processed (e.g. from external code
// that handled the batch before the poller was created).
func (bp *BatchPoller) MarkProcessed(batchID string) {
	bp.mu.Lock()
	defer bp.mu.Unlock()
	bp.processedBatches[batchID] = true
}

// registerBatchPollerHandlers sets up handlers for all known batch types.
func (s *Server) registerBatchPollerHandlers() {
	if s.batchPoller == nil {
		return
	}

	// author_dedup: download results and store as suggestions
	s.batchPoller.RegisterHandler("author_dedup", func(ctx context.Context, batchID, outputFileID string) error {
		if outputFileID == "" {
			return fmt.Errorf("no output file for batch %s", batchID)
		}
		discoveries, err := s.batchPoller.parser.DownloadBatchResults(ctx, outputFileID)
		if err != nil {
			return fmt.Errorf("download author_dedup results: %w", err)
		}
		logging.Info(ctx, "batch_poller author_dedup batch yielded suggestions", "batchID", batchID, "discoveries_count", len(discoveries))
		// Store results in any operation referencing this batch
		s.storeBatchResultForOperation(batchID, map[string]any{
			"mode":        "batch-full",
			"suggestions": discoveries,
			"batch_id":    batchID,
		})
		return nil
	})

	// author_review: download group results and store
	s.batchPoller.RegisterHandler("author_review", func(ctx context.Context, batchID, outputFileID string) error {
		if outputFileID == "" {
			return fmt.Errorf("no output file for batch %s", batchID)
		}
		suggestions, err := s.batchPoller.parser.DownloadBatchGroupsResults(ctx, outputFileID)
		if err != nil {
			return fmt.Errorf("download author_review results: %w", err)
		}
		logging.Info(ctx, "batch_poller author_review batch yielded suggestions", "batchID", batchID, "suggestions_count", len(suggestions))
		s.storeBatchResultForOperation(batchID, map[string]any{
			"mode":        "batch-groups",
			"suggestions": suggestions,
			"batch_id":    batchID,
		})
		return nil
	})

	// diagnostics: download raw results and store in operation
	s.batchPoller.RegisterHandler("diagnostics", func(ctx context.Context, batchID, outputFileID string) error {
		if outputFileID == "" {
			return fmt.Errorf("no output file for batch %s", batchID)
		}
		rawResults, err := s.batchPoller.parser.DownloadBatchRaw(ctx, outputFileID)
		if err != nil {
			return fmt.Errorf("download diagnostics results: %w", err)
		}
		logging.Info(ctx, "batch_poller diagnostics batch yielded results", "batchID", batchID, "rawResults_count", len(rawResults))
		s.storeBatchResultForOperation(batchID, map[string]any{
			"raw_responses": rawResults,
			"batch_id":      batchID,
		})
		return nil
	})

	// pipeline: delegate to the pipeline manager
	s.batchPoller.RegisterHandler("pipeline", func(ctx context.Context, batchID, outputFileID string) error {
		if s.pipelineManager == nil {
			return fmt.Errorf("pipeline manager not initialized")
		}
		s.pipelineManager.PollBatchPhases(ctx)
		return nil
	})

	// aijobs: unified layer for all bulk-scale LLM work. All such batches
	// carry metadata.type="aijobs"; the per-feature routing happens inside
	// aijobs.Dispatch by looking up the ai_jobs row for this batch_id.
	s.batchPoller.RegisterHandler("aijobs", func(ctx context.Context, batchID, outputFileID string) error {
		if outputFileID == "" {
			return fmt.Errorf("aijobs: no output file for batch %s", batchID)
		}
		raw, err := s.batchPoller.parser.DownloadBatchRaw(ctx, outputFileID)
		if err != nil {
			return fmt.Errorf("aijobs: download batch %s: %w", batchID, err)
		}
		results := make([]aijobs.RowResult, 0, len(raw))
		for _, r := range raw {
			results = append(results, aijobs.RowResult{
				CustomID: r.CustomID,
				Content:  r.Content,
				Err:      r.Error,
			})
		}
		store := database.GetAIJobs(s.Store())
		if store == nil {
			return fmt.Errorf("aijobs: store does not implement AIJobsStore")
		}
		return aijobs.Dispatch(ctx, store, batchID, results)
	})

	s.batchPoller.RegisterHandler("embed_async", func(ctx context.Context, batchID, outputFileID string) error {
		if outputFileID == "" {
			return fmt.Errorf("embed_async: no output file for batch %s", batchID)
		}
		if s.embedClient == nil || s.embeddingStore == nil {
			return fmt.Errorf("embed_async: embedding client or store not available")
		}
		results, err := s.embedClient.DownloadEmbeddingBatchResults(ctx, outputFileID)
		if err != nil {
			return fmt.Errorf("embed_async: download results for batch %s: %w", batchID, err)
		}
		stored := 0
		for _, r := range results {
			if err := s.embeddingStore.Upsert(database.Embedding{
				EntityType: "book",
				EntityID:   r.BookID,
				Vector:     r.Vector,
				Model:      "text-embedding-3-large",
			}); err != nil {
				logging.Warn(ctx, "embed_async upsert book", "r", r.BookID, "err", err)
			} else {
				stored++
			}
		}
		logging.Info(ctx, "embed_async stored / embeddings from batch", "stored", stored, "results_count", len(results), "batchID", batchID)
		return nil
	})
}

// storeBatchResultForOperation finds operations that reference a batch ID
// in their result_data and updates them with the final results.
func (s *Server) storeBatchResultForOperation(batchID string, payload map[string]any) {
	store := s.batchPoller.db
	if store == nil {
		store = s.Store()
	}
	if store == nil {
		slog.Warn("batch_poller no store available to save batch results", "batchID", batchID)
		return
	}

	// Search recent operations for ones referencing this batch ID
	ops, _, err := store.ListOperations(100, 0)
	if err != nil {
		slog.Warn("batch_poller failed to list operations", "err", err)
		return
	}

	for _, op := range ops {
		if op.ResultData == nil || *op.ResultData == "" {
			continue
		}
		// Check if this operation's result_data references our batch ID
		var existing map[string]interface{}
		if json.Unmarshal([]byte(*op.ResultData), &existing) != nil {
			continue
		}
		if existingBatchID, ok := existing["batch_id"].(string); ok && existingBatchID == batchID {
			resultJSON, jErr := json.Marshal(payload)
			if jErr != nil {
				slog.Warn("batch_poller failed to marshal results for batch", "batchID", batchID, "jErr", jErr)
				continue
			}
			if err := store.UpdateOperationResultData(op.ID, string(resultJSON)); err != nil {
				slog.Warn("batch_poller failed to update operation", "op", op.ID, "err", err)
			} else {
				_ = store.UpdateOperationStatus(op.ID, "completed", 100, 100, "Batch results received")
				slog.Info("batch_poller updated operation with batch results", "op", op.ID, "batchID", batchID)
			}
			return
		}
	}
	slog.Info("batch_poller no operation found referencing batch", "batchID", batchID)
}
