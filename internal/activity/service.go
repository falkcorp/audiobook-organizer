// file: internal/activity/service.go
// version: 1.11.0
// guid: a1b2c3d4-e5f6-7890-abcd-ef1234567890
// last-edited: 2026-09-19

package activity

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

// Service wraps an ActivityStorer and provides business-level methods
// for recording and querying unified activity log entries.
type Service struct {
	store database.ActivityStorer

	// Deferred writes (RecordDeferred). mu guards everything below it.
	mu sync.Mutex
	// deferred holds entries not yet handed to the store, oldest first.
	deferred []database.ActivityEntry
	// released is set by StartDeferredFlush; before it, deferred entries only
	// queue.
	released bool
	// flushDone is non-nil while a flush goroutine is running and is closed
	// when it exits.
	flushDone chan struct{}
	// inflight is the size of the batch the flush goroutine has taken off
	// deferred and is handing to the store right now.
	inflight int
	// closed is set by Close once every deferred entry has been written and
	// the store is about to be closed. After it, RecordDeferred refuses (and
	// says so) instead of starting a write against a closed store.
	closed bool
}

var serviceLog = logger.New("activity")

// NewService creates a new Service backed by the given store.
func NewService(store database.ActivityStorer) *Service {
	return &Service{store: store}
}

// Record inserts an activity entry into the store. Automatically enriches entry
// with derived tags (op:, book:, outcome:, source:, action:, scope:) before
// storing. The entry ID is discarded; callers that need it should call the
// store directly.
func (s *Service) Record(entry database.ActivityEntry) error {
	EnrichTags(&entry)
	_, err := s.store.Record(entry)
	return err
}

// RecordDeferred queues entry for a later, asynchronous write and returns
// immediately. It never touches the store on the caller's goroutine.
//
// It exists for the server's startup path. On 2026-09-14 the "Server started"
// Record inside NewServer was the first SQLite write after a deploy, it landed
// on a multi-GB WAL left by an interrupted compaction, and it ran the whole
// checkpoint before the HTTP listener opened: about six minutes of downtime.
// Any store write can be slow for reasons outside the caller's control, so
// nothing before the listener may wait on one.
//
// Entries queue in memory until StartDeferredFlush; after that each call
// queues and wakes the background flusher. The store's error is not returned
// because the write has not happened yet when this returns.
//
// What is guaranteed: no entry is lost without a log line that counts it.
//   - Close writes every queued entry before it closes the store, and never
//     closes the store under an in-flight deferred write. If its time budget
//     runs out first, it leaves the store open, logs how many entries were not
//     yet confirmed written, and returns an error. Those entries are still
//     being written and land if the process lives long enough.
//   - A store error during the flush is logged with the number of entries it
//     cost.
//   - A call after Close has closed the store is refused and logged.
//
// What is not guaranteed: that an entry is on disk if the process exits
// before the flush reaches it (SIGKILL, or a stop that outruns the budget).
// Those losses are the ones counted in Close's log line.
func (s *Service) RecordDeferred(entry database.ActivityEntry) {
	EnrichTags(&entry)
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		serviceLog.Warn("activity entry %q not written: the activity store is already closed (1 entry lost)", entry.Summary)
		return
	}
	s.deferred = append(s.deferred, entry)
	if s.released {
		s.startFlushLocked()
	}
	s.mu.Unlock()
}

// StartDeferredFlush releases the RecordDeferred queue: everything queued so
// far, and anything queued later, is written by a background goroutine. The
// server calls it once the HTTP listener has been started. It does not wait.
func (s *Service) StartDeferredFlush() {
	s.mu.Lock()
	s.released = true
	s.startFlushLocked()
	s.mu.Unlock()
}

// FlushDeferred releases the queue (if StartDeferredFlush has not already) and
// waits until every deferred entry has been handed to the store, or until ctx
// ends. Shutdown calls it before closing the store so a queued entry is not
// lost to an early exit.
func (s *Service) FlushDeferred(ctx context.Context) error {
	s.mu.Lock()
	s.released = true
	s.startFlushLocked()
	done := s.flushDone
	s.mu.Unlock()
	if done == nil {
		return nil
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		s.mu.Lock()
		pending := len(s.deferred) + s.inflight
		s.mu.Unlock()
		return fmt.Errorf("activity: %d deferred entries not yet confirmed written: %w", pending, ctx.Err())
	}
}

