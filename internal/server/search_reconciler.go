// file: internal/server/search_reconciler.go
// version: 1.0.0
// guid: 7c2bb743-3521-45cf-8815-32a1bb927cca
// last-edited: 2026-08-09
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

package server

import (
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// Reconciler tuning. Named constants because the drain rate is a real
// operational trade-off, not an implementation detail — see nextBatchSize.
const (
	// reconcileInterval is how often the dirty set is drained. Short enough
	// that a normal-day backlog (a handful of drops) clears promptly, long
	// enough that the scan is nowhere near a hot path.
	reconcileInterval = 30 * time.Second

	// reconcileMinBatch is drained even when the backlog is tiny, so small
	// backlogs clear in a single tick rather than trickling.
	reconcileMinBatch = 500

	// reconcileMaxBatch caps a single tick's work so a huge backlog cannot
	// monopolise the store or Bleve in one burst.
	reconcileMaxBatch = 5000

	// reconcileBacklogDivisor sets the adaptive rate: each tick drains
	// backlog/divisor, clamped to [reconcileMinBatch, reconcileMaxBatch].
	//
	// 10 (i.e. 10% per tick) rather than the 1% first sketched: at 1% a
	// 56,537-entry backlog drains ~565 per tick, which is indistinguishable
	// from the fixed floor of 500 and takes ~50 minutes. Percentage drains
	// also decay — the batch shrinks as the backlog does, so the tail is the
	// slowest part, which is backwards. At 10% capped, the same backlog
	// clears in ~11 ticks (~5.5 minutes) and small backlogs still clear in
	// one. Change this one constant to retune.
	reconcileBacklogDivisor = 10
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

// nextBatchSize returns how many dirty books to drain this tick.
//
// Adaptive: proportional to the backlog, clamped at both ends. Small
// backlogs clear immediately via the floor; bulk-operation backlogs clear
// quickly without a single tick doing unbounded work.
func nextBatchSize(backlog int) int {
	if backlog <= 0 {
		return 0
	}
	n := backlog / reconcileBacklogDivisor
	if n < reconcileMinBatch {
		n = reconcileMinBatch
	}
	if n > reconcileMaxBatch {
		n = reconcileMaxBatch
	}
	if n > backlog {
		n = backlog
	}
	return n
}

// markIndexDirty records a dropped index event in the durable dirty set.
//
// Best-effort by design: this runs on the drop path, and a store that cannot
// record the mark must not panic or block the caller's write. A failure here
// is logged at ERROR (not WARN) because it means an index update is lost with
// no way to recover it — strictly worse than the drop itself.
func (s *Server) markIndexDirty(bookID string) {
	ds := database.AsSearchIndexDirtyStore(s.Store())
	if ds == nil {
		// No durable set available (memdb-only test server, or the store
		// does not implement the capability). The drop is still counted and
		// logged; there is simply nothing to reconcile against.
		return
	}
	if err := ds.MarkSearchIndexDirty(bookID); err != nil {
		slog.Error("search index dirty-set write failed; index update is now unrecoverable",
			"bookID", bookID, "err", err)
	}
}

// runSearchReconciler drains the dirty set on a ticker until bgCtx is done.
//
// Runs as a single goroutine so re-index work stays serialized with respect
// to itself, matching runIndexWorker's rationale: Bleve sees ordered writes
// and no read of DB state races another reconcile of the same book.
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
			s.reconcileOnce()
		}
	}
}

// reconcileOnce drains one adaptive batch from the dirty set.
//
// Each entry is re-derived from the DB rather than replaying a recorded
// upsert/delete intent: if the book still exists it is re-indexed, otherwise
// it is removed from the index. Re-reading truth is the point — this whole
// mechanism exists because a recorded intent was lost, so trusting another
// recorded intent would repeat the mistake.
func (s *Server) reconcileOnce() {
	ds := database.AsSearchIndexDirtyStore(s.Store())
	if ds == nil {
		return
	}

	backlog, err := ds.CountSearchIndexDirty()
	if err != nil {
		slog.Warn("search reconcile: count dirty set", "err", err)
		return
	}
	if backlog == 0 {
		return
	}

	batch := nextBatchSize(backlog)
	ids, err := ds.ListSearchIndexDirty(batch)
	if err != nil {
		slog.Warn("search reconcile: list dirty set", "err", err)
		return
	}

	start := time.Now()
	repaired, removed, failed := 0, 0, 0

	for _, id := range ids {
		// Shutdown mid-batch: stop cleanly. Un-drained keys stay in the set
		// and are picked up on the next start, which is exactly what
		// persisting the set buys us.
		if s.bgCtx.Err() != nil {
			break
		}

		// GetBookByID, matching IndexBookByID's own read — a nil book with a
		// nil error is the "row is gone" signal, which is what distinguishes
		// a reindex from an index delete.
		book, gerr := s.Store().GetBookByID(id)
		switch {
		case gerr != nil:
			// Leave the key in place so the next tick retries it.
			slog.Warn("search reconcile: read book", "bookID", id, "err", gerr)
			failed++
			continue
		case book == nil:
			// Book is gone; the index entry must go too.
			if derr := s.DeleteIndexedBook(id); derr != nil {
				slog.Warn("search reconcile: delete from index", "bookID", id, "err", derr)
				failed++
				continue
			}
			removed++
		default:
			if ierr := s.IndexBookByID(id); ierr != nil {
				slog.Warn("search reconcile: reindex", "bookID", id, "err", ierr)
				failed++
				continue
			}
			repaired++
		}

		// Clear only after the index write succeeded. A failed clear leaves a
		// redundant re-index next tick, which is harmless; clearing early
		// would silently drop the repair, which is not.
		if cerr := ds.ClearSearchIndexDirty(id); cerr != nil {
			slog.Warn("search reconcile: clear dirty key", "bookID", id, "err", cerr)
		}
	}

	slog.Info("search index reconcile",
		"backlog", backlog,
		"batch", len(ids),
		"repaired", repaired,
		"removed", removed,
		"failed", failed,
		"remaining", backlog-repaired-removed,
		"took", time.Since(start).Round(time.Millisecond),
	)
}
