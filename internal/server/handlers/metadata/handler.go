// file: internal/server/handlers/metadata/handler.go
// version: 1.8.0
// guid: 54bb4ad0-cab0-41fc-b9cb-557c96beee44
// last-edited: 2026-08-11

// Package metadatahandler hosts the metadata-domain HTTP handlers extracted
// from the server package's metadata_handlers.go: batch-update / validate /
// export / import, external metadata search, per-book fetch / search / apply /
// mark-no-match / revert, metadata-rejections, copy-on-write version list /
// prune, write-back, bulk fetch + bulk write-back enqueue, batch write-back
// enqueue, the field-enumeration endpoint, and the rating PATCH (19 handlers
// total). Behavior is preserved byte-for-byte (status codes, JSON shapes, error
// strings, cache keys, op-enqueue payloads).
//
// The package is named metadatahandler (dir handlers/metadata) to avoid
// clashing with the existing internal/metadata package (imported here as
// metadatapkg) and internal/metafetch.
//
// Dependencies that lived on the *Server receiver are reached through narrow
// interfaces (MetadataStore, MetadataFetchService, WriteBackEnqueuer,
// OperationsRegistry, FileIOPool) plus the concrete *cache.Cache[gin.H] (the
// cache exception) and a set of INJECTED func fields that wrap helpers /
// behavior that STAY in package server because they reference server- or
// metafetch-private types or are shared with files that did not move:
//
//   - enrichBook — wraps *Server.enrichBookForResponseSingle, whose concrete
//     return type (enrichedBookResponse) is server-private (surfaced as any).
//   - isProtectedPath — wraps *Server.isProtectedPath (server_middleware.go).
//   - loadMetadataState — wraps *Server.loadMetadataState (server_metadata.go);
//     its return type map[string]metafetch.MetadataFieldState is EXPORTED and
//     the bulk-fetch handler reads OverrideLocked / OverrideValue off it, so it
//     is injected with the concrete type (not any).
//   - updateFetchedMetadataState — wraps *Server.updateFetchedMetadataState
//     (server_metadata.go).
//   - publishEvent — wraps *Server.publishEvent (the shared plugin event bus).
//
// As a result package metadatahandler never imports package server.
//
// The store is a WIRE-TIME SNAPSHOT (assigned once in New, never swapped after
// that). The write-back batcher remains a lazy provider closure because
// integration tests swap server.writeBackBatcher post-wire. The interface-typed
// service deps (metadataFetchService, opRegistry, fileIOPool) are wire-time
// snapshots, each guarded against typed-nil boxing by the controller in
// wire_handlers.go.

package metadatahandler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/cache"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	metadatapkg "github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/plugin"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
	"github.com/gin-gonic/gin"
	ulid "github.com/oklog/ulid/v2"
)

// Handler hosts the metadata-domain HTTP endpoints.
type Handler struct {
	// store is the wire-time snapshot of the database store. All handlers read
	// from this field directly (no lazy provider needed — no post-wire swap for
	// this package's store). Typed MetadataStore (which embeds database.BookStore)
	// so the handler can pass it straight to metadata.BatchUpdateMetadata /
	// metadata.ImportMetadata.
	store MetadataStore

	metadataFetchService MetadataFetchService

	// getWriteBack resolves the iTunes write-back batcher LAZILY, at request
	// time. server.writeBackBatcher is swapped AFTER wireHandlers by integration
	// tests (and the original handlers read it at request time / late binding),
	// so a wire-time snapshot would capture the pre-swap value and miss the
	// enqueue. The provider performs the typed-nil guard so the in-method
	// `!= nil` checks (mirroring the old `s.writeBackBatcher != nil` guards) hold.
	getWriteBack func() WriteBackEnqueuer

	// opRegistry backs handleBulkWriteBack / batchWriteBackAudiobooks. Interface
	// snapshot, typed-nil guarded by the controller so the in-method
	// `== nil` guard (mirroring `s.opRegistry == nil`) holds.
	opRegistry OperationsRegistry

	// fileIOPool backs applyAudiobookMetadata's background file-IO submission.
	// Interface snapshot, typed-nil guarded by the controller so the in-method
	// `pool != nil` guard (mirroring `if pool := s.fileIOPool; pool != nil`)
	// holds.
	fileIOPool FileIOPool

	// listCache is the concrete *cache.Cache[gin.H] the original handlers read /
	// wrote directly (the meta_search:* keys). Passed concrete (the cache
	// exception) because it is a clean generic db-adjacent type under heavy
	// multi-method use.
	listCache *cache.Cache[gin.H]

	// --- injected funcs wrapping behavior that stays in package server ---

	// enrichBook wraps *Server.enrichBookForResponseSingle, whose concrete return
	// type (enrichedBookResponse) is server-private, so it is surfaced as any.
	enrichBook func(book *database.Book) any

	// isProtectedPath wraps *Server.isProtectedPath (server_middleware.go), used
	// by handleBulkWriteBack to skip write-back for protected paths.
	isProtectedPath func(filePath string) bool

	// loadMetadataState wraps *Server.loadMetadataState (server_metadata.go). Its
	// return type is exported (map[string]metafetch.MetadataFieldState) and the
	// bulk-fetch handler reads OverrideLocked / OverrideValue off it, so it is
	// injected with the concrete type.
	loadMetadataState func(bookID string) (map[string]metafetch.MetadataFieldState, error)

	// updateFetchedMetadataState wraps *Server.updateFetchedMetadataState
	// (server_metadata.go), used by bulkFetchMetadata to persist fetched-but-
	// not-applied field values.
	updateFetchedMetadataState func(bookID string, values map[string]any) error

	// publishEvent wraps *Server.publishEvent (the shared plugin event bus), used
	// by applyAudiobookMetadata.
	publishEvent func(ctx context.Context, event plugin.Event)
}

