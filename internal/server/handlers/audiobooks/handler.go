// file: internal/server/handlers/audiobooks/handler.go
// version: 1.5.0
// guid: 51fac747-9478-4075-8621-9da4bbdedc37
// last-edited: 2026-08-13

// Package audiobookshandler hosts the main library list / CRUD HTTP handlers
// extracted from the server package's audiobooks_handlers.go: book listing
// (with quick-query / file-error / quarantine / per-user-filter fast paths and
// the list cache), count, facets, soft-delete listing / restore / purge,
// rescan, cover art, get, segments, book-file listing + patch, track-info
// extraction, relocate, segment tags, metadata + path history, field states,
// undo (single field + last apply), external IDs, user tags + detailed tags,
// alternative titles CRUD, batch tag update, batch update / operations,
// changelog, and change tracking (36 handlers total). Behavior is preserved
// byte-for-byte (response shapes, status codes, pagination, cache keys).
//
// The package is named audiobookshandler (dir handlers/audiobooks) to avoid
// clashing with the existing internal/audiobooks package, imported here as
// audiobookspkg.
//
// Dependencies that lived on the *Server receiver are reached through narrow
// interfaces (AudiobookService, AudiobookUpdater, WriteBackEnqueuer,
// MetadataStateService, MetadataFetchService, BatchService, ChangelogService,
// ExternalIDStore) plus concrete *cache.Cache[T] caches (the cache exception)
// and a set of INJECTED func fields that wrap helpers / behavior that STAY in
// package server because they are shared with files that did not move
// (library_list_warmer.go, server_lifecycle.go, server_maintenance_deps.go) or
// reference server-private types:
//
//   - buildListResponse — wraps the relocated *Server.buildAudiobookListResponse
//     (audiobooks_helpers.go), shared with the library list cache warmer.
//   - isProtectedPath — wraps *Server.isProtectedPath (server_middleware.go).
//   - enrichBook — wraps *Server.enrichBookForResponseSingle, whose return type
//     (enrichedBookResponse) is server-private (the Phase-3 ai pattern).
//   - getFieldStates — wraps *MetadataStateService.LoadMetadataState, whose
//     return type (map[string]metadataFieldState) is metafetch-private.
//   - getExternalIDStore — wraps asExternalIDStore(s.Store()) (server adapter).
//   - publishEvent — wraps *Server.publishEvent (shared plugin event bus).
//
// As a result package audiobookshandler never imports package server.
//
// The store is a WIRE-TIME SNAPSHOT (assigned once in New, never swapped after
// that). The interface-typed service deps are also wire-time snapshots, each
// guarded against typed-nil boxing by the controller in wire_handlers.go.
// The write-back batcher is the sole exception: it remains a lazy provider
// closure because integration tests swap server.writeBackBatcher post-wire.

package audiobookshandler

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"strings"

	audiobookspkg "github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/cache"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/fingerprint"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/plugin"
	"github.com/falkcorp/audiobook-organizer/internal/security/pathvalidation"
	servermiddleware "github.com/falkcorp/audiobook-organizer/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

// facetsCacheKey is the single cache key under which audiobookFacets stores /
// reads the {genres, languages} payload. It MUST match the value used by the
// server-resident warmFacetsCache (audiobooks_helpers.go) or the warmer and the
// handler would miss each other's cache writes.
const facetsCacheKey = "all"