// Close writes every deferred entry and then closes the store, waiting at
// most until ctx ends. It is the only way the store should be closed while
// RecordDeferred may have queued work.
//
// It never closes the store under an in-flight deferred write. If ctx ends
// first, the store is left open: the flush goroutine keeps writing, the
// SQLite store's own bounded Close is not reached, and the returned error
// counts the entries not yet confirmed written, so a loss at process exit is
// never silent. A later Close retries. Once Close has succeeded, further calls
// return nil and RecordDeferred refuses new entries.
//
// The store may not own what it writes to: PebbleActivityStore borrows the
// main store's DB and its Close is a no-op, so for that backend "left open"
// does not keep the DB open — the main store's owner closes it regardless.
// A flush still running then gets pebble.ErrClosed back as an error (the store
// recovers the panic), and writeDeferred logs the lost count.
func (s *Service) Close(ctx context.Context) error {
	for {
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return nil
		}
		// Everything written and no flush running: shut the door under the same
		// lock, so a RecordDeferred cannot slip a new write in between this check
		// and store.Close.
		if s.flushDone == nil && len(s.deferred) == 0 {
			s.closed = true
			s.mu.Unlock()
			return s.store.Close()
		}
		s.mu.Unlock()
		if err := s.FlushDeferred(ctx); err != nil {
			return fmt.Errorf("activity store left open: %w", err)
		}
	}
}

// startFlushLocked starts the flush goroutine when there is work and none is
// running. Caller holds s.mu.
func (s *Service) startFlushLocked() {
	if s.flushDone != nil || len(s.deferred) == 0 {
		return
	}
	done := make(chan struct{})
	s.flushDone = done
	go s.flushDeferred(done)
}

// flushDeferred writes the queue in arrival order until it is empty. Entries
// queued while a batch is being written are picked up by the next iteration,
// so order is preserved and nothing is stranded between the empty check and
// the goroutine exiting (both happen under s.mu).
func (s *Service) flushDeferred(done chan struct{}) {
	defer close(done)
	for {
		s.mu.Lock()
		s.inflight = 0
		batch := s.deferred
		s.deferred = nil
		if len(batch) == 0 {
			s.flushDone = nil
			s.mu.Unlock()
			return
		}
		s.inflight = len(batch)
		s.mu.Unlock()
		s.writeDeferred(batch)
	}
}

// writeDeferred hands one batch to the store: one commit when the store
// supports RecordBatch, else one Record per entry. Tags were enriched when the
// entry was queued.
func (s *Service) writeDeferred(batch []database.ActivityEntry) {
	if br, ok := s.store.(batchRecorder); ok {
		written, err := br.RecordBatch(batch)
		if lost := len(batch) - written; err != nil || lost > 0 {
			serviceLog.Warn("deferred activity write stored %d of %d entries: %v", written, len(batch), err)
		}
		return
	}
	lost := 0
	var lastErr error
	for _, e := range batch {
		if _, err := s.store.Record(e); err != nil {
			lost++
			lastErr = err
		}
	}
	if lost > 0 {
		serviceLog.Warn("deferred activity write lost %d of %d entries: %v", lost, len(batch), lastErr)
	}
}

// Query returns entries matching the filter plus the total matching count.
// Callers on a request path must pass the request's context: the scan aborts
// as soon as it is cancelled, which is what stops an abandoned request from
// scanning the whole log after the client has disconnected.
func (s *Service) Query(ctx context.Context, filter database.ActivityFilter) ([]database.ActivityEntry, int, error) {
	return s.store.Query(ctx, filter)
}

// QueryWithPartial is Query plus whether the answer is partial — the store's
// walk hit its budget before filling the page or exhausting the matches, so
// older matches were not examined. The /activity handler returns it so the UI
// can say so rather than presenting a short result as complete.
func (s *Service) QueryWithPartial(ctx context.Context, filter database.ActivityFilter) (database.ActivityQueryResult, error) {
	return database.QueryWithPartialOf(ctx, s.store, filter)
}

// Summarize collapses old entries in the given tier that are older than olderThan.
// Returns the count of original rows deleted.
func (s *Service) Summarize(ctx context.Context, olderThan time.Time, tier string) (int, error) {
	return s.store.Summarize(ctx, olderThan, tier)
}

// Prune hard-deletes all entries of the given tier older than olderThan.
// Returns the number of rows deleted. ctx is forwarded to the store, so a
// cancelled cleanup stops at the store's next batch (see
// database.ActivityRetention.Prune).
func (s *Service) Prune(ctx context.Context, olderThan time.Time, tier string) (int, error) {
	return s.store.Prune(ctx, olderThan, tier)
}