// New constructs a metadata Handler from its dependencies.
func New(
	store MetadataStore,
	metadataFetchService MetadataFetchService,
	getWriteBack func() WriteBackEnqueuer,
	opRegistry OperationsRegistry,
	fileIOPool FileIOPool,
	listCache *cache.Cache[gin.H],
	enrichBook func(book *database.Book) any,
	isProtectedPath func(filePath string) bool,
	loadMetadataState func(bookID string) (map[string]metafetch.MetadataFieldState, error),
	updateFetchedMetadataState func(bookID string, values map[string]any) error,
	publishEvent func(ctx context.Context, event plugin.Event),
) *Handler {
	return &Handler{
		store:                      store,
		metadataFetchService:       metadataFetchService,
		getWriteBack:               getWriteBack,
		opRegistry:                 opRegistry,
		fileIOPool:                 fileIOPool,
		listCache:                  listCache,
		enrichBook:                 enrichBook,
		isProtectedPath:            isProtectedPath,
		loadMetadataState:          loadMetadataState,
		updateFetchedMetadataState: updateFetchedMetadataState,
		publishEvent:               publishEvent,
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

// stringPtr is a local copy of the *string helper used by bulkFetchMetadata.
// The server copy is package-private (server_helpers.go), so a local copy keeps
// this package decoupled (mirrors the audiobooks ptrStr local copy).
func stringPtr(s string) *string { return &s }

// ratingPatchRequest aliases the canonical type from internal/server/handlers
// (no import cycle — handlers is a leaf package).
type ratingPatchRequest = handlers.RatingPatchRequest

// bulkFetchMetadataRequest mirrors the server-private request struct (server.go)
// byte-for-byte so the JSON binding (including the `binding:"required"` tag and
// the `only_missing,omitempty` shape) is identical.
type bulkFetchMetadataRequest struct {
	BookIDs     []string `json:"book_ids" binding:"required"`
	OnlyMissing *bool    `json:"only_missing,omitempty"`
}

// bulkFetchMetadataResult mirrors the server-private result struct (server.go)
// byte-for-byte so the serialized per-book result shape is identical.
type bulkFetchMetadataResult struct {
	BookID        string   `json:"book_id"`
	Status        string   `json:"status"`
	Message       string   `json:"message,omitempty"`
	AppliedFields []string `json:"applied_fields,omitempty"`
	FetchedFields []string `json:"fetched_fields,omitempty"`
}

// bulkFetchMetadataConcurrency bounds how many book IDs bulkFetchMetadataImpl
// fetches from external metadata sources in parallel (CONC-12). This loop
// runs inline on a user-facing HTTP request — not a background maintenance
// job — and each iteration makes a network call to a rate-limited external
// metadata source, so the pool is a small fixed constant (not
// runtime.NumCPU()) to avoid starving the server and tripping the source's
// rate limit on a "select all" bulk request with hundreds/thousands of IDs.
const bulkFetchMetadataConcurrency = 4

// bulkFetchNoopReporter is a tiny inert registry.Reporter adapter used only to
// satisfy RunItems' Reporter parameter for bulkFetchMetadataImpl, which has no
// backing async operation/run row — it is a synchronous HTTP request, not an
// op-registry job. RunItems' concurrent path (runItemsPar) only ever calls
// SetCurrentItem and UpdateProgress per item; the remaining methods are
// unreachable from that path but are implemented as safe, nil-safe no-ops so
// the type fully satisfies registry.Reporter.
type bulkFetchNoopReporter struct{}

func (bulkFetchNoopReporter) UpdateProgress(current, total int, message string) error {
	return nil
}

func (bulkFetchNoopReporter) Log(level slog.Level, message string, attrs ...slog.Attr) error {
	return nil
}

func (bulkFetchNoopReporter) Logger() *slog.Logger { return slog.Default() }

func (bulkFetchNoopReporter) Checkpoint(state any) error { return nil }

func (bulkFetchNoopReporter) IsCanceled() bool { return false }

func (r bulkFetchNoopReporter) RunPhase(ctx context.Context, name string, fn func(context.Context, opsregistry.Reporter) error) error {
	return fn(ctx, r)
}

func (bulkFetchNoopReporter) Trigger(ctx context.Context, eventName string, payload any) error {
	return nil
}

func (bulkFetchNoopReporter) SetCurrentItem(label string) {}

// bulkWriteBackOpParams mirrors the server-private op-param struct
// (library_writeback_op.go) byte-for-byte so the JSON enqueued via
// OperationsRegistry.EnqueueOp (generic json.Marshal) is identical.
type bulkWriteBackOpParams struct {
	BookIDs []string `json:"book_ids"`
	Rename  bool     `json:"rename"`
}

// batchSaveOpParams mirrors the server-private op-param struct (batch_save_op.go)
// byte-for-byte so the JSON enqueued via OperationsRegistry.EnqueueOp (generic
// json.Marshal) is identical.
type batchSaveOpParams struct {
	LegacyOpID string   `json:"legacy_op_id"`
	BookIDs    []string `json:"book_ids"`
	Organize   bool     `json:"organize"`
	Force      bool     `json:"force"`
}

// batchUpdateMetadata handles batch metadata updates with validation
func (h *Handler) batchUpdateMetadataImpl(c *gin.Context) {
	store := h.store
	if store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}

	var req struct {
		Updates  []metadatapkg.MetadataUpdate `json:"updates" binding:"required"`
		Validate bool                         `json:"validate"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}

	errs, successCount := metadatapkg.BatchUpdateMetadata(req.Updates, store, req.Validate)

	response := gin.H{
		"success_count": successCount,
		"total_count":   len(req.Updates),
	}

	if len(errs) > 0 {
		errorMessages := make([]string, len(errs))
		for i, err := range errs {
			errorMessages[i] = err.Error()
		}
		response["errors"] = errorMessages
		httputil.RespondWithSuccess(c, 206, response)
	} else {
		httputil.RespondWithOK(c, response)
	}
}

// validateMetadata validates metadata updates without applying them
func (h *Handler) validateMetadataImpl(c *gin.Context) {
	var req struct {
		Updates map[string]any `json:"updates" binding:"required"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}

	rules := metadatapkg.DefaultValidationRules()
	errs := metadatapkg.ValidateMetadata(req.Updates, rules)

	if len(errs) > 0 {
		errorMessages := make([]string, len(errs))
		for i, err := range errs {
			errorMessages[i] = err.Error()
		}
		httputil.RespondWithBadRequest(c, fmt.Sprintf("validation errors: %v", errorMessages))
	} else {
		httputil.RespondWithOK(c, gin.H{
			"valid":   true,
			"message": "metadata is valid",
		})
	}
}

// exportMetadata exports all audiobook metadata
func (h *Handler) exportMetadataImpl(c *gin.Context) {
	store := h.store
	if store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}

	// Get all books
	books, err := store.GetAllBooksCore(0, 0) // No limit/offset
	if err != nil {
		httputil.InternalError(c, "failed to get audiobooks", err)
		return
	}

	// Export metadata
	exportData, err := metadatapkg.ExportMetadata(books)
	if err != nil {
		httputil.InternalError(c, "failed to export metadata", err)
		return
	}

	httputil.RespondWithOK(c, exportData)
}