// Handler hosts the audiobooks-domain HTTP endpoints.
type Handler struct {
	// store is the wire-time snapshot of the database store. All handlers read
	// from this field directly (no lazy provider needed — no post-wire swap for
	// this package's store). The concrete store value (un-stripped) is preserved
	// so the handlers' inline type assertions (Unwrap / ListBooksWithFileErrors /
	// GetAllBookIDsForQuickQuery / GetBookFilesForIDsCore / InvalidateLibraryStats)
	// still resolve against the dynamic type.
	store AudiobooksStore

	audiobookService AudiobookService
	audiobookUpdater AudiobookUpdater

	// getWriteBack resolves the iTunes write-back batcher LAZILY, at request time.
	// Unlike the other service deps, server.writeBackBatcher is swapped AFTER
	// wireHandlers by integration tests (and the original handlers read
	// s.writeBackBatcher at request time / late binding), so a wire-time snapshot
	// would capture the pre-swap value and miss the enqueue. The provider performs
	// the typed-nil guard so the in-method `!= nil` checks (mirroring the old
	// `s.writeBackBatcher != nil` guards) hold.
	getWriteBack func() WriteBackEnqueuer

	metadataStateService MetadataStateService
	metadataFetchService MetadataFetchService
	batchService         BatchService
	changelogService     ChangelogService

	// Concrete caches (the cache exception): clean generic db-adjacent types
	// under heavy multi-method use, passed by pointer. The handlers nil-check
	// them exactly where the originals did.
	listCache    *cache.Cache[gin.H]
	facetsCache  *cache.Cache[gin.H]
	authorsCache *cache.Cache[*audiobookspkg.AuthorWithCountListResponse]
	seriesCache  *cache.Cache[*audiobookspkg.SeriesWithCountsResponse]

	// --- injected funcs wrapping behavior that stays in package server ---

	// buildListResponse wraps the relocated *Server.buildAudiobookListResponse
	// (audiobooks_helpers.go), shared with the library list cache warmer. The
	// signature uses audiobookspkg.ListFilters directly (the server ListFilters
	// alias IS that type), so no type move / any-cast is needed; the response is
	// byte-for-byte identical.
	buildListResponse func(ctx context.Context, limit, offset int, search string, authorID, seriesID *int, filters audiobookspkg.ListFilters, showQuarantined bool) (gin.H, error)

	// buildFacetsResponse wraps *Server.buildFacetsResponse (audiobooks_helpers.go),
	// shared with the startup facets cache warmer (warmFacetsCache) so both
	// produce the identical {"genres":[...],"languages":[...]} shape, plus the
	// additive genre_counts/language_counts/tag_counts keys (INIT-4 T4). The
	// wrapped method folds in AudiobookService.FacetCounts() (internal/audiobooks/
	// service_facets.go) for those additive keys, failing open to the
	// DB-distinct-only shape on any FacetCounts error (including the nil-index
	// ErrSearchIndexUnavailable sentinel) — never a 500 from this handler.
	buildFacetsResponse func(ctx context.Context) (gin.H, error)

	// isProtectedPath wraps *Server.isProtectedPath (server_middleware.go), used
	// by updateAudiobook to skip write-back for protected paths.
	isProtectedPath func(filePath string) bool

	// enrichBook wraps *Server.enrichBookForResponseSingle, whose concrete return
	// type (enrichedBookResponse) is server-private, so it is surfaced as any.
	enrichBook func(book *database.Book) any

	// getFieldStates wraps *MetadataStateService.LoadMetadataState, whose return
	// type (map[string]metadataFieldState) is metafetch-private, surfaced as any.
	getFieldStates func(id string) (any, error)

	// getExternalIDStore wraps asExternalIDStore(s.Store()) (the server adapter),
	// returning nil when the store does not implement external IDs.
	getExternalIDStore func() ExternalIDStore

	// publishEvent wraps *Server.publishEvent (the shared plugin event bus), used
	// by deleteAudiobook.
	publishEvent func(ctx context.Context, event plugin.Event)
}

// libraryGeneration resolves the store's book-mutation counter, which scopes
// every library-list cache key.
//
// Resolved at request time from h.store (the un-stripped store) rather than
// snapshotted, for the same reason the inline Unwrap assertions above are:
// production runs behind the indexedStore decorator, and a bare assertion
// against the wrapper misses. Resolution is two type assertions, which is
// nothing against building a list response.
//
// The library-list cache warmer resolves the same counter from the same
// underlying store, so both compute identical keys. If they ever diverged the
// warmer would write entries no request could read — a silent cache-miss
// regression rather than a visible failure, which is why both sides go through
// Generation.Key instead of formatting keys by hand.
func (h *Handler) libraryGeneration() *cache.Generation {
	gen, _ := database.LibraryGenerationOf(h.store)
	return gen
}

