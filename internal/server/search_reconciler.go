// file: internal/server/search_reconciler.go
// version: 1.2.0
// guid: 7c2bb743-3521-45cf-8815-32a1bb927cca
// last-edited: 2026-09-25
//
// Reconciles the Bleve search index against the DB after dropped updates.
//
// WHY THIS EXISTS
//
// enqueueIndex sends onto a bounded channel and drops the event when the
// channel is full. That is a defensible choice — the alternative is letting a
// slow indexer backpressure every write path in the app. What was NOT
// defensible is that nothing reconciled afterwards, so a dropped update
// diverged the index from the DB permanently.
//
// Three separate comments asserted that "a startup reindex will heal any
// gaps". It does not. buildSearchIndexIfEmpty — the only reindex — returns
// early unless DocCount() == 0, so on a populated library it has never run.
// The drop was designed as safe under a guarantee that was never true.
//
// Measured on prod 2026-08-10: 56,537 dropped operations in seven days, all
// on bulk-operation days (Aug 03 and Aug 07).
//
// WHY IT MATTERS MORE NOW
//
// Today a dropped update means stale relevance ranking — tolerable and
// invisible. Once filters and sort are pushed into the Bleve query (design
// doc option A1), a dropped update means a book whose library_state changed
// is ABSENT from the correct filter and PRESENT in the wrong one, with no
// error shown. That promotes the index from a relevance dependency to a
// correctness one, which is why reconciliation lands first.
//
// See docs/design/2026-08-09-search-backend-options.md and
// todo.d/20260810-search-index-queue-drops-silently.md.

//
// WHY IT WEDGED ON 2026-09-24/25 (and what this file now does about it)
//
// Production sat at 61,334 indexed docs against 100,774 books across 13
// restarts with a 100,161-entry dirty set and not one "search index
// reconcile" line. The goroutine dump showed every index writer (this
// reconciler, the index worker, the coverage pass) blocked in scorch's
// prepareSegment waiting for `persisted`; scorch's own stats showed why:
// CurOnDiskFiles=10,202, 92 failed file-merge plans and zero successful ones,
// and the persister paused in pausePersisterForMergerCatchUp, which waits for
// a merge to succeed whenever the directory holds >= 1000 files. A safe
// scorch write returns only after persistence, so every write blocked
// forever. The old per-tick summary was logged AFTER the loop, so it never
// printed. Three changes follow from that:
//
//   - scorch async errors are now logged (internal/search/bleve_index.go);
//   - a watchdog (runSearchIndexWatchdog) reports any write that has been
//     inside bleve longer than searchIndexWriteStallAfter, with the scorch
//     counters that name the cause, from a goroutine that is never itself
//     inside a write;
//   - the drain applies chunks as ONE bleve batch each (one segment, one
//     persistence wait) instead of one single-document segment per book,
//     so a 100k backlog is ~400 batches rather than 100k.

package server

import (
	"errors"
	"log/slog"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/metrics"
	"github.com/falkcorp/audiobook-organizer/internal/search"
)

var searchIndexLog = logger.New("search-index")

// Reconciler tuning. Named constants because the drain rate is a real
// operational trade-off, not an implementation detail — see nextBatchSize.
const (
	// reconcileInterval is how often an EMPTY-or-stalled dirty set is
	// re-checked. A backlog that is making progress is drained continuously
	// (see runSearchReconciler), not once per interval.
	reconcileInterval = 30 * time.Second

	// reconcileMinBatch is drained even when the backlog is tiny, so small
	// backlogs clear in a single pass rather than trickling.
	reconcileMinBatch = 500

	// reconcileMaxBatch caps one pass. A pass lists this many keys, applies
	// them in reconcileChunkSize batches, clears them and logs; the next pass
	// starts immediately while the backlog is still shrinking. The cap bounds
	// how much work one summary line covers, not the drain rate.
	reconcileMaxBatch = 20000

	// reconcileBacklogDivisor sets the adaptive rate: each pass drains
	// backlog/divisor, clamped to [reconcileMinBatch, reconcileMaxBatch].
	reconcileBacklogDivisor = 10

	// reconcileChunkSize is books per bleve batch: one GetBooksByIDs, three
	// relation batch reads and one Batch commit. Inside bleve's recommended
	// 100-1000 docs per batch.
	reconcileChunkSize = 250

	// searchIndexWriteStallAfter is how long a single bleve write may take
	// before the watchdog reports it. Healthy batches persist in well under a
	// second; a minute is unambiguous.
	searchIndexWriteStallAfter = time.Minute
)