// importMetadata imports audiobook metadata
func (h *Handler) importMetadataImpl(c *gin.Context) {
	store := h.store
	if store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}

	var req struct {
		Data     map[string]any `json:"data" binding:"required"`
		Validate bool           `json:"validate"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}

	importCount, errs := metadatapkg.ImportMetadata(req.Data, store, req.Validate)

	response := gin.H{
		"import_count": importCount,
	}

	if len(errs) > 0 {
		errorMessages := make([]string, len(errs))
		for i, err := range errs {
			errorMessages[i] = err.Error()
		}
		response["errors"] = errorMessages
		httputil.RespondWithSuccess(c, 206, response)
	} else {
		httputil.RespondWithOK(c, response)
	}
}

// searchMetadata searches external metadata sources
func (h *Handler) searchMetadataImpl(c *gin.Context) {
	title := c.Query("title")
	author := c.Query("author")

	if title == "" {
		httputil.RespondWithBadRequest(c, "title parameter required")
		return
	}

	// Use Open Library for now
	client := metadatapkg.NewOpenLibraryClient()

	var results []metadatapkg.BookMetadata
	var err error

	ctx := c.Request.Context()
	if author != "" {
		results, err = client.SearchByTitleAndAuthor(ctx, title, author)
	} else {
		results, err = client.SearchByTitle(ctx, title)
	}

	if err != nil {
		httputil.InternalError(c, "metadata search failed", err)
		return
	}

	httputil.RespondWithOK(c, gin.H{
		"results": results,
		"source":  "Open Library",
	})
}

// fetchAudiobookMetadata fetches and applies metadata to an audiobook
func (h *Handler) fetchAudiobookMetadataImpl(c *gin.Context) {
	id := c.Param("id")

	store := h.store
	if store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}

	resp, err := h.metadataFetchService.FetchMetadataForBook(c.Request.Context(), id)
	if err != nil {
		httputil.RespondWithError(c, 404, err.Error(), "NOT_FOUND")
		return
	}

	// METADATA-CACHED-MATCHER: fetch+apply rewrites book identity (title,
	// author, etc), so the cached candidates are stale by definition.
	// Invalidate so the next read fetches fresh.
	_ = h.metadataFetchService.InvalidateCachedCandidates(id)

	// Enqueue for iTunes auto write-back if metadata was updated
	if wb := h.resolveWriteBack(); wb != nil {
		wb.Enqueue(id)
	}

	// Re-fetch to get fully enriched book with author/series/narrator names
	enrichedBook := resp.Book
	if fresh, err := store.GetBookByID(id); err == nil && fresh != nil {
		enrichedBook = fresh
	}
	httputil.RespondWithOK(c, gin.H{
		"message": resp.Message,
		"book":    h.enrichBook(enrichedBook),
		"source":  resp.Source,
	})
}

// searchAudiobookMetadata handles POST /api/v1/audiobooks/:id/search-metadata.
//
// METADATA-CACHED-MATCHER: when the caller doesn't override the search
// inputs, we consult the persistent per-book cache first. `?refresh=true`
// forces a fresh fetch + cache replace. Explicit query/author/narrator/
// series in the body always bypass the cache because they're an
// alternative-search, not a re-read of the same query.
func (h *Handler) searchAudiobookMetadataImpl(c *gin.Context) {
	id := c.Param("id")
	if h.store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}
	// These five fields are the entire search query AND the persistent cache key
	// built from them below. A malformed body zeroed all of them, which made
	// plainFetch true and produced a cache key indistinguishable from a genuine
	// empty query — so an unreadable search could WRITE a result under a key that
	// later legitimate searches read back. A parse failure that poisons a cache
	// outlives the request that caused it, which is why this one is refused
	// rather than defaulted.
	var body struct {
		Query     string `json:"query"`
		Author    string `json:"author"`
		Narrator  string `json:"narrator"`
		Series    string `json:"series"`
		UseRerank bool   `json:"use_rerank"`
	}
	if err := c.ShouldBindJSON(&body); err != nil && !errors.Is(err, io.EOF) {
		httputil.RespondWithBadRequest(c, "invalid request body: "+err.Error())
		return
	}
	refresh := c.Query("refresh") == "true"

	// Persistent cache read: only when the caller is doing a plain
	// per-book fetch (no alt-query) and didn't force refresh.
	// NOT generation-scoped, unlike the "list:" keys in the same cache
	// instance. The two keyspaces are disjoint, so there is no collision to
	// avoid, and the contents are unrelated: these are metadata-provider
	// search results for one book, not a view of the book corpus. Scoping them
	// to the library generation would discard every provider result on any
	// book create/update/delete — throwing away exactly the external API calls
	// this cache exists to avoid. Freshness here is owned by ?refresh=true and
	// the persistent candidate cache's own is_fresh flag.
	cacheKey := fmt.Sprintf("meta_search:%s:%s:%s:%s:%s:%t",
		id, body.Query, body.Author, body.Narrator, body.Series, body.UseRerank)
	plainFetch := body.Query == "" && body.Author == "" && body.Narrator == "" && body.Series == ""
	if !refresh && plainFetch && h.metadataFetchService != nil {
		if entry, fresh, err := h.metadataFetchService.GetCachedCandidates(id); err == nil && entry != nil {
			results := decodeCachedCandidates(entry)
			respH := gin.H{
				"results":    results,
				"query":      body.Query,
				"from_cache": true,
				"is_fresh":   fresh,
				"fetched_at": entry.FetchedAt,
			}
			h.listCache.Set(cacheKey, respH)
			httputil.RespondWithOK(c, respH)
			return
		}
	}

	// 60-second in-memory cache: still useful for back-to-back UI re-renders
	// even after the persistent cache lands. Keyed identically.
	if !refresh {
		if cached, ok := h.listCache.Get(cacheKey); ok {
			httputil.RespondWithOK(c, cached)
			return
		}
	}

	// Plain per-book fetches go through FetchAndCache so the persistent
	// cache stays warm. Alt-query searches use the bare search path
	// because they're exploring outside the canonical fetch.
	if plainFetch && h.metadataFetchService != nil {
		entry, err := h.metadataFetchService.FetchAndCache(c.Request.Context(), id, body.Query, body.Author, body.Narrator, body.Series, metafetch.SearchOptions{UseRerank: body.UseRerank})
		if err != nil {
			httputil.RespondWithError(c, 404, err.Error(), "NOT_FOUND")
			return
		}
		results := decodeCachedCandidates(entry)
		respH := gin.H{"results": results, "query": body.Query, "from_cache": false, "is_fresh": true, "fetched_at": entry.FetchedAt}
		h.listCache.Set(cacheKey, respH)
		httputil.RespondWithOK(c, respH)
		return
	}

	resp, err := h.metadataFetchService.SearchMetadataForBookWithOptions(
		id, body.Query, body.Author, body.Narrator, body.Series,
		metafetch.SearchOptions{UseRerank: body.UseRerank},
	)
	if err != nil {
		httputil.RespondWithError(c, 404, err.Error(), "NOT_FOUND")
		return
	}
	respH := gin.H{"results": resp.Results, "query": resp.Query, "sources_tried": resp.SourcesTried, "sources_failed": resp.SourcesFailed}
	h.listCache.Set(cacheKey, respH)
	httputil.RespondWithOK(c, resp)
}

// decodeCachedCandidates unwraps the persisted []json.RawMessage into
// []metafetch.MetadataCandidate. Drops candidates that don't parse so
// a single corrupt cache entry doesn't poison the whole response.
//
// Used ONLY by searchAudiobookMetadata, so it moves into the sub-package (the
// server-resident async-op machinery never touches it).
func decodeCachedCandidates(entry *metafetch.MetadataCandidateCache) []metafetch.MetadataCandidate {
	out := make([]metafetch.MetadataCandidate, 0, len(entry.Candidates))
	for _, raw := range entry.Candidates {
		var c metafetch.MetadataCandidate
		if err := json.Unmarshal(raw, &c); err == nil {
			out = append(out, c)
		}
	}
	return out
}

// applyAudiobookMetadata handles POST /api/v1/audiobooks/:id/apply-metadata.
func (h *Handler) applyAudiobookMetadataImpl(c *gin.Context) {
	id := c.Param("id")
	store := h.store
	if store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}
	var body struct {
		Candidate metafetch.MetadataCandidate `json:"candidate"`
		Fields    []string                    `json:"fields"`
		WriteBack *bool                       `json:"write_back"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		httputil.RespondWithBadRequest(c, "invalid request body")
		return
	}
	resp, err := h.metadataFetchService.ApplyMetadataCandidate(id, body.Candidate, body.Fields)
	if err != nil {
		httputil.InternalError(c, "failed to apply metadata", err)
		return
	}

	// METADATA-CACHED-MATCHER: applying invalidates the cache so the
	// next read goes through a fresh fetch chain that reflects the
	// new title/author/etc.
	if h.metadataFetchService != nil {
		_ = h.metadataFetchService.InvalidateCachedCandidates(id)
	}

	// Kick off slow file I/O (cover embed, tags, rename) in background.
	// Cover download is already done inline so the response has the URL.
	shouldWriteBack := body.WriteBack == nil || *body.WriteBack

	// Enqueue in the write-back batcher immediately (before pool submission)
	// so the batcher picks up the metadata change even if the background
	// file-IO job panics on a malformed audio file. The DB metadata is
	// already updated at this point, so early enqueueing is correct.
	if wb := h.resolveWriteBack(); shouldWriteBack && wb != nil {
		wb.Enqueue(id)
	}

	if pool := h.fileIOPool; pool != nil {
		bookID := id
		mfs := h.metadataFetchService
		pool.Submit(bookID, func() {
			mfs.ApplyMetadataFileIO(bookID)
			if shouldWriteBack {
				if _, wbErr := mfs.WriteBackMetadataForBook(bookID); wbErr != nil {
					slog.Warn("background write-back for", "bookID", bookID, "wbErr", wbErr)
				}
			}
		})
	}

	h.publishEvent(c.Request.Context(), plugin.NewEvent(plugin.EventMetadataApplied, id, map[string]any{
		"source":  resp.Source,
		"message": resp.Message,
	}))

	// Re-fetch to get fully enriched book with author/series/narrator names
	enrichedBook := resp.Book
	if fresh, err := store.GetBookByID(id); err == nil && fresh != nil {
		enrichedBook = fresh
	}
	httputil.RespondWithOK(c, gin.H{
		"message": resp.Message,
		"book":    h.enrichBook(enrichedBook),
		"source":  resp.Source,
	})
}