// New constructs an audiobooks Handler from its dependencies.
func New(
	store AudiobooksStore,
	audiobookService AudiobookService,
	audiobookUpdater AudiobookUpdater,
	getWriteBack func() WriteBackEnqueuer,
	metadataStateService MetadataStateService,
	metadataFetchService MetadataFetchService,
	batchService BatchService,
	changelogService ChangelogService,
	listCache *cache.Cache[gin.H],
	facetsCache *cache.Cache[gin.H],
	authorsCache *cache.Cache[*audiobookspkg.AuthorWithCountListResponse],
	seriesCache *cache.Cache[*audiobookspkg.SeriesWithCountsResponse],
	buildListResponse func(ctx context.Context, limit, offset int, search string, authorID, seriesID *int, filters audiobookspkg.ListFilters, showQuarantined bool) (gin.H, error),
	buildFacetsResponse func(ctx context.Context) (gin.H, error),
	isProtectedPath func(filePath string) bool,
	enrichBook func(book *database.Book) any,
	getFieldStates func(id string) (any, error),
	getExternalIDStore func() ExternalIDStore,
	publishEvent func(ctx context.Context, event plugin.Event),
) *Handler {
	return &Handler{
		store:                store,
		audiobookService:     audiobookService,
		audiobookUpdater:     audiobookUpdater,
		getWriteBack:         getWriteBack,
		metadataStateService: metadataStateService,
		metadataFetchService: metadataFetchService,
		batchService:         batchService,
		changelogService:     changelogService,
		listCache:            listCache,
		facetsCache:          facetsCache,
		authorsCache:         authorsCache,
		seriesCache:          seriesCache,
		buildListResponse:    buildListResponse,
		buildFacetsResponse:  buildFacetsResponse,
		isProtectedPath:      isProtectedPath,
		enrichBook:           enrichBook,
		getFieldStates:       getFieldStates,
		getExternalIDStore:   getExternalIDStore,
		publishEvent:         publishEvent,
	}
}

// resolveWriteBack returns the live write-back batcher via the lazy provider, or
// nil if no provider was supplied or the provider yields nil.
func (h *Handler) resolveWriteBack() WriteBackEnqueuer {
	if h.getWriteBack == nil {
		return nil
	}
	return h.getWriteBack()
}