// searchIndexDropped counts index events dropped because the queue was full,
// for the lifetime of the process.
//
// This exists because the drop was previously observable ONLY as a WARN line.
// Establishing the 56,537 figure required grepping journald on prod, which is
// not a thing anyone does before being told there is a problem. A counter is
// what makes the next occurrence visible without knowing to look for it.
var searchIndexDropped atomic.Int64

// SearchIndexDroppedCount reports index events dropped since process start.
// Exposed for the metrics endpoint and for tests.
func SearchIndexDroppedCount() int64 { return searchIndexDropped.Load() }

// nextBatchSize returns how many dirty books to drain this pass.
//
// Adaptive: proportional to the backlog, clamped at both ends. Small
// backlogs clear immediately via the floor; bulk-operation backlogs clear
// in a bounded number of back-to-back passes.
func nextBatchSize(backlog int) int {
	if backlog <= 0 {
		return 0
	}
	n := min(min(max(backlog/reconcileBacklogDivisor, reconcileMinBatch), reconcileMaxBatch), backlog)
	return n
}

// markIndexDirty records a dropped index event in the durable dirty set.
//
// Best-effort by design: this runs on the drop path, and a store that cannot
// record the mark must not panic or block the caller's write. A failure here
// is logged at ERROR (not WARN) because it means an index update is lost with
// no way to recover it — strictly worse than the drop itself.
func (s *Server) markIndexDirty(bookID string) {
	ds := database.AsSearchIndexDirtyStore(s.Ops())
	if ds == nil {
		// No durable set available (memdb-only test server, or the store
		// does not implement the capability). The drop is still counted and
		// logged; there is simply nothing to reconcile against.
		return
	}
	if err := ds.MarkSearchIndexDirty(bookID); err != nil {
		slog.Error("search index dirty-set write failed; index update is now unrecoverable",
			"bookID", logger.SanitizeLogValue(bookID), "err", err)
	}
}

// indexChunkStore is what applyIndexChunk reads: a batch point read plus
// the three relation batch reads search.LoadBookRelations uses.
type indexChunkStore interface {
	GetBooksByIDs(ids []string) ([]database.Book, error)
	GetBookByID(id string) (*database.Book, error)
	GetAuthorsByIDs(ids []int) (map[int]*database.Author, error)
	GetSeriesByIDs(ids []int) (map[int]*database.Series, error)
	GetBookTagsByBookIDs(bookIDs []string) (map[string][]string, error)
}

// Compile-time proof that the production store (the indexedStore decorator,
// which embeds database.Store) satisfies indexChunkStore. Without it a method
// missing from database.Store would silently disable the reconciler and send
// every worker event to the dirty set.
var _ indexChunkStore = database.Store(nil)

// reconcileStuckPassLimit is how many consecutive passes may fail EVERY
// remaining key before the rebuild gate is released anyway (see
// reconcileOnce). Without it one permanently unreadable row would keep
// library search on the substring path forever.
const reconcileStuckPassLimit = 2

// indexChunkResult reports one applyIndexChunk call. done holds the IDs
// whose index state now matches the store (upserted or removed); failed
// holds IDs that must stay dirty.
type indexChunkResult struct {
	upserted, removed int
	done, failed      []string
}

