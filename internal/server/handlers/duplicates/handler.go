// file: internal/server/handlers/duplicates/handler.go
// version: 1.5.0
// guid: 9f41f363-34fc-4ad2-b2f1-46d5ac0ba2f3
// last-edited: 2026-07-01

// Package duplicates hosts the SQL-backed duplicate-detection HTTP handlers
// extracted from the server package's duplicates_handlers.go: book / author /
// series duplicate listing, async scan / merge / dismiss triggers, series
// prune / normalize preview + apply, deduplicate, and dedup-entry metadata
// validation (17 handlers total). Distinct from the embedding-based dedup flow
// in handlers/dedup.
//
// Dependencies that lived on the *Server receiver are reached through narrow
// interfaces (DuplicatesStore, OperationsRegistry, MergeService,
// MetadataFetchService, AudiobookService) plus the concrete
// *cache.Cache[gin.H] (the dedup cache — a clean generic type held by pointer,
// the established cache exception) and a set of INJECTED func fields that wrap
// helpers which STAY in package server because they are shared with files that
// did not move (duplicates_ops.go, server_maintenance_deps.go). Those helpers
// were relocated to internal/server/duplicates_helpers.go; the controller
// passes thin closures over them. As a result package duplicates never imports
// package server.
//
// The store is a WIRE-TIME SNAPSHOT (assigned once in New, never swapped after
// that). opRegistry / mergeService / audiobookService / metadataFetchService are
// also interface snapshots taken at wire time (assigned once before setupRoutes,
// never swapped), each guarded against typed-nil boxing by the controller.

package duplicates

import (
	"fmt"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/cache"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/gin-gonic/gin"
	ulid "github.com/oklog/ulid/v2"
)

// Handler hosts the duplicates-domain HTTP endpoints.
type Handler struct {
	// store is the wire-time snapshot of the database store. All handlers read
	// from this field directly (no lazy provider needed).
	store DuplicatesStore

	// dedupCache is the concrete *cache.Cache[gin.H] the original handlers read /
	// wrote directly (book-duplicates / author-duplicates / series-duplicates /
	// book-dedup-scan keys). Passed concrete (the cache exception) because it is a
	// clean generic db-adjacent type under heavy multi-method use; the handler
	// nil-checks it where the original did.
	dedupCache *cache.Cache[gin.H]

	// opRegistry backs the scan / merge / refresh / dedup / prune / normalize
	// triggers. Interface snapshot guarded by the controller against typed-nil
	// boxing so the in-method `== nil` guards (mirroring the old
	// `s.opRegistry == nil` checks) hold.
	opRegistry OperationsRegistry

	// audiobookService backs listDuplicateAudiobooks (GetDuplicateBooks).
	// Interface snapshot, typed-nil guarded by the controller.
	audiobookService AudiobookService

	// metadataFetchService backs validateDedupEntry (BuildSourceChain). Interface
	// snapshot, typed-nil guarded by the controller.
	metadataFetchService MetadataFetchService

	// getMergeService resolves the merge service for mergeBookDuplicatesAsVersions.
	// The original handler used s.mergeService when non-nil, else constructed
	// merge.NewService(s.Store()). The controller closes over that exact fallback
	// so this package needs neither the merge constructor nor a server import.
	getMergeService func() MergeService

	// dedupEngine backs the post-merge orphan-candidate sweep for
	// mergeBookDuplicatesAsVersions and combineBooks (mirrors the sweep the
	// dedup handler package already runs after its own per-candidate Merge
	// button). Interface snapshot, typed-nil guarded by the controller; nil is
	// tolerated (skips the sweep) since CleanupCandidatesAfterMerge itself
	// also nil-checks its receiver.
	dedupEngine DedupEngine

	// --- injected funcs wrapping helpers that stay in package server ---

	// dismissDedupGroup wraps the loadDismissedDedupGroups / saveDismissedDedupGroups
	// pair (defined in server_middleware.go, package server, operating on a full
	// database.Store). dismissBookDuplicateGroup calls it with the group key.
	dismissDedupGroup func(groupKey string)

	// computeSeriesPrunePreview wraps the server-resident computeSeriesPrunePreview
	// (server_title_helpers.go) over the live store, returning the preview payload
	// (any) and an error. seriesPrunePreview serializes the payload verbatim.
	computeSeriesPrunePreview func() (any, error)

	// seriesNormalizePreview wraps the relocated computeSeriesNormalizeActions
	// over the live store and builds the dry-run preview response payload
	// server-side (the seriesNormalizeAction / seriesNormalizePreviewResult types
	// stay in package server — duplicates_helpers.go — because a server unit test
	// references them). The handler serializes the returned payload verbatim.
	seriesNormalizePreview func() any
}

