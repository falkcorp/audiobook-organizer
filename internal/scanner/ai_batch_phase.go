// file: internal/scanner/ai_batch_phase.go
// version: 1.3.0
// guid: dc72fe25-f58e-4135-88f4-7f842e7e9a7a
// last-edited: 2026-09-08

package scanner

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/falkcorp/audiobook-organizer/internal/ai"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

// maxTotalFailures is how many failed batches abort the whole phase. Package
// scope rather than a local const so a test can assert the policy is UNCHANGED
// across edits to the counter that feeds it -- the counter moved on 2026-09-08
// and the abort threshold deliberately did not.
const maxTotalFailures = 3

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

	// The counters above say HOW MANY batches failed. These say WHICH books were
	// in them and WHY they failed, which is the only thing that makes a failed
	// run actionable -- and until now it existed at the failure site and was
	// thrown away into log.Warn, which LoggerFromReporter does not forward, so
	// the operation record never saw it.
	//
	// Slices, not atomics, so they need a real lock: every write below happens on
	// a worker goroutine and aiBatchWorkers is greater than one.
	var detailMu sync.Mutex
	var batchFailures []AIBatchFailure
	var saveFailures []AISaveFailure
	recordBatchFailure := func(f AIBatchFailure) {
		detailMu.Lock()
		defer detailMu.Unlock()
		if len(batchFailures) < maxRecordedBatchFailures {
			batchFailures = append(batchFailures, f)
		}
	}
	recordSaveFailure := func(f AISaveFailure) {
		detailMu.Lock()
		defer detailMu.Unlock()
		if len(saveFailures) < maxRecordedSaveFailures {
			saveFailures = append(saveFailures, f)
		}
	}

	aiGroup, aiGroupCtx := errgroup.WithContext(ctx)
	aiGroup.SetLimit(aiBatchWorkers)

	for start := 0; start < len(candidates); start += batchSize {
		end := min(start+batchSize, len(candidates))
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
				// Counted BEFORE the permanent-failure branch below, not after.
				// The increment used to sit under it, so the worst failure there
				// is -- a revoked key, an exhausted quota, every batch dead --
				// returned with BatchesFailed still 0 and the summary printed
				// "0 batch failure(s)" for a run that failed everything. The
				// threshold test below reads the counter instead of the return
				// value of Add, which keeps the abort policy identical: each
				// error increments exactly once and the third one trips it.
				failed := failures.Add(1)
				permanent := isPermanentAIFailure(aiErr)
				recordBatchFailure(AIBatchFailure{
					Batch:     batchNum,
					Total:     totalBatches,
					Filenames: cappedFilenames(filenames),
					Omitted:   max(0, len(filenames)-maxRecordedFilenames),
					Err:       aiErr.Error(),
					Permanent: permanent,
				})
				log.Warn("AI batch parsing failed (batch %d/%d, %d file(s): %s): %v",
					batchNum, totalBatches, len(filenames), summarizeFilenames(filenames), aiErr)

				// A permanent backend state -- no credits, revoked key,
				// quota exhausted -- will not clear by the next batch, so
				// stop the whole phase on the first one rather than retrying
				// it for every remaining batch.
				if permanent {
					abortedPermanent.Store(true)
					log.Warn("AI batch parsing disabled for this scan after a non-retryable error at batch %d/%d: %v — "+
						"the remaining books keep their filename-derived metadata",
						batchNum, totalBatches, aiErr)
					abort()
					return nil
				}

				if failed >= maxTotalFailures {
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
					recordSaveFailure(AISaveFailure{Path: books[idx].FilePath, Err: saveErr.Error()})
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

	// Every worker has returned, so the slices are quiescent -- but read them
	// under the same lock anyway rather than relying on that argument holding
	// after the next edit to this function.
	detailMu.Lock()
	defer detailMu.Unlock()

	return AIPhaseSummary{
		BooksNominated:   len(candidates),
		BatchesTotal:     totalBatches,
		BatchesOK:        int(batchesOK.Load()),
		BatchesFailed:    int(failures.Load()),
		BooksParsed:      int(booksParsed.Load()),
		SavesFailed:      int(savesFailed.Load()),
		AbortedPermanent: abortedPermanent.Load(),
		AbortedThreshold: abortedThreshold.Load(),
		BatchFailures:    batchFailures,
		SaveFailures:     saveFailures,
	}
}

// How much of a failure is kept for the operation record.
//
// Capped because reporter.Log writes into the activity store, and that store's
// growth is a measured production problem -- an unbounded dump of every failed
// batch's every filename would be a new source of it. The caps are chosen to
// keep a typical failure WHOLE: batchSize is 20 and the runs in the report that
// prompted this were 5 books, so maxRecordedFilenames covers a small batch
// entirely and truncates a full one with an explicit "+N more".
const (
	maxRecordedBatchFailures = 5
	maxRecordedFilenames     = 10
	maxRecordedSaveFailures  = 10
)

// AIBatchFailure is one failed LLM batch: which books were in it, and why it
// failed. Captured at the failure site because that is the only place both
// facts exist -- the summary is assembled after every worker has returned.
type AIBatchFailure struct {
	// Batch and Total are the 1-based batch number and batch count as reported
	// to progress, so a failure here names the same batch the progress line did.
	Batch     int      `json:"batch"`
	Total     int      `json:"total"`
	Filenames []string `json:"filenames"`
	// Omitted is how many filenames the cap dropped, so a truncated list says so
	// rather than silently reading as the whole batch.
	Omitted   int    `json:"omitted,omitempty"`
	Err       string `json:"error"`
	Permanent bool   `json:"permanent,omitempty"`
}

// AISaveFailure is one book whose AI-filled fields could not be written back.
// Distinct from a batch failure: the LLM answered, and the answer was lost.
type AISaveFailure struct {
	Path string `json:"path"`
	Err  string `json:"error"`
}

// cappedFilenames returns at most maxRecordedFilenames names, copied so the
// summary does not retain the caller's slice.
func cappedFilenames(names []string) []string {
	out := make([]string, 0, min(len(names), maxRecordedFilenames))
	return append(out, names[:min(len(names), maxRecordedFilenames)]...)
}

// summarizeFilenames renders a filename list for a single log line.
func summarizeFilenames(names []string) string {
	if len(names) == 0 {
		return "(none)"
	}
	shown := cappedFilenames(names)
	s := strings.Join(shown, ", ")
	if omitted := len(names) - len(shown); omitted > 0 {
		s += fmt.Sprintf(", +%d more", omitted)
	}
	return s
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
	// BatchFailures and SaveFailures are the capped detail behind the two
	// counters above: which books, and what the error said. See the cap
	// constants for why they are bounded.
	BatchFailures []AIBatchFailure
	SaveFailures  []AISaveFailure
}

// Aborted reports whether the phase stopped short, leaving nominated books
// unparsed.
//
// This is NOT the predicate for "was this run a failure" -- see Failed. It
// answers only "did we stop early", and a run that had one batch, failed it,
// and had nothing left to try stopped exactly on schedule.
func (s AIPhaseSummary) Aborted() bool { return s.AbortedPermanent || s.AbortedThreshold }

// Failed reports whether the run must be recorded as a failed operation.
//
// The distinction that has to exist here: a healthy no-op has BatchesFailed
// == 0. Parsing 0 books because the only batch failed is NOT the same as
// parsing 0 books because nothing needed changing, and until 2026-09-08 the
// operation could not tell them apart -- it failed only on Aborted(), which is
// AbortedPermanent || AbortedThreshold, so a single-batch run whose only batch
// failed tripped neither and finished green with a full progress bar.
//
// Still NOT keyed on a low change count, for the reason Aborted's comment gave:
// a library where every candidate was already filled in by another path
// legitimately parses and changes nothing, and making that red would train
// everyone to ignore the status.
//
// SavesFailed counts too. The LLM answered and the answer was thrown away, so
// those books keep their filename-derived metadata exactly as if the batch had
// failed -- from the library's point of view it is the same loss.
func (s AIPhaseSummary) Failed() bool {
	if s.Disabled {
		return false
	}
	return s.Aborted() || s.BatchesFailed > 0 || s.SavesFailed > 0
}

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
	// The cause, on the one line the Activity page actually renders.
	//
	// This line is the operation's progress_message, so it stays ONE line and
	// the full per-batch detail goes to FailureDetails below. But a row reading
	// "0/5 book(s) parsed ... 1 batch failure(s)" and nothing else is a report
	// that something went wrong with no way to find out what, and that was the
	// complaint. Whatever else is true, the first error belongs here.
	if len(s.BatchFailures) > 0 {
		f := s.BatchFailures[0]
		msg += fmt.Sprintf("; first failure: batch %d/%d (%d file(s)): %s",
			f.Batch, f.Total, len(f.Filenames)+f.Omitted, f.Err)
	} else if len(s.SaveFailures) > 0 {
		msg += fmt.Sprintf("; first save failure: %s: %s", s.SaveFailures[0].Path, s.SaveFailures[0].Err)
	}
	return msg
}