// ptrStr is a local copy of the *string→string helper used by UpdateAudiobook.
// The server copy is cycle-bound and the audiobookspkg copy is unexported, so a
// local copy keeps this package decoupled (the codebase already keeps two
// copies, so this is consistent).
func ptrStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// ListAudiobooks handles GET /audiobooks. Mirrors the original listAudiobooks:
// has_file_errors fast-path, quick-query (missing_covers / in_import_path /
// no_isbn / duplicates_flagged) fast-path, then the filtered list pipeline with
// the list cache (skipped when per-user filters are active).
func (h *Handler) ListAudiobooks(c *gin.Context) {
	store := h.store

	// Parse pagination parameters
	params := httputil.ParsePaginationParams(c)
	authorID := httputil.ParseQueryIntPtr(c, "author_id")
	seriesID := httputil.ParseQueryIntPtr(c, "series_id")

	// If the client asked for books with file errors, handle that fast-path here.
	if c.Query("has_file_errors") == "true" {
		if store == nil {
			httputil.RespondWithInternalError(c, "database not initialized")
			return
		}
		var bookIDs []string
		// Try direct method on store, fallback to Unwrap() if decorated
		if lf, ok := store.(interface{ ListBooksWithFileErrors() ([]string, error) }); ok {
			ids, err := lf.ListBooksWithFileErrors()
			if err != nil {
				httputil.InternalError(c, "failed to list books with file errors", err)
				return
			}
			bookIDs = ids
		} else if uw, ok := store.(interface{ Unwrap() database.Store }); ok {
			if inner, ok2 := uw.Unwrap().(interface{ ListBooksWithFileErrors() ([]string, error) }); ok2 {
				ids, err := inner.ListBooksWithFileErrors()
				if err != nil {
					httputil.InternalError(c, "failed to list books with file errors", err)
					return
				}
				bookIDs = ids
			}
		}

		if bookIDs == nil {
			// No implementation available — return empty set
			httputil.RespondWithOK(c, gin.H{"items": []database.Book{}, "count": 0, "limit": params.Limit, "offset": params.Offset})
			return
		}

		total := len(bookIDs)
		start := params.Offset
		if start < 0 {
			start = 0
		}
		end := start + params.Limit
		if params.Limit <= 0 || end > len(bookIDs) {
			end = len(bookIDs)
		}
		if start > len(bookIDs) {
			start = len(bookIDs)
		}
		selected := bookIDs[start:end]
		books := make([]database.Book, 0, len(selected))
		for _, id := range selected {
			b, err := store.GetBookByID(id)
			if err != nil || b == nil {
				continue
			}
			books = append(books, *b)
		}
		enriched := h.audiobookService.EnrichAudiobooksWithNames(books)
		httputil.RespondWithOK(c, gin.H{"items": enriched, "count": total, "limit": params.Limit, "offset": params.Offset})
		return
	}

	// Quick-query boolean params fast-path: missing_covers, in_import_path, no_isbn,
	// duplicates_flagged. Replicates the has_file_errors pattern — scan ALL matching
	// book IDs first, slice for pagination, then fetch each book individually.
	// This ensures totalCount and page offsets are correct regardless of page size.
	quickQueryID := ""
	switch {
	case c.Query("missing_covers") == "true":
		quickQueryID = "missing_covers"
	case c.Query("in_import_path") == "true":
		quickQueryID = "in_import_path"
	case c.Query("no_isbn") == "true":
		quickQueryID = "no_isbn"
	case c.Query("duplicates_flagged") == "true":
		quickQueryID = "duplicates_flagged"
	}
	if quickQueryID != "" {
		var bookIDs []string
		if qqStore, ok := store.(interface {
			GetAllBookIDsForQuickQuery(id string) ([]string, error)
		}); ok {
			ids, err := qqStore.GetAllBookIDsForQuickQuery(quickQueryID)
			if err != nil {
				httputil.InternalError(c, "failed to list books for quick query", err)
				return
			}
			bookIDs = ids
		} else if uw, ok := store.(interface{ Unwrap() database.Store }); ok {
			if inner, ok2 := uw.Unwrap().(interface {
				GetAllBookIDsForQuickQuery(id string) ([]string, error)
			}); ok2 {
				ids, err := inner.GetAllBookIDsForQuickQuery(quickQueryID)
				if err != nil {
					httputil.InternalError(c, "failed to list books for quick query", err)
					return
				}
				bookIDs = ids
			}
		}

		if bookIDs == nil {
			// Store doesn't support this method — return empty set.
			httputil.RespondWithOK(c, gin.H{"items": []audiobookspkg.AudiobookDetail{}, "count": 0, "limit": params.Limit, "offset": params.Offset})
			return
		}

		total := len(bookIDs)
		start := params.Offset
		if start < 0 {
			start = 0
		}
		end := start + params.Limit
		if params.Limit <= 0 || end > len(bookIDs) {
			end = len(bookIDs)
		}
		if start > len(bookIDs) {
			start = len(bookIDs)
		}
		selected := bookIDs[start:end]
		books := make([]database.Book, 0, len(selected))
		for _, id := range selected {
			b, err := store.GetBookByID(id)
			if err != nil || b == nil {
				continue
			}
			books = append(books, *b)
		}
		enriched := h.audiobookService.EnrichAudiobooksWithNames(books)

		// Batch-load all book files at once instead of per-book (N+1 optimization)
		qqBookIDs := make([]string, len(enriched))
		for i, book := range enriched {
			qqBookIDs[i] = book.ID
		}

		qqBookFilesMap := make(map[string][]database.BookFileCore)
		if qqGgf, ok := store.(interface {
			GetBookFilesForIDsCore(ids []string) (map[string][]database.BookFileCore, error)
		}); ok {
			if qqBfm, err := qqGgf.GetBookFilesForIDsCore(qqBookIDs); err == nil {
				qqBookFilesMap = qqBfm
			}
		} else if qqUw, ok := store.(interface{ Unwrap() database.Store }); ok {
			if qqInner, ok2 := qqUw.Unwrap().(interface {
				GetBookFilesForIDsCore(ids []string) (map[string][]database.BookFileCore, error)
			}); ok2 {
				if qqBfm, err := qqInner.GetBookFilesForIDsCore(qqBookIDs); err == nil {
					qqBookFilesMap = qqBfm
				}
			}
		}

		// Compute fingerprinting fields for the selected books.
		for i, book := range enriched {
			files := qqBookFilesMap[book.ID]
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

		httputil.RespondWithOK(c, gin.H{"items": enriched, "count": total, "limit": params.Limit, "offset": params.Offset})
		return
	}

	// Parse optional filters
	sortOrder := httputil.ParseQueryString(c, "sort_order")
	if sortOrder != "" && sortOrder != "asc" && sortOrder != "desc" {
		sortOrder = "asc"
	}
	tags := c.QueryArray("tags")
	if len(tags) == 0 {
		tags = c.QueryArray("tags[]")
	}

	// Parse fingerprinting filters
	var coveragePercentMin, coveragePercentMax *int
	if minStr := c.Query("coverage_percent_min"); minStr != "" {
		if minVal, err := strconv.Atoi(minStr); err == nil && minVal >= 0 && minVal <= 100 {
			coveragePercentMin = &minVal
		}
	}
	if maxStr := c.Query("coverage_percent_max"); maxStr != "" {
		if maxVal, err := strconv.Atoi(maxStr); err == nil && maxVal >= 0 && maxVal <= 100 {
			coveragePercentMax = &maxVal
		}
	}

	filters := audiobookspkg.ListFilters{
		IsPrimaryVersion:   httputil.ParseQueryBoolPtr(c, "is_primary_version"),
		LibraryState:       httputil.ParseQueryString(c, "library_state"),
		Tag:                httputil.ParseQueryString(c, "tag"),
		Tags:               tags,
		SortBy:             httputil.ParseQueryString(c, "sort_by"),
		SortOrder:          sortOrder,
		FingerprintStatus:  httputil.ParseQueryString(c, "fingerprint_status"),
		CoveragePercentMin: coveragePercentMin,
		CoveragePercentMax: coveragePercentMax,
	}

	// Parse field filters from JSON query param. Per-user filters
	// (read_status / progress_pct / last_played) are split off so the
	// service can apply them via UserBookState lookups; book-global
	// filters stay on the original FieldFilters slice.
	if filtersJSON := c.Query("filters"); filtersJSON != "" {
		var fieldFilters []audiobookspkg.FieldFilter
		if err := json.Unmarshal([]byte(filtersJSON), &fieldFilters); err != nil {
			httputil.RespondWithBadRequest(c, "invalid filters parameter: "+err.Error())
			return
		}
		// Reject empty filter values at the boundary. strings.Contains(x, "")
		// is always true, so an empty value silently matches EVERY book instead
		// of narrowing anything — measured on production as title="" returning
		// all 63,870 books while title="zzqqxx" correctly returned 0. A caller
		// that sends an empty value has a bug (usually a cleared UI field that
		// was submitted anyway), and answering it with the whole library is the
		// worst possible response. See audiobooks.FirstEmptyFilterValue.
		if field, empty := audiobookspkg.FirstEmptyFilterValue(fieldFilters); empty {
			httputil.RespondWithBadRequest(c,
				"filter on \""+field+"\" has an empty value; an empty value matches every "+
					"book rather than narrowing the results. Omit the filter to list everything, "+
					"or supply a value to filter by.")
			return
		}
		for _, ff := range fieldFilters {
			if audiobookspkg.IsPerUserField(ff.Field) {
				filters.PerUserFilters = append(filters.PerUserFilters, ff)
			} else {
				filters.FieldFilters = append(filters.FieldFilters, ff)
			}
		}
	}

	// Resolve caller for per-user filters; anon callers just don't
	// get per-user filtering applied (filters.UserID stays "" and
	// the service skips that pass).
	if caller, ok := servermiddleware.CurrentUser(c); ok && caller != nil {
		filters.UserID = caller.ID
	}

	// Cache key from the full query string. Skip the cache when
	// per-user filters are active because the cache key doesn't
	// encode userID — a hit could leak User A's filtered list
	// to User B.
	cacheKey := h.libraryGeneration().Key("list:", c.Request.URL.RawQuery)
	if len(filters.PerUserFilters) == 0 {
		if cached, ok := h.listCache.Get(cacheKey); ok {
			httputil.RespondWithOK(c, cached)
			return
		}
	}

	showQuarantined := c.Query("show_quarantined") == "true"
	resp, err := h.buildListResponse(c.Request.Context(), params.Limit, params.Offset, params.Search, authorID, seriesID, filters, showQuarantined)
	if err != nil {
		httputil.InternalError(c, "failed to list audiobooks", err)
		return
	}
	if len(filters.PerUserFilters) == 0 {
		h.listCache.Set(cacheKey, resp)
	}
	httputil.RespondWithOK(c, resp)
}

// ListSoftDeletedAudiobooks handles GET /audiobooks/soft-deleted.
func (h *Handler) ListSoftDeletedAudiobooks(c *gin.Context) {
	params := httputil.ParsePaginationParams(c)
	olderThanDays := httputil.ParseQueryIntPtr(c, "older_than_days")

	books, err := h.audiobookService.GetSoftDeletedBooks(c.Request.Context(), params.Limit, params.Offset, olderThanDays)
	if err != nil {
		httputil.InternalError(c, "failed to list deleted audiobooks", err)
		return
	}

	// Get total count (unpaginated) for proper pagination support
	allBooks, _ := h.audiobookService.GetSoftDeletedBooks(c.Request.Context(), 10000, 0, olderThanDays)
	total := len(allBooks)

	httputil.RespondWithOK(c, gin.H{
		"items":  books,
		"count":  len(books),
		"total":  total,
		"limit":  params.Limit,
		"offset": params.Offset,
	})
}

// PurgeSoftDeletedAudiobooks handles DELETE /audiobooks/purge-soft-deleted.
func (h *Handler) PurgeSoftDeletedAudiobooks(c *gin.Context) {
	deleteFiles := c.Query("delete_files") == "true"
	olderThanStr := c.Query("older_than_days")

	var olderThanDays *int
	if olderThanStr != "" {
		if days, err := strconv.Atoi(olderThanStr); err == nil && days > 0 {
			olderThanDays = &days
		}
	}

	result, err := h.audiobookService.PurgeSoftDeletedBooks(c.Request.Context(), deleteFiles, olderThanDays)
	if err != nil {
		httputil.InternalError(c, "failed to purge deleted audiobooks", err)
		return
	}

	httputil.RespondWithOK(c, result)
}

// RescanAudiobook re-stats the book's files on disk and updates FileSize fields
// in the DB to match physical reality. POST /audiobooks/:id/rescan.
func (h *Handler) RescanAudiobook(c *gin.Context) {
	id := c.Param("id")
	store := h.store
	if store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}
	book, err := store.GetBookByID(id)
	if err != nil || book == nil {
		httputil.RespondWithNotFound(c, "audiobook", id)
		return
	}

	type fileResult struct {
		ID      string `json:"id"`
		Path    string `json:"path"`
		OldSize int64  `json:"old_size"`
		NewSize int64  `json:"new_size"`
		Missing bool   `json:"missing"`
	}

	results := []fileResult{}
	var newTotal int64

	files, _ := store.GetBookFiles(id)
	if len(files) > 0 {
		for i := range files {
			f := files[i]
			fr := fileResult{ID: f.ID, Path: f.FilePath, OldSize: f.FileSize}
			info, statErr := os.Stat(f.FilePath)
			if statErr != nil {
				fr.Missing = true
				if !f.Missing {
					f.Missing = true
					_ = store.UpdateBookFile(f.ID, &f)
				}
				results = append(results, fr)
				continue
			}
			size := info.Size()
			fr.NewSize = size
			if f.FileSize != size || f.Missing {
				f.FileSize = size
				f.Missing = false
				if upErr := store.UpdateBookFile(f.ID, &f); upErr != nil {
					httputil.InternalError(c, "failed to update book file", upErr)
					return
				}
			}
			newTotal += size
			results = append(results, fr)
		}
	} else if book.FilePath != "" {
		// Legacy single-file book with no BookFile rows — stat the main path.
		fr := fileResult{Path: book.FilePath}
		if book.FileSize != nil {
			fr.OldSize = *book.FileSize
		}
		if info, statErr := os.Stat(book.FilePath); statErr == nil {
			fr.NewSize = info.Size()
			newTotal = info.Size()
		} else {
			fr.Missing = true
		}
		results = append(results, fr)
	}

	// Update the Book.FileSize aggregate if it changed.
	oldBookSize := int64(0)
	if book.FileSize != nil {
		oldBookSize = *book.FileSize
	}
	if newTotal != oldBookSize {
		book.FileSize = &newTotal
		if _, upErr := store.UpdateBook(id, book); upErr != nil {
			httputil.InternalError(c, "failed to update book", upErr)
			return
		}
		// Invalidate the dashboard cache so the next /system/status sees
		// the new sum.
		if inv, ok := store.(interface{ InvalidateLibraryStats() }); ok {
			inv.InvalidateLibraryStats()
		}
	}

	httputil.RespondWithOK(c, gin.H{
		"book_id":    id,
		"old_total":  oldBookSize,
		"new_total":  newTotal,
		"file_count": len(results),
		"files":      results,
	})
}