// markAudiobookNoMatch handles POST /api/v1/audiobooks/:id/mark-no-match.
func (h *Handler) markAudiobookNoMatchImpl(c *gin.Context) {
	id := c.Param("id")
	store := h.store
	if store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}
	if err := h.metadataFetchService.MarkNoMatch(id); err != nil {
		httputil.InternalError(c, "failed to mark no match", err)
		return
	}
	rejection := database.MetadataRejection{
		ID:              ulid.Make().String(),
		BookID:          id,
		Source:          "user",
		RejectionReason: "user_rejected",
		RejectedAt:      time.Now(),
	}
	if rerr := store.AddMetadataRejection(rejection); rerr != nil {
		slog.Warn("markAudiobookNoMatch could not record rejection for", "id", id, "rerr", rerr)
	}
	httputil.RespondWithOK(c, gin.H{"message": "Book marked as no match"})
}

// handleGetMetadataRejections handles GET /api/v1/audiobooks/:id/metadata-rejections.
func (h *Handler) handleGetMetadataRejectionsImpl(c *gin.Context) {
	id := c.Param("id")
	store := h.store
	if store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}
	rejections, err := store.GetMetadataRejections(id)
	if err != nil {
		httputil.InternalError(c, "failed to get metadata rejections", err)
		return
	}
	if rejections == nil {
		rejections = []database.MetadataRejection{}
	}
	httputil.RespondWithOK(c, gin.H{"rejections": rejections})
}

