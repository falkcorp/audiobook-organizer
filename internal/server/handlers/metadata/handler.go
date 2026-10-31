// file: internal/server/handlers/metadata/handler.go
// version: 1.45.8
// guid: 54bb4ad0-cab0-41fc-b9cb-557c96beee44
// last-edited: 2026-10-30

// Package metadatahandler hosts the metadata-domain HTTP handlers extracted
// from the server package's metadata_handlers.go: batch-update / validate /
// export / import, external metadata search, per-book fetch / search / apply /
// mark-no-match / revert, metadata-rejections, copy-on-write version list /
// prune, write-back, bulk write-back enqueue, batch write-back
// enqueue, the field-enumeration endpoint, and the rating PATCH (18 handlers
// total). Behavior is preserved byte-for-byte (status codes, JSON shapes, error
// strings, cache keys, op-enqueue payloads).
//
// The package is named metadatahandler (dir handlers/metadata) to avoid
// clashing with the existing internal/metadata package (imported here as
// metadatapkg) and internal/metafetch.
//
// Dependencies that lived on the *Server receiver are reached through narrow
// interfaces (MetadataStore, MetadataFetchService,
// OperationsRegistry, FileIOPool) plus the concrete *cache.Cache[gin.H] (the
// cache exception) and a set of INJECTED func fields that wrap helpers /
// behavior that STAY in package server because they reference server- or
// metafetch-private types or are shared with files that did not move:
//
//   - enrichBook — wraps *Server.enrichBookForResponseSingle, whose concrete
//     return type (enrichedBookResponse) is server-private (surfaced as any).
//   - isProtectedPath — wraps *Server.isProtectedPath (server_middleware.go).
//   - publishEvent — wraps *Server.publishEvent (the shared plugin event bus).
//
// As a result package metadatahandler never imports package server.
//
// The store is a WIRE-TIME SNAPSHOT (assigned once in New, never swapped after
// that). The interface-typed
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
	"maps"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/applycap"
	"github.com/falkcorp/audiobook-organizer/internal/applygate"
	"github.com/falkcorp/audiobook-organizer/internal/cache"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/metabatch"
	metadatapkg "github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
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

	// opRegistry backs handleBulkWriteBack / batchWriteBackAudiobooks. Interface
	// snapshot, typed-nil guarded by the controller so the in-method
	// `== nil` guard (mirroring `s.opRegistry == nil`) holds.
	opRegistry OperationsRegistry

	// queuedApply hands an apply whose book the library scan holds past
	// requestBookLockWait to the metadata.apply-when-scanned op (202). Nil
	// makes the request keep waiting. Set by SetQueuedApplyEnqueuer.
	queuedApply QueuedApplyEnqueuer

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

	// publishEvent wraps *Server.publishEvent (the shared plugin event bus), used
	// by applyAudiobookMetadata.
	publishEvent func(ctx context.Context, event plugin.Event)

	// browseSources returns the author-catalog reads the browse search
	// (Candidates view "Search again") uses. New sets the production
	// default (defaultBrowseSources); tests replace it.
	browseSources func() metafetch.BrowseSources
}

// New constructs a metadata Handler from its dependencies.
func New(
	store MetadataStore,
	metadataFetchService MetadataFetchService,
	opRegistry OperationsRegistry,
	fileIOPool FileIOPool,
	listCache *cache.Cache[gin.H],
	enrichBook func(book *database.Book) any,
	isProtectedPath func(filePath string) bool,
	publishEvent func(ctx context.Context, event plugin.Event),
) *Handler {
	h := &Handler{
		store:                store,
		metadataFetchService: metadataFetchService,
		opRegistry:           opRegistry,
		fileIOPool:           fileIOPool,
		listCache:            listCache,
		enrichBook:           enrichBook,
		isProtectedPath:      isProtectedPath,
		publishEvent:         publishEvent,
	}
	h.browseSources = h.defaultBrowseSources
	return h
}

