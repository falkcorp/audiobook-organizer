// file: internal/server/metadata_candidate_op.go
// version: 3.5.0
// guid: 3f7e2c91-b4a0-4d8e-9c5f-1a6b7d8e0f23
// last-edited: 2026-10-01
//
// Registers the metadata.candidate-fetch v2 OperationDef. Pure params
// type moved to internal/metabatch.FetchOpParams.

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/metabatch"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"golang.org/x/time/rate"
)

// metadataCandidateFetchOpParams is a server-local alias for the shared params
// type so callers in this package do not need to qualify the package name.
type metadataCandidateFetchOpParams = metabatch.FetchOpParams

// candidateFetchCheckpointEvery is how many completed books sit between
// checkpoints. Each checkpoint is a store write of the remaining id list, so
// it is amortised rather than per-book; the cost of an interrupt is at most
// this many re-fetches, and the result-row filter in Run absorbs even those.
const candidateFetchCheckpointEvery = 25

// remainingAfterDone returns the members of all that are not in done, in
// all's original order. It is the done-SET shape TODO.md prescribes for a
// parallel loop: workers finish out of order, so neither a completion count
// nor a "last id" cursor is a valid resume position — a resume from either
// would skip whichever stragglers were still in flight.
func remainingAfterDone(all []string, done map[string]struct{}) []string {
	remaining := make([]string, 0, max(len(all)-len(done), 0))
	for _, id := range all {
		if _, ok := done[id]; ok {
			continue
		}
		remaining = append(remaining, id)
	}
	return remaining
}

// doneSet tracks which ids of a parallel loop have finished, and decides when
// a checkpoint is due. Every method is safe for concurrent use.
type doneSet struct {
	mu    sync.Mutex
	done  map[string]struct{}
	every int
	count int
}

func newDoneSet(every int) *doneSet {
	return &doneSet{done: make(map[string]struct{}), every: max(every, 1)}
}

// mark records id as finished and reports whether a periodic checkpoint is
// due, i.e. the count of finished ids just crossed a multiple of every.
func (d *doneSet) mark(id string) (checkpointDue bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, dup := d.done[id]; dup {
		return false
	}
	d.done[id] = struct{}{}
	d.count++
	return d.count%d.every == 0
}

// remaining returns the ids of all that have not been marked, in order.
func (d *doneSet) remaining(all []string) []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return remainingAfterDone(all, d.done)
}

// candidateFetchCheckpointState is the payload Checkpoint persists and
// resumeRestart overlays onto the row's params. BookIDs carries no omitempty
// on purpose (see batch_apply_op.go for the incident shape): an empty
// remaining set must REPLACE the original list in the overlay, not let it show
// through. TotalBooks is carried so the resumed run's progress bar keeps the
// batch's original size rather than shrinking to the remainder.
func candidateFetchCheckpointState(all []string, done *doneSet, total int) metadataCandidateFetchOpParams {
	return metadataCandidateFetchOpParams{BookIDs: done.remaining(all), TotalBooks: total}
}

// RegisterMetadataCandidateFetchOp registers the "metadata.candidate-fetch"
// v2 OperationDef. The HTTP handler enqueues this def and returns the id
// EnqueueOp minted; Run writes OperationResult rows under that same id.
//
// It used to mint a separate v1 operations row first and key results on that,
// which meant the id the client held resolved at one endpoint and not the
// other.
func (s *Server) RegisterMetadataCandidateFetchOp(reg *opsregistry.Registry) error {
	return reg.RegisterOp(opsregistry.OperationDef{
		ID:              "metadata.candidate-fetch",
		Liveness:        opsregistry.LivenessManual,
		Plugin:          "metadata",
		DisplayName:     "Fetch Metadata Candidates",
		Description:     "Fetch and cache metadata candidates for a set of audiobooks (rate-limited, parallel). Results are stored as OperationResult rows for review.",
		DefaultPriority: opsregistry.PriorityNormal,
		Cancellable:     true,
		Isolate:         false,
		Timeout:         8 * time.Hour,
		// ResumeRestart, not ResumeDrop. Dropping was only ever survivable because
		// resumeInterruptedMetadataFetch re-enqueued the remainder by hand off the
		// v1 interrupted-ops list; with the v1 row gone that hand-rolled path goes
		// with it, and leaving ResumeDrop would mean an 8-hour fetch interrupted by
		// a restart silently never resumes.
		//
		// RESUME AUDIT 2026-09-11 (a): keep ResumeRestart, and back it with a
		// real checkpoint. Run now persists the remaining book ids every
		// candidateFetchCheckpointEvery completions and once more on cancel, as
		// a done-set (workers finish out of order, so a count or a last-id
		// cursor would skip stragglers). resumeRestart overlays that onto the
		// row's params, so the resumed run is handed only the unfinished tail
		// and never re-issues a provider call for a fetched book. The
		// result-row filter below stays as a second, independent guard.
		ResumePolicy:   opsregistry.ResumeRestart,
		ConcurrencyKey: "metadata.candidate-fetch",
		Permissions:    []auth.Permission{auth.PermLibraryEditMetadata},
		Capabilities:   []opsregistry.Capability{opsregistry.CapLibraryRead, opsregistry.CapLibraryWrite, opsregistry.CapNetworkGeneric},
		Run:            s.runMetadataCandidateFetchOp,
	})
}