// revertAudiobookMetadata handles POST /api/v1/audiobooks/:id/revert-metadata.
// It restores a book to a previous CoW version snapshot via the store layer.
func (h *Handler) revertAudiobookMetadataImpl(c *gin.Context) {
	id := c.Param("id")
	store := h.store
	if store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}
	var body struct {
		Timestamp string `json:"timestamp"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Timestamp == "" {
		httputil.RespondWithBadRequest(c, "timestamp is required")
		return
	}
	ts, err := time.Parse(time.RFC3339Nano, body.Timestamp)
	if err != nil {
		httputil.RespondWithBadRequest(c, "invalid timestamp format, use RFC3339Nano")
		return
	}
	book, err := store.RevertBookToVersion(id, ts)
	if err != nil {
		httputil.InternalError(c, "failed to revert metadata", err)
		return
	}
	// METADATA-CACHED-MATCHER: revert changes book identity, so cached
	// candidates may no longer match.
	if h.metadataFetchService != nil {
		_ = h.metadataFetchService.InvalidateCachedCandidates(id)
	}
	httputil.RespondWithOK(c, gin.H{"message": "Book reverted to version", "book": book})
}

// listBookCOWVersions handles GET /api/v1/audiobooks/:id/cow-versions.
// Returns copy-on-write version snapshots from the store layer.
func (h *Handler) listBookCOWVersionsImpl(c *gin.Context) {
	id := c.Param("id")
	store := h.store
	if store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}
	p := httputil.ParsePaginationParams(c)
	limit := p.Limit
	versions, err := store.GetBookSnapshots(id, limit)
	if err != nil {
		httputil.InternalError(c, "failed to list versions", err)
		return
	}
	httputil.RespondWithOK(c, gin.H{"versions": versions})
}

// pruneBookCOWVersions handles POST /api/v1/audiobooks/:id/cow-versions/prune.
// Prunes old copy-on-write version snapshots, keeping the most recent N.
func (h *Handler) pruneBookCOWVersionsImpl(c *gin.Context) {
	id := c.Param("id")
	store := h.store
	if store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}
	var body struct {
		KeepCount int `json:"keep_count"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.KeepCount <= 0 {
		httputil.RespondWithBadRequest(c, "keep_count must be a positive integer")
		return
	}
	pruned, err := store.PruneBookSnapshots(id, body.KeepCount)
	if err != nil {
		httputil.InternalError(c, "failed to prune versions", err)
		return
	}
	httputil.RespondWithOK(c, gin.H{"pruned": pruned})
}

// writeBackAudiobookMetadata handles POST /api/v1/audiobooks/:id/write-back.
// It writes current DB metadata to audio files AND renames files if AutoRenameOnApply is enabled.
func (h *Handler) writeBackAudiobookMetadataImpl(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		httputil.RespondWithBadRequest(c, "book id is required")
		return
	}

	// Parse optional segment filter and rename flag from request body.
	//
	// segment_ids is the SCOPE. Note the interaction directly below: the rename
	// branch fires on `doRename && len(body.SegmentIDs) == 0`, so a malformed
	// body did not merely lose the segment filter — losing it is exactly what
	// UNLOCKS the whole-book rename path. A request scoped to two segments
	// became a full rename of the book's files on disk.
	var body struct {
		SegmentIDs []string `json:"segment_ids"`
		Rename     *bool    `json:"rename"`
	}
	if err := c.ShouldBindJSON(&body); err != nil && !errors.Is(err, io.EOF) {
		httputil.RespondWithBadRequest(c, "invalid request body: "+err.Error())
		return
	}

	store := h.store
	book, err := store.GetBookByID(id)
	if err != nil || book == nil {
		httputil.RespondWithNotFound(c, "audiobook", "")
		return
	}

	// Step 1: Rename files if requested or AutoRenameOnApply is on
	renamed := 0
	doRename := (body.Rename != nil && *body.Rename) || config.AppConfig.AutoRenameOnApply
	if doRename && len(body.SegmentIDs) == 0 {
		if err := h.metadataFetchService.RunApplyPipelineRenameOnly(id, book); err != nil {
			slog.Warn("rename failed for book", "id", id, "err", err)
		} else {
			renamed = 1
		}
	}

	// Step 2: Write tags to files
	var writtenCount int
	if len(body.SegmentIDs) > 0 {
		writtenCount, err = h.metadataFetchService.WriteBackMetadataForBook(id, body.SegmentIDs)
	} else {
		writtenCount, err = h.metadataFetchService.WriteBackMetadataForBook(id)
	}
	if err != nil {
		httputil.InternalError(c, "failed to write back metadata", err)
		return
	}

	msg := fmt.Sprintf("metadata written to %d file(s)", writtenCount)
	if writtenCount == 0 {
		msg = "no files needed tag updates (tags already match DB values)"
	}
	if renamed > 0 {
		msg += ", files renamed"
	}

	httputil.RespondWithOK(c, gin.H{
		"message":       msg,
		"written_count": writtenCount,
		"renamed":       renamed > 0,
	})
}

