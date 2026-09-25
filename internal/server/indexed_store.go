// file: internal/server/indexed_store.go
// version: 1.11.0
// guid: 5d2e4f3a-7b5a-4a70-b8c5-3d7e0f1b9a79
// last-edited: 2026-09-25
//
// indexedStore decorates a database.Store so that every successful
// book mutation (create / update / delete) schedules an async
// Bleve index update. This keeps the search index in sync without
// threading explicit index calls through every handler and service
// that touches books.
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
	"fmt"
	"log/slog"

	"sync/atomic"

	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/metrics"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// indexedStore wraps an inner Store and fires index-update events on
// book mutations. The embedded interface forwards every other method
// to the underlying store transparently.
//
// "Every other method" means every method DECLARED ON database.Store. Embedding
// an interface promotes only that interface's method set, so the narrow
// capability interfaces that deliberately live outside database.Store
// (SyncIdentityStore, SyncFileStore, BookmarkStore, ...) are NOT reachable
// through this type. Unwrap below is what makes them discoverable again.
type indexedStore struct {
	database.Store
	server *Server
}

// Compile-time proof that this decorator advertises the unwrap capability, which
// is what lets database.As*Store lookups resolve capabilities against the store
// it wraps. If Unwrap is ever dropped or renamed, the build fails here instead of
// the failure reappearing at runtime as a silent nil in an unrelated package.
var _ database.StoreUnwrapper = (*indexedStore)(nil)

// Compile-time proof that the fingerprint-window methods reach callers through
// the decorator. They are part of database.Store (via BookFileFingerprintStore)
// and are promoted by the embedded Store; no override is needed because windows
// live in the fpwin: keyspace, which Bleve does not index. If they ever move to
// a capability interface outside Store, this line fails the build instead of a
// type assertion failing silently in production.
var _ database.FingerprintWindowStore = (*indexedStore)(nil)

// Compile-time proof that the raw KV methods -- including the paged
// ScanPrefixPage and DeleteRawBatch the AI result-journal prune relies on --
// reach callers through the decorator. They are part of database.Store (via
// RawKVStore) and promoted by the embedded Store; the aijournal: keyspace is
// not indexed by Bleve, so no override is needed.
var _ database.RawKVStore = (*indexedStore)(nil)

// CreateBook writes to the inner store and schedules an index
// refresh for the newly-assigned book ID on success.
func (s *indexedStore) CreateBook(b *database.Book) (*database.Book, error) {
	created, err := s.Store.CreateBook(b)
	if err == nil && created != nil {
		s.server.enqueueIndex(created.ID, false)
	}
	return created, err
}

// UpdateBook schedules a re-index on success. The update may be a
// narrow field change but we reindex the full document to keep
// things simple — BookToDoc is cheap relative to Bleve's cost.
//
// A soft-delete (SoftDeleteBook in internal/merge/service.go) goes through
// this exact path: it sets MarkedForDeletion=true and calls store.UpdateBook,
// same as any other field edit. Before this check, every UpdateBook — soft-
// delete included — unconditionally enqueued a REINDEX (delete=false). The
// worker's IndexBookByID reads the book back via GetBookByID, which does not
// filter MarkedForDeletion (only the *listing* paths do), so it happily
// upserted the just-trashed book straight back into Bleve: the book stayed
// searchable until the next periodic reconcileSearchIndexCoverage pass
// noticed it as a stale doc and deleted it, which could be minutes away. Now
// the updated row's own trash bit decides delete vs. reindex, so the search
// index and the trash agree immediately, with no reconcile pass in between.
func (s *indexedStore) UpdateBook(id string, b *database.Book) (*database.Book, error) {
	updated, err := s.Store.UpdateBook(id, b)
	if err == nil {
		s.server.enqueueIndex(id, updated.IsSoftDeleted())
	}
	return updated, err
}

// ModifyBook schedules a re-index exactly like UpdateBook. Without this
// override the embedded Store's ModifyBook would be promoted and every caller
// converted from GetBookByID -> UpdateBook to ModifyBook would silently stop
// refreshing Bleve. A nil row (book gone) or an error schedules nothing; a
// callback that skipped the write still re-indexes, which is harmless.
func (s *indexedStore) ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error) {
	updated, err := s.Store.ModifyBook(id, fn)
	if err == nil && updated != nil {
		s.server.enqueueIndex(id, updated.IsSoftDeleted())
	}
	return updated, err
}

// FillBookMediaInfo schedules a re-index when it wrote: duration, bitrate,
// sample rate and channels are in the Bleve document (search.BookToDoc), so a
// backfill that bypassed this decorator would leave the index stale. A nil
// row (book gone) or an error schedules nothing.
func (s *indexedStore) FillBookMediaInfo(id string, patch database.BookMediaInfoPatch) (*database.Book, error) {
	updated, err := s.Store.FillBookMediaInfo(id, patch)
	if err == nil && updated != nil {
		s.server.enqueueIndex(id, updated.IsSoftDeleted())
	}
	return updated, err
}

// Unwrap returns the inner store so decorator-aware helpers can peel layers and
// reach concrete sub-interfaces. database.asCapability walks this chain, which is
// what makes database.AsSyncIdentityStore / AsSyncFileStore / AsBookmarkStore
// keep working through this decorator, which NewServer installs before any
// service captures the store.
//
// Reaching past this decorator is safe for those capabilities specifically: sync
// identity, sync files and bookmarks live in keyspaces this type does not index,
// so a caller that writes to them directly loses no index update. Book mutations
// still arrive through the explicit CreateBook/UpdateBook/DeleteBook overrides,
// which is what actually keeps Bleve in sync. Do NOT rely on Unwrap to bypass a
// decorator whose behaviour is load-bearing.
func (s *indexedStore) Unwrap() database.Store {
	return s.Store
}

// DeleteBook removes the row and schedules a Bleve delete on success.
func (s *indexedStore) DeleteBook(id string) error {
	if err := s.Store.DeleteBook(id); err != nil {
		return err
	}
	s.server.enqueueIndex(id, true)
	return nil
}

// indexRequest is the payload carried on the index worker channel.
// Delete=true removes from Bleve; otherwise a reindex is performed
// by reading the current book state from the store.
type indexRequest struct {
	bookID string
	delete bool
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
func (s *Server) enqueueIndex(bookID string, del bool) {
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
	case s.indexQueue <- indexRequest{bookID: bookID, delete: del}:
	default:
		atomic.AddInt32(&s.indexWorkerBusy, -1)
		searchIndexDropped.Add(1)
		metrics.IncSearchIndexDropped()
		// Record it durably before logging, so a crash between the two still
		// leaves the book reconcilable.
		s.markIndexDirty(bookID)
		// The old message hardcoded "(delete)" while also logging del=false,
		// which read as a delete on every upsert drop and made the prod logs
		// actively misleading. The operation is in the del field.
		slog.Warn("search index queue full, event dropped and marked for reconcile",
			"bookID", logger.SanitizeLogValue(bookID), "del", logger.SanitizeLogValue(fmt.Sprint(del)), "dropped_total", searchIndexDropped.Load())
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
// every further event became a drop. The recorded delete flag is not needed:
// applyIndexChunk removes a book that is gone or soft-deleted, which is
// exactly when the decorator enqueued a delete.
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