// applyIndexChunk re-derives the index state of ids from the store and
// writes it as ONE bleve batch.
//
// Truth is re-read rather than trusting a recorded upsert/delete intent (see
// the package comment): a row that is gone or soft-deleted is removed from the
// index, every other row is upserted.
//
// GetBooksByIDs skips not-found rows silently and, on the first real read
// error, returns the rows read so far PLUS the error. "Requested but not
// returned" therefore means "gone" only when err == nil; on an error the chunk
// falls back to per-ID reads so a single unreadable row fails alone instead
// of pinning its whole chunk dirty forever.
func (s *Server) applyIndexChunk(store indexChunkStore, ids []string) indexChunkResult {
	var res indexChunkResult
	if len(ids) == 0 || s.searchIndex == nil {
		return res
	}
	books, err := store.GetBooksByIDs(ids)
	var live []database.Book
	var deletes []string
	if err == nil {
		got := make(map[string]struct{}, len(books))
		for i := range books {
			got[books[i].ID] = struct{}{}
			if books[i].IsSoftDeleted() {
				deletes = append(deletes, books[i].ID)
			} else {
				live = append(live, books[i])
			}
		}
		for _, id := range ids {
			if _, ok := got[id]; !ok {
				deletes = append(deletes, id)
			}
		}
	} else {
		searchIndexLog.Warn("search index: batch book read failed, retrying chunk per book: %v", err)
		for _, id := range ids {
			b, gerr := store.GetBookByID(id)
			switch {
			case gerr != nil:
				searchIndexLog.Warn("search index: read book %s: %v", logger.SanitizeLogValue(id), gerr)
				res.failed = append(res.failed, id)
			case b == nil || b.IsSoftDeleted():
				deletes = append(deletes, id)
			default:
				live = append(live, *b)
			}
		}
	}

	rel, rerr := search.LoadBookRelations(store, live)
	if rerr != nil {
		searchIndexLog.Warn("search index: relation batch read failed (indexing %d books with partial relations): %v", len(live), rerr)
	}
	docs := make([]search.BookDocument, 0, len(live))
	for i := range live {
		docs = append(docs, search.BookToDocWithRelations(&live[i], rel))
	}
	if err := s.searchIndex.ApplyBatch(docs, deletes); err != nil {
		// Retry one document at a time, as indexBookChunk does, so a single
		// bad document costs one row rather than the chunk.
		searchIndexLog.Warn("search index: batch commit failed for %d books, retrying per book: %v", len(docs)+len(deletes), err)
		for i := range docs {
			if e := s.searchIndex.ApplyBatch(docs[i:i+1], nil); e != nil {
				searchIndexLog.Warn("search index: index %s: %v", logger.SanitizeLogValue(docs[i].BookID), e)
				res.failed = append(res.failed, docs[i].BookID)
				continue
			}
			res.upserted++
			res.done = append(res.done, docs[i].BookID)
		}
		for _, id := range deletes {
			if e := s.searchIndex.ApplyBatch(nil, []string{id}); e != nil {
				searchIndexLog.Warn("search index: delete %s: %v", logger.SanitizeLogValue(id), e)
				res.failed = append(res.failed, id)
				continue
			}
			res.removed++
			res.done = append(res.done, id)
		}
		s.recordIndexCommit(res.done)
		return res
	}
	res.upserted, res.removed = len(docs), len(deletes)
	for i := range docs {
		res.done = append(res.done, docs[i].BookID)
	}
	res.done = append(res.done, deletes...)
	s.recordIndexCommit(res.done)
	return res
}

// runSearchReconciler drains the dirty set until bgCtx is done.
//
// While a pass makes progress and backlog remains, the next pass starts
// immediately; the ticker only paces re-checks of an empty or non-draining
// set. The previous shape — at most one capped batch per 30 s tick — put a
// floor of hours under a 100k backlog even with a healthy index.
func (s *Server) runSearchReconciler() {
	if s.searchIndex == nil {
		return
	}
	ticker := time.NewTicker(reconcileInterval)
	defer ticker.Stop()

	for {
		select {
		case <-s.bgCtx.Done():
			return
		case <-ticker.C:
		}
		for s.bgCtx.Err() == nil {
			drained, remaining := s.reconcileOnce()
			if drained == 0 || remaining == 0 {
				break
			}
		}
	}
}