// CompactByDay groups old change/debug entries by UTC day into digest rows.
func (s *Service) CompactByDay(ctx context.Context, olderThan time.Time) (database.CompactResult, error) {
	return s.store.CompactByDay(ctx, olderThan)
}

// GetDistinctSources returns all unique sources with their entry counts,
// narrowed by the given filter's tier/level/since/until/search fields.
// As with Query, request-path callers must pass the request's context.
func (s *Service) GetDistinctSources(ctx context.Context, filter database.ActivityFilter) ([]database.SourceCount, error) {
	return s.store.GetDistinctSources(ctx, filter)
}

// RecompactDigests re-derives type, tier, and tags on all stored daily-digest
// entries that were compacted before tag enrichment was added (2026-05-20).
// Returns the count of digests touched and skipped.
func (s *Service) RecompactDigests(ctx context.Context) (database.RecompactResult, error) {
	return s.store.RecompactDigests(ctx)
}

// Store returns the underlying ActivityStorer (e.g. for close or direct access).
func (s *Service) Store() database.ActivityStorer {
	return s.store
}

// ErrSummaryClampUnsupported is returned when the active activity backend has no
// SQLite side to clamp (ActivityBackend=pebble). It is a distinct error rather
// than a zero result so a caller can tell "nothing needed clamping" from "this
// backend cannot be clamped at all" — the two look identical in the counters and
// mean opposite things about whether the work still has to happen.
var ErrSummaryClampUnsupported = errors.New("activity: summary clamp requires the SQLite backend")

// ClampSummaries retroactively applies the write-path summary cap to rows
// written before that cap existed, optionally VACUUMing afterwards to hand the
// freed pages back to the filesystem.
//
// vacuum is a separate switch because clamping alone shrinks values without
// shrinking the file: a run that skips it truthfully reports gigabytes reclaimed
// while df does not move. It is skipped only for a dry run, which has nothing to
// compact.
//
// It is deliberately NOT conditioned on this pass having clamped anything. That
// guard existed until 2026-09-08 and made the operation unable to fix the exact
// situation it was written for: after the first production clamp reclaimed
// 10.66 GB, the WAL held 11,514,065,152 bytes that VACUUM would have released —
// but every subsequent call found zero rows left to clamp and therefore skipped
// the vacuum, so no request could reach the reclaim path. Restarting does not
// help either: SQLite only deletes the -wal on a clean last-connection close.
// `vacuum` already defaults to false, so passing it is an explicit request and
// honouring it unconditionally is what the caller asked for.
func (s *Service) ClampSummaries(ctx context.Context, max int, dryRun, vacuum bool) (database.ClampSummariesResult, error) {
	var zero database.ClampSummariesResult
	clamper, ok := database.FindSummaryClamper(s.store)
	if !ok {
		return zero, ErrSummaryClampUnsupported
	}

	res, err := clamper.ClampOversizedSummaries(ctx, database.ClampSummariesOptions{Max: max, DryRun: dryRun})
	if err != nil {
		return res, err
	}
	if vacuum && !dryRun {
		if _, verr := clamper.VacuumActivity(ctx); verr != nil {
			// The clamp already committed. Surface the vacuum failure without
			// discarding a successful pass that may have taken hours.
			return res, fmt.Errorf("clamp committed but vacuum failed: %w", verr)
		}
	}
	return res, nil
}

// ReclaimMigratedActivity deletes Pebble-side activity rows that the SQLite
// cutover has made redundant, freeing space in the main database.
//
// It takes no store parameter on purpose: the Service already holds the store,
// so threading one through the signature would widen the coupling for nothing
// (see the worked example in CLAUDE.md). When the wired store is not the
// migration wrapper — the Pebble-only escape hatch, or a SQLite-open fallback —
// the reclaim reports a refusal naming the type it got, rather than failing,
// because "there is no duplicate copy" is an answer and not an error.
func (s *Service) ReclaimMigratedActivity(
	ctx context.Context,
	retain time.Duration,
	dryRun bool,
	onTier database.ActivityReclaimProgress,
) (database.ActivityReclaimResult, error) {
	// Pass the store as wired. Asserting the wrapper type here would throw away
	// the concrete type before the refusal could name it, and that name is the
	// only thing distinguishing "legitimately Pebble-only" from "a wrapper got
	// wired in front and this op has been silently refusing ever since".
	return database.ReclaimMigratedActivity(ctx, s.store, retain, dryRun, onTier)
}
