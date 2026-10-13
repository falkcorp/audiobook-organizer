// file: internal/server/index_queue.go
// version: 2.0.0
// guid: 5d2e4f3a-7b5a-4a70-b8c5-3d7e0f1b9a79
// last-edited: 2026-10-13
//
// The search index queue: a bounded channel the store's ChangeObserver
// (serverChangeObserver, search_observer.go) feeds with the IDs of books whose
// search document may be stale, and the single worker that drains it into
// Bleve. Every write path reaches the queue through that observer; there is no
// store decorator (the indexedStore decorator that used to intercept
// CreateBook/UpdateBook/ModifyBook/FillBookMediaInfo/DeleteBook was removed in
// 07-S3, because the observer already covered every other path and two
// mechanisms disagreed about which writes counted).
//
// Indexing is async via a bounded channel. If the channel fills up
// (worker stuck, Bleve slow) the request is dropped from the queue and
// recorded in a durable dirty set, which runSearchReconciler drains on a
// ticker (search_reconciler.go).
//
// CORRECTION 2026-08-09: this comment previously said the drop was safe
// because "the library search rebuilds on startup (see
// buildSearchIndexIfEmpty)". That was false. buildSearchIndexIfEmpty
// returns early unless the index has ZERO documents, so on a populated
// library it has never run and nothing repaired a dropped update. Prod
// dropped 56,537 index operations in the seven days to 2026-08-10 with no
// reconciliation whatsoever. The dirty set is what actually makes the drop
// safe; do not restore the old claim.

package server

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/metrics"
)

// indexRequest is the payload carried on the index worker channel: the book
// whose search document may be stale. It carries no delete flag because the
// worker re-derives delete versus upsert from the store (applyIndexChunk
// removes a book that is gone or soft-deleted and upserts the rest), so a flag
// would be a second opinion that could only disagree with the row.
type indexRequest struct {
	bookID string
}

// dropLogInterval is the least time between "queue full" warnings. A bulk
// operation that saturates the queue drops thousands of events a second, each
// from inside a store write; one log line per drop would turn the overflow
// into the load that keeps it overflowing. searchIndexDropped and the
// search_index_dropped metric still count every drop.
const dropLogInterval = 10 * time.Second

// dropLogGate lets one warning through per dropLogInterval and counts the
// drops it swallowed in between.
type dropLogGate struct {
	mu         sync.Mutex
	last       time.Time
	suppressed int
}

// allow reports whether to log a drop at now, and how many drops were
// swallowed since the last logged one.
func (g *dropLogGate) allow(now time.Time) (bool, int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.last.IsZero() && now.Sub(g.last) < dropLogInterval {
		g.suppressed++
		return false, 0
	}
	g.last = now
	n := g.suppressed
	g.suppressed = 0
	return true, n
}

// enqueueIndex asks the worker to re-read a book's search document.
//
// A book already queued and not yet popped is not queued again: the worker
// reads the row when it pops the ID, so that one request already covers every
// write made while it waited. Without this a bulk write (a credit edit reports
// the book several times, a scan thousands of books) fills the bounded channel
// with duplicates that only the worker's per-batch dedup removes, and the
// overflow is what drops real events. indexRequestPopped lifts the mark when
// the worker pops the ID, BEFORE it reads the row, so a write that lands after
// the pop queues a fresh request.
//
// A full queue drops the event from the channel and records the book in the
// durable dirty set instead, so runSearchReconciler repairs it on the next
// tick. (It previously dropped with no record at all, on the false premise that
// a startup reindex would heal it -- see the package comment.) Safe to call
// concurrently with Shutdown: the mutex + closed flag prevents sending on a
// closed channel during teardown.
//
// The channel and indexPending are memory only, so every path that would leave a
// write with no queued request behind it records the book in the durable dirty
// set instead (the reconciler reads the latest row, so one mark covers every
// write to the book):
//   - before Start has created the queue (indexQueue nil, not closed): a write
//     made between NewServer and Start is not lost, because the reconciler
//     started by startSearchIndexing drains the set once the index has opened.
//     If the index never opens, the set just holds one key per book written
//     since boot and is never drained;
//   - after the queue is closed (Shutdown): ops that outlive the registry's
//     bounded shutdown keep writing books, and nothing will pop a request;
//   - when the queue is full and the book is already pending: the worker is
//     behind or wedged (production wedged for hours in September), the pending
//     request may never be popped before a restart, and this write would
//     otherwise coalesce into nothing durable;
//   - when the queue is full and the send fails (the overflow branch).
//
// markPendingIndexDirty covers whatever is still pending at Shutdown.
//
// indexWorkerBusy is incremented before enqueueing so drainQueue can
// reliably wait for completion: the window between dequeue and the
// worker starting work is covered by the pre-increment. A coalesced call adds
// nothing to it: the request already queued is what the counter is waiting on.
func (s *Server) enqueueIndex(bookID string) {
	if bookID == "" {
		return
	}
	s.indexQueueMu.RLock()
	defer s.indexQueueMu.RUnlock()
	if s.indexQueueClosed || s.indexQueue == nil {
		s.markIndexDirty(bookID)
		return
	}
	s.indexPendingMu.Lock()
	if _, queued := s.indexPending[bookID]; queued {
		s.indexPendingMu.Unlock()
		if len(s.indexQueue) == cap(s.indexQueue) {
			s.markIndexDirty(bookID)
		}
		return
	}
	if s.indexPending == nil {
		s.indexPending = map[string]struct{}{}
	}
	s.indexPending[bookID] = struct{}{}
	s.indexPendingMu.Unlock()

	atomic.AddInt32(&s.indexWorkerBusy, 1)
	select {
	case s.indexQueue <- indexRequest{bookID: bookID}:
	default:
		atomic.AddInt32(&s.indexWorkerBusy, -1)
		searchIndexDropped.Add(1)
		metrics.IncSearchIndexDropped()
		// Lift the pending mark first: no request is queued for this book, so a
		// write arriving now must queue itself (or find the queue full and be
		// recorded) rather than coalesce against a mark that will never be
		// popped. Then record the drop durably so the reconciler repairs it.
		s.indexRequestPopped(bookID)
		s.markIndexDirty(bookID)
		if ok, swallowed := s.indexDropLog.allow(time.Now()); ok {
			searchIndexLog.Warn("search index queue full: events are being dropped and marked for reconcile (latest: book %s; %d more dropped since the last report, %d in total)",
				logger.SanitizeLogValue(bookID), swallowed, searchIndexDropped.Load())
		}
	}
}

