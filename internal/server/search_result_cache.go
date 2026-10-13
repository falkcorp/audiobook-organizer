// file: internal/server/search_result_cache.go
// version: 1.2.0
// guid: 8b45d0ae-7a0c-42ed-93bc-a16869225dfc
// last-edited: 2026-10-13

package server

import (
	"time"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/searchcache"
)

var searchCacheLog = logger.New("search-cache")

// initSearchResultCache creates, when search.result_cache.enabled, the shared
// result cache and hands it to every consumer. The store change log and the
// observer that feeds it exist from NewServer on (installSearchObserver), so
// the cache has the full history of writes whenever it is turned on.
func (s *Server) initSearchResultCache() {
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
