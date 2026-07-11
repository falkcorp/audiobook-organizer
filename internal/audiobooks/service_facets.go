// file: internal/audiobooks/service_facets.go
// version: 1.0.0
// guid: 7d2b1c4e-8f3a-4b6d-9e0c-1a2b3c4d5e6f
// last-edited: 2026-07-11

// AudiobookService.FacetCounts (INIT-4 T4) — a thin wrapper around
// BleveIndex.FacetCounts giving the /audiobooks/facets response optional
// genre/language/tag value->count maps. Deliberately its own file (not
// service_query.go, which owns searchWithBleve) per the task-04 brief: this
// surface is additive and independently owned so sibling tasks touching
// service_query.go don't collide with it.

package audiobooks

import "errors"

// ErrSearchIndexUnavailable indicates the Bleve index is not open yet (e.g.
// early startup, before Server.Start finishes opening it). Mirrors
// internal/playlist/evaluator.go's sentinel of the same name; defined
// locally rather than imported so this package doesn't take a dependency on
// internal/playlist for a single error value. Callers fail open to a
// DB-distinct-only response rather than surfacing a 500.
var ErrSearchIndexUnavailable = errors.New("search index not yet available")

// FacetCounts returns value->count maps for the genre, language, and tags
// keyword fields via the Bleve index. Returns ErrSearchIndexUnavailable
// when the index hasn't been wired in yet (SetSearchIndex not called, or
// called with nil) — never a panic, never a partial/inconsistent result.
func (svc *AudiobookService) FacetCounts() (genres, languages, tags map[string]int, err error) {
	if svc.searchIndex == nil {
		return nil, nil, nil, ErrSearchIndexUnavailable
	}
	return svc.searchIndex.FacetCounts(0)
}
