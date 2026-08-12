// file: internal/server/audiobooks_helpers.go
// version: 1.4.0
// guid: 439aa827-edea-481d-8918-ddacd2c140b7
// last-edited: 2026-08-12

// Server-package helpers relocated out of audiobooks_handlers.go when the
// audiobooks HTTP handlers were extracted into the handlers/audiobooks
// sub-package (ADR-003 Phase 4). These helpers STAY in package server
// because they are shared with files that did NOT move:
//
//   - buildAudiobookListResponse — the list-pipeline builder, called by both the
//     relocated ListAudiobooks handler (via the injected buildListResponse
//     closure) and the library list cache warmer (library_list_warmer.go). Its
//     signature + *Server-method form are preserved EXACTLY so existing callers
//     compile unchanged.
//   - buildFacetsResponse (INIT-4 T4) — the facets response builder, called by
//     both the relocated AudiobookFacets handler (via the injected
//     buildFacetsResponse closure, mirroring buildListResponse above) and
//     warmFacetsCache below, so the handler and the startup warmer can never
//     drift into different response shapes. DB-distinct genre/language lists
//     are unconditional; AudiobookService.FacetCounts() (internal/audiobooks/
//     service_facets.go) adds genre_counts/language_counts/tag_counts on
//     success and is skipped (not a 500) on any error, including the
//     nil-index ErrSearchIndexUnavailable sentinel.
//   - warmFacetsCache — the startup facets cache pre-warmer, launched as a
//     goroutine from server_lifecycle.go. facetsCacheKey is shared with the
//     audiobookFacets handler in the sub-package; the string value MUST match
//     (both use "all").
//   - runAutoPurgeSoftDeleted — the auto-purge maintenance op body, called by
//     server_maintenance_deps.go (RunAutoPurgeSoftDeleted). Its only caller is
//     in package server, so no func injection into the sub-package is needed.

package server

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/falkcorp/audiobook-organizer/internal/activity"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/fingerprint"
	"github.com/gin-gonic/gin"
)

// buildAudiobookListResponse runs the full /api/v1/audiobooks list pipeline
// (service call, quarantine filter, enrichment, batch file load, fingerprint
// compute, count) and returns the response payload. Shared between the HTTP
// handler (handlers/audiobooks ListAudiobooks, via the injected
// buildListResponse closure) and the startup cache warmer so both produce
// identical results.
func (s *Server) buildAudiobookListResponse(ctx context.Context, limit, offset int, search string, authorID, seriesID *int, filters ListFilters, showQuarantined bool) (gin.H, error) {
	// Push quarantine exclusion DOWN into the indexed scan (and the count) so a
	// page of N returns N non-quarantined books and totalCount agrees. Dropping
	// quarantined rows AFTER pagination (as this used to) made a 500-page return
	// fewer than 500 and made count != items.
	if !showQuarantined {
		filters.ExcludeQuarantined = true
	}

	books, matchTotal, err := s.audiobookService.GetAudiobooksWithTotal(ctx, limit, offset, search, authorID, seriesID, filters)
	if err != nil {
		return nil, err
	}

	// Safety net for the degraded (memdb-down) read path, where the Pebble
	// fallback does not honor ExcludeQuarantined. In the normal memdb path the
	// scan already excluded these, so this drops nothing.
	if !showQuarantined {
		filtered := books[:0]
		for _, b := range books {
			if b.QuarantinedAt == nil {
				filtered = append(filtered, b)
			}
		}
		books = filtered
	}

	// Fetch book_files ONCE up front; thread the map into both enrichment
	// (for duration/file-size aggregation) and the fingerprint compute loop
	// below. Previously each path independently called GetBookFilesForIDsCore.
	bookFilesMap := s.audiobookService.FetchBookFilesForBooks(books)

	enriched := s.audiobookService.EnrichAudiobooksWithNamesAndFiles(books, bookFilesMap)

	for i, book := range enriched {
		files := bookFilesMap[book.ID]
		fpFiles := make([]fingerprint.FileWithFingerprint, len(files))
		for j := range files {
			fpFiles[j] = &files[j]
		}
		status, fpCount, coverage, lastFp := fingerprint.ComputeFingerprintFields(fpFiles)
		enriched[i].FingerprintStatus = status
		enriched[i].FingerprintedFileCount = fpCount
		enriched[i].TotalFileCount = len(files)
		enriched[i].CoveragePercent = coverage
		enriched[i].LastFingerprintedAt = lastFp
	}

	// len(enriched) is the PAGE length. It is only ever the right answer when
	// the page happens to hold every match, which is why reporting it looked
	// correct in casual testing and was wrong the moment a limit bit.
	//
	// Measured on production 2026-08-12 before this change: search=honour
	// reported count=5 at limit=5, count=3 at limit=3 and count=21 at
	// limit=250. The count tracked the limit, so the UI could never show how
	// many matches existed and any "page 2 of N" was fiction.
	totalCount := len(enriched)

	// The service returns -1 when it cannot establish a true total; anything
	// >= 0 is a real match count (exact, or an explicitly-warned lower bound
	// when a post-filter over-fetch window was exhausted). Prefer it.
	if matchTotal >= 0 {
		totalCount = matchTotal
	}

	hasFilters := filters.IsPrimaryVersion != nil || filters.ExcludeQuarantined || filters.LibraryState != "" || filters.Tag != "" || len(filters.Tags) > 0
	if search == "" && authorID == nil && seriesID == nil {
		if hasFilters {
			if tc, err := s.audiobookService.CountAudiobooksFiltered(ctx, filters); err == nil {
				totalCount = tc
			}
		} else {
			if tc, err := s.audiobookService.CountAudiobooks(ctx); err == nil {
				totalCount = tc
			}
		}
	}

	return gin.H{"items": enriched, "count": totalCount, "limit": limit, "offset": offset}, nil
}