// bulkFetchMetadata fetches external metadata for multiple audiobooks and applies
// fields only when they are missing and not manually overridden or locked.
func (h *Handler) bulkFetchMetadataImpl(c *gin.Context) {
	store := h.store
	if store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}

	var req bulkFetchMetadataRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}
	if len(req.BookIDs) == 0 {
		httputil.RespondWithBadRequest(c, "book_ids is required")
		return
	}

	onlyMissing := true
	if req.OnlyMissing != nil {
		onlyMissing = *req.OnlyMissing
	}

	// results is pre-sized and written by index (results[i], not append) so
	// the parallel pass below preserves the exact input order of req.BookIDs,
	// matching the original serial loop's output byte-for-byte. updatedCount
	// and the results slice are the shared mutable state across workers (per
	// CONC-12); both are guarded by resultsMu via setResult.
	results := make([]bulkFetchMetadataResult, len(req.BookIDs))
	updatedCount := 0
	var resultsMu sync.Mutex

	setResult := func(i int, r bulkFetchMetadataResult) {
		resultsMu.Lock()
		results[i] = r
		if r.Status == "updated" {
			updatedCount++
		}
		resultsMu.Unlock()
	}

	// indices (not req.BookIDs directly) so each worker can write its result
	// into the correct output slot via setResult(i, ...).
	indices := make([]int, len(req.BookIDs))
	for i := range indices {
		indices[i] = i
	}

	// context.Background(), not c.Request.Context(): a client disconnect must
	// not truncate the run and leave zero-value entries in results — the
	// parallel pass must always run to completion, same as the serial loop
	// did. Concurrency is a small fixed constant (bulkFetchMetadataConcurrency)
	// because this is a request-scoped, network-bound, rate-limited fetch —
	// not a CPU-bound background job.
	_ = opsregistry.RunItems(context.Background(), bulkFetchNoopReporter{}, indices, func(_ context.Context, i int) error {
		bookID := req.BookIDs[i]
		result := bulkFetchMetadataResult{
			BookID: bookID,
			Status: "skipped",
		}

		book, err := store.GetBookByID(bookID)
		if err != nil || book == nil {
			result.Status = "not_found"
			result.Message = "audiobook not found"
			setResult(i, result)
			return nil
		}

		if strings.TrimSpace(book.Title) == "" {
			result.Message = "missing title"
			setResult(i, result)
			return nil
		}

		state, err := h.loadMetadataState(bookID)
		if err != nil {
			result.Status = "error"
			result.Message = "failed to load metadata state"
			setResult(i, result)
			return nil
		}
		if state == nil {
			state = map[string]metafetch.MetadataFieldState{}
		}

		// Delegate search to service using empty query (uses book's title).
		// Service handles source chain, caching, and candidate scoring.
		searchResp, searchErr := h.metadataFetchService.SearchMetadataForBookWithOptions(
			bookID, "", "", "", "",
			metafetch.SearchOptions{},
		)
		if searchErr != nil {
			result.Status = "error"
			result.Message = fmt.Sprintf("search failed: %v", searchErr)
			setResult(i, result)
			return nil
		}
		if searchResp == nil || len(searchResp.Results) == 0 {
			result.Status = "not_found"
			result.Message = "no metadata found from any source"
			setResult(i, result)
			return nil
		}

		// Pick best match from service's scored candidates (first is already best)
		candidate := searchResp.Results[0]
		// Convert MetadataCandidate to BookMetadata for field mapping
		meta := metadatapkg.BookMetadata{
			Title:                    candidate.Title,
			Author:                   candidate.Author,
			Narrator:                 candidate.Narrator,
			Series:                   candidate.Series,
			SeriesPosition:           candidate.SeriesPosition,
			PublishYear:              candidate.Year,
			Publisher:                candidate.Publisher,
			ISBN:                     candidate.ISBN,
			CoverURL:                 candidate.CoverURL,
			Description:              candidate.Description,
			Language:                 candidate.Language,
			DurationSec:              candidate.DurationSec,
			AudibleRatingOverall:     candidate.AudibleRatingOverall,
			AudibleRatingPerformance: 0, // not available from candidate
			AudibleRatingStory:       0, // not available from candidate
			AudibleRatingCount:       candidate.AudibleRatingCount,
			AudibleNumReviews:        0, // not available from candidate
			GoogleRatingAverage:      candidate.GoogleRatingAverage,
			GoogleRatingCount:        candidate.GoogleRatingCount,
		}
		sourceName := candidate.Source
		fetchedValues := map[string]any{}
		appliedFields := []string{}
		fetchedFields := []string{}

		addFetched := func(field string, value any) {
			fetchedValues[field] = value
			fetchedFields = append(fetchedFields, field)
		}

		shouldApply := func(field string, hasValue bool) bool {
			entry := state[field]
			if entry.OverrideLocked || entry.OverrideValue != nil {
				return false
			}
			if onlyMissing && hasValue {
				return false
			}
			return true
		}

		hasBookValue := func(field string) bool {
			switch field {
			case "title":
				return strings.TrimSpace(book.Title) != ""
			case "author_name":
				return book.AuthorID != nil || book.Author != nil
			case "publisher":
				return book.Publisher != nil && strings.TrimSpace(*book.Publisher) != ""
			case "language":
				return book.Language != nil && strings.TrimSpace(*book.Language) != ""
			case "audiobook_release_year":
				return book.AudiobookReleaseYear != nil && *book.AudiobookReleaseYear != 0
			case "isbn10":
				return book.ISBN10 != nil && strings.TrimSpace(*book.ISBN10) != ""
			case "isbn13":
				return book.ISBN13 != nil && strings.TrimSpace(*book.ISBN13) != ""
			case "duration":
				return book.Duration != nil && *book.Duration > 0
			case "audible_rating_overall":
				return book.AudibleRatingOverall != nil && *book.AudibleRatingOverall > 0
			case "google_rating_average":
				return book.GoogleRatingAverage != nil && *book.GoogleRatingAverage > 0
			default:
				return false
			}
		}

		didUpdate := false

		if meta.Title != "" && !metafetch.IsGarbageValue(meta.Title) {
			addFetched("title", meta.Title)
			if shouldApply("title", hasBookValue("title")) {
				book.Title = meta.Title
				appliedFields = append(appliedFields, "title")
				didUpdate = true
			}
		}

		if meta.Author != "" && !metafetch.IsGarbageValue(meta.Author) {
			addFetched("author_name", meta.Author)
			if shouldApply("author_name", hasBookValue("author_name")) {
				author, err := store.GetAuthorByName(meta.Author)
				if err != nil {
					result.Status = "error"
					result.Message = "failed to resolve author"
					setResult(i, result)
					return nil
				}
				if author == nil {
					author, err = store.CreateAuthor(meta.Author)
					if err != nil {
						result.Status = "error"
						result.Message = "failed to create author"
						setResult(i, result)
						return nil
					}
				}
				book.AuthorID = &author.ID
				appliedFields = append(appliedFields, "author_name")
				didUpdate = true
			}
		}

		if meta.Publisher != "" && !metafetch.IsGarbageValue(meta.Publisher) {
			addFetched("publisher", meta.Publisher)
			if shouldApply("publisher", hasBookValue("publisher")) {
				book.Publisher = stringPtr(meta.Publisher)
				appliedFields = append(appliedFields, "publisher")
				didUpdate = true
			}
		}

		if meta.Language != "" && !metafetch.IsGarbageValue(meta.Language) {
			addFetched("language", meta.Language)
			if shouldApply("language", hasBookValue("language")) {
				book.Language = stringPtr(meta.Language)
				appliedFields = append(appliedFields, "language")
				didUpdate = true
			}
		}

		if meta.PublishYear != 0 {
			addFetched("audiobook_release_year", meta.PublishYear)
			if shouldApply("audiobook_release_year", hasBookValue("audiobook_release_year")) {
				year := meta.PublishYear
				book.AudiobookReleaseYear = &year
				appliedFields = append(appliedFields, "audiobook_release_year")
				didUpdate = true
			}
		}

		if meta.ISBN != "" {
			if len(meta.ISBN) == 10 {
				addFetched("isbn10", meta.ISBN)
				if shouldApply("isbn10", hasBookValue("isbn10")) {
					book.ISBN10 = stringPtr(meta.ISBN)
					appliedFields = append(appliedFields, "isbn10")
					didUpdate = true
				}
			} else {
				addFetched("isbn13", meta.ISBN)
				if shouldApply("isbn13", hasBookValue("isbn13")) {
					book.ISBN13 = stringPtr(meta.ISBN)
					appliedFields = append(appliedFields, "isbn13")
					didUpdate = true
				}
			}
		}

		if meta.DurationSec > 0 {
			addFetched("duration", meta.DurationSec)
			if shouldApply("duration", hasBookValue("duration")) {
				dur := meta.DurationSec
				book.Duration = &dur
				appliedFields = append(appliedFields, "duration")
				didUpdate = true
			}
		}

		// Audible ratings — always overwrite (ratings update over time; newer > older).
		if meta.AudibleRatingOverall > 0 {
			addFetched("audible_rating_overall", meta.AudibleRatingOverall)
			if shouldApply("audible_rating_overall", hasBookValue("audible_rating_overall")) {
				v := meta.AudibleRatingOverall
				book.AudibleRatingOverall = &v
				v2 := meta.AudibleRatingPerformance
				book.AudibleRatingPerformance = &v2
				v3 := meta.AudibleRatingStory
				book.AudibleRatingStory = &v3
				cnt := meta.AudibleRatingCount
				book.AudibleRatingCount = &cnt
				r := meta.AudibleNumReviews
				book.AudibleNumReviews = &r
				appliedFields = append(appliedFields, "audible_ratings")
				didUpdate = true
			}
		}

		// Google Books rating.
		if meta.GoogleRatingAverage > 0 {
			addFetched("google_rating_average", meta.GoogleRatingAverage)
			if shouldApply("google_rating_average", hasBookValue("google_rating_average")) {
				v := meta.GoogleRatingAverage
				book.GoogleRatingAverage = &v
				cnt := meta.GoogleRatingCount
				book.GoogleRatingCount = &cnt
				appliedFields = append(appliedFields, "google_rating")
				didUpdate = true
			}
		}

		if len(fetchedValues) > 0 {
			if err := h.updateFetchedMetadataState(bookID, fetchedValues); err != nil {
				slog.Warn("bulkFetchMetadata failed to persist fetched metadata state for", "bookID", bookID, "err", err)
			}
		}

		if didUpdate {
			// Record change history before applying
			h.metadataFetchService.RecordChangeHistory(book, meta, sourceName)

			if _, err := store.UpdateBook(bookID, book); err != nil {
				result.Status = "error"
				result.Message = fmt.Sprintf("failed to update book: %v", err)
				setResult(i, result)
				return nil
			}
			result.Status = "updated"

			// System tag the source and language so the review UI
			// and future upgrade jobs know where this came from.
			h.metadataFetchService.ApplyMetadataSystemTags(bookID, sourceName, meta.Language)
		} else if len(fetchedValues) > 0 {
			result.Status = "fetched"
		}

		result.AppliedFields = appliedFields
		result.FetchedFields = fetchedFields
		setResult(i, result)
		return nil
	}, opsregistry.RunItemsOptions{
		Concurrency: bulkFetchMetadataConcurrency,
	})

	httputil.RespondWithOK(c, gin.H{
		"updated_count": updatedCount,
		"total_count":   len(req.BookIDs),
		"results":       results,
	})
}