// New constructs a duplicates Handler from its dependencies.
func New(
	store DuplicatesStore,
	dedupCache *cache.Cache[gin.H],
	opRegistry OperationsRegistry,
	audiobookService AudiobookService,
	metadataFetchService MetadataFetchService,
	getMergeService func() MergeService,
	dedupEngine DedupEngine,
	dismissDedupGroup func(groupKey string),
	computeSeriesPrunePreview func() (any, error),
	seriesNormalizePreview func() any,
) *Handler {
	return &Handler{
		store:                     store,
		dedupCache:                dedupCache,
		opRegistry:                opRegistry,
		audiobookService:          audiobookService,
		metadataFetchService:      metadataFetchService,
		getMergeService:           getMergeService,
		dedupEngine:               dedupEngine,
		dismissDedupGroup:         dismissDedupGroup,
		computeSeriesPrunePreview: computeSeriesPrunePreview,
		seriesNormalizePreview:    seriesNormalizePreview,
	}
}

// ListDuplicateAudiobooks handles GET /audiobooks/duplicates.
func (h *Handler) ListDuplicateAudiobooks(c *gin.Context) {
	if h.dedupCache != nil {
		if cached, ok := h.dedupCache.Get("book-duplicates"); ok {
			httputil.RespondWithOK(c, cached)
			return
		}
	}

	if h.audiobookService == nil {
		httputil.RespondWithInternalError(c, "audiobook service not initialized")
		return
	}

	result, err := h.audiobookService.GetDuplicateBooks(c.Request.Context())
	if err != nil {
		httputil.InternalError(c, "failed to list duplicate audiobooks", err)
		return
	}

	resp := gin.H{
		"groups":          result.Groups,
		"group_count":     result.GroupCount,
		"duplicate_count": result.DuplicateCount,
	}
	if h.dedupCache != nil {
		h.dedupCache.Set("book-duplicates", resp)
	}
	httputil.RespondWithOK(c, resp)
}

// ListBookDuplicateScanResults returns cached results from the last async
// book-dedup scan. GET /audiobooks/duplicates/scan-results.
func (h *Handler) ListBookDuplicateScanResults(c *gin.Context) {
	if h.dedupCache != nil {
		if cached, ok := h.dedupCache.Get("book-dedup-scan"); ok {
			httputil.RespondWithOK(c, cached)
			return
		}
	}
	httputil.RespondWithOK(c, gin.H{"groups": []any{}, "group_count": 0, "duplicate_count": 0, "needs_refresh": true})
}

// launchLegacyOp creates a legacy DB operation record, enqueues defID with the
// params returned by buildParams(legacyOpID), and responds 202 with the op.
// Returns false if the registry is nil or any step fails (response already written).
func (h *Handler) launchLegacyOp(c *gin.Context, defID, legacyType, detail string, buildParams func(legacyOpID string) any) bool {
	if h.opRegistry == nil {
		httputil.RespondWithInternalError(c, "operation registry not initialized")
		return false
	}
	store := h.store
	legacyID := ulid.Make().String()
	d := detail
	op, err := store.CreateOperation(legacyID, legacyType, &d)
	if err != nil {
		httputil.InternalError(c, "failed to create operation", err)
		return false
	}
	if _, err := h.opRegistry.EnqueueOp(c.Request.Context(), defID, buildParams(op.ID)); err != nil {
		httputil.InternalError(c, "failed to enqueue operation", err)
		return false
	}
	httputil.RespondWithSuccess(c, 202, op)
	return true
}

// ScanBookDuplicates triggers an async scan for book duplicates using metadata
// matching. POST /audiobooks/duplicates/scan.
func (h *Handler) ScanBookDuplicates(c *gin.Context) {
	h.launchLegacyOp(c, "dedup.book-scan", "book-dedup-scan", "book-dedup-scan",
		func(id string) any { return bookDedupScanOpParams{LegacyOpID: id} })
}

