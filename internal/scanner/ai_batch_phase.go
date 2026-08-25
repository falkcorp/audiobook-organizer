// file: internal/scanner/ai_batch_phase.go
// version: 1.2.0
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
//
// The returned summary is the only honest record of what happened. Every
// failure in this phase is a log.Warn and a `return nil` -- deliberately, since
// a dead LLM must not fail the scan -- which means the phase looks identical
// from the outside whether it parsed every book or aborted on batch 1 of 10.
// The queued library.ai-parse operation reports this summary into its own
// operation record so a run that did nothing cannot show up green.
func runAIBatchPhase(ctx context.Context, parser aiBatchParser, books []Book, candidates []int, log logger.Logger, save func(context.Context, *Book) (string, error)) AIPhaseSummary {
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
	var batchesOK, booksParsed, savesFailed atomic.Int64
	var abortedPermanent, abortedThreshold atomic.Bool
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
					abortedPermanent.Store(true)
					log.Warn("AI batch parsing disabled for this scan after a non-retryable error at batch %d/%d: %v — "+
						"the remaining books keep their filename-derived metadata",
						batchNum, totalBatches, aiErr)
					abort()
					return nil
				}

				if failures.Add(1) >= maxTotalFailures {
					abortedThreshold.Store(true)
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

			// Each batch owns a disjoint slice of candidates, so no two workers
			// write the same books[idx].
			//
			// That is true of the in-memory slice and NOT of the database row.
			// The queued path's saver redirects a demoted row to its version
			// group's primary, so two hash-duplicate sources in one batch
			// resolve to the same primary and two workers do a concurrent
			// whole-row read-modify-write on it: last writer wins and the
			// other's field is lost. Known and unfixed -- it needs row-level
			// serialization, not a change here.
			for i, idx := range batch {
				var aiMeta *ai.ParsedMetadata
				if i < len(results) {
					aiMeta = results[i]
				}
				if aiMeta == nil {
					// No result for this filename. Still saved below: the save
					// is what resolves the row and reports the path to stamp,
					// and a book the LLM could not parse must still be recorded
					// as attempted or it is re-read and re-queued every scan.
					aiMeta = &ai.ParsedMetadata{}
				}
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

				stampPath, saveErr := save(ctx, &books[idx])
				if saveErr != nil {
					savesFailed.Add(1)
					log.Warn("failed to re-save AI-enriched book %s: %v", books[idx].FilePath, saveErr)
				}
				booksParsed.Add(1)

				// A parse has now been ATTEMPTED for this book, which is what
				// earns the scan-cache stamp the scan deliberately withheld
				// when it nominated the book (see the stamp site in
				// ProcessBooksParallel).
				//
				// Stamped on the path the SAVER reports, which is the row's
				// current one -- the path in this batch's params may have been
				// renamed out from under it by organize. An empty string means
				// the save could not resolve a row at all; leaving that
				// unstamped is right, since nothing was recorded anywhere.
				if stampPath != "" {
					writeBackScanCache(stampPath, nil, log)
				}
			}

			batchesOK.Add(1)
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

	return AIPhaseSummary{
		BooksNominated:   len(candidates),
		BatchesTotal:     totalBatches,
		BatchesOK:        int(batchesOK.Load()),
		BatchesFailed:    int(failures.Load()),
		BooksParsed:      int(booksParsed.Load()),
		SavesFailed:      int(savesFailed.Load()),
		AbortedPermanent: abortedPermanent.Load(),
		AbortedThreshold: abortedThreshold.Load(),
	}
}

// AIPhaseSummary is what runAIBatchPhase actually did, as opposed to what its
// (always nil) error return suggests.
type AIPhaseSummary struct {
	// Disabled means the batch never ran because AI parsing is switched off.
	// Distinct from a zero-count run, which means it ran and found nothing.
	Disabled         bool
	BooksNominated   int
	BatchesTotal     int
	BatchesOK        int
	BatchesFailed    int
	BooksParsed      int
	SavesFailed      int
	AbortedPermanent bool
	AbortedThreshold bool
}

// Aborted reports whether the phase stopped short, leaving nominated books
// unparsed. This -- not "did any field change" -- is what makes a queued run a
// failure: a healthy library where every candidate was already filled in by
// another path legitimately changes nothing, and must not be reported as an
// error.
func (s AIPhaseSummary) Aborted() bool { return s.AbortedPermanent || s.AbortedThreshold }

// String renders the summary in the same shape as the scan's own
// "scan summary:" line, which is the established idiom for per-run counters
// in this package.
func (s AIPhaseSummary) String() string {
	if s.Disabled {
		return fmt.Sprintf("ai parse summary: skipped %d book(s), AI parsing is not enabled", s.BooksNominated)
	}
	msg := fmt.Sprintf("ai parse summary: %d/%d book(s) parsed in %d/%d batches; %d batch failure(s), %d save failure(s)",
		s.BooksParsed, s.BooksNominated, s.BatchesOK, s.BatchesTotal, s.BatchesFailed, s.SavesFailed)
	switch {
	case s.AbortedPermanent:
		msg += "; ABORTED on a non-retryable backend error -- the remaining books keep their filename-derived metadata"
	case s.AbortedThreshold:
		msg += "; ABORTED after hitting the batch failure threshold -- the remaining books keep their filename-derived metadata"
	}
	return msg
}