// handleBulkWriteBack handles POST /api/v1/audiobooks/bulk-write-back.
// It creates an async operation that writes metadata tags and renames files
// for all books matching the provided filters (or all organized/imported books).
func (h *Handler) handleBulkWriteBackImpl(c *gin.Context) {
	store := h.store
	if store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}
	if h.opRegistry == nil {
		httputil.RespondWithInternalError(c, "operations registry not initialized")
		return
	}

	// This one fails twice over on a malformed body, in the same direction.
	//
	//   1. DryRun falls to false — a preview becomes a real apply.
	//   2. All three filter fields fall to nil, so the else-branch below runs
	//      GetAllBooksCore(0, 0) — the ENTIRE library.
	//
	// A request scoped to one author, asking for a dry run, became an unfiltered
	// whole-library metadata write. Both halves of the escalation come from the
	// same discarded error, which is why neither a filter check nor a dry-run
	// check downstream would have caught it.
	var req struct {
		Filter struct {
			LibraryState *string `json:"library_state"`
			AuthorID     *int    `json:"author_id"`
			SeriesID     *int    `json:"series_id"`
		} `json:"filter"`
		DryRun bool `json:"dry_run"`
		Rename bool `json:"rename"`
	}
	if err := c.ShouldBindJSON(&req); err != nil && !errors.Is(err, io.EOF) {
		httputil.RespondWithBadRequest(c, "invalid request body: "+err.Error())
		return
	}

	// Gather matching books based on filters. store.GetBooksByAuthorIDCore /
	// GetBooksBySeriesIDCore / GetAllBooksCore are all Core-typed (STOREFID
	// P3-W2 / W4 / W5a), so `books` is uniformly []database.BookCore now — no
	// ToBook() round-trip needed. Only book.ID, MarkedForDeletion, FilePath,
	// and LibraryState (all Core-safe, no heavy fields) are read from
	// `books`/`filtered` below.
	var books []database.BookCore
	var err error

	if req.Filter.AuthorID != nil {
		books, err = store.GetBooksByAuthorIDCore(*req.Filter.AuthorID)
	} else if req.Filter.SeriesID != nil {
		books, err = store.GetBooksBySeriesIDCore(*req.Filter.SeriesID)
	} else {
		// Get all books, then filter by library_state
		books, err = store.GetAllBooksCore(0, 0)
	}
	if err != nil {
		httputil.InternalError(c, "failed to query books", err)
		return
	}

	// Filter by library_state if specified, otherwise default to organized+imported
	targetStates := map[string]bool{"organized": true, "imported": true}
	if req.Filter.LibraryState != nil && *req.Filter.LibraryState != "" {
		targetStates = map[string]bool{*req.Filter.LibraryState: true}
	}

	var filtered []database.BookCore
	for _, book := range books {
		// Skip soft-deleted books
		if book.IsSoftDeleted() {
			continue
		}
		// Skip books with empty file paths
		if book.FilePath == "" {
			continue
		}
		// Skip protected paths
		if h.isProtectedPath(book.FilePath) {
			continue
		}
		// Filter by library state (only when not filtering by author/series exclusively)
		if book.LibraryState != nil {
			if !targetStates[*book.LibraryState] {
				continue
			}
		} else if req.Filter.AuthorID == nil && req.Filter.SeriesID == nil {
			// No library_state set and no author/series filter: skip
			continue
		}
		filtered = append(filtered, book)
	}

	estimatedBooks := len(filtered)

	// Dry run: just return the count
	if req.DryRun {
		httputil.RespondWithOK(c, gin.H{
			"estimated_books": estimatedBooks,
			"dry_run":         true,
		})
		return
	}

	if estimatedBooks == 0 {
		httputil.RespondWithOK(c, gin.H{
			"estimated_books": 0,
			"message":         "no books match the given filters",
		})
		return
	}

	doRename := req.Rename
	bookIDs := make([]string, len(filtered))
	for i, b := range filtered {
		bookIDs[i] = b.ID
	}

	rawParams, _ := json.Marshal(bulkWriteBackOpParams{BookIDs: bookIDs, Rename: doRename})
	opID, err := h.opRegistry.EnqueueOp(c.Request.Context(), "library.bulk-write-back", rawParams)
	if err != nil {
		httputil.InternalError(c, "enqueue failed", err)
		return
	}

	httputil.RespondWithSuccess(c, 202, gin.H{
		"operation_id":    opID,
		"id":              opID,
		"estimated_books": estimatedBooks,
	})
}