// MergeBookDuplicatesAsVersions merges a group of duplicate books into a version
// group. POST /audiobooks/duplicates/merge.
func (h *Handler) MergeBookDuplicatesAsVersions(c *gin.Context) {
	var req struct {
		BookIDs []string `json:"book_ids" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}
	if len(req.BookIDs) < 2 {
		httputil.RespondWithBadRequest(c, "need at least 2 book IDs")
		return
	}

	ms := h.getMergeService()
	if ms == nil {
		httputil.RespondWithInternalError(c, "merge service not initialized")
		return
	}

	result, err := ms.MergeBooks(req.BookIDs, "")
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			httputil.RespondWithNotFound(c, "book", "")
		} else {
			httputil.InternalError(c, "failed to merge duplicate books", err)
		}
		return
	}

	if h.dedupCache != nil {
		h.dedupCache.Invalidate("book-dedup-scan")
		h.dedupCache.Invalidate("book-duplicates")
	}

	if h.dedupEngine != nil {
		h.dedupEngine.CleanupCandidatesAfterMerge(mergedAwayIDs(req.BookIDs, result.PrimaryID))
	}

	httputil.RespondWithOK(c, gin.H{
		"message":          fmt.Sprintf("Merged %d books into version group", result.MergedCount),
		"version_group_id": result.VersionGroupID,
		"primary_id":       result.PrimaryID,
	})
}

// mergedAwayIDs returns ids minus the survivor, for handing to
// DedupEngine.CleanupCandidatesAfterMerge after a successful merge/combine.
func mergedAwayIDs(ids []string, primaryID string) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id != primaryID {
			out = append(out, id)
		}
	}
	return out
}

// DismissBookDuplicateGroup marks a book duplicate group as not-duplicates.
// POST /audiobooks/duplicates/dismiss.
func (h *Handler) DismissBookDuplicateGroup(c *gin.Context) {
	var req struct {
		GroupKey string `json:"group_key" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}

	store := h.store
	if store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}

	// loadDismissedDedupGroups / saveDismissedDedupGroups stay in package server
	// (server_middleware.go); the controller injects the load-modify-save pair.
	h.dismissDedupGroup(req.GroupKey)

	if h.dedupCache != nil {
		h.dedupCache.Invalidate("book-dedup-scan")
	}

	httputil.RespondWithOK(c, gin.H{"message": "Group dismissed"})
}

// MergeBooks enqueues an async book-merge operation. POST /audiobooks/merge.
func (h *Handler) MergeBooks(c *gin.Context) {
	var req struct {
		KeepID   string   `json:"keep_id" binding:"required"`
		MergeIDs []string `json:"merge_ids" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}

	if len(req.MergeIDs) == 0 {
		httputil.RespondWithBadRequest(c, "merge_ids must not be empty")
		return
	}

	store := h.store
	keepBook, err := store.GetBookByID(req.KeepID)
	if err != nil || keepBook == nil {
		httputil.RespondWithNotFound(c, "book", req.KeepID)
		return
	}

	detail := fmt.Sprintf("merge-books:keep=%s,merge=%d", req.KeepID, len(req.MergeIDs))
	h.launchLegacyOp(c, "dedup.book-merge", "book-merge", detail,
		func(id string) any {
			return bookMergeOpParams{LegacyOpID: id, KeepID: req.KeepID, MergeIDs: req.MergeIDs, Detail: detail}
		})
}

// CombineBooks combines several single-file books into ONE multi-file book on the
// survivor (keep_id) and hard-deletes the absorbed shells. Distinct from
// MergeBooks, which links them as alternate VERSIONS in a version group.
// Synchronous — combine is a fast DB-only operation. POST /audiobooks/combine.
func (h *Handler) CombineBooks(c *gin.Context) {
	var req struct {
		KeepID   string   `json:"keep_id" binding:"required"`
		MergeIDs []string `json:"merge_ids" binding:"required"`
		// Override is optional. Non-empty fields overwrite the survivor's metadata
		// after all files are reassigned. Useful when none of the source books has
		// the correct title/author (e.g. every file was titled after its chapter).
		Override *merge.CombineOverride `json:"override,omitempty"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}

	if len(req.MergeIDs) == 0 {
		httputil.RespondWithBadRequest(c, "merge_ids must not be empty")
		return
	}

	ms := h.getMergeService()
	if ms == nil {
		httputil.RespondWithInternalError(c, "merge service not initialized")
		return
	}

	// Pass ALL ids (the absorbed books plus the survivor) and the survivor id.
	result, err := ms.CombineBooks(append(req.MergeIDs, req.KeepID), req.KeepID, req.Override)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			httputil.RespondWithNotFound(c, "book", req.KeepID)
		} else {
			httputil.InternalError(c, "failed to combine books", err)
		}
		return
	}

	if h.dedupCache != nil {
		h.dedupCache.Invalidate("book-dedup-scan")
		h.dedupCache.Invalidate("book-duplicates")
	}

	if h.dedupEngine != nil {
		h.dedupEngine.CleanupCandidatesAfterMerge(mergedAwayIDs(req.MergeIDs, result.PrimaryID))
	}

	httputil.RespondWithOK(c, gin.H{
		"message":       fmt.Sprintf("Combined %d files onto book; deleted %d shells", result.FilesMoved, result.BooksDeleted),
		"primary_id":    result.PrimaryID,
		"files_moved":   result.FilesMoved,
		"books_deleted": result.BooksDeleted,
	})
}

