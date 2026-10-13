// file: internal/server/search_observer.go
// version: 1.0.0
// guid: 6a1d9c3e-8f42-4b57-a0e6-3c7b5d2f9e18
// last-edited: 2026-10-13

package server

import (
	"sync"
	"sync/atomic"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/searchcache"
)

// installSearchObserver creates the store change log and installs the server's
// one database.ChangeObserver on the store. NewServer calls it right after the
// Server exists, before the service registry is built and before any goroutine
// can write, so no write misses the observer; enqueueIndex is a no-op until
// Start creates the index queue.
//
// There is exactly one observer slot on the store (PebbleStore.changeObs), so
// everything that needs to hear about store writes is composed inside
// serverChangeObserver rather than registered next to it.
func (s *Server) installSearchObserver() {
	s.searchChanges = searchcache.NewChangeLog(searchcache.DefaultRingSize)
	obs, ok := database.AsCapability[database.ChangeObservable](s.store)
	if !ok {
		// Nothing tells a store without the capability's writes to the index,
		// so search would silently go stale. Every production store is a
		// PebbleStore; a double without the hook is a test.
		searchCacheLog.Warn("search: store %T does not report writes (no ChangeObservable); the search index and result cache will not follow its writes", s.store)
		return
	}
	o := &serverChangeObserver{s: s}
	obs.SetChangeObserver(o)
	// By identity, so a server shutting down never removes the observer a
	// newer server installed on the same store.
	s.releaseSearchObserver = func() { obs.ClearChangeObserver(o) }
}

// renameFanOutWorkers bounds how many author/series rename fan-outs run at
// once. Each one reads every book of the author or series; when memdb is
// short rows that read is a full Pebble book scan, so a bulk rename pass
// (author strip-merge, duplicate merge) must not start one scan per rename.
const renameFanOutWorkers = 2

// serverChangeObserver is the one database.ChangeObserver the server installs
// on its store. Every store write that can change a search result reaches the
// search subsystem through it, and nowhere else:
//
//   - the change log the search result cache patches itself from
//     (searchChanges.Record), for every book written, created or deleted;
//   - the Bleve index queue (enqueueIndex), for every one of those books. This
//     replaces the indexedStore decorator, which intercepted only the five
//     methods called through it and so missed every in-store write (aggregate
//     recomputes after a book-file edit, tag writes, author and series
//     renames). The hooks sit at the store's memdb write-through choke points,
//     which every book-row writer already calls after its Pebble commit, so a
//     new book writer is covered without anyone remembering to add a call;
//   - rename fan-out (AuthorRenamed, SeriesRenamed): every book carrying the
//     renamed name in its document is recorded and re-indexed.
//
// The index worker also records into the change log after each Bleve commit
// (applyIndexChunk -> recordIndexCommit): the web search reads Bleve, which is
// updated asynchronously, so a result re-evaluated between the store write and
// the index commit would be stamped current while reading the old document.
// MarkRebuilt (RecordAll) covers the engine being replaced wholesale.
//
// The store contract (change_observer.go) is that implementations are cheap
// and never call back into the store synchronously, because several hooks fire
// while a store lock is held. enqueueIndex qualifies: a bounded non-blocking
// channel send whose overflow branch is one lock-free Pebble Set into the
// dirty set. A book written before Start has opened the index (indexQueue nil)
// is not indexed at once; enqueueIndex records it in the durable dirty set and
// the reconciler Start launches repairs it once the index is open (the startup
// coverage pass alone seeds only documents MISSING from the index, so it would
// not repair a stale title written in that gap).
type serverChangeObserver struct {
	s *Server

	// Rename fan-outs waiting for a worker, coalesced by (kind, id): a queued
	// fan-out has not read its books yet, so it already covers a second
	// rename of the same author or series.
	mu      sync.Mutex
	pending map[fanOutKey]func() ([]database.BookCore, error)
	queue   []fanOutKey
	workers int
}

type fanOutKey struct {
	kind string
	id   int
}

// BooksChanged is told about book rows written, created or deleted: the result
// cache records them and the index re-reads them. A deleted or soft-deleted book is removed by the worker, which
// reads the row rather than trusting a flag.
func (o *serverChangeObserver) BooksChanged(ids ...string) {
	o.recordAndIndex(ids)
}