const facetsCacheKey = "all"

// buildFacetsResponse composes the /audiobooks/facets response body: the
// DB-distinct genre/language lists (unconditional, byte-identical to the
// pre-INIT-4-T4 shape) plus best-effort Bleve facet counts. Shared between
// the relocated AudiobookFacets handler (via the injected buildFacetsResponse
// closure passed into audiobookshandler.New, see wire_handlers.go) and
// warmFacetsCache below so both produce the identical shape — see this
// file's package doc comment for why the shared builder lives here rather
// than in the handler sub-package.
//
// Any AudiobookService.FacetCounts() error (including the nil-index
// ErrSearchIndexUnavailable sentinel) fails OPEN: the genre_counts /
// language_counts / tag_counts keys are simply omitted and the DB-distinct
// response is returned unchanged. Only a DB-distinct fetch failure (store
// nil, GetDistinctGenres/Languages erroring) returns a non-nil error here —
// that is the pre-existing 500 path, unaffected by this task.
func (s *Server) buildFacetsResponse(ctx context.Context) (gin.H, error) {
	_ = ctx // reserved for parity with buildAudiobookListResponse; no context-aware call in this path yet.
	if s.Store() == nil {
		return nil, fmt.Errorf("database not initialized")
	}
	genres, err := s.Store().GetDistinctGenres()
	if err != nil {
		return nil, fmt.Errorf("failed to fetch genres: %w", err)
	}
	languages, err := s.Store().GetDistinctLanguages()
	if err != nil {
		return nil, fmt.Errorf("failed to fetch languages: %w", err)
	}
	if genres == nil {
		genres = []string{}
	}
	if languages == nil {
		languages = []string{}
	}
	result := gin.H{"genres": genres, "languages": languages}

	if s.audiobookService != nil {
		genreCounts, languageCounts, tagCounts, fcErr := s.audiobookService.FacetCounts()
		if fcErr != nil {
			slog.Debug("facets: Bleve FacetCounts unavailable, serving DB-distinct only", "err", fcErr)
		} else {
			result["genre_counts"] = genreCounts
			result["language_counts"] = languageCounts
			result["tag_counts"] = tagCounts
		}
	}

	return result, nil
}

// warmFacetsCache pre-computes the facets response at startup via
// buildFacetsResponse (shared with the AudiobookFacets handler — see that
// method's doc comment). Called as a goroutine from Server.Start so the
// first Library page load hits the cache instead of triggering a full
// PebbleDB scan.
func (s *Server) warmFacetsCache() {
	if s.Store() == nil {
		return
	}
	slog.Info("facets pre-warming genres/languages cache")
	result, err := s.buildFacetsResponse(context.Background())
	if err != nil {
		slog.Info("facets warm-up failed", "err", err)
		return
	}
	s.facetsCache.Set(facetsCacheKey, result)
	genreCount, langCount := 0, 0
	if g, ok := result["genres"].([]string); ok {
		genreCount = len(g)
	}
	if l, ok := result["languages"].([]string); ok {
		langCount = len(l)
	}
	slog.Info("facets cache warm genres, languages", "genres_count", genreCount, "languages_count", langCount)
}

// runAutoPurgeSoftDeleted purges soft-deleted books older than the configured
// retention window, emitting activity log entries. Invoked from the maintenance
// scheduler (server_maintenance_deps.go RunAutoPurgeSoftDeleted).
func (s *Server) runAutoPurgeSoftDeleted(opID string) {
	if config.AppConfig.PurgeSoftDeletedAfterDays <= 0 {
		return
	}
	if s.Store() == nil {
		slog.Debug("Auto-purge skipped database not initialized")
		return
	}

	days := config.AppConfig.PurgeSoftDeletedAfterDays
	result, err := s.audiobookService.PurgeSoftDeletedBooks(context.Background(), config.AppConfig.PurgeSoftDeletedDeleteFiles, &days)
	if err != nil {
		slog.Warn("Auto-purge failed", "err", err)
		return
	}

	msg := fmt.Sprintf("Purged %d/%d soft-deleted books (%d files deleted, %d errors)",
		result.Purged, result.Attempted, result.FilesDeleted, len(result.Errors))
	slog.Info("Auto-purge", "msg", msg)
	activity.EmitInfo(s.activityWriter, opID, "purge-deleted", "purge-deleted", msg,
		activity.TagsIf(result.Purged == 0, activity.NoOpTag)...)
	for _, e := range result.Errors {
		activity.LogBatch(s.activityWriter, opID, "purge-deleted", "purge-deleted",
			activity.BatchItem{Name: e, Detail: "error"})
	}
}
