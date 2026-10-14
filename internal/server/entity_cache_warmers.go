// file: internal/server/entity_cache_warmers.go
// version: 1.1.0
// guid: 3a9cb455-8b92-441a-abd9-319d13111a14
// last-edited: 2026-10-14

// entity_cache_warmers holds the non-HTTP author/series cache warmers invoked
// from server_lifecycle.go at startup. They stay *Server methods (calling the
// server-owned authorSeriesService and caches) and are intentionally NOT moved
// into the handlers/entities sub-package, which only owns HTTP handlers.

package server

import "log/slog"

// Both warmers wait for the memdb warmup first (waitForMemdbWarmup). They start
// at boot, before memdb is published, and without the wait their book-file and
// count reads take the Pebble full-scan fallbacks -- ~11GB of allocation in the
// first three minutes on the production library, for caches memdb fills in
// milliseconds a moment later.

func (s *Server) warmAuthorsCache() {
	if s.Ops() == nil {
		return
	}
	if !s.waitForMemdbWarmup("authors") {
		return
	}
	slog.Info("authors pre-warming cache")
	result, err := s.authorSeriesService.ListAuthorsWithCounts()
	if err != nil {
		slog.Info("authors warm-up failed", "err", err)
		return
	}
	s.authorsCache.Set("all", result)
	slog.Info("authors cache warmed", "count", result.Count)
}

func (s *Server) warmSeriesCache() {
	if s.Ops() == nil {
		return
	}
	if !s.waitForMemdbWarmup("series") {
		return
	}
	slog.Info("series pre-warming cache")
	result, err := s.authorSeriesService.ListSeriesWithCounts()
	if err != nil {
		slog.Info("series warm-up failed", "err", err)
		return
	}
	s.seriesCache.Set("all", result)
	slog.Info("series cache warmed", "count", result.Count)
}