// RestoreAudiobook handles POST /audiobooks/:id/restore.
func (h *Handler) RestoreAudiobook(c *gin.Context) {
	id := c.Param("id")
	updated, err := h.audiobookService.RestoreAudiobook(c.Request.Context(), id)
	if err != nil {
		httputil.RespondWithNotFound(c, "audiobook", id)
		return
	}

	httputil.RespondWithOK(c, gin.H{
		"message": "audiobook restored",
		"book":    updated,
	})
}

// CountAudiobooks handles GET /audiobooks/count.
func (h *Handler) CountAudiobooks(c *gin.Context) {
	count, err := h.audiobookService.CountAudiobooks(c.Request.Context())
	if err != nil {
		httputil.InternalError(c, "failed to count audiobooks", err)
		return
	}

	httputil.RespondWithOK(c, gin.H{"count": count})
}

// AudiobookFacets handles GET /audiobooks/facets. Returns lightweight lists of
// distinct genres and languages for filter dropdowns, plus (INIT-4 T4)
// additive genre_counts/language_counts/tag_counts maps when the Bleve index
// is available. Results are cached for 5 minutes and pre-warmed at startup
// (warmFacetsCache stays in package server); both the cache-miss path here
// and the warmer build the response through the shared buildFacetsResponse
// closure (see its doc comment on the Handler struct) so they can never
// drift into different shapes.
func (h *Handler) AudiobookFacets(c *gin.Context) {
	if h.store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}
	if cached, ok := h.facetsCache.Get(facetsCacheKey); ok {
		httputil.RespondWithOK(c, cached)
		return
	}
	// Cache miss (e.g. first request before warm-up goroutine completes, or after TTL expiry).
	result, err := h.buildFacetsResponse(c.Request.Context())
	if err != nil {
		httputil.InternalError(c, "failed to fetch facets", err)
		return
	}
	h.facetsCache.Set(facetsCacheKey, result)
	httputil.RespondWithOK(c, result)
}

// ServeAudiobookCover handles GET /audiobooks/:id/cover.
func (h *Handler) ServeAudiobookCover(c *gin.Context) {
	id := pathvalidation.SanitizeFilename(c.Param("id"))
	if id == "" {
		httputil.RespondWithBadRequest(c, "invalid book id")
		return
	}
	if config.AppConfig.RootDir == "" {
		httputil.RespondWithInternalError(c, "root_dir not configured")
		return
	}
	coverPath := metadata.CoverPathForBook(config.AppConfig.RootDir, id)
	if coverPath == "" {
		httputil.RespondWithNotFound(c, "cover art", id)
		return
	}
	c.File(coverPath)
}

// GetAudiobook handles GET /audiobooks/:id.
func (h *Handler) GetAudiobook(c *gin.Context) {
	id := c.Param("id")

	book, err := h.audiobookService.GetAudiobook(c.Request.Context(), id)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			httputil.RespondWithNotFound(c, "audiobook", id)
			return
		}
		httputil.InternalError(c, "failed to get audiobook", err)
		return
	}

	httputil.RespondWithOK(c, h.enrichBook(book))
}