// ratingPatchRequest aliases the canonical type from internal/server/handlers
// (no import cycle — handlers is a leaf package).
type ratingPatchRequest = handlers.RatingPatchRequest

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
//
// ⚠️ "BYTE-FOR-BYTE" IS AN UNCHECKED CLAIM. The two structs are coupled only by
// their JSON tags, so dropping a field from one and leaving it on the other
// still compiles and still marshals — it just emits a key the decoder ignores.
// That is exactly what happened here: LegacyOpID was removed from the server
// side and this copy kept it, silently sending "legacy_op_id":"" . Both were
// cleaned up on 2026-08-22. Change one, change the other in the same commit.
type batchSaveOpParams struct {
	BookIDs  []string `json:"book_ids"`
	Organize bool     `json:"organize"`
	Force    bool     `json:"force"`
}

// batchUpdateMetadata handles batch metadata updates with validation
//
// No scan coordination is needed here and none is taken: it writes only the
// database and schedules no file work, and a running scan's merge keeps every
// field another writer changed after the scan read the book
// (scanner.mergeScannedKeepingForeignEdits). It used to refuse with 409 while
// any scan ran.
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

	// Fail-safe cap (internal/applycap). This path writes straight through
	// store.UpdateBook with no candidate gating, so it is the cheapest way to
	// rewrite the whole library by mistake. Refusal, not truncation.
	if ex := applycap.Refuse("metadata/batch-update", len(req.Updates), config.AppConfig.BulkApplyMaxItems); ex != nil {
		httputil.RespondWithApplyCapExceeded(c, ex)
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

	// Per-book scan lock (book_scan_lock.go). The fetch applies and schedules
	// file work through the pool, so it holds the book like an apply does.
	hold, ok := h.lockBookForRequest(c, QueuedApply{Kind: QueuedFetch, BookID: id})
	if !ok {
		return
	}
	defer hold.Release()

	// fetchCore also invalidates the candidate cache (fetch+apply rewrites
	// the book's identity) and enqueues the iTunes write-back.
	resp, err := h.fetchCore(c.Request.Context(), id)
	if errors.Is(err, metafetch.ErrReviewOnlyCandidatesNotApplied) {
		// A match WAS found, on a review-only source (Open Library, Google
		// Books), and the auto-fetch never applies one (owner decision
		// 2026-10-06). Not a 404: nothing is missing, the match waits for the
		// owner's review. The book is returned unchanged.
		book, berr := store.GetBookByID(id)
		if berr != nil || book == nil {
			httputil.RespondWithError(c, 404, "audiobook not found", "NOT_FOUND")
			return
		}
		httputil.RespondWithOK(c, gin.H{
			"message":     "Match found, left for review: " + reviewOnlySources(err) + " matches are applied by hand from the review page, never automatically.",
			"book":        h.enrichBook(book),
			"source":      "",
			"review_only": true,
		})
		return
	}
	if errors.Is(err, metafetch.ErrOwnerRejectedNotApplied) {
		// Matches WERE found, and every one the fetch could apply is a
		// record the owner rejected for this book. Not a 404: nothing is
		// missing. The book is returned unchanged.
		book, berr := store.GetBookByID(id)
		if berr != nil || book == nil {
			httputil.RespondWithError(c, 404, "audiobook not found", "NOT_FOUND")
			return
		}
		httputil.RespondWithOK(c, gin.H{
			"message":        "Not applied: every match found is one you rejected for this book. Un-reject it from the metadata search window to apply it.",
			"book":           h.enrichBook(book),
			"source":         "",
			"owner_rejected": true,
		})
		return
	}
	if errors.Is(err, metafetch.ErrOwnerRejectionCheckFailed) {
		// Fails closed: the rejections could not be read, so nothing was
		// fetched or applied.
		httputil.InternalError(c, "could not read your rejected matches for this book; nothing was applied", err)
		return
	}
	if err != nil {
		httputil.RespondWithError(c, 404, err.Error(), "NOT_FOUND")
		return
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

// reviewOnlySources names the review-only sources that matched in a
// metafetch.ErrReviewOnlyCandidatesNotApplied error, for a message.
func reviewOnlySources(err error) string {
	var ro *metafetch.ReviewOnlyNotAppliedError
	if errors.As(err, &ro) && len(ro.Sources) > 0 {
		return strings.Join(ro.Sources, " and ")
	}
	return "Open Library / Google Books"
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
		// Browse asks the browse search (metafetch.BrowseSearch): what was
		// typed, not the book's identity -- an empty query stays empty
		// instead of becoming the book's title, the author catalog answers
		// first, and no answer is dropped for a low score. The Candidates
		// view's "Search again" sends it. CatalogOnly (with Browse) answers
		// from the local catalog alone, without a provider request.
		Browse      bool `json:"browse"`
		CatalogOnly bool `json:"catalog_only"`
	}
	if err := c.ShouldBindJSON(&body); err != nil && !errors.Is(err, io.EOF) {
		httputil.RespondWithBadRequest(c, "invalid request body: "+err.Error())
		return
	}
	if body.Browse {
		h.browseSearch(c, id, body.Query, body.Author, body.CatalogOnly, c.Query("refresh") == "true")
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
	// staleRow: the persistent row is marked Stale (the book was retitled or
	// re-credited after its candidates were fetched), so they answer another
	// question. It is a cache miss: the plain fetch below searches the
	// current title and rewrites the row unmarked. The 60-second listCache is
	// skipped too, since a plain-fetch entry there predates the retitle (every
	// path that writes one after it either reads an unmarked row or rewrites
	// the row unmarked).
	staleRow := false
	if !refresh && plainFetch && h.metadataFetchService != nil {
		entry, fresh, err := h.metadataFetchService.GetCachedCandidates(id)
		staleRow = err == nil && entry != nil && entry.Stale
		if err == nil && entry != nil && !entry.Stale {
			results := decodeCachedCandidates(entry)
			respH := gin.H{
				"results":    results,
				"query":      body.Query,
				"from_cache": true,
				"is_fresh":   fresh,
				"fetched_at": entry.FetchedAt,
			}
			h.listCache.Set(cacheKey, respH)
			h.respondCandidates(c, id, respH, entry)
			return
		}
	}

	// 60-second in-memory cache: still useful for back-to-back UI re-renders
	// even after the persistent cache lands. Keyed identically.
	if !refresh && !staleRow {
		if cached, ok := h.listCache.Get(cacheKey); ok {
			// Results of a fresh search (an alternative query, or a plain
			// fetch whose cache row has since gone): no cache row to judge.
			h.respondCandidates(c, id, cached, nil)
			return
		}
	}

	// Plain per-book fetches go through FetchAndCache so the persistent
	// cache stays warm. Alt-query searches use the bare search path
	// because they're exploring outside the canonical fetch.
	if plainFetch && h.metadataFetchService != nil {
		// BypassProviderThrottle: this handler is the interactive per-book
		// search dialog — a human is waiting on one book. Bulk paths (the
		// batch apply loop below, the v2 fetch ops) deliberately do NOT set it.
		// BypassFetchCache: ?refresh=true re-asks the providers; without it
		// the per-source fetch cache answered the "refresh" for the same
		// identity.
		//
		// MergeRank ranks the row this fetch writes the way the batch fetch
		// ranks its own (metabatch.MergeRanker): an owner-rejected candidate
		// goes last, so a dialog refetch cannot put one back into slot 0,
		// where the review lane and every bulk apply read.
		opts := metafetch.SearchOptions{UseRerank: body.UseRerank, BypassProviderThrottle: true, BypassFetchCache: refresh}
		if book, berr := h.store.GetBookByID(id); berr == nil && book != nil {
			opts.MergeRank = metabatch.MergeRanker(h.store, h.store, book)
		}
		entry, err := h.metadataFetchService.FetchAndCache(c.Request.Context(), id, body.Query, body.Author, body.Narrator, body.Series, opts)
		if err != nil {
			// Every provider failed: an outage, not a missing book.
			if errors.Is(err, metafetch.ErrNoSourceAnswered) {
				httputil.RespondWithError(c, http.StatusServiceUnavailable, err.Error(), "SERVICE_UNAVAILABLE")
				return
			}
			httputil.RespondWithError(c, 404, err.Error(), "NOT_FOUND")
			return
		}
		results := decodeCachedCandidates(entry)
		respH := gin.H{"results": results, "query": body.Query, "from_cache": false, "is_fresh": true, "fetched_at": entry.FetchedAt}
		h.listCache.Set(cacheKey, respH)
		h.respondCandidates(c, id, respH, entry)
		return
	}

	resp, err := h.metadataFetchService.SearchMetadataForBookWithOptions(
		id, body.Query, body.Author, body.Narrator, body.Series,
		metafetch.SearchOptions{UseRerank: body.UseRerank, BypassProviderThrottle: true, BypassFetchCache: refresh},
	)
	if err != nil {
		httputil.RespondWithError(c, 404, err.Error(), "NOT_FOUND")
		return
	}
	respH := gin.H{"results": resp.Results, "query": resp.Query, "sources_tried": resp.SourcesTried, "sources_failed": resp.SourcesFailed}
	h.listCache.Set(cacheKey, respH)
	h.respondCandidates(c, id, respH, nil)
}

// respondCandidates answers a search with respH, its results turned into
// dialog candidates carrying their apply checks against the book as it is NOW
// (withApplyChecks). The checks are computed per response, never stored in the
// 60-second listCache: the book's ASIN can change inside that window. entry is
// the cache row the results came from, nil for a fresh search.
func (h *Handler) respondCandidates(c *gin.Context, id string, respH gin.H, entry *metafetch.MetadataCandidateCache) {
	results, _ := respH["results"].([]metafetch.MetadataCandidate)
	var book *database.Book
	if h.store != nil {
		b, err := h.store.GetBookByID(id)
		if err != nil {
			// The search itself succeeded; answer it unchecked rather than
			// fail it, and say so in the log.
			searchCheckLog.Warn("search-metadata: book %s unreadable for the candidate checks: %s",
				logger.SanitizeLogValue(id), logger.SanitizeLogValue(err.Error()))
		}
		book = b
	}
	out := make(gin.H, len(respH))
	maps.Copy(out, respH)
	// The dialog reads this list; order it by the ranking score. results is a
	// copy, so the cache row, the 60-second listCache and every other reader
	// keep the stored order. The owner's rejections mark candidates the apply
	// would refuse; a failed read marks none, and the apply itself refuses on it.
	var rejected metafetch.RejectedSet
	if h.store != nil {
		rejected = metabatch.LoadRejectedCandidateKeys(h.store, id)
	}
	out["results"] = withApplyChecks(book, entry, rejected, metafetch.SortForDisplay(results))
	httputil.RespondWithOK(c, out)
}

// searchCheckLog is the process log of the search's candidate checks.
var searchCheckLog = logger.New("metadata.search-checks")

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
		// OverrideASINConflict is the book ASIN the person confirmed applying
		// a conflicting candidate over (the search's apply_check.book_asin).
		// Without it, or once the book carries another ASIN, a candidate whose
		// ASIN conflicts with the book's is refused with 409 asin_conflict.
		OverrideASINConflict string `json:"override_asin_conflict"`
		// Background asks for the apply to run as the durable
		// metadata.apply-when-scanned op and the request to answer 202 at
		// once, instead of holding the request open for the scan-lock wait,
		// the rename preflight and the database write. The Search Metadata
		// dialog sends it so closing the dialog never waits on the apply.
		Background bool `json:"background"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		httputil.RespondWithBadRequest(c, "invalid request body")
		return
	}
	// The ASIN check runs here first, so a refused conflict is answered now
	// rather than queued (behind a scan or in the background) to fail later;
	// applyCandidateCore runs it again under the lock against the book as it
	// is then.
	if book, berr := store.GetBookByID(id); berr == nil {
		if conflict := asinConflictRefusal(book, &body.Candidate, body.OverrideASINConflict); conflict != nil {
			respondASINConflict(c, conflict)
			return
		}
	}
	// Likewise an owner-rejected candidate, answered now rather than queued.
	if rerr := ownerRejectedRefusal(store, id, &body.Candidate); rerr != nil {
		respondOwnerRejected(c, rerr)
		return
	}
	q := QueuedApply{Kind: QueuedApplyCandidate, BookID: id,
		Candidate: &body.Candidate, Fields: body.Fields, WriteBack: body.WriteBack,
		OverrideASINConflict: body.OverrideASINConflict}
	if body.Background && h.queuedApply != nil {
		// No op to hand it to (h.queuedApply nil) falls through to the
		// synchronous apply: slower for the caller, never lost.
		h.respondQueued(c, q, true)
		return
	}
	// Per-book scan lock (book_scan_lock.go): waits only if the library scan
	// is reading THIS book, at most requestBookLockWait, then hands off to the
	// queued op (202). The file job is submitted under the lock; the pool marks
	// the book pending until it has run.
	hold, ok := h.lockBookForRequest(c, q)
	if !ok {
		return
	}
	defer hold.Release()

	resp, err := h.applyCandidateCore(c.Request.Context(), id, body.Candidate, body.Fields, body.WriteBack, "", body.OverrideASINConflict)
	if err != nil {
		if conflict, ok := errors.AsType[*errASINConflict](err); ok {
			respondASINConflict(c, conflict)
			return
		}
		if _, ok := errors.AsType[*errOwnerRejected](err); ok {
			respondOwnerRejected(c, err)
			return
		}
		if refused, ok := errors.AsType[*errRenameWouldFail](err); ok {
			httputil.RespondWithErrorFields(c, http.StatusConflict, refused.Error(), "CONFLICT",
				map[string]any{"reason": metafetch.ApplyRefusedReasonFileWorkWouldFail})
			return
		}
		httputil.InternalError(c, "failed to apply metadata", err)
		return
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

// respondASINConflict answers 409 for a refused conflicting apply, with the
// fields the dialog needs to offer the override: reason asin_conflict, the
// book's ASIN (the value to send back as override_asin_conflict) and the
// candidate's.
func respondASINConflict(c *gin.Context, conflict *errASINConflict) {
	httputil.RespondWithErrorFields(c, http.StatusConflict, conflict.Error(), "CONFLICT",
		map[string]any{"reason": applyReasonASINConflict, "book_asin": conflict.BookASIN,
			"candidate_asin": conflict.CandidateASIN, "detail": conflict.Detail})
}

// respondOwnerRejected answers 409 reason owner_rejected for a candidate the
// owner rejected, or 500 when the rejections could not be read.
func respondOwnerRejected(c *gin.Context, err error) {
	var rejected *errOwnerRejected
	if !errors.As(err, &rejected) {
		httputil.InternalError(c, "failed to read owner rejections", err)
		return
	}
	httputil.RespondWithErrorFields(c, http.StatusConflict, rejected.Error(), "CONFLICT",
		map[string]any{"reason": applygate.ReasonOwnerRejected})
}

var noMatchLog = logger.New("metadata.nomatch")

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
	// The user rejected the match while the book's title/author/IDs stay the
	// same, so the provider fetch-cache rows still carry a matching identity
	// stamp and the candidate cache is keyed on the same unchanged search
	// inputs: both would replay the rejected result. Delete both, so the next
	// fetch or search starts from a fresh provider result (A3#14). This does
	// NOT enforce the rejection: only FetchMetadataForBook refuses "no_match"
	// books; the bulk and batch-candidate paths do not check it yet.
	if ferr := h.metadataFetchService.InvalidateFetchCacheForBook(id); ferr != nil {
		noMatchLog.Warn("markAudiobookNoMatch could not clear the metadata caches for %s: %v", logger.SanitizeLogValue(id), ferr)
	}
	rejection := database.MetadataRejection{
		ID:              ulid.Make().String(),
		BookID:          id,
		Source:          "user",
		RejectionReason: "user_rejected",
		RejectedAt:      time.Now(),
	}
	if rerr := store.AddMetadataRejection(rejection); rerr != nil {
		slog.Warn("markAudiobookNoMatch could not record rejection for", "id", logger.SanitizeLogValue(id), "rerr", rerr)
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
	// Under the merge lock every retire holds. The store refuses a retired
	// shell under the book's write stripe, but a retire commits under the
	// shell's OWNER stripe and the merge lock, not that stripe, so without the
	// lock a retire could land between the store's check and its commit and the
	// whole-row revert would revive the shell (un-trash it, clear its merged-into
	// link) next to the audio it just lost. With it the check is exact.
	//
	// Lock order: merge lock, then the book's write stripe (RevertBookToVersion
	// takes it; the retired-row reads of its shell check are lock-free and no
	// owner stripe is taken here). A retire takes the merge lock, then its
	// version, file and owner stripes. revertMetadataUpdate (the operation
	// revert) also takes the merge lock before any stripe. Nothing here takes a
	// stripe and then the merge lock, and nothing under the lock calls a
	// merge-family path (the lock is not reentrant): the cache invalidation
	// below runs after the lock is released.
	book, err := func() (*database.Book, error) {
		merge.LockMergeRMW()
		defer merge.UnlockMergeRMW()
		return store.RevertBookToVersion(id, ts)
	}()
	if err != nil {
		if errors.Is(err, database.ErrBookIsRetiredShell) {
			// A whole-row revert would un-trash the shell and clear its
			// merged-into link while its files stay retired under another book.
			httputil.RespondWithError(c, http.StatusConflict, err.Error(), httputil.CodeRetiredShell)
			return
		}
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

	if b, err := h.store.GetBookByID(id); err != nil || b == nil {
		httputil.RespondWithNotFound(c, "audiobook", "")
		return
	}
	doRename := (body.Rename != nil && *body.Rename) || config.AppConfig.AutoRenameOnApply

	// Per-book scan lock (book_scan_lock.go), held for the whole synchronous
	// rename + tag write: the scanner must not read these files mid-write.
	hold, ok := h.lockBookForRequest(c, QueuedApply{Kind: QueuedWriteBack, BookID: id,
		SegmentIDs: body.SegmentIDs, Rename: &doRename})
	if !ok {
		return
	}
	defer hold.Release()

	res, err := h.writeBackCore(c.Request.Context(), id, body.SegmentIDs, doRename)
	if err != nil {
		if conflict, ok := errors.AsType[*errWriteBackConflict](err); ok {
			if conflict.status == http.StatusNotFound {
				httputil.RespondWithNotFound(c, "audiobook", "")
				return
			}
			httputil.RespondWithError(c, conflict.status, conflict.msg, conflict.code)
			return
		}
		httputil.InternalError(c, "failed to write back metadata", err)
		return
	}
	writtenCount := res.written
	renamed := 0
	if res.renamed {
		renamed = 1
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

	doOrganize := req.Organize || req.Rename

	bookIDs := make([]string, len(req.BookIDs))
	copy(bookIDs, req.BookIDs)
	totalBooks := len(bookIDs)

	// Return the id EnqueueOp minted rather than minting a v1 row and handing
	// back its id. Operation logs are written under this same id by the Run
	// func, so a caller holding it can read both status and logs.
	params := batchSaveOpParams{
		BookIDs:  bookIDs,
		Organize: doOrganize,
		Force:    req.Force,
	}
	opID, enqErr := h.opRegistry.EnqueueOp(c.Request.Context(), "metadata.batch-save", params)
	if enqErr != nil {
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
			"max_length":  500,
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
