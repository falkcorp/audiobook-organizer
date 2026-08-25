// file: internal/scanner/ai_batch_phase.go
// version: 1.1.0
// guid: dc72fe25-f58e-4135-88f4-7f842e7e9a7a
// last-edited: 2026-08-24

package scanner

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/falkcorp/audiobook-organizer/internal/ai"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

// aiBatchParser is the one AI capability this phase needs. Named as a consumer
// interface so the phase can be driven by a fake in tests: the production
// implementation is a concrete *ai.OpenAIParser built inline, which left the
// failure-abort logic below untestable.
type aiBatchParser interface {
	ParseBatch(ctx context.Context, filenames []string) ([]*ai.ParsedMetadata, error)
}

// Batch AI parsing phase.
//
// This was a strictly serial loop: one batch of 20 at a time, each taking up
// to 30s, plus a 2s sleep between them -- and it runs once per 500-book
// chunk, so on a 40,000-book library it dominated the entire scan. Measured
// on the reference deployment at ~30s per batch and ~8 batches per chunk,
// that is roughly 5 hours of the scan's wall clock spent waiting on one
// in-flight request at a time.
//
// CLAUDE.md's concurrency rule names exactly this shape: a whole-library
// loop doing per-item network calls must be bounded-concurrent, "a smaller
// fixed concurrency for network-bound work that respects the target's own
// rate limits". The per-batch delay is kept and now applies per worker, so
// the aggregate request rate rises by the worker count rather than becoming
// unbounded.
// save is how a parsed book is persisted. It is a parameter, not the package
// saveBook global, because the two callers need genuinely different writes: the
// inline scan path passes saveBook (the full scan write path, correct there),
// while the queued library.ai-parse operation passes saveAIFieldsToPrimary,
// which resolves the row fresh and touches only the AI-filled fields. Sharing
// saveBook between them puts the queued batch's fields on a row organize has
// already demoted. See saveAIFieldsToPrimary for the full reasoning.
func runAIBatchPhase(ctx context.Context, parser aiBatchParser, books []Book, candidates []int, log logger.Logger, save func(context.Context, *Book) error) {
	const batchSize = 20
	const delayBetweenBatches = 2 * time.Second
	const maxTotalFailures = 3

	totalBatches := (len(candidates) + batchSize - 1) / batchSize
	log.Info("AI batch parsing %d books in %d batches of %d, %d at a time",
		len(candidates), totalBatches, batchSize, aiBatchWorkers)

	// The serial version counted CONSECUTIVE failures. Under concurrency
	// "consecutive" has no meaning -- batches finish out of order -- so the
	// guard becomes a total count. It is the same intent (stop when the
	// backend is down rather than grinding through every batch) expressed in
	// terms that survive parallelism, and it is strictly more eager: 3
	// failures anywhere aborts, where before 3 had to land in a row.
	var failures atomic.Int64
	var started atomic.Int64
	aborted := make(chan struct{})
	var abortOnce sync.Once
	abort := func() { abortOnce.Do(func() { close(aborted) }) }

	aiGroup, aiGroupCtx := errgroup.WithContext(ctx)
	aiGroup.SetLimit(aiBatchWorkers)

	for start := 0; start < len(candidates); start += batchSize {
		end := start + batchSize
		if end > len(candidates) {
			end = len(candidates)
		}
		start, end := start, end
		batch := candidates[start:end]

		aiGroup.Go(func() error {
			select {
			case <-aborted:
				return nil
			case <-aiGroupCtx.Done():
				return nil
			default:
			}

			// Report BEFORE the call, not after: a batch takes up to 30s and
			// a failing one takes longer, so reporting only on success
			// leaves gaps that exceed the watchdog's ProgressTimeout. This
			// phase is why library.scan could complete its entire file walk
			// and still be canceled for inactivity.
			batchNum := int(started.Add(1))
			log.UpdateProgress(batchNum, totalBatches,
				fmt.Sprintf("AI parsing batch %d/%d (%d books)", batchNum, totalBatches, len(batch)))

			filenames := make([]string, len(batch))
			for i, idx := range batch {
				filenames[i] = filepath.Base(books[idx].FilePath)
			}

			aiCtx, cancel := context.WithTimeout(aiGroupCtx, 30*time.Second)
			results, aiErr := parser.ParseBatch(aiCtx, filenames)
			cancel()

			if aiErr != nil {
				log.Warn("AI batch parsing failed (batch %d-%d): %v", start, end, aiErr)

				// A permanent backend state -- no credits, revoked key,
				// quota exhausted -- will not clear by the next batch, so
				// stop the whole phase on the first one rather than retrying
				// it for every remaining batch.
				if isPermanentAIFailure(aiErr) {
					log.Warn("AI batch parsing disabled for this scan after a non-retryable error at batch %d/%d: %v — "+
						"the remaining books keep their filename-derived metadata",
						batchNum, totalBatches, aiErr)
					abort()
					return nil
				}

				if failures.Add(1) >= maxTotalFailures {
					log.Warn("AI batch parsing disabled for this scan: %d batch failures by batch %d/%d — "+
						"the remaining books keep their filename-derived metadata",
						failures.Load(), batchNum, totalBatches)
					abort()
					return nil
				}

				// Rate-limit shaped error: back off before this worker takes
				// its next batch.
				time.Sleep(5 * time.Second)
				return nil
			}

			// Each batch owns a disjoint slice of candidates, so no two
			// workers write the same books[idx].
			for i, idx := range batch {
				if i >= len(results) || results[i] == nil {
					continue
				}
				aiMeta := results[i]
				if books[idx].Title == "" && aiMeta.Title != "" {
					books[idx].Title = aiMeta.Title
				}
				if books[idx].Author == "" && aiMeta.Author != "" {
					books[idx].Author = aiMeta.Author
				}
				if books[idx].Series == "" && aiMeta.Series != "" {
					books[idx].Series = aiMeta.Series
				}
				if books[idx].Position == 0 && aiMeta.SeriesNum > 0 {
					books[idx].Position = aiMeta.SeriesNum
				}
				if books[idx].Narrator == "" && aiMeta.Narrator != "" {
					books[idx].Narrator = aiMeta.Narrator
				}
				if books[idx].Publisher == "" && aiMeta.Publisher != "" {
					books[idx].Publisher = aiMeta.Publisher
				}

				if saveErr := save(ctx, &books[idx]); saveErr != nil {
					log.Warn("failed to re-save AI-enriched book %s: %v", books[idx].FilePath, saveErr)
				}
			}

			log.Info("AI batch %d-%d complete (%d results)", start, end, len(results))
			time.Sleep(delayBetweenBatches)
			return nil
		})
	}

	// Every callback returns nil -- a failed batch degrades that batch, it
	// does not fail the scan -- so this only surfaces a context error.
	if err := aiGroup.Wait(); err != nil && ctx.Err() == nil {
		log.Warn("AI batch parsing phase ended early: %v", err)
	}
}