// runMetadataCandidateFetchOp is the Run body of metadata.candidate-fetch. It is
// a named method rather than a closure so the resume test can drive it with a
// recording reporter and a cancelled context.
func (s *Server) runMetadataCandidateFetchOp(ctx context.Context, rawParams json.RawMessage, reporter opsregistry.Reporter) error {
	var p metadataCandidateFetchOpParams
	if len(rawParams) > 0 {
		if err := json.Unmarshal(rawParams, &p); err != nil {
			return fmt.Errorf("metadata-candidate-fetch: decode params: %w", err)
		}
	}
	if len(p.BookIDs) == 0 {
		return nil
	}

	store := s.storeForWiring()
	mfs := s.metadataFetchService
	progress := registryProgressAdapter{r: reporter}
	totalBooks := p.TotalBooks
	if totalBooks == 0 {
		totalBooks = len(p.BookIDs)
	}
	// This op's OWN v2 id. Results used to be keyed on a v1 row minted
	// separately by the handler; that row is gone, and the result keyspace
	// takes an arbitrary string key, so it keys on the id every reader
	// already has.
	opID := opsregistry.ReporterOpID(reporter)
	// ReporterOpID documents "" as a legitimate return that callers must
	// treat as unknown. Every result row keys on this, so an empty id would
	// file the whole run under one blank key and make its results
	// unreadable by every reader — a silent total loss. The old code took
	// the id from params, where it was structurally non-empty; this is the
	// guard that trade needs.
	if opID == "" {
		return fmt.Errorf("metadata-candidate-fetch: reporter returned no operation id")
	}

	// A resumed run arrives with BookIDs already pruned to the checkpoint's
	// remaining set and TotalBooks carrying the original size, so the
	// difference is work an earlier attempt finished. Count it as done up
	// front; otherwise the bar would restart at zero on every resume.
	alreadyDone := max(totalBooks-len(p.BookIDs), 0)

	// SKIP WHAT IS ALREADY FETCHED. This is the second guard behind the
	// checkpoint: a restart that predates the first checkpoint, or a crash
	// between a result write and the next checkpoint, still hands Run a few
	// books that already have result rows. Without this filter those would be
	// re-fetched — and before the checkpoint existed, a restart re-fetched
	// every book, up to ~10K external API calls for a full-library run.
	//
	// It lives here rather than in a restart-time helper because Run is the
	// one place every trigger passes through. The previous arrangement put it
	// in resumeInterruptedMetadataFetch, which only the startup path called,
	// so any other resume trigger silently refetched.
	// Running outcome counts: matched / no match / skipped / errors, plus how
	// many books were answered from the candidate cache without a provider
	// call. Until 2026-09-27 the op log carried only "fetched 880/44647 (from
	// cache: 0, known empty: 0)" per book, so a run that matched 1,088 of
	// 1,165 books looked identical to one that matched none; the per-book
	// outcomes existed only as result rows nobody reads while the op runs.
	// Every book now also gets its own outcome line (logCandidateOutcome) and
	// the progress line carries the counts.
	//
	// Seeded below from this op's existing result rows, so a resumed run's
	// "fetched N/M" and its counts describe the same books; counting only
	// this attempt would read "fetched 900/1000 — matched 40" as 860 lost.
	tally := &candidateFetchTally{}

	if existing, rerr := store.GetOperationResults(opID); rerr == nil && len(existing) > 0 {
		for _, row := range existing {
			var prior CandidateResult
			if json.Unmarshal([]byte(row.ResultJSON), &prior) != nil {
				prior.Status = row.Status
			}
			tally.record(prior)
		}
		remaining := metabatch.RemainingBooksToFetch(existing, p.BookIDs)
		skipped := len(p.BookIDs) - len(remaining)
		alreadyDone += skipped
		p.BookIDs = remaining
		if skipped > 0 {
			candidateFetchLog.Info("resuming, skipping already-fetched books: opID=%s skipped=%d remaining=%d",
				logger.SanitizeLogValue(opID), skipped, len(p.BookIDs))
		}
		if len(p.BookIDs) == 0 {
			_ = progress.UpdateProgress(totalBooks, totalBooks, "completed")
			return nil
		}
	}

	_ = progress.UpdateProgress(alreadyDone, totalBooks, fmt.Sprintf("starting: %d books to fetch", len(p.BookIDs)))

	// The op's gate and pool are sized from the enabled sources' own budgets
	// (candidateFetchLimiter, candidateFetchWorkers): a fixed 10/s across
	// every source and 8 workers had become the cap once Audible alone was
	// configured for 8/s.
	budgetRPS, budgetBurst := metafetch.EnabledSourcesBudget()
	limiter := candidateFetchLimiter(budgetRPS, budgetBurst)
	// One folder memo per run, shared by every worker (resolver folder reads).
	folderMemo := s.newFolderMemo()

	workCh := make(chan string, len(p.BookIDs))
	for _, id := range p.BookIDs {
		workCh <- id
	}
	close(workCh)

	var completed int64 = int64(alreadyDone)
	var wg sync.WaitGroup
	numWorkers := min(candidateFetchWorkers(budgetRPS, config.AppConfig.MetadataCandidateFetchWorkers), len(p.BookIDs))
	candidateFetchLog.Info("budget: opID=%s rps=%.1f burst=%d workers=%d",
		logger.SanitizeLogValue(opID), budgetRPS, budgetBurst, numWorkers)

	// CHECKPOINTING. A book joins the done-set only after its result row is
	// written, so a checkpoint never drops a book whose fetch has not been
	// persisted. The marshal-failure path below skips the mark on purpose:
	// that book has no result row, so it must stay owed and be retried.
	// ckptMu serialises the Checkpoint calls themselves so two workers crossing
	// a multiple of the cadence at once cannot write an older remaining set
	// over a newer one.
	done := newDoneSet(candidateFetchCheckpointEvery)
	var ckptMu sync.Mutex
	writeCheckpoint := func() {
		ckptMu.Lock()
		defer ckptMu.Unlock()
		if err := reporter.Checkpoint(candidateFetchCheckpointState(p.BookIDs, done, totalBooks)); err != nil {
			candidateFetchLog.Warn("checkpoint failed: opID=%s err=%s",
				logger.SanitizeLogValue(opID), logger.SanitizeLogValue(err.Error()))
		}
	}

	for range numWorkers {
		wg.Go(func() {
			for bookID := range workCh {
				if ctx.Err() != nil {
					return
				}
				result := s.fetchCandidateForBook(ctx, mfs, store, limiter, opID, bookID, p.Force, folderMemo)
				resultJSON, err := json.Marshal(result)
				if err != nil {
					// No result row, so the book stays owed (not marked done)
					// and is retried on resume -- but it is an error for this
					// attempt, and it is counted and logged as one.
					tally.errored.Add(1)
					_ = reporter.Log(slog.LevelWarn, fmt.Sprintf("error: %s — could not record the result: %s",
						opLogBookRef(result.Book.Title, result.Book.Author, bookID, result.Book.FilePath), logger.SanitizeLogValue(err.Error())),
						slog.String("book_id", logger.SanitizeLogValue(bookID)), slog.String("outcome", "error"))
					continue
				}
				tally.record(result)
				logCandidateOutcome(reporter, result)
				if err := store.CreateOperationResult(&database.OperationResult{
					OperationID: opID,
					BookID:      bookID,
					ResultJSON:  string(resultJSON),
					Status:      result.Status,
				}); err != nil {
					// The outcome line above already went out; this says the
					// row behind it was not persisted, so the review page will
					// not show it.
					_ = reporter.Log(slog.LevelWarn, fmt.Sprintf("error: %s — result row not saved: %s",
						opLogBookRef(result.Book.Title, result.Book.Author, bookID, result.Book.FilePath), logger.SanitizeLogValue(err.Error())),
						slog.String("book_id", logger.SanitizeLogValue(bookID)))
				}
				finished := atomic.AddInt64(&completed, 1)
				_ = progress.UpdateProgress(int(finished), totalBooks, tally.progressLine(finished, totalBooks))
				if done.mark(bookID) {
					writeCheckpoint()
				}
			}
		})
	}
	wg.Wait()

	finalCount := atomic.LoadInt64(&completed)

	// Cancellation is REPORTED, not swallowed.
	//
	// This does NOT change the recorded status, and an earlier version of
	// this comment claiming otherwise was wrong: worker.go evaluates
	// `case ctxCanceled:` FIRST when classifying a finished run, reading
	// the run context rather than this return value, so returning nil on a
	// canceled context already persisted "canceled". What returning
	// ctx.Err() changes is the finish log, which now carries the error
	// instead of reading like a clean completion — and it stops the next
	// reader from concluding that a partial fetch reported success.
	if ctx.Err() != nil {
		// Final checkpoint on the way out, so the books finished since the
		// last periodic one are not re-owed. The store does not take ctx, so
		// this write succeeds under a cancelled context.
		writeCheckpoint()
		_ = reporter.Log(slog.LevelInfo, fmt.Sprintf("canceled after %d/%d books — %s",
			finalCount, totalBooks, tally.counts()))
		candidateFetchLog.Info("canceled: opID=%s finalCount=%d totalBooks=%d %s",
			logger.SanitizeLogValue(opID), finalCount, totalBooks, tally.counts())
		return ctx.Err()
	}
	summary := fmt.Sprintf("completed %d/%d books — %s", finalCount, totalBooks, tally.counts())
	_ = progress.UpdateProgress(int(finalCount), totalBooks, summary)
	candidateFetchLog.Info("done: opID=%s force=%v %s", logger.SanitizeLogValue(opID), p.Force, summary)
	return nil
}