// reconcileOnce drains one adaptive pass from the dirty set and reports how
// many keys it cleared and how many remain.
//
// The pass is split into reconcileChunkSize chunks applied by a bounded
// errgroup: chunk IDs are disjoint slices of one listing, so two workers
// never write the same document. Each chunk's keys are cleared right after
// its batch commits (a safe scorch batch returns only once persisted), so a
// restart mid-pass loses at most the in-flight chunks' work, never a clear
// that ran ahead of its write.
func (s *Server) reconcileOnce() (drained, remaining int) {
	ds := database.AsSearchIndexDirtyStore(s.Ops())
	if ds == nil || s.searchIndex == nil {
		return 0, 0
	}
	store, ok := s.Ops().(indexChunkStore)
	if !ok {
		searchIndexLog.Warn("search reconcile: store cannot batch-read books; reconcile disabled")
		return 0, 0
	}

	backlog, err := ds.CountSearchIndexDirty()
	if err != nil {
		searchIndexLog.Warn("search reconcile: count dirty set: %v", err)
		return 0, 0
	}
	metrics.SetSearchIndexDirtyBacklog(backlog)
	if backlog == 0 {
		s.maybeMarkSearchIndexRebuilt()
		return 0, 0
	}

	ids, err := ds.ListSearchIndexDirty(nextBatchSize(backlog))
	if err != nil {
		searchIndexLog.Warn("search reconcile: list dirty set: %v", err)
		return 0, backlog
	}

	start := time.Now()
	var mu sync.Mutex
	upserted, removed, failed, cleared := 0, 0, 0, 0
	g, gctx := errgroup.WithContext(s.bgCtx)
	g.SetLimit(max(1, runtime.NumCPU()))
	for lo := 0; lo < len(ids); lo += reconcileChunkSize {
		chunk := ids[lo:min(lo+reconcileChunkSize, len(ids))]
		g.Go(func() error {
			if err := gctx.Err(); err != nil {
				return err
			}
			res := s.applyIndexChunk(store, chunk)
			// Clear only what was written. A failed clear costs one redundant
			// re-index next pass; clearing early would silently drop a repair.
			n := 0
			for _, id := range res.done {
				if cerr := ds.ClearSearchIndexDirty(id); cerr != nil {
					searchIndexLog.Warn("search reconcile: clear dirty key %s: %v", logger.SanitizeLogValue(id), cerr)
					continue
				}
				n++
			}
			mu.Lock()
			upserted += res.upserted
			removed += res.removed
			failed += len(res.failed)
			cleared += n
			mu.Unlock()
			return nil
		})
	}
	if werr := g.Wait(); werr != nil && !errors.Is(werr, s.bgCtx.Err()) {
		searchIndexLog.Warn("search reconcile: pass stopped: %v", werr)
	}

	remaining = max(0, backlog-cleared)
	metrics.SetSearchIndexDirtyBacklog(remaining)
	searchIndexLog.Info("search index reconcile: batch=%d repaired=%d removed=%d failed=%d elapsed=%s remaining=%d",
		len(ids), upserted, removed, failed, time.Since(start).Round(time.Millisecond), remaining)

	// A pass that listed the WHOLE remaining set and failed every key made
	// no progress and never will on its own. After reconcileStuckPassLimit
	// such passes, release the rebuild gate: the rest of the library is
	// indexed, and the failing keys stay dirty and keep being retried.
	if len(ids) == backlog && cleared == 0 && failed == len(ids) {
		if s.reconcileStuckPasses.Add(1) >= reconcileStuckPassLimit && s.searchIndex.Rebuilding() && s.searchCoverageSeeded.Load() {
			sample := ids[:min(len(ids), 5)]
			for i := range sample {
				sample[i] = logger.SanitizeLogValue(sample[i])
			}
			searchIndexLog.Error("search index: %d book(s) fail to index on every pass (e.g. %v); "+
				"releasing the rebuild gate anyway, they stay dirty and are retried", len(ids), sample)
			s.forceMarkSearchIndexRebuilt()
		}
	} else {
		s.reconcileStuckPasses.Store(0)
	}
	if remaining == 0 {
		s.maybeMarkSearchIndexRebuilt()
	}
	return cleared, remaining
}

