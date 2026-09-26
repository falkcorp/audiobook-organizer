// file: internal/server/search_result_cache.go
// version: 1.1.0
// guid: 8b45d0ae-7a0c-42ed-93bc-a16869225dfc
// last-edited: 2026-09-25

package server

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/searchcache"
)

var searchCacheLog = logger.New("search-cache")

// initSearchResultCache creates the store change log (always: it is cheap and
// the index worker records into it) and, when search.result_cache.enabled, the
// shared result cache, and installs the store change observer.
//
// Where changes are recorded, and why each place is needed:
//
//   - the store's memdb write-through choke points (database.ChangeObserver):
//     every book-row write, including the inner-store writes the indexedStore
//     decorator never sees (aggregate recomputes after a book-file edit), plus
//     tag writes and author/series renames;
//   - applyIndexChunk, after each Bleve commit: the web search reads Bleve,
//     which is updated asynchronously, so a result re-evaluated between the
//     store write and the index commit would be stamped current while reading
//     the old document. Recording again at commit time re-patches it;
//   - MarkRebuilt (RecordAll): the engine the search runs on changes wholesale.
func (s *Server) initSearchResultCache() {
	s.searchChanges = searchcache.NewChangeLog(searchcache.DefaultRingSize)
	if obs, ok := database.AsCapability[database.ChangeObservable](s.store); ok {
		obs.SetChangeObserver(&searchChangeObserver{s: s})
	}
	cfg := config.AppConfig.Search.ResultCache
	if !cfg.Enabled {
		return
	}
	s.enableSearchResultCache(searchcache.Config{
		MaxBytes: cfg.MaxBytes,
		Wait:     time.Duration(cfg.WaitSeconds) * time.Second,
	})
}

// enableSearchResultCache builds the result cache and hands it to every
// consumer. Split out so tests can enable it without the global config.
func (s *Server) enableSearchResultCache(cfg searchcache.Config) {
	s.searchResults = searchcache.New(s.searchChanges, cfg)
	if s.audiobookService != nil {
		s.audiobookService.SetSearchResultCache(s.searchResults)
	}
}

// recordIndexCommit records the books a Bleve commit just changed.
func (s *Server) recordIndexCommit(ids []string) {
	if s.searchChanges != nil && len(ids) > 0 {
		s.searchChanges.Record(ids...)
	}
}

// renameFanOutWorkers bounds how many author/series rename fan-outs run at
// once. Each one reads every book of the author or series; when memdb is
// short rows that read is a full Pebble book scan, so a bulk rename pass
// (author strip-merge, duplicate merge) must not start one scan per rename.
const renameFanOutWorkers = 2

// searchChangeObserver turns store writes into change-log records and, for
// writes the indexedStore decorator never sees, into index updates.
type searchChangeObserver struct {
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

func (o *searchChangeObserver) BooksChanged(ids ...string) {
	o.s.searchChanges.Record(ids...)
}

func (o *searchChangeObserver) BooksNeedReindex(ids ...string) {
	o.s.searchChanges.Record(ids...)
	for _, id := range ids {
		o.s.enqueueIndex(id, false)
	}
}

// AuthorRenamed fans out to every book carrying the author's name in its
// search document. It runs off the caller's goroutine because the store fires
// it while holding the author name-index lock. indexWorkerBusy covers the fan
// out, so a test that drains the index queue also waits for it.
func (o *searchChangeObserver) AuthorRenamed(authorID int) {
	o.fanOut(func() ([]database.BookCore, error) {
		return o.s.store.GetBooksByAuthorIDForRelinkCore(authorID)
	}, "author", authorID)
}

// SeriesRenamed is AuthorRenamed for series: every version, not just
// primaries, carries the series name in its document.
func (o *searchChangeObserver) SeriesRenamed(seriesID int) {
	o.fanOut(func() ([]database.BookCore, error) {
		return o.s.store.GetBooksBySeriesIDAllVersions(seriesID)
	}, "series", seriesID)
}

// fanOut queues a rename fan-out and returns at once: the store calls it while
// holding its name-index lock. At most renameFanOutWorkers run at a time;
// workers start on demand and exit when the queue is empty. indexWorkerBusy
// counts every queued fan-out, so a test that drains the index queue also
// waits for them.
func (o *searchChangeObserver) fanOut(load func() ([]database.BookCore, error), kind string, id int) {
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

func (o *searchChangeObserver) drainFanOuts() {
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

func (o *searchChangeObserver) runFanOut(load func() ([]database.BookCore, error), kind string, id int) {
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
		s.enqueueIndex(bid, false)
	}
}

// getSearchJob handles GET /api/v1/search/:search_id: the status of a search
// that outlived its request's wait (the 202 path of the list endpoint). When
// it reports done, the client re-issues its original request, which is then a
// cache hit.
func (s *Server) getSearchJob(c *gin.Context) {
	if s.searchResults == nil {
		httputil.RespondWithNotFound(c, "search", c.Param("search_id"))
		return
	}
	st, ok := s.searchResults.Job(c.Param("search_id"))
	if !ok {
		httputil.RespondWithNotFound(c, "search", c.Param("search_id"))
		return
	}
	httputil.RespondWithOK(c, st)
}