// ListDuplicateAuthors handles GET /authors/duplicates.
func (h *Handler) ListDuplicateAuthors(c *gin.Context) {
	if h.dedupCache != nil {
		if cached, ok := h.dedupCache.Get("author-duplicates"); ok {
			httputil.RespondWithOK(c, cached)
			return
		}
	}

	// No cache — return empty with needs_refresh flag so frontend triggers async scan
	httputil.RespondWithOK(c, gin.H{"groups": []any{}, "count": 0, "needs_refresh": true})
}

// RefreshDuplicateAuthors enqueues an async author-dedup scan.
// POST /authors/duplicates/refresh.
func (h *Handler) RefreshDuplicateAuthors(c *gin.Context) {
	h.launchLegacyOp(c, "dedup.author-scan", "author-dedup-scan", "author-dedup-scan",
		func(id string) any { return authorDedupScanOpParams{LegacyOpID: id} })
}

// ListSeriesDuplicates handles GET /series/duplicates.
func (h *Handler) ListSeriesDuplicates(c *gin.Context) {
	if h.dedupCache != nil {
		if cached, ok := h.dedupCache.Get("series-duplicates"); ok {
			httputil.RespondWithOK(c, cached)
			return
		}
	}

	// No cache — return empty with needs_refresh flag so frontend triggers async scan
	httputil.RespondWithOK(c, gin.H{"groups": []any{}, "count": 0, "total_series": 0, "needs_refresh": true})
}

// RefreshSeriesDuplicates enqueues an async series-dedup scan.
// POST /series/duplicates/refresh.
func (h *Handler) RefreshSeriesDuplicates(c *gin.Context) {
	h.launchLegacyOp(c, "dedup.series-scan", "series-dedup-scan", "series-dedup-scan",
		func(id string) any { return seriesDedupScanOpParams{LegacyOpID: id} })
}

// ValidateDedupEntry searches metadata sources (OpenLibrary, Audible, etc.) to
// validate a series name, author name, or book title during dedup review.
// POST /dedup/validate.
func (h *Handler) ValidateDedupEntry(c *gin.Context) {
	var req struct {
		Query string `json:"query" binding:"required"`
		Type  string `json:"type"` // "series", "author", "book"
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		httputil.RespondWithBadRequest(c, "query is required")
		return
	}
	if req.Type == "" {
		req.Type = "series"
	}

	if h.metadataFetchService == nil {
		httputil.RespondWithOK(c, gin.H{"results": []interface{}{}, "message": "no metadata sources configured"})
		return
	}

	chain := h.metadataFetchService.BuildSourceChain()
	if len(chain) == 0 {
		httputil.RespondWithOK(c, gin.H{"results": []interface{}{}, "message": "no metadata sources configured"})
		return
	}

	type validationResult struct {
		Source         string `json:"source"`
		Title          string `json:"title"`
		Author         string `json:"author"`
		Series         string `json:"series,omitempty"`
		SeriesPosition string `json:"series_position,omitempty"`
		CoverURL       string `json:"cover_url,omitempty"`
		ISBN           string `json:"isbn,omitempty"`
	}

	var results []validationResult
	ctx := c.Request.Context()
	for _, src := range chain {
		matches, err := src.SearchByTitle(ctx, req.Query)
		if err != nil {
			continue
		}
		for _, m := range matches {
			r := validationResult{
				Source:         src.Name(),
				Title:          m.Title,
				Author:         m.Author,
				Series:         m.Series,
				SeriesPosition: m.SeriesPosition,
				CoverURL:       m.CoverURL,
				ISBN:           m.ISBN,
			}
			// For series validation, prioritize results that have series info
			if req.Type == "series" && m.Series == "" {
				continue
			}
			results = append(results, r)
		}
		// Limit total results
		if len(results) >= 20 {
			results = results[:20]
			break
		}
	}

	if results == nil {
		results = []validationResult{}
	}
	httputil.RespondWithOK(c, gin.H{"results": results, "query": req.Query, "type": req.Type})
}