// indexRequestPopped lifts bookID's pending mark. The worker calls it for
// every request it takes off the channel, before it reads the book.
func (s *Server) indexRequestPopped(bookID string) {
	s.indexPendingMu.Lock()
	delete(s.indexPending, bookID)
	s.indexPendingMu.Unlock()
}

// markPendingIndexDirty records every request still waiting on the queue in
// the durable dirty set. Shutdown calls it after the background goroutines have
// been waited for: whatever the worker did not pop (it was wedged, or the wait
// timed out) would otherwise be lost with the process, and startup only seeds
// documents that are missing from the index, not stale ones.
func (s *Server) markPendingIndexDirty() {
	s.indexPendingMu.Lock()
	ids := make([]string, 0, len(s.indexPending))
	for id := range s.indexPending {
		ids = append(ids, id)
	}
	s.indexPendingMu.Unlock()
	for _, id := range ids {
		s.markIndexDirty(id)
	}
}

// closeIndexQueue takes the write lock, closes the channel, and
// flips the closed flag so subsequent enqueueIndex calls no-op.
// Called exactly once from Shutdown.
func (s *Server) closeIndexQueue() {
	s.indexQueueMu.Lock()
	defer s.indexQueueMu.Unlock()
	if s.indexQueueClosed || s.indexQueue == nil {
		return
	}
	s.indexQueueClosed = true
	close(s.indexQueue)
}

// indexWorkerMaxBatch caps how many queued events one worker commit covers.
const indexWorkerMaxBatch = 256

// runIndexWorker drains the index queue in micro-batches. Designed as a
// single long-lived goroutine so the live write path has one ordered writer.
// Exits when the queue is closed by Shutdown, after flushing what it holds.
//
// It takes whatever is already queued (up to indexWorkerMaxBatch), dedups by
// book ID, re-derives each book's index state from the store and commits the
// lot as ONE bleve batch via applyIndexChunk. The previous one-event-one-
// Index() shape wrote a single-document segment per event and waited for
// persistence each time, so a bulk operation filled the 1024-deep queue and
// every further event became a drop. There is no delete flag to honour:
// applyIndexChunk removes a book that is gone or soft-deleted.
//
// indexWorkerBusy is decremented by the number of events consumed and only
// after the commit, so a waiter that sees 0 knows the index reflects them.
// IDs the commit could not write go to the durable dirty set.
func (s *Server) runIndexWorker() {
	if s.indexQueue == nil {
		return
	}
	store, ok := s.Ops().(indexChunkStore)
	for {
		req, open := <-s.indexQueue
		if !open {
			return
		}
		s.indexRequestPopped(req.bookID)
		ids := []string{req.bookID}
		seen := map[string]struct{}{req.bookID: {}}
		consumed := 1
	collect:
		for consumed < indexWorkerMaxBatch {
			select {
			case r, more := <-s.indexQueue:
				if !more {
					break collect
				}
				consumed++
				s.indexRequestPopped(r.bookID)
				if _, dup := seen[r.bookID]; !dup {
					seen[r.bookID] = struct{}{}
					ids = append(ids, r.bookID)
				}
			default:
				break collect
			}
		}
		if ok {
			res := s.applyIndexChunk(store, ids)
			for _, id := range res.failed {
				s.markIndexDirty(id)
			}
		} else {
			for _, id := range ids {
				s.markIndexDirty(id)
			}
		}
		atomic.AddInt32(&s.indexWorkerBusy, -int32(consumed))
	}
}