// batchWriteBackAudiobooks handles POST /api/v1/audiobooks/batch-write-back.
func (h *Handler) batchWriteBackAudiobooksImpl(c *gin.Context) {
	var req struct {
		BookIDs  []string `json:"book_ids"`
		Rename   bool     `json:"rename"`
		Organize bool     `json:"organize"`
		Force    bool     `json:"force"` // skip change detection, rewrite everything
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}
	if len(req.BookIDs) == 0 {
		httputil.RespondWithBadRequest(c, "book_ids is required")
		return
	}

	store := h.store
	doOrganize := req.Organize || req.Rename

	// Create a supervisor operation for tracking
	opID := ulid.Make().String()
	if _, err := store.CreateOperation(opID, "batch_save_to_files", nil); err != nil {
		httputil.InternalError(c, "failed to create operation", err)
		return
	}

	bookIDs := make([]string, len(req.BookIDs))
	copy(bookIDs, req.BookIDs)
	totalBooks := len(bookIDs)

	params := batchSaveOpParams{
		LegacyOpID: opID,
		BookIDs:    bookIDs,
		Organize:   doOrganize,
		Force:      req.Force,
	}
	if _, enqErr := h.opRegistry.EnqueueOp(c.Request.Context(), "metadata.batch-save", params); enqErr != nil {
		httputil.InternalError(c, "failed to enqueue operation", enqErr)
		return
	}

	httputil.RespondWithOK(c, gin.H{
		"operation_id": opID,
		"message":      fmt.Sprintf("Save to files queued for %d books", totalBooks),
		"book_count":   totalBooks,
	})
}

// getMetadataFields returns available metadata fields with their types and validation rules
func (h *Handler) getMetadataFieldsImpl(c *gin.Context) {
	fields := []map[string]any{
		{
			"name":        "title",
			"type":        "string",
			"required":    true,
			"maxLength":   500,
			"description": "Book title",
		},
		{
			"name":        "author",
			"type":        "string",
			"required":    false,
			"description": "Author name",
		},
		{
			"name":        "narrator",
			"type":        "string",
			"required":    false,
			"description": "Narrator name",
		},
		{
			"name":        "publisher",
			"type":        "string",
			"required":    false,
			"description": "Publisher name",
		},
		{
			"name":        "publishDate",
			"type":        "integer",
			"required":    false,
			"min":         1000,
			"max":         9999,
			"description": "Publication year",
		},
		{
			"name":        "series",
			"type":        "string",
			"required":    false,
			"description": "Series name",
		},
		{
			"name":        "language",
			"type":        "string",
			"required":    false,
			"pattern":     "^[a-z]{2}$",
			"description": "ISO 639-1 language code (e.g., 'en', 'es')",
		},
		{
			"name":        "isbn10",
			"type":        "string",
			"required":    false,
			"pattern":     "^[0-9]{9}[0-9X]$",
			"description": "ISBN-10",
		},
		{
			"name":        "isbn13",
			"type":        "string",
			"required":    false,
			"pattern":     "^97[89][0-9]{10}$",
			"description": "ISBN-13",
		},
		{
			"name":        "series_sequence",
			"type":        "integer",
			"required":    false,
			"min":         1,
			"description": "Position in series",
		},
	}

	httputil.RespondWithOK(c, gin.H{
		"fields": fields,
	})
}

// parseOptionalRating decodes a json.RawMessage into a *float64 and a clear flag.
// Returns (nil, false, nil) if raw is empty (field omitted).
// Returns (nil, true, nil) if raw is JSON null (clear).
// Returns (&v, false, nil) if raw is a valid number in [0,5] step 0.5.
// Returns (nil, false, err) on invalid value.
func parseOptionalRating(raw json.RawMessage, fieldName string) (*float64, bool, error) {
	if len(raw) == 0 {
		return nil, false, nil
	}
	if string(raw) == "null" {
		return nil, true, nil
	}
	var v float64
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, false, fmt.Errorf("%s: must be a number", fieldName)
	}
	if v < 0 || v > 5 {
		return nil, false, fmt.Errorf("%s: must be between 0 and 5", fieldName)
	}
	// check 0.5 step: v*2 must be an integer
	if math.Round(v*2) != v*2 {
		return nil, false, fmt.Errorf("%s: must be a multiple of 0.5", fieldName)
	}
	return &v, false, nil
}

// handleUpdateBookRating handles PATCH /api/v1/audiobooks/:id/rating.
func (h *Handler) handleUpdateBookRatingImpl(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		httputil.RespondWithBadRequest(c, "missing book id")
		return
	}

	var body ratingPatchRequest
	// Allow empty body (no changes)
	if c.Request.ContentLength != 0 {
		if err := c.ShouldBindJSON(&body); err != nil {
			httputil.RespondWithBadRequest(c, err.Error())
			return
		}
	}

	req := database.UpdateBookRatingRequest{}

	if overall, clear, err := parseOptionalRating(body.Overall, "overall"); err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	} else {
		req.Overall = overall
		req.ClearOverall = clear
	}

	if story, clear, err := parseOptionalRating(body.Story, "story"); err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	} else {
		req.Story = story
		req.ClearStory = clear
	}

	if perf, clear, err := parseOptionalRating(body.Performance, "performance"); err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	} else {
		req.Performance = perf
		req.ClearPerf = clear
	}

	// Notes: null clears, string sets, absent leaves alone
	if len(body.Notes) > 0 {
		if string(body.Notes) == "null" {
			req.ClearNotes = true
		} else {
			var notes string
			if err := json.Unmarshal(body.Notes, &notes); err != nil {
				httputil.RespondWithBadRequest(c, "notes: must be a string or null")
				return
			}
			req.Notes = &notes
		}
	}

	store := h.store
	if store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}
	if err := store.UpdateBookRating(id, req); err != nil {
		if strings.Contains(err.Error(), "not found") {
			httputil.RespondWithError(c, http.StatusNotFound, "book not found", "NOT_FOUND")
			return
		}
		httputil.InternalError(c, "UpdateBookRating failed", err)
		return
	}

	// Return the updated book
	book, err := store.GetBookByID(id)
	if err != nil || book == nil {
		if errors.Is(err, nil) && book == nil {
			httputil.RespondWithError(c, http.StatusNotFound, "book not found", "NOT_FOUND")
			return
		}
		httputil.InternalError(c, "GetBook after rating update failed", err)
		return
	}
	httputil.RespondWithOK(c, book)
}