// DeduplicateSeriesHandler enqueues an async series-dedup operation.
// POST /series/deduplicate.
func (h *Handler) DeduplicateSeriesHandler(c *gin.Context) {
	h.launchLegacyOp(c, "dedup.series-dedup", "series-dedup", "series-deduplicate",
		func(id string) any { return seriesDedupOpParams{LegacyOpID: id, Detail: "series-deduplicate"} })
}

// SeriesPrunePreview returns a dry-run preview of the series auto-prune.
// GET /series/prune/preview.
func (h *Handler) SeriesPrunePreview(c *gin.Context) {
	store := h.store
	if store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}

	preview, err := h.computeSeriesPrunePreview()
	if err != nil {
		httputil.InternalError(c, "failed to compute series prune preview", err)
		return
	}

	httputil.RespondWithOK(c, preview)
}

// SeriesPrune enqueues an async series-prune operation. POST /series/prune.
func (h *Handler) SeriesPrune(c *gin.Context) {
	h.launchLegacyOp(c, "dedup.series-prune", "series-prune", "series-prune",
		func(id string) any { return seriesPruneOpParams{LegacyOpID: id, Detail: "series-prune"} })
}

// MergeSeriesGroup enqueues an async series-merge operation, reassigning all
// books from the merge IDs to the keep ID. POST /series/merge.
func (h *Handler) MergeSeriesGroup(c *gin.Context) {
	var req struct {
		KeepID     int    `json:"keep_id" binding:"required"`
		MergeIDs   []int  `json:"merge_ids" binding:"required"`
		CustomName string `json:"custom_name"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}

	if len(req.MergeIDs) == 0 {
		httputil.RespondWithBadRequest(c, "merge_ids must not be empty")
		return
	}

	store := h.store
	keepSeries, err := store.GetSeriesByID(req.KeepID)
	if err != nil || keepSeries == nil {
		httputil.RespondWithNotFound(c, "series", "")
		return
	}

	detail := fmt.Sprintf("merge-series:keep=%d,merge=%v", req.KeepID, req.MergeIDs)
	h.launchLegacyOp(c, "dedup.series-merge", "series-merge", detail,
		func(id string) any {
			return seriesMergeOpParams{LegacyOpID: id, KeepID: req.KeepID, MergeIDs: req.MergeIDs, CustomName: req.CustomName, Detail: detail}
		})
}

// SeriesNormalizePreview returns a dry-run preview of what the series
// name-normalization pass would do, with no database writes.
// GET /series/normalize/preview.
func (h *Handler) SeriesNormalizePreview(c *gin.Context) {
	store := h.store
	if store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}

	httputil.RespondWithOK(c, h.seriesNormalizePreview())
}

// SeriesNormalize enqueues an async operation that renames/merges contaminated
// series and re-organizes affected books in place. POST /series/normalize.
func (h *Handler) SeriesNormalize(c *gin.Context) {
	store := h.store
	if store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}
	if h.opRegistry == nil {
		httputil.RespondWithInternalError(c, "operation registry not initialized")
		return
	}

	opID := ulid.Make().String()
	detail := "series-normalize"
	op, err := store.CreateOperation(opID, "series-normalize", &detail)
	if err != nil {
		httputil.InternalError(c, "failed to create operation", err)
		return
	}

	if _, err := h.opRegistry.EnqueueOp(c.Request.Context(), "dedup.series-normalize", seriesNormalizeOpParams{LegacyOpID: op.ID}); err != nil {
		httputil.InternalError(c, "failed to enqueue operation", err)
		return
	}

	httputil.RespondWithSuccess(c, 202, op)
}