// maybeMarkSearchIndexRebuilt clears a rebuilding index's marker once the
// index provably covers the library: the coverage pass has seeded every
// missing book in THIS process and the dirty set is empty. Without the
// coverage condition a reconciler pass that ran before seeding finished would
// see an empty set and declare a 0%-covered index complete.
//
// The dirty set is re-counted here rather than trusting a pass's arithmetic:
// a pass that started before coverage wrote its last marks can compute zero
// while real keys remain. Seeded is loaded FIRST so the count is taken after
// seeding finished.
func (s *Server) maybeMarkSearchIndexRebuilt() {
	if s.searchIndex == nil || !s.searchIndex.Rebuilding() || !s.searchCoverageSeeded.Load() {
		return
	}
	ds := database.AsSearchIndexDirtyStore(s.Ops())
	if ds == nil {
		return
	}
	if n, err := ds.CountSearchIndexDirty(); err != nil || n != 0 {
		return
	}
	s.forceMarkSearchIndexRebuilt()
}

// forceMarkSearchIndexRebuilt clears the rebuilding marker and logs it.
func (s *Server) forceMarkSearchIndexRebuilt() {
	if err := s.searchIndex.MarkRebuilt(); err != nil {
		searchIndexLog.Error("search index: rebuild finished but the marker could not be cleared: %v", err)
		return
	}
	// Searches move from the substring fallback back to the index: every
	// cached result was computed by the other engine.
	if s.searchChanges != nil {
		s.searchChanges.RecordAll()
	}
	searchIndexLog.Info("search index rebuild complete; library search is served by the index again")
}

// runSearchIndexWatchdog reports bleve writes that do not return.
//
// It must run in its own goroutine: on 2026-09-24/25 every goroutine that
// could have logged the stall was itself blocked inside a write. The report
// carries scorch's own counters, which is what named the cause then
// (CurOnDiskFiles over the persister's 1000-file pause threshold, merge plans
// failing with none succeeding).
func (s *Server) runSearchIndexWatchdog() {
	if s.searchIndex == nil {
		return
	}
	ticker := time.NewTicker(searchIndexWriteStallAfter / 2)
	defer ticker.Stop()
	for {
		select {
		case <-s.bgCtx.Done():
			return
		case <-ticker.C:
			s.checkSearchIndexStall()
		}
	}
}

// checkSearchIndexStall logs one Error line when the oldest in-flight write
// has exceeded searchIndexWriteStallAfter. Returns whether it reported.
func (s *Server) checkSearchIndexStall() bool {
	oldest, n := s.searchIndex.OldestWrite()
	if oldest < searchIndexWriteStallAfter {
		return false
	}
	st := s.searchIndex.HealthStats()
	searchIndexLog.Error("search index write stalled: oldest=%s inflight=%d on_disk_files=%d root_segments=%d "+
		"merge_tasks_failed=%d merge_tasks_done=%d last_merged_epoch=%d last_persisted_epoch=%d "+
		"persister_merger_pauses=%d resumes=%d async_errors=%d",
		oldest.Round(time.Second), n, st["CurOnDiskFiles"], st["TotFileSegmentsAtRoot"],
		st["TotFileMergePlanTasksErr"], st["TotFileMergePlanTasksDone"], st["LastMergedEpoch"],
		st["LastPersistedEpoch"], st["TotPersisterSlowMergerPause"], st["TotPersisterSlowMergerResume"],
		search.AsyncErrorCount())
	return true
}