// BooksNeedReindex is told about books whose search document changed without a
// book-row write: tag writes.
func (o *serverChangeObserver) BooksNeedReindex(ids ...string) {
	o.recordAndIndex(ids)
}

// BookCreditsChanged is told about author/narrator junction rows. The search
// document is built from the book row and never reads the junction, so this
// invalidates cached results only; scheduling a re-index would rebuild an
// identical document (and a credit edit that also writes the book row reports
// that through BooksChanged).
func (o *serverChangeObserver) BookCreditsChanged(ids ...string) {
	o.s.searchChanges.Record(ids...)
}

func (o *serverChangeObserver) recordAndIndex(ids []string) {
	o.s.searchChanges.Record(ids...)
	for _, id := range ids {
		o.s.enqueueIndex(id)
	}
}

// AuthorRenamed fans out to every book carrying the author's name in its
// search document. It runs off the caller's goroutine because the store fires
// it while holding the author name-index lock. indexWorkerBusy covers the fan
// out, so a test that drains the index queue also waits for it.
func (o *serverChangeObserver) AuthorRenamed(authorID int) {
	o.fanOut(func() ([]database.BookCore, error) {
		return o.s.store.GetBooksByAuthorIDForRelinkCore(authorID)
	}, "author", authorID)
}

// SeriesRenamed is AuthorRenamed for series: every version, not just
// primaries, carries the series name in its document.
func (o *serverChangeObserver) SeriesRenamed(seriesID int) {
	o.fanOut(func() ([]database.BookCore, error) {
		return o.s.store.GetBooksBySeriesIDAllVersions(seriesID)
	}, "series", seriesID)
}

// fanOut queues a rename fan-out and returns at once: the store calls it while
// holding its name-index lock. At most renameFanOutWorkers run at a time;
// workers start on demand and exit when the queue is empty. indexWorkerBusy
// counts every queued fan-out, so a test that drains the index queue also
// waits for them.
func (o *serverChangeObserver) fanOut(load func() ([]database.BookCore, error), kind string, id int) {
	k := fanOutKey{kind: kind, id: id}
	o.mu.Lock()
	if _, queued := o.pending[k]; queued {
		o.mu.Unlock()
		return
	}
	if o.pending == nil {
		o.pending = map[fanOutKey]func() ([]database.BookCore, error){}
	}
	o.pending[k] = load
	o.queue = append(o.queue, k)
	atomic.AddInt32(&o.s.indexWorkerBusy, 1)
	start := o.workers < renameFanOutWorkers
	if start {
		o.workers++
	}
	o.mu.Unlock()
	if start {
		go o.drainFanOuts()
	}
}

func (o *serverChangeObserver) drainFanOuts() {
	for {
		o.mu.Lock()
		if len(o.queue) == 0 {
			o.workers--
			o.queue = nil
			o.mu.Unlock()
			return
		}
		k := o.queue[0]
		o.queue = o.queue[1:]
		load := o.pending[k]
		delete(o.pending, k)
		o.mu.Unlock()
		o.runFanOut(load, k.kind, k.id)
		atomic.AddInt32(&o.s.indexWorkerBusy, -1)
	}
}

func (o *serverChangeObserver) runFanOut(load func() ([]database.BookCore, error), kind string, id int) {
	s := o.s
	books, err := load()
	if err != nil {
		// The rename is in the store but the cached results naming the old
		// name cannot be found book by book: invalidate everything.
		searchCacheLog.Warn("search cache: %s %d rename fan-out failed, invalidating all: %v", kind, id, err)
		s.searchChanges.RecordAll()
		return
	}
	ids := make([]string, 0, len(books))
	for i := range books {
		ids = append(ids, books[i].ID)
	}
	s.searchChanges.Record(ids...)
	for _, bid := range ids {
		s.enqueueIndex(bid)
	}
}

// releaseSearchAndAuthority detaches this server's change observer from the
// store and unregisters its authority provider, each by identity. Idempotent.
func (s *Server) releaseSearchAndAuthority() {
	if s.releaseSearchObserver != nil {
		s.releaseSearchObserver()
	}
	if s.releaseAuthorityProvider != nil {
		s.releaseAuthorityProvider()
	}
}