// FailureDetails is one line per captured failure, naming the books involved.
//
// Separate from String because they go to different places: String is the
// operation's progress message (one line, rendered in the row) while these are
// logged into the operation record, where the expanded row shows them. Empty
// for a run with nothing to report, so the caller can range over it blindly.
func (s AIPhaseSummary) FailureDetails() []string {
	if len(s.BatchFailures) == 0 && len(s.SaveFailures) == 0 {
		return nil
	}
	lines := make([]string, 0, len(s.BatchFailures)+len(s.SaveFailures)+2)
	for _, f := range s.BatchFailures {
		names := strings.Join(f.Filenames, ", ")
		if f.Omitted > 0 {
			names += fmt.Sprintf(", +%d more", f.Omitted)
		}
		kind := "batch failed"
		if f.Permanent {
			kind = "batch failed (non-retryable)"
		}
		lines = append(lines, fmt.Sprintf("%s: batch %d/%d, %d file(s) [%s]: %s",
			kind, f.Batch, f.Total, len(f.Filenames)+f.Omitted, names, f.Err))
	}
	// The counters are the census; these lists are a capped sample. Say so,
	// rather than letting five recorded failures read as the total.
	if s.BatchesFailed > len(s.BatchFailures) {
		lines = append(lines, fmt.Sprintf("... and %d further batch failure(s) not recorded (cap %d)",
			s.BatchesFailed-len(s.BatchFailures), maxRecordedBatchFailures))
	}
	for _, f := range s.SaveFailures {
		lines = append(lines, fmt.Sprintf("save failed: %s: %s", f.Path, f.Err))
	}
	if s.SavesFailed > len(s.SaveFailures) {
		lines = append(lines, fmt.Sprintf("... and %d further save failure(s) not recorded (cap %d)",
			s.SavesFailed-len(s.SaveFailures), maxRecordedSaveFailures))
	}
	return lines
}
