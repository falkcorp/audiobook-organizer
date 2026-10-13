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
	"sync/atomic"

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

// enqueueIndex submits an index event. A full queue drops the event from
// the channel and records the book in the durable dirty set instead, so
// runSearchReconciler repairs it on the next tick. (It previously dropped
// with no record at all, on the false premise that a startup reindex would
// heal it — see the package comment.) Safe to call
// concurrently with Shutdown: the mutex + closed flag prevents
// sending on a closed channel during teardown.
//
// indexWorkerBusy is incremented before enqueueing so drainQueue can
// reliably wait for completion: the window between dequeue and the
// worker starting work is covered by the pre-increment.
func (s *Server) enqueueIndex(bookID string) {
	if bookID == "" {
		return
	}
	s.indexQueueMu.RLock()
	defer s.indexQueueMu.RUnlock()
	if s.indexQueueClosed || s.indexQueue == nil {
		return
	}
	atomic.AddInt32(&s.indexWorkerBusy, 1)
	select {
	case s.indexQueue <- indexRequest{bookID: bookID}:
	default:
		atomic.AddInt32(&s.indexWorkerBusy, -1)
		searchIndexDropped.Add(1)
		metrics.IncSearchIndexDropped()
		// Record it durably before logging, so a crash between the two still
		// leaves the book reconcilable.
		s.markIndexDirty(bookID)
		searchIndexLog.Warn("search index queue full, event dropped and marked for reconcile: book %s, %d dropped in total",
			logger.SanitizeLogValue(bookID), searchIndexDropped.Load())
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
