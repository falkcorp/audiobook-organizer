// file: internal/server/audiobooks_helpers.go
// version: 1.1.0
// guid: 439aa827-edea-481d-8918-ddacd2c140b7
// last-edited: 2026-06-28

// Server-package helpers relocated out of audiobooks_handlers.go when the
// audiobooks HTTP handlers were extracted into the handlers/audiobooks
// sub-package (ADR-003 Phase 4). These three helpers STAY in package server
// because they are shared with files that did NOT move:
//
//   - buildAudiobookListResponse — the list-pipeline builder, called by both the
//     relocated ListAudiobooks handler (via the injected buildListResponse
//     closure) and the library list cache warmer (library_list_warmer.go). Its
//     signature + *Server-method form are preserved EXACTLY so existing callers
//     compile unchanged.
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

	"github.com/gin-gonic/gin"
	"github.com/falkcorp/audiobook-organizer/internal/activity"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/fingerprint"
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

	books, err := s.audiobookService.GetAudiobooks(ctx, limit, offset, search, authorID, seriesID, filters)
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
	// below. Previously each path independently called GetBookFilesForIDs.
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

	totalCount := len(enriched)
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

// warmFacetsCache pre-computes genres and languages at startup.
// Called as a goroutine from Server.Start so the first Library page load
// hits the cache instead of triggering a full PebbleDB scan.
func (s *Server) warmFacetsCache() {
	if s.Store() == nil {
		return
	}
	slog.Info("facets pre-warming genres/languages cache")
	genres, err := s.Store().GetDistinctGenres()
	if err != nil {
		slog.Info("facets genre warm-up failed", "err", err)
		return
	}
	languages, err := s.Store().GetDistinctLanguages()
	if err != nil {
		slog.Info("facets language warm-up failed", "err", err)
		return
	}
	if genres == nil {
		genres = []string{}
	}
	if languages == nil {
		languages = []string{}
	}
	s.facetsCache.Set(facetsCacheKey, gin.H{"genres": genres, "languages": languages})
	slog.Info("facets cache warm genres, languages", "genres_count", len(genres), "languages_count", len(languages))
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