// candidateFetchLog is the process-log side of metadata.candidate-fetch
// (internal/logger, printf-style); the per-book lines go to the op log through
// the reporter.
var candidateFetchLog = logger.New("metadata-candidate-fetch")

func init() {
	addOpRegistrar(func(s *Server, reg *opsregistry.Registry) error {
		return s.RegisterMetadataCandidateFetchOp(reg)
	})
}

// candidateFetchFallbackRPS is the op's gate when no enabled source reports a
// budget (none configured): the old fixed figure.
const candidateFetchFallbackRPS = 10

// candidateFetchLimiter is the op's global gate on live provider calls
// (threaded into searchMetadataForBook, which waits on it before every live
// call; cache hits take no token). It is set to the SUM of the enabled
// sources' effective budgets, not removed: each provider's own token bucket
// (providerhttp) already paces that provider, so a sum never binds below
// them, while the gate still bounds the op's total outbound rate if a call
// path ever reaches a provider without its bucket. Before 2026-10-01 it was a
// fixed 10/s across every source, below Audible's configured 8/s plus the
// rest.
func candidateFetchLimiter(rps float64, burst int) *rate.Limiter {
	if rps <= 0 {
		rps = candidateFetchFallbackRPS
	}
	return rate.NewLimiter(rate.Limit(rps), max(burst, 1))
}

// Worker sizing (candidateFetchWorkers). By Little's law the in-flight
// requests needed to keep the budget busy are rps x latency; each book holds
// a worker across its calls (callsPerBook, a ladder of a few rungs per
// source), so workers = rps x latency x callsPerBook. The floor keeps at
// least twice the old fixed 8, so workers are not the cap at prod's budget;
// the ceiling bounds the goroutines and the store's concurrent writes.
const (
	candidateFetchCallLatencySec = 0.15
	candidateFetchCallsPerBook   = 4
	candidateFetchMinWorkers     = 16
	candidateFetchMaxWorkers     = 32
)

// candidateFetchWorkers returns the op's worker count: configured (clamped to
// 1-64) when set, else sized from the summed budget rps and clamped to
// [candidateFetchMinWorkers, candidateFetchMaxWorkers].
func candidateFetchWorkers(rps float64, configured int) int {
	if configured > 0 {
		return min(configured, 64)
	}
	n := int(math.Ceil(rps * candidateFetchCallLatencySec * candidateFetchCallsPerBook))
	return min(max(n, candidateFetchMinWorkers), candidateFetchMaxWorkers)
}
