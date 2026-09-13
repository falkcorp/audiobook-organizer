// file: internal/scanner/ai_batch_phase.go
// version: 1.8.0
// guid: dc72fe25-f58e-4135-88f4-7f842e7e9a7a
// last-edited: 2026-09-13

package scanner

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/falkcorp/audiobook-organizer/internal/ai"
	"github.com/falkcorp/audiobook-organizer/internal/config"
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
	// Both of these were hardcoded (20 and 30s) until 2026-09-09. They are read
	// together from one resolver because they are one setting in two halves --
	// see AIBackendConfig.ParseBatchSize for the measurements that forced this,
	// and for why the timeout is clamped below the watchdog's ProgressTimeout.
	batch := config.AppConfig.ResolveAIParseBatch()
	batchSize, batchTimeout := batch.Size, batch.Timeout
	const delayBetweenBatches = 2 * time.Second

	totalBatches := (len(candidates) + batchSize - 1) / batchSize
	// batchTimeout is logged, not just used: when this phase fails, the first
	// question is always "against what deadline?", and the answer is now
	// operator-controlled rather than a constant a reader can look up.
	log.Info("AI batch parsing %d books in %d batches of %d, %d at a time, %s per batch",
		len(candidates), totalBatches, batchSize, batch.Workers, batchTimeout)

	// The serial version counted CONSECUTIVE failures. Under concurrency
	// "consecutive" has no meaning -- batches finish out of order -- so the
	// guard becomes a total count. It is the same intent (stop when the
	// backend is down rather than grinding through every batch) expressed in
	// terms that survive parallelism, and it is strictly more eager: 3
	// failures anywhere aborts, where before 3 had to land in a row.
	var failures atomic.Int64
	var started atomic.Int64
	var batchesOK, booksParsed, savesFailed, batchesSplit, booksFailed atomic.Int64
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
	var bookFailures []AIBookFailure
	recordBookFailure := func(f AIBookFailure) {
		detailMu.Lock()
		defer detailMu.Unlock()
		if len(bookFailures) < maxRecordedBookFailures {
			bookFailures = append(bookFailures, f)
		}
	}
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
	aiGroup.SetLimit(batch.Workers)

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

			// Report BEFORE the call, not after: a batch takes up to
			// batchTimeout and a failing one takes longer, so reporting
			// only on success leaves gaps that exceed the watchdog's
			// ProgressTimeout. This phase is why library.scan could
			// complete its entire file walk and still be canceled for
			// inactivity.
			//
			// This is also why ResolveAIParseBatch clamps the configured
			// timeout to AIParseBatchTimeoutCeiling (4m): the gap this
			// report covers is exactly one batchTimeout wide, so a timeout
			// at or above the 5m ProgressTimeout would reintroduce the
			// very inactivity kill that reporting-first exists to prevent.
			batchNum := int(started.Add(1))
			log.UpdateProgress(batchNum, totalBatches,
				fmt.Sprintf("AI parsing batch %d/%d (%d books)", batchNum, totalBatches, len(batch)))

			filenames := make([]string, len(batch))
			for i, idx := range batch {
				filenames[i] = filepath.Base(books[idx].FilePath)
			}

			// One call = one fresh deadline. The split below makes several calls
			// for one batch, and sharing a single deadline across them would hand
			// the later sub-calls whatever the earlier ones left -- the defect
			// ai_parser_chain.go's minRungBudget documents one level down.
			call := func(names []string) ([]*ai.ParsedMetadata, error) {
				aiCtx, cancel := context.WithTimeout(aiGroupCtx, batchTimeout)
				defer cancel()
				return parser.ParseBatch(aiCtx, names)
			}
			// Runs before every EXTRA call the split makes. It keeps the split
			// polite and alive: the same inter-batch pause a new batch would get,
			// an abort check so a phase that has given up stops re-asking, and a
			// progress report so a batch that splits all the way down (up to
			// 2n-2 extra calls, each up to batchTimeout) cannot outlast the
			// watchdog's ProgressTimeout on the single report made above. Same
			// batchNum and totalBatches: the split is invisible to the
			// denominator and must not move it.
			beforeSplitCall := func(size int) bool {
				t := time.NewTimer(splitCallDelay)
				defer t.Stop()
				select {
				case <-aborted:
					return false
				case <-aiGroupCtx.Done():
					return false
				case <-t.C:
				}
				log.UpdateProgress(batchNum, totalBatches,
					fmt.Sprintf("AI parsing batch %d/%d: re-asking %d of its %d books in a smaller batch after a short reply",
						batchNum, totalBatches, size, len(batch)))
				return true
			}
			out := parseBatchSplitting(filenames, call, beforeSplitCall)

			if out.split {
				batchesSplit.Add(1)
				log.Info("AI batch %d/%d returned the wrong number of results; split it with %d extra call(s)",
					batchNum, totalBatches, out.extraCalls)
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
			//
			// Only RESOLVED books are saved: ones some call answered with a
			// correctly-sized reply. A book the split could not resolve is
			// neither saved nor stamped, exactly as every book in a rejected
			// batch was before the split existed -- it is left for the next run.
			for i, idx := range batch {
				if !out.resolved[i] {
					continue
				}
				aiMeta := out.results[i]
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
				// Carried as the model returned it. The plausibility range,
				// the empty-column check and the lock are all decided in one
				// place, the saver (saveAIFieldsToPrimary).
				if books[idx].Year == 0 && aiMeta.Year > 0 {
					books[idx].Year = aiMeta.Year
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

			// Books the split narrowed down to a batch of one that STILL came
			// back with the wrong count. They are failed books, not a failed
			// batch: they say nothing about the backend's health, so they do not
			// feed maxTotalFailures -- one poisoned batch of 8 counting per book
			// would trip the 3-failure abort by itself and stop the phase.
			for i, idx := range batch {
				if out.bookErrs[i] == nil {
					continue
				}
				booksFailed.Add(1)
				recordBookFailure(AIBookFailure{Path: books[idx].FilePath, Err: out.bookErrs[i].Error()})
				log.Warn("AI parsing failed for %s even on its own (batch %d/%d): %v",
					books[idx].FilePath, batchNum, totalBatches, out.bookErrs[i])
			}

			if aiErr := out.err; aiErr != nil {
				// The books this failure leaves unparsed: those no call resolved
				// and that were not already counted as failed books above. For a
				// batch whose FIRST call failed that is the whole batch, which is
				// what this branch recorded before the split existed.
				var failedNames []string
				for i := range batch {
					if !out.resolved[i] && out.bookErrs[i] == nil {
						failedNames = append(failedNames, filenames[i])
					}
				}

				// Counted BEFORE the permanent-failure branch below, not after.
				// The increment used to sit under it, so the worst failure there
				// is -- a revoked key, an exhausted quota, every batch dead --
				// returned with BatchesFailed still 0 and the summary printed
				// "0 batch failure(s)" for a run that failed everything. The
				// threshold test below reads the counter instead of the return
				// value of Add, which keeps the abort policy identical: each
				// error increments exactly once and the third one trips it.
				//
				// Once per ORIGINAL batch, however many calls the split made:
				// parseBatchSplitting stops at the first non-count error.
				failed := failures.Add(1)
				permanent := isPermanentAIFailure(aiErr)
				recordBatchFailure(AIBatchFailure{
					Batch:     batchNum,
					Total:     totalBatches,
					Filenames: cappedFilenames(failedNames),
					Omitted:   max(0, len(failedNames)-maxRecordedFilenames),
					Err:       aiErr.Error(),
					Permanent: permanent,
				})
				log.Warn("AI batch parsing failed (batch %d/%d, %d file(s): %s): %v",
					batchNum, totalBatches, len(failedNames), summarizeFilenames(failedNames), aiErr)

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
			if out.stopped {
				// The phase aborted or its context ended mid-split. What was
				// resolved is saved above; the rest is left, like every other
				// batch the abort did not reach.
				return nil
			}

			batchesOK.Add(1)
			log.Info("AI batch %d-%d complete (%d books resolved)", start, end, len(batch)-out.unresolved())
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
		BatchesSplit:     int(batchesSplit.Load()),
		BooksFailed:      int(booksFailed.Load()),
		AbortedPermanent: abortedPermanent.Load(),
		AbortedThreshold: abortedThreshold.Load(),
		BatchFailures:    batchFailures,
		SaveFailures:     saveFailures,
		BookFailures:     bookFailures,
	}
}

// splitCallDelay is the pause before each extra call a split makes. The same
// 2s the phase leaves between batches, so re-asking a batch in pieces never
// talks to the model faster than asking fresh batches would. A var only so
// tests need not wait 2s per sub-call.
var splitCallDelay = 2 * time.Second

// maxSplitExtraCalls is the most extra calls parseBatchSplitting may make for a
// batch of n filenames, on top of the one call the batch always gets.
//
// The bound: halving a batch of n down to single files is a binary tree with n
// leaves and therefore 2n-1 nodes, one call per node. The root is the batch's
// ordinary call, so the split adds at most 2n-2 -- 14 for the production batch
// of 8, 38 for the default of 20. It is reached only when EVERY sub-batch,
// down to every single file, comes back with the wrong count; a batch with one
// bad filename costs about 2*log2(n) extra calls.
//
// The tree arithmetic already guarantees this; the budget enforces it anyway,
// so a later change to the split rule (thirds, retries, anything not a clean
// halving) runs into a hard stop instead of quietly multiplying the load on
// the model host. The calls are sequential inside the batch's own worker, so
// the number of requests in flight never exceeds parse_batch_workers either.
func maxSplitExtraCalls(n int) int {
	return max(0, 2*n-2)
}

// isResultCountMismatch reports whether err is the one failure the split
// handles: the model answered with a well-formed reply of the wrong length.
// Typed, never text-matched -- the text under a *ai.ReplyParseError is
// model-written (see ai_failure.go). A permanent error anywhere in the tree
// wins, so a count mismatch joined to a quota failure still aborts the phase.
func isResultCountMismatch(err error) bool {
	if err == nil || isPermanentAIFailure(err) {
		return false
	}
	_, ok := errors.AsType[*ai.ResultCountError](err)
	return ok
}

// splitOutcome is what parseBatchSplitting learned about one batch.
type splitOutcome struct {
	// results[i] is filenames[i]'s metadata, meaningful only where
	// resolved[i]: some call answered for it with a correctly-sized reply.
	results  []*ai.ParsedMetadata
	resolved []bool
	// bookErrs[i] is set for a file that came back with the wrong count even
	// in a batch of one. Such a file has no further split available.
	bookErrs []error
	// err is the first failure that is NOT a count mismatch -- a transport
	// error, timeout, other bad reply, or the spent budget. It ends the split;
	// the files it leaves unresolved are the batch failure.
	err error
	// stopped means the phase aborted (or its context ended) mid-split.
	stopped    bool
	split      bool
	extraCalls int
}

// unresolved counts the files that no call answered.
func (o splitOutcome) unresolved() int {
	n := 0
	for _, r := range o.resolved {
		if !r {
			n++
		}
	}
	return n
}

// parseBatchSplitting asks call about filenames once and, if and only if the
// reply had the wrong number of results, halves the batch and asks about each
// half, recursing down to single files.
//
// Why split rather than reject: results are matched to filenames by position,
// so a short reply cannot be used -- but the batch is not the problem, usually
// one filename in it is. qwen2.5:7b-instruct collapses some batches of 6-8
// into a single object (all-null, or one book's title) and does it again on
// every retry, so rejecting the batch lost the same 6-8 books on every run.
// Asked in smaller groups, the other books parse.
//
// Every other failure keeps its pre-split handling: the first one ends the
// split and is returned in err, unsplit, for the phase's existing
// retry/threshold/permanent-abort logic. A dead or refusing backend is never
// asked more than once per batch because of this function.
//
// beforeSplitCall runs before each extra call with the sub-batch size; false
// stops the split (the phase aborted). Halves are asked sequentially, never
// concurrently. Extra calls never exceed maxSplitExtraCalls(len(filenames)).
func parseBatchSplitting(
	filenames []string,
	call func([]string) ([]*ai.ParsedMetadata, error),
	beforeSplitCall func(size int) bool,
) splitOutcome {
	n := len(filenames)
	out := splitOutcome{
		results:  make([]*ai.ParsedMetadata, n),
		resolved: make([]bool, n),
		bookErrs: make([]error, n),
	}
	accept := func(lo int, res []*ai.ParsedMetadata, size int) {
		for i := range size {
			if i < len(res) {
				out.results[lo+i] = res[i]
			}
			out.resolved[lo+i] = true
		}
	}

	res, err := call(filenames)
	if err == nil {
		accept(0, res, n)
		return out
	}
	if !isResultCountMismatch(err) {
		out.err = err
		return out
	}

	budget := maxSplitExtraCalls(n)
	// walk handles [lo,hi), which has just failed with a count mismatch.
	var walk func(lo, hi int, countErr error)
	walk = func(lo, hi int, countErr error) {
		if hi-lo <= 1 {
			// The recursion floor: a single file cannot be split further.
			out.bookErrs[lo] = countErr
			return
		}
		out.split = true
		mid := lo + (hi-lo)/2
		for _, half := range [2][2]int{{lo, mid}, {mid, hi}} {
			if out.err != nil || out.stopped {
				return
			}
			if out.extraCalls >= budget {
				out.err = fmt.Errorf("AI batch split stopped: its budget of %d extra call(s) for %d file(s) is spent", budget, n)
				return
			}
			a, b := half[0], half[1]
			if !beforeSplitCall(b - a) {
				out.stopped = true
				return
			}
			out.extraCalls++
			res, err := call(filenames[a:b])
			switch {
			case err == nil:
				accept(a, res, b-a)
			case isResultCountMismatch(err):
				walk(a, b, err)
			default:
				out.err = err
				return
			}
		}
	}
	walk(0, n, err)
	return out
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
	maxRecordedBookFailures  = 10
)

// AIBookFailure is one book the split could not resolve: it came back with the
// wrong number of results even when asked about on its own. Distinct from a
// batch failure, which is the backend failing to answer at all.
type AIBookFailure struct {
	Path string `json:"path"`
	Err  string `json:"error"`
}

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
	Disabled       bool
	BooksNominated int
	BatchesTotal   int
	BatchesOK      int
	BatchesFailed  int
	BooksParsed    int
	SavesFailed    int
	// BatchesSplit counts batches re-asked in smaller pieces after a reply
	// with the wrong number of results (parseBatchSplitting). BooksFailed
	// counts the books that still failed alone, with detail in BookFailures.
	BatchesSplit     int
	BooksFailed      int
	AbortedPermanent bool
	AbortedThreshold bool
	// BatchFailures and SaveFailures are the capped detail behind the two
	// counters above: which books, and what the error said. See the cap
	// constants for why they are bounded.
	BatchFailures []AIBatchFailure
	SaveFailures  []AISaveFailure
	BookFailures  []AIBookFailure
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
	// BooksFailed counts for the same reason: those books were not parsed.
	return s.Aborted() || s.BatchesFailed > 0 || s.SavesFailed > 0 || s.BooksFailed > 0
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
	// Only when a split happened, so the line is unchanged for every run that
	// never needed one.
	if s.BatchesSplit > 0 || s.BooksFailed > 0 {
		msg += fmt.Sprintf("; %d batch(es) split after a wrong result count, %d book(s) failed on their own",
			s.BatchesSplit, s.BooksFailed)
	}
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
	} else if len(s.BookFailures) > 0 {
		msg += fmt.Sprintf("; first book failure: %s: %s", s.BookFailures[0].Path, s.BookFailures[0].Err)
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
	if len(s.BatchFailures) == 0 && len(s.SaveFailures) == 0 && len(s.BookFailures) == 0 {
		return nil
	}
	lines := make([]string, 0, len(s.BatchFailures)+len(s.SaveFailures)+len(s.BookFailures)+3)
	for _, f := range s.BookFailures {
		lines = append(lines, fmt.Sprintf("book failed even on its own after a batch split: %s: %s", f.Path, f.Err))
	}
	if s.BooksFailed > len(s.BookFailures) {
		lines = append(lines, fmt.Sprintf("... and %d further book failure(s) not recorded (cap %d)",
			s.BooksFailed-len(s.BookFailures), maxRecordedBookFailures))
	}
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

// ReportTo surfaces a failed phase through warn, one call per line: first
// String() (the one-line verdict), then each FailureDetails() line. A no-op
// when the phase did not fail or warn is nil, so callers can invoke it
// unconditionally.
//
// This exists because the INLINE path (ProcessBooksParallel, called directly
// from scanner.go) has no operation reporter to hand a failed AIPhaseSummary
// to -- only the logger.Logger passed down through ScanRequest, and
// operations.LoggerFromReporter's bridge overrides UpdateProgress and With,
// not Warn (see its doc comment), so a plain scanLog.Warn call here would
// reach the process log and nothing else: exactly the blind spot this method
// exists to close. warn is the caller-supplied channel (ProcessBooksParallel's
// onAIPhaseWarning) that ultimately reaches reporter.Log for the two real op
// call sites (library.scan, library.folder-auto-scan) while staying nil, and
// therefore free, for every other caller and test.
//
// Deliberately NOT wired through UpdateProgress: reporter_db.go's
// UpdateProgress overwrites the operation's progressCurrent/progressTotal
// fields (and the OPS-5 Prometheus gauge) with whatever it is given, and this
// phase's own BooksParsed/BooksNominated counts are local to the AI
// candidates in ONE chunk -- not the scan's cumulative book count that
// service.go's progressCallback (service.go:412-423) is careful to keep
// monotonic. Calling UpdateProgress here would clobber that with a much
// smaller number and make a healthy scan's progress bar jump backwards.
func (s AIPhaseSummary) ReportTo(warn func(string)) {
	if warn == nil || !s.Failed() {
		return
	}
	warn(s.String())
	for _, line := range s.FailureDetails() {
		warn(line)
	}
}
