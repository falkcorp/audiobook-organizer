// file: internal/server/handlers/audiobooks/handler.go
// version: 1.41.3
// guid: 51fac747-9478-4075-8621-9da4bbdedc37
// last-edited: 2026-10-28

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
// interfaces (AudiobookService, AudiobookUpdater,
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
//   - getExternalIDStore — wraps asExternalIDStore(s.Ops()) (server adapter).
//   - publishEvent — wraps *Server.publishEvent (shared plugin event bus).
//
// As a result package audiobookshandler never imports package server.
//
// The store is a WIRE-TIME SNAPSHOT (assigned once in New, never swapped after
// that). The interface-typed service deps are also wire-time snapshots, each
// guarded against typed-nil boxing by the controller in wire_handlers.go.

package audiobookshandler

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	audiobookspkg "github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/cache"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/metrics"
	"github.com/falkcorp/audiobook-organizer/internal/plugin"
	"github.com/falkcorp/audiobook-organizer/internal/querygrammar"
	"github.com/falkcorp/audiobook-organizer/internal/search"
	"github.com/falkcorp/audiobook-organizer/internal/searchcache"
	"github.com/falkcorp/audiobook-organizer/internal/security/pathvalidation"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers/opaccess"
	servermiddleware "github.com/falkcorp/audiobook-organizer/internal/server/middleware"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary"
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

	// enqueuePurge queues the manual purge operation (SetPurgeEnqueuer).
	enqueuePurge PurgeEnqueuer

	metadataStateService MetadataStateService
	metadataFetchService MetadataFetchService
	batchService         BatchService
	changelogService     ChangelogService

	// Concrete caches (the cache exception): clean generic db-adjacent types
	// under heavy multi-method use, passed by pointer. The handlers nil-check
	// them exactly where the originals did.
	listCache *cache.Cache[gin.H]
	// listFlight collapses concurrent identical list-cache misses into one
	// build (ListAudiobooks), cancelled when its last waiter leaves
	// (list_flight.go). The zero value is ready to use.
	listFlight listFlight
	// searchCached reports whether a search request is served by the shared
	// search result cache (AudiobookService.SearchIsCached).
	searchCached func(search string, authorID, seriesID *int, f audiobookspkg.ListFilters) bool
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

	// getExternalIDStore wraps asExternalIDStore(s.Ops()) (the server adapter),
	// returning nil when the store does not implement external IDs.
	getExternalIDStore func() ExternalIDStore

	// publishEvent wraps *Server.publishEvent (the shared plugin event bus), used
	// by deleteAudiobook.
	publishEvent func(ctx context.Context, event plugin.Event)

	// repairsAccess decides which of a book's change-journal rows the caller
	// may read (GET /audiobooks/:id/changes and /changelog): a Repairs fixer's
	// rows, such as one user's listening state, need the fixer's permissions.
	// Unset (nil) refuses every row that could be Repairs data: see
	// WithRepairsOpAccess.
	repairsAccess *opaccess.Repairs

	// permCheck is the check for a permission the handler learns it needs only
	// after reading the request body (BatchOperations: library.delete when an
	// operation is a hard delete). nil means the strict check
	// (StrictPermissionCheck); the server installs its own with
	// SetPermissionCheck.
	permCheck PermissionCheck
	// permHolds answers, without writing a response, whether the caller holds a
	// permission (BatchOperations and BatchUpdateAudiobooks hand what the caller
	// may do to books' place in the library to the service, which judges each
	// row). nil means the strict answer (StrictPermissionHolds).
	permHolds PermissionHolds
}

// WithRepairsOpAccess installs the check that keeps a Repairs fixer's
// change-journal rows on a book (GET /audiobooks/:id/changes and
// /audiobooks/:id/changelog) to callers who hold the fixer's permissions
// (opaccess.Reader.MayReadChange). It is the same *opaccess.Repairs the
// operations handlers are given. Omitting it fails closed: every user-state
// row and every row naming a fixer source is left out. The server's wiring is
// pinned by TestWireHandlers_RepairsOpAccessIsInstalled.
func (h *Handler) WithRepairsOpAccess(a *opaccess.Repairs) *Handler {
	h.repairsAccess = a
	return h
}

// SetSearchResultCached tells the handler which search requests are served
// from the shared search result cache (see ListAudiobooks).
func (h *Handler) SetSearchResultCached(fn func(search string, authorID, seriesID *int, f audiobookspkg.ListFilters) bool) {
	h.searchCached = fn
}

func (h *Handler) searchServedByResultCache(search string, authorID, seriesID *int, f audiobookspkg.ListFilters) bool {
	return h.searchCached != nil && h.searchCached(search, authorID, seriesID, f)
}

// wantsAsyncSearch reports whether the request opted in to a 202 for a search
// that outlives the wait (RFC 7240 "Prefer: respond-async", sent by the web
// client's poller). Without it the request waits for its result as it always
// did: a client that cannot poll — an older bundle still open in a tab, a
// script — must never read a 202 body as an empty page.
func wantsAsyncSearch(c *gin.Context) bool {
	return hasPreference(c, "respond-async")
}

// wantsStaleSearch reports whether the request accepts a search list that
// may predate a bulk change while the cache rebuilds it ("Prefer:
// allow-stale"). The web client sends it only from its quick-search pickers;
// the Library list never does, because bulk actions run on its rows.
func wantsStaleSearch(c *gin.Context) bool {
	return hasPreference(c, "allow-stale")
}

// hasPreference reports whether any Prefer header carries token.
func hasPreference(c *gin.Context, token string) bool {
	for _, v := range c.Request.Header.Values("Prefer") {
		for part := range strings.SplitSeq(v, ",") {
			if strings.EqualFold(strings.TrimSpace(part), token) {
				return true
			}
		}
	}
	return false
}

// libraryGeneration resolves the store's book-mutation counter, which scopes
// every library-list cache key.
//
// Resolved at request time from h.store (the un-stripped store) rather than
// snapshotted, for the same reason the inline Unwrap assertions above are:
// a store can run behind a decorator (the server's was indexedStore until
// 07-S3), and a bare assertion against the wrapper misses. Resolution is two type assertions, which is
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

// bareParamAllowList names fields that are BOTH a filter field and a genuine
// bare query parameter of the library list, and so must never be rejected.
//
// The asymmetry that governs this file: omitting a name from the guard is
// harmless (the guard just does not fire, which is the pre-guard behavior).
// INCLUDING one wrongly rejects a request that used to work. So the allow-list
// is the safety valve, and a name belongs here the moment it has a real
// accessor on THIS endpoint.
//
// "library_state" is the known case: read at ParseQueryString(c,
// "library_state") in ListAudiobooks, and TestListBooksWithTagFilter asserts
// ?tag=…&library_state=organized narrows to one book. An earlier revision of
// the old hand-written set included it and broke that test. The near-miss is
// worth keeping on the record: the collision survey that produced that set
// grepped only c.Query("…") and so could not see the ParseQueryString form.
// Check every accessor spelling, not one, before removing a name from here.
//
// "fingerprint_status" is not in KnownFilterFields today, so the derivation
// below already excludes it. It is named anyway so the protection does not
// depend on that staying true — if it is ever added to the canonical field
// list, this endpoint would otherwise start rejecting a parameter it reads.
var bareParamAllowList = map[string]struct{}{
	"library_state":      {},
	"fingerprint_status": {},
}

// filterFieldQueryParams are names that mean something only INSIDE the
// `filters` JSON parameter of the library list.
//
// DERIVED, not hand-maintained. audiobooks.KnownFilterFields() is the single
// source of truth for what the list can filter on, and it is already pinned to
// the matcher by TestFilterFieldNames_MatchTheMatcher. A literal map here was a
// THIRD copy of that set — nothing held it against the first, so it drifted:
// 17 real filter fields (year, work_id, isbn13, marked_for_deletion, …) were
// missing, and ?year=2001 answered with all 63,870 books while looking exactly
// like a narrowed query.
//
// Deriving it means a field added to the canonical list is guarded here the
// same day, and the only judgment left is the allow-list above.
//
// Note "author" is a filter field while "author_id" is a genuine bare
// parameter — adjacent names, different things, and only exact-name lookup
// keeps them apart. That is why this is a set lookup and not a prefix match.
var filterFieldQueryParams = buildFilterFieldQueryParams()

func buildFilterFieldQueryParams() map[string]struct{} {
	out := make(map[string]struct{})
	for _, name := range audiobookspkg.KnownFilterFields() {
		if _, allowed := bareParamAllowList[name]; allowed {
			continue
		}
		out[name] = struct{}{}
	}
	return out
}

// firstBareFilterFieldParam reports the first filter-field name the request
// passed as a bare query parameter, if any. Sorted so the message is
// deterministic when a caller gets several wrong at once — an error that names
// a different field on each retry is its own small cruelty.
func firstBareFilterFieldParam(c *gin.Context) (string, bool) {
	found := make([]string, 0, 2)
	for name := range c.Request.URL.Query() {
		if _, ok := filterFieldQueryParams[name]; ok {
			found = append(found, name)
		}
	}
	if len(found) == 0 {
		return "", false
	}
	sort.Strings(found)
	return found[0], true
}

// idSetFrom turns a slice of book IDs into a set for
// ListFilters.RestrictToIDs. It ALWAYS returns a non-nil map, including for a
// nil or empty slice, because nil and empty mean opposite things to the store
// walkers: nil is "no ID restriction" (list everything) and empty is "no book
// is eligible" (list nothing). A capability-less store produces a nil slice
// here, and answering that with "list everything" would turn a narrowing
// request into a full-library response.
func idSetFrom(ids []string) map[string]struct{} {
	set := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		set[id] = struct{}{}
	}
	return set
}

// intersectIDSets ANDs two RestrictToIDs sets, preserving the nil/empty
// distinction: a nil operand means "no restriction" and yields the other set
// unchanged, while a non-nil empty operand correctly collapses the result to
// empty rather than being treated as absent.
func intersectIDSets(a, b map[string]struct{}) map[string]struct{} {
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}
	out := make(map[string]struct{}, len(a))
	for id := range a {
		if _, ok := b[id]; ok {
			out[id] = struct{}{}
		}
	}
	return out
}

// sortByMetricLabel bounds a client-supplied sort_by to a metric label
// (TASK-095). Every field SortBooks understands keeps its own name, an omitted
// sort_by is "default", and anything else collapses to "other" -- the label
// set is fixed by database.SortableBookFields plus the service-side
// audiobookspkg.SortBySeriesPosition, never by what a client types.
//
// audiobookspkg.CanSortBy (CanSortBooksBy plus series_position), not
// CanPushDownSort: the question this metric answers is
// which fields to ADD to enabled_sort_indexes, and CanPushDownSort only admits
// fields already in that set (empty by default), so bucketing by it would
// report every request as "other" and answer nothing.
func sortByMetricLabel(sortBy string) string {
	switch {
	case sortBy == "":
		return "default"
	case audiobookspkg.CanSortBy(sortBy):
		return sortBy
	default:
		return "other"
	}
}

// maxListIDs caps the ids= parameter of the library list.
const maxListIDs = 500

// searchNamesUnindexedFilterField reports the first field in a search query
// that the library filter layer knows but the search index cannot answer.
// A query that does not parse is left to the search path's own fallback.
func searchNamesUnindexedFilterField(q string) (string, bool) {
	if strings.TrimSpace(q) == "" || !strings.Contains(q, ":") {
		return "", false
	}
	ast, err := search.ParseQuery(q)
	if err != nil {
		return "", false
	}
	for _, f := range search.FieldNames(ast) {
		if audiobookspkg.FieldIsKnown(f) && !search.IsIndexedField(f) {
			return f, true
		}
	}
	return "", false
}

// listRequest is one GET /audiobooks request parsed into the arguments of the
// list pipeline. Built ONLY by parseListRequest, which both ListAudiobooks and
// AudiobookFacets (scoped=1) call, so the tag facets of a request are
// evaluated over exactly the books the list returns for it.
type listRequest struct {
	params   httputil.PaginationParams
	authorID *int
	seriesID *int
	filters  audiobookspkg.ListFilters
}

// parseListRequest validates and parses the list query parameters. On a bad
// request it has already written the 4xx/5xx response and returns false.
func (h *Handler) parseListRequest(c *gin.Context) (listRequest, bool) {
	store := h.store

	// A field name passed as a bare query parameter (?title=Skills) is not a
	// filter — field filters travel inside the `filters` JSON parameter. gin
	// silently ignores unrecognized parameters, so such a request returned the
	// ENTIRE library while looking exactly like a narrowed query, count and
	// all.
	//
	// Measured on production 2026-08-13: ?title=Skills answered count=63870
	// with non-matching rows, while
	// filters=[{"field":"title","value":"Skills"}] correctly answered 25. That
	// 63,870 was read as evidence of a storage-layer filtering bug and written
	// into a handoff as a root cause. It was not one. The cost of this silence
	// was a misdiagnosis, not a user-facing outage — the web UI sends the
	// `filters` form.
	//
	// Same reasoning as the empty-filter-value rejection below: answering a
	// request that meant to narrow with the whole library is the worst
	// available response, so say so instead.
	if field, bare := firstBareFilterFieldParam(c); bare {
		httputil.RespondWithBadRequest(c,
			"\""+field+"\" is not a query parameter; it is a filter field. A bare "+
				"\""+field+"\" is ignored, which would list the entire library rather than "+
				"narrowing it. Pass it inside the filters parameter instead, e.g. "+
				"filters=[{\"field\":\""+field+"\",\"value\":\"...\"}].")
		return listRequest{}, false
	}

	// Parse pagination parameters
	params := httputil.ParsePaginationParams(c)

	// A library filter field inside `search` that the search index does not
	// hold (review:, metadata:, has_duration:, has_written:, ...) is not a
	// filter there: the term matches no document, so `-review:matched` became
	// NOT(nothing) = the whole library -- measured on prod 2026-09-27 as
	// count 100,856 for search=-review:matched. Those fields are evaluated by
	// the `filters` parameter (the web search bar sends them there), so say so
	// rather than answer a narrowing request with everything.
	if field, bad := searchNamesUnindexedFilterField(params.Search); bad {
		httputil.RespondWithBadRequest(c,
			"\""+field+"\" is a library filter field that the search index does not hold, so "+
				"inside the search parameter it would match nothing (and, negated, everything). "+
				"Pass it in the filters parameter instead, e.g. "+
				"filters=[{\"field\":\""+field+"\",\"value\":\"...\"}].")
		return listRequest{}, false
	}
	if err := audiobookspkg.SearchPerUserProblem(params.Search); err != nil {
		httputil.RespondWithBadRequest(c, "invalid search: "+search.QueryTermReason(err))
		return listRequest{}, false
	}
	authorID := httputil.ParseQueryIntPtr(c, "author_id")
	seriesID := httputil.ParseQueryIntPtr(c, "series_id")

	// has_file_errors and the quick-query params below both narrow the library to
	// a set of book IDs. Each used to answer the whole request from that slice
	// alone -- hand-paginating it and reporting its length as the count -- which
	// silently discarded the search, the filters JSON, author_id, series_id and
	// the sort that arrived on the SAME request.
	//
	// The UI reaches that combination on its own: the Dashboard links to
	// /library?has_file_errors=true and the library query hook sends the user's
	// active filters alongside it. The response was a 200 with plausible rows,
	// the filter chips still lit above them, and a total belonging to a
	// different query -- nothing surfaced that the narrowing had been dropped.
	//
	// Both now feed ListFilters.RestrictToIDs into the normal filtered pipeline,
	// which ANDs the ID set with every other predicate and derives the count
	// from the same filter. The store walkers short-circuit on the set
	// (memdb_summaries.go, pebble_store.go), so this is not a return to
	// scanning the library.
	var restrictIDs map[string]struct{}

	if c.Query("has_file_errors") == "true" {
		if store == nil {
			httputil.RespondWithInternalError(c, "database not initialized")
			return listRequest{}, false
		}
		var bookIDs []string
		// Resolved through any decorator chain -- the search-index wrapper hides
		// this capability from a bare assertion.
		if lf, ok := database.AsCapability[interface {
			ListBooksWithFileErrors() ([]string, error)
		}](store); ok {
			ids, err := lf.ListBooksWithFileErrors()
			if err != nil {
				httputil.InternalError(c, "failed to list books with file errors", err)
				return listRequest{}, false
			}
			bookIDs = ids
		}
		// A store without the capability yields a non-nil EMPTY set, which the
		// walkers read as "no book is eligible" -- the same empty answer this
		// path gave before, now with a count that agrees with it. It must not
		// be left nil: nil means "no restriction" and would list the whole
		// library for a request that asked to narrow it.
		restrictIDs = idSetFrom(bookIDs)
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
		if qqStore, ok := database.AsCapability[interface {
			GetAllBookIDsForQuickQuery(id string) ([]string, error)
		}](store); ok {
			ids, err := qqStore.GetAllBookIDsForQuickQuery(quickQueryID)
			if err != nil {
				httputil.InternalError(c, "failed to list books for quick query", err)
				return listRequest{}, false
			}
			bookIDs = ids
		}
		// Intersected, not assigned: has_file_errors and a quick-query param can
		// arrive on the same request, and the old code answered whichever it
		// tested first while ignoring the other. Both are restrictions, so both
		// have to apply.
		restrictIDs = intersectIDSets(restrictIDs, idSetFrom(bookIDs))
	}

	// ids=a,b,c narrows the list to those books — the Library's live-update
	// path refetches just the rows a books.changed event named instead of
	// reloading the page. It is a restriction like the ones above, ANDed with
	// everything else on the request, and capped so a client cannot turn it
	// into an unbounded fan-out.
	if raw := strings.TrimSpace(c.Query("ids")); raw != "" {
		var ids []string
		for id := range strings.SplitSeq(raw, ",") {
			if id = strings.TrimSpace(id); id != "" {
				ids = append(ids, id)
			}
		}
		if len(ids) > maxListIDs {
			httputil.RespondWithBadRequest(c, "ids: at most "+strconv.Itoa(maxListIDs)+" book ids per request")
			return listRequest{}, false
		}
		restrictIDs = intersectIDSets(restrictIDs, idSetFrom(ids))
	}

	// Parse optional filters
	sortBy := httputil.ParseQueryString(c, "sort_by")
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
		SortBy:             sortBy,
		SortOrder:          sortOrder,
		FingerprintStatus:  httputil.ParseQueryString(c, "fingerprint_status"),
		CoveragePercentMin: coveragePercentMin,
		CoveragePercentMax: coveragePercentMax,
		RestrictToIDs:      restrictIDs,
	}

	// Parse field filters from JSON query param. Per-user filters
	// (read_status / progress_pct / last_played) are split off so the
	// service can apply them via UserBookState lookups; book-global
	// filters stay on the original FieldFilters slice.
	if filtersJSON := c.Query("filters"); filtersJSON != "" {
		var fieldFilters []audiobookspkg.FieldFilter
		if err := json.Unmarshal([]byte(filtersJSON), &fieldFilters); err != nil {
			httputil.RespondWithBadRequest(c, "invalid filters parameter: "+err.Error())
			return listRequest{}, false
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
			return listRequest{}, false
		}
		// Reject unknown field names for the mirror-image reason. An unknown
		// field is not ignored — it matches NOTHING, so the endpoint answers
		// count:0, and "0" is indistinguishable from a truthful "no books
		// match". Measured on production 2026-08-14: the nonsense field
		// zzz_not_a_real_field returned the same count:0 as marked_for_deletion,
		// for which 3,953 books qualified, and as year / duration / isbn13 and
		// ten more names the search bar offers but this backend had never
		// implemented. Naming the field and listing the alternatives is the only
		// answer that cannot be mistaken for a fact about the library.
		if field, unknown := audiobookspkg.FirstUnknownFilterField(fieldFilters); unknown {
			httputil.RespondWithBadRequest(c,
				"filter on \""+querygrammar.ShortToken(field)+"\" names a field the library list cannot filter on; "+
					"filtering on it would match no books and report a count of 0, which reads "+
					"as \"none exist\". Valid fields: "+
					strings.Join(audiobookspkg.KnownFilterFields(), ", ")+".")
			return listRequest{}, false
		}
		// Bound the set as a whole, as the Review query bounds its q. This runs
		// BEFORE the value check below so that no value is parsed, and no error
		// message built from one, until the set is known to be small: an error
		// that quotes a value must not be able to quote a pasted wall of them.
		if err := audiobookspkg.CheckFilterSetSize(fieldFilters); err != nil {
			httputil.RespondWithBadRequest(c, "invalid filters parameter: "+err.Error())
			return listRequest{}, false
		}
		// Reject values the matcher cannot evaluate (duration:>abc,
		// has_duration:maybe, metadata:whatever) for the same reason: a
		// value that parses to nothing matches nothing and reads as "0 books".
		if err := audiobookspkg.FirstInvalidFilterValue(fieldFilters); err != nil {
			httputil.RespondWithBadRequest(c, "invalid filter value: "+err.Error())
			return listRequest{}, false
		}
		for _, ff := range fieldFilters {
			if audiobookspkg.IsPerUserField(ff.Field) {
				filters.PerUserFilters = append(filters.PerUserFilters, ff)
			} else {
				filters.FieldFilters = append(filters.FieldFilters, ff)
			}
		}
	}

	// applied_filters echoes, from the fully-populated filters value above,
	// exactly what the server is about to apply — not what the caller sent.
	// It is built here (before UserID is attached) purely because filters is
	// complete at this point; UserID itself is never echoed since it isn't a
	// filter value a chip would render.
	//
	// The sort is reported as the service will run it, not as requested:
	// series_position without an author_id or series_id is dropped (see
	// audiobookspkg.ScopedSort), and its absence from applied_filters is how
	// the caller learns that. The sort_by metric above still counts the
	// request as series_position on purpose, because it records what clients
	// ask for.
	filters.SortBy, filters.SortOrder = audiobookspkg.ScopedSort(filters.SortBy, filters.SortOrder, authorID, seriesID)

	// Resolve caller for per-user filters; anon callers just don't
	// get per-user filtering applied (filters.UserID stays "" and
	// the service skips that pass).
	if caller, ok := servermiddleware.CurrentUser(c); ok && caller != nil {
		filters.UserID = caller.ID
	}

	return listRequest{params: params, authorID: authorID, seriesID: seriesID, filters: filters}, true
}

// ListAudiobooks handles GET /audiobooks. Mirrors the original listAudiobooks:
// has_file_errors fast-path, quick-query (missing_covers / in_import_path /
// no_isbn / duplicates_flagged) fast-path, then the filtered list pipeline with
// the list cache (skipped when per-user filters are active).
func (h *Handler) ListAudiobooks(c *gin.Context) {
	metrics.IncSortByRequested(sortByMetricLabel(httputil.ParseQueryString(c, "sort_by")))
	req, ok := h.parseListRequest(c)
	if !ok {
		return
	}
	params, authorID, seriesID, filters := req.params, req.authorID, req.seriesID, req.filters
	// applied_filters echoes, from the fully-populated filters value, exactly
	// what the server is about to apply (see parseListRequest).
	appliedFilters := buildAppliedFilters(filters)

	// Cache key from the full query string. Skip the cache when
	// per-user filters are active because the cache key doesn't
	// encode userID — a hit could leak User A's filtered list
	// to User B.
	cacheKey := h.libraryGeneration().Key("list:", c.Request.URL.RawQuery)
	// The response cache is keyed on the library generation, which author and
	// series renames, tag writes and the store's inner aggregate writes do not
	// advance. A search is instead served from the search result cache, which
	// is invalidated by all of them, so a search page never goes through this
	// cache while that one is on. A per-user DSL query (read_status: and
	// friends) is never cached here either: the key does not carry the user. Nor
	// is a relative date query (last_played:>-30d, or a date_added:>-1y filter):
	// it depends on the clock.
	// An ident: filter is never cached here either: identification states
	// move on metadata_cache writes, which do not move the library generation.
	useListCache := len(filters.PerUserFilters) == 0 &&
		!audiobookspkg.HasIdentFilter(filters.FieldFilters) &&
		!audiobookspkg.HasRelativeDateFilter(filters.FieldFilters) &&
		!(params.Search != "" && (h.searchServedByResultCache(params.Search, authorID, seriesID, filters) || audiobookspkg.SearchHasPerUserFilters(params.Search) || audiobookspkg.SearchHasRelativeDate(params.Search)))
	if useListCache {
		if cached, ok := h.listCache.Get(cacheKey); ok {
			httputil.RespondWithOK(c, cached)
			return
		}
	}

	showQuarantined := c.Query("show_quarantined") == "true"
	// A request that asked for it (Prefer: respond-async) can be answered
	// with 202 while a long search runs; one that also asked for it
	// (Prefer: allow-stale) with a list flagged stale while a rebuild runs.
	// Every other request gets a current, complete result.
	reqCtx := c.Request.Context()
	if wantsAsyncSearch(c) {
		reqCtx = audiobookspkg.WithPendingSearchResponse(reqCtx)
	}
	if wantsStaleSearch(c) {
		reqCtx = audiobookspkg.WithStaleSearchResponse(reqCtx)
	}
	// Identical cache misses share ONE build (listFlight). After every prod
	// restart the list cache and memdb are cold, and the Library's retry loop
	// re-issues the same query: 14 identical requests landed in one second on
	// 2026-10-06, each building the same response (44-160 s apiece) and
	// competing for the same CPU. Only a cacheable request without the
	// async/stale preferences joins -- those change the response shape. The
	// shared build is cancelled when the last caller waiting on it leaves, as
	// an unshared build was by its one caller's disconnect.
	//
	// applied_filters is set and the cache filled inside the shared build:
	// every joined caller receives the same map, and nothing writes it after
	// it leaves there (cache hits share it the same way). An error reaches
	// only the callers that were waiting and is never cached.
	var resp gin.H
	var err error
	if useListCache && !wantsAsyncSearch(c) && !wantsStaleSearch(c) {
		resp, err = h.listFlight.do(reqCtx, cacheKey, func(ctx context.Context) (gin.H, error) {
			r, berr := h.buildListResponse(ctx, params.Limit, params.Offset, params.Search, authorID, seriesID, filters, showQuarantined)
			if berr != nil {
				return nil, berr
			}
			r["applied_filters"] = appliedFilters
			h.listCache.Set(cacheKey, r)
			return r, nil
		})
		if err != nil && reqCtx.Err() != nil {
			// This caller left before the build finished. Not a server fault,
			// and never a 200 with an empty body (see statusClientClosedRequest).
			c.AbortWithStatus(statusClientClosedRequest)
			return
		}
	} else {
		resp, err = h.buildListResponse(reqCtx, params.Limit, params.Offset, params.Search, authorID, seriesID, filters, showQuarantined)
		if err == nil {
			// Additive: applied_filters never replaces or renames an existing
			// key (items/count/limit/offset are untouched) and never changes
			// which books are returned — it only reports, from ground truth,
			// what was applied. Always present, even when empty, so the
			// frontend can tell "no filters were applied" apart from "the
			// server didn't say."
			resp["applied_filters"] = appliedFilters
			if useListCache {
				h.listCache.Set(cacheKey, resp)
			}
		}
	}
	// Both paths answer errors the same way. A *searchcache.PendingError
	// needs the request to have opted in (Prefer: respond-async), which keeps
	// it off the shared path today; it is still handled here for both, so the
	// shared path can never turn a pending search into a 500.
	if pending, ok := errors.AsType[*searchcache.PendingError](err); ok {
		// The search is still running (detached from this request) and will
		// land in the search result cache. The client polls
		// GET /api/v1/search/:search_id and re-issues this request when done.
		c.JSON(http.StatusAccepted, gin.H{
			"search_id":      pending.SearchID,
			"status":         "running",
			"matches_so_far": pending.MatchesSoFar,
		})
		return
	}
	if respondIdentIndexBuilding(c, err) || respondSearchLimit(c, err) {
		return
	}
	if err != nil {
		httputil.InternalError(c, "failed to list audiobooks", err)
		return
	}
	httputil.RespondWithOK(c, resp)
}

// statusClientClosedRequest is nginx's 499: the client went away before the
// response was written. Not in net/http, so it is spelled out here.
const statusClientClosedRequest = 499

// buildAppliedFilters reports, from the fully-populated ListFilters the
// request is about to be run against, every filter the server actually
// applied — as opposed to what the caller sent (a rejected/unknown field
// never reaches here; see FirstEmptyFilterValue / FirstUnknownFilterField
// above) or what the frontend assumes it sent. Each entry is a {field,
// value} pair so the response shape matches the filters= JSON param the UI
// already understands.
//
// Always returns a non-nil, possibly-empty slice: a request with no filters
// must still echo applied_filters as [] rather than omit the key, so the
// frontend can render "no active filters" from ground truth instead of
// treating a missing key and an empty list the same way.
func buildAppliedFilters(filters audiobookspkg.ListFilters) []map[string]string {
	applied := make([]map[string]string, 0, len(filters.FieldFilters)+len(filters.PerUserFilters)+len(filters.Tags)+6)
	for _, ff := range filters.FieldFilters {
		applied = append(applied, map[string]string{"field": ff.Field, "value": ff.Value})
	}
	// PerUserFilters (read_status / progress_pct / last_played) are also
	// something the server actually applied, not just something it parsed.
	for _, ff := range filters.PerUserFilters {
		applied = append(applied, map[string]string{"field": ff.Field, "value": ff.Value})
	}
	if filters.LibraryState != "" {
		applied = append(applied, map[string]string{"field": "library_state", "value": filters.LibraryState})
	}
	if filters.Tag != "" {
		applied = append(applied, map[string]string{"field": "tag", "value": filters.Tag})
	}
	for _, tag := range filters.Tags {
		applied = append(applied, map[string]string{"field": "tags", "value": tag})
	}
	if filters.FingerprintStatus != "" {
		applied = append(applied, map[string]string{"field": "fingerprint_status", "value": filters.FingerprintStatus})
	}
	if filters.CoveragePercentMin != nil {
		applied = append(applied, map[string]string{"field": "coverage_percent_min", "value": strconv.Itoa(*filters.CoveragePercentMin)})
	}
	if filters.CoveragePercentMax != nil {
		applied = append(applied, map[string]string{"field": "coverage_percent_max", "value": strconv.Itoa(*filters.CoveragePercentMax)})
	}
	if filters.SortBy != "" {
		applied = append(applied, map[string]string{"field": "sort_by", "value": filters.SortBy})
	}
	if filters.SortOrder != "" {
		applied = append(applied, map[string]string{"field": "sort_order", "value": filters.SortOrder})
	}
	return applied
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

	// Exact total for pagination. This replaced a fetch-of-10,000-and-len()
	// whose count silently saturated at 10,000 AND whose error was discarded —
	// a failed count reported total 0, indistinguishable from an empty trash.
	total, err := h.audiobookService.CountSoftDeletedBooks(c.Request.Context(), olderThanDays)
	if err != nil {
		httputil.InternalError(c, "failed to count deleted audiobooks", err)
		return
	}

	// Files an earlier purge pass decided to remove and did not finish. They are
	// the purge dialog's work even when the trash is empty (its "Finish removing"
	// button), so the toolbar needs the count apart from the trash's. A failed
	// read is null, not 0: unknown must not read as "nothing waiting", and the
	// listing is not worth failing for it.
	//
	// The records that wait for a folder that is missing or empty are counted
	// apart (files_waiting_for_folder): a purge does not remove them and they
	// are not "finishable", so the dialog words and offers them differently.
	var pendingFiles, waitingFiles any
	if counts, perr := h.audiobookService.PendingFileRemovals(); perr != nil {
		trashListLog.Warn("trash listing: cannot count the file removals an earlier purge left pending: %s", logger.SanitizeLogValue(perr.Error()))
	} else {
		pendingFiles = counts.Pending
		waitingFiles = counts.WaitingForFolder
	}

	httputil.RespondWithOK(c, gin.H{
		"items":                           h.withTrashProgress(c, books),
		"count":                           len(books),
		"total":                           total,
		"limit":                           params.Limit,
		"offset":                          params.Offset,
		"files_pending_from_earlier_pass": pendingFiles,
		"files_waiting_for_folder":        waitingFiles,
		// How many of the trash a purge would remove right now versus how many
		// are blocked, by reason, as the last purge pass or preview measured
		// it (null before one has run). The listing never recomputes it.
		"purge_breakdown": h.audiobookService.LastPurgeBreakdown(),
	})
}

var trashListLog = logger.New("audiobooks.trash")

// trashListItem is one trash row: the book plus whether a user still has
// listening state on it (the state the nightly purge keeps it for when no
// live version of the book exists to carry it to).
type trashListItem struct {
	database.Book
	audiobookspkg.TrashProgressInfo
}

// trashProgressViewer is who is asking for a trash listing's progress, and
// whether they may see every user's: only a user who may manage users
// (auth.PermUsersManage) sees other users' names and progress; everyone
// else sees their own line and a count of the others.
func trashProgressViewer(c *gin.Context) (viewerID string, seeAll bool) {
	if u, ok := servermiddleware.CurrentUser(c); ok && u != nil {
		viewerID = u.ID
	}
	return viewerID, auth.Can(c.Request.Context(), auth.PermUsersManage)
}

// withTrashProgress attaches has_progress / progress_summary to each trash
// row, narrowed to what the viewer may see (TrashProgressInfo.ForViewer).
// A failure to read the state marks every row progress_unknown rather than
// failing the listing (or, worse, reading as "no progress").
func (h *Handler) withTrashProgress(c *gin.Context, books []database.Book) []trashListItem {
	viewerID, seeAll := trashProgressViewer(c)
	ids := make([]string, len(books))
	for i := range books {
		ids[i] = books[i].ID
	}
	info, err := h.audiobookService.TrashProgress(c.Request.Context(), ids)
	if err != nil {
		trashListLog.Warn("trash listing: cannot read listening state: %s", logger.SanitizeLogValue(err.Error()))
	}
	items := make([]trashListItem, len(books))
	for i := range books {
		p, ok := info[books[i].ID]
		if !ok {
			p = audiobookspkg.TrashProgressInfo{Unknown: true}
		}
		items[i] = trashListItem{Book: books[i], TrashProgressInfo: p.ForViewer(viewerID, seeAll)}
	}
	return items
}

// DiscardProgressAndPurge handles POST /audiobooks/:id/discard-progress-and-purge:
// the owner's explicit choice to drop every user's listening state on one
// book in the trash and purge it. 404 for an unknown book, 409 for a book
// that is not in the trash or still owns book_file rows (nothing changed),
// 503 when there is no activity log to record it in. A delete that fails
// after the progress was erased (a file row a non-merge writer added in the
// instant after the last check) answers PROGRESS_DISCARDED_BOOK_KEPT, 409
// when the cause was file rows and 500 otherwise: the progress is gone, so it
// is never reported as "nothing changed".
func (h *Handler) DiscardProgressAndPurge(c *gin.Context) {
	id := c.Param("id")
	actor := "unknown"
	if u, ok := servermiddleware.CurrentUser(c); ok && u != nil {
		actor = u.Username + " (" + u.ID + ")"
	}
	res, err := h.audiobookService.DiscardProgressAndPurge(c.Request.Context(), id, actor)
	switch {
	case err == nil:
		viewerID, seeAll := trashProgressViewer(c)
		httputil.RespondWithOK(c, res.ForViewer(viewerID, seeAll))
	case errors.Is(err, audiobookspkg.ErrAudiobookNotFound):
		httputil.RespondWithNotFound(c, "audiobook", id)
	case errors.Is(err, merge.ErrDiscardedBookKept):
		status := http.StatusInternalServerError
		if errors.Is(err, database.ErrBookOwnsFiles) {
			status = http.StatusConflict
		}
		httputil.RespondWithError(c, status, err.Error(), CodeProgressDiscardedBookKept)
	case errors.Is(err, database.ErrBookOwnsFiles):
		httputil.RespondWithError(c, http.StatusConflict, err.Error(), ownsFilesCode(err))
	case errors.Is(err, database.ErrBookIsRetiredShell):
		httputil.RespondWithError(c, http.StatusConflict, err.Error(), CodeRetiredShell)
	case errors.Is(err, audiobookspkg.ErrPurgeUndoPossible):
		httputil.RespondWithError(c, http.StatusConflict, err.Error(), CodeUndoPossible)
	case errors.Is(err, merge.ErrMergeBusy):
		httputil.RespondWithError(c, http.StatusConflict, err.Error(), CodeMergeBusy)
	case errors.Is(err, audiobookspkg.ErrNotInTrash), errors.Is(err, audiobookspkg.ErrDiscardRefused):
		httputil.RespondWithConflict(c, err.Error())
	case errors.Is(err, audiobookspkg.ErrAuditUnavailable):
		httputil.RespondWithServiceUnavailable(c, err.Error())
	default:
		httputil.InternalError(c, "failed to discard progress and purge", err)
	}
}

// CodePurgeBusy: 409, a purge or replay is removing files and holds the lock the
// forget action needs; nothing was changed. Try again when it has finished.
const CodePurgeBusy = "PURGE_BUSY"

// CodeRootNotReady: 503, the library root is not mounted (or not answering), so
// a missing file cannot be told from a missing mount; nothing was changed.
const CodeRootNotReady = "ROOT_NOT_READY"

// ListWaitingPurgeFileRecords handles
// GET /audiobooks/purge-soft-deleted/waiting-file-records: the files whose
// removal waits for a folder that is missing or empty, at most the number one
// forget may take, with the true total. The dialog shows them before the owner
// confirms, and sends exactly that list back. Read-only.
func (h *Handler) ListWaitingPurgeFileRecords(c *gin.Context) {
	res, err := h.audiobookService.WaitingFileRecordPaths()
	if err != nil {
		httputil.InternalError(c, "failed to list the waiting file records", err)
		return
	}
	httputil.RespondWithOK(c, res)
}

// forgetWaitingRequest is the body of the forget action: the paths the dialog
// listed. Only those are forgotten.
type forgetWaitingRequest struct {
	Paths []string `json:"paths"`
}

// ForgetWaitingPurgeFileRecords handles
// POST /audiobooks/purge-soft-deleted/forget-waiting-file-records: the owner's
// explicit choice to delete the journal records of the file removals the dialog
// listed, which wait for a folder that is missing or empty (the folder is gone
// for good). The body names those paths ({"paths": [...]}); a path that is no
// longer waiting, or whose folder holds files again, is skipped and reported.
// It deletes records only, never a file or a book, and writes an audit row
// naming the actor and every forgotten path. 400 without paths or with too many,
// 409 PURGE_BUSY while a purge is removing files, 503 ROOT_NOT_READY while the
// library root is not ready, 503 when there is no activity log to record it in
// (nothing deleted in any of these).
func (h *Handler) ForgetWaitingPurgeFileRecords(c *gin.Context) {
	var req forgetWaitingRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httputil.RespondWithBadRequest(c, "the body must be {\"paths\": [...]}, the files the dialog listed: "+err.Error())
		return
	}
	actor := "unknown"
	if u, ok := servermiddleware.CurrentUser(c); ok && u != nil {
		actor = u.Username + " (" + u.ID + ")"
	}
	res, err := h.audiobookService.ForgetWaitingFileRecords(c.Request.Context(), actor, req.Paths)
	switch {
	case err == nil:
		httputil.RespondWithOK(c, res)
	case errors.Is(err, audiobookspkg.ErrForgetNothingShown), errors.Is(err, audiobookspkg.ErrForgetTooMany):
		httputil.RespondWithBadRequest(c, err.Error())
	case errors.Is(err, audiobookspkg.ErrPurgeBusy):
		httputil.RespondWithError(c, http.StatusConflict, err.Error(), CodePurgeBusy)
	case errors.Is(err, audiobookspkg.ErrLibraryRootNotReady):
		httputil.RespondWithError(c, http.StatusServiceUnavailable, err.Error(), CodeRootNotReady)
	case errors.Is(err, audiobookspkg.ErrAuditUnavailable):
		httputil.RespondWithServiceUnavailable(c, err.Error())
	default:
		httputil.InternalError(c, "failed to forget the waiting file records", err)
	}
}

// PurgeEnqueuer queues the manual purge operation and returns its id. The
// server wires it to the operations registry (SetPurgeEnqueuer). actorUserID is
// the signed-in user who asked ("" when authentication is off): it is stored on
// the operation row, which is what lets that user cancel their own preview
// (DELETE /audiobooks/purge-soft-deleted/preview/:id) without settings.manage.
type PurgeEnqueuer func(ctx context.Context, actorUserID string, params any) (opID string, err error)

// SetPurgeEnqueuer wires the manual purge operation's enqueue. Without it the
// purge endpoints answer 500, rather than purging inside the request.
func (h *Handler) SetPurgeEnqueuer(fn PurgeEnqueuer) { h.enqueuePurge = fn }

// purgeOpParams is the body of the library.purge-soft-deleted op (the server's
// PurgeSoftDeletedOpParams). The mode is always sent: DryRun is never omitted,
// so neither endpoint depends on the op's omitted-means-preview default.
type purgeOpParams struct {
	DeleteFiles   bool  `json:"delete_files,omitempty"`
	OlderThanDays *int  `json:"older_than_days,omitempty"`
	DryRun        *bool `json:"dry_run"`
	// TrashedBefore is the instant the preview returned (RFC 3339): the purge
	// takes no book trashed after it. Sent only by DELETE.
	TrashedBefore *time.Time `json:"trashed_before,omitempty"`
}

// PurgeSoftDeletedAudiobooks handles DELETE /audiobooks/purge-soft-deleted.
//
// It queues the library.purge-soft-deleted operation and answers 202 with its
// id; the purge runs there, with progress, and its result (what was purged and
// what was kept, by reason) is the operation's result. It used to run inside
// the request: 72 s on 2026-10-21, no progress, no log line, and a response
// whose breakdown the UI discarded. WHICH books are purgeable is unchanged.
//
// The request must carry trashed_before, the instant the preview returned: the
// purge is bound to the books that preview showed and never takes one trashed
// after it (the trash keeps changing while the dialog is open, and a purge has
// no rollback). Without it the request is a 400 that says to preview first.
func (h *Handler) PurgeSoftDeletedAudiobooks(c *gin.Context) {
	h.enqueuePurgeOp(c, false)
}

// PreviewPurgeSoftDeleted handles POST /audiobooks/purge-soft-deleted/preview:
// the same operation as a dry run. It classifies every trashed book exactly as
// the purge would and changes nothing, so the dialog can say what a purge will
// do before it is clicked.
func (h *Handler) PreviewPurgeSoftDeleted(c *gin.Context) {
	h.enqueuePurgeOp(c, true)
}

func (h *Handler) enqueuePurgeOp(c *gin.Context, dryRun bool) {
	if h.enqueuePurge == nil {
		httputil.RespondWithInternalError(c, "operation registry not initialized")
		return
	}
	var olderThanDays *int
	if raw := c.Query("older_than_days"); raw != "" {
		if days, err := strconv.Atoi(raw); err == nil && days > 0 {
			olderThanDays = &days
		}
	}
	// trashed_before binds a real purge to the preview before it. Parsed, not
	// passed through, so a malformed value is a 400 here rather than an op that
	// fails to decode its params after it was accepted.
	var trashedBefore *time.Time
	if raw := c.Query("trashed_before"); raw != "" {
		at, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			httputil.RespondWithBadRequest(c, "trashed_before must be the RFC 3339 instant the preview returned: "+err.Error())
			return
		}
		trashedBefore = &at
	}
	if !dryRun && trashedBefore == nil {
		httputil.RespondWithBadRequest(c, "trashed_before is required: preview the purge first and send the trashed_before it returns, so the purge takes only the books the preview showed")
		return
	}
	params := purgeOpParams{
		DeleteFiles:   c.Query("delete_files") == "true",
		OlderThanDays: olderThanDays,
		DryRun:        &dryRun,
	}
	if !dryRun {
		params.TrashedBefore = trashedBefore
	}
	actorUserID := ""
	if u, ok := servermiddleware.CurrentUser(c); ok && u != nil {
		actorUserID = u.ID
	}
	opID, err := h.enqueuePurge(c.Request.Context(), actorUserID, params)
	if err != nil {
		httputil.InternalError(c, "failed to enqueue the purge operation", err)
		return
	}
	httputil.RespondWithSuccess(c, http.StatusAccepted, gin.H{
		"id":      opID,
		"type":    "purge-soft-deleted",
		"status":  "queued",
		"dry_run": dryRun,
	})
}

// ReconcileAudiobookFiles re-stats the book's files on disk and updates the
// FileSize fields in the DB to match physical reality.
// POST /audiobooks/:id/reconcile-files.
//
// Renamed from RescanAudiobook on 2026-08-24. The old name promised a rescan it
// never performed: it stats files and corrects sizes, but nothing is re-read,
// no tags are parsed and no hash is recomputed. For an actual forced re-read of
// a book see ForceRescanAudiobook. The old POST /audiobooks/:id/rescan alias was
// retired behind a 410 stub (01-P81d); this handler serves reconcile-files only.
func (h *Handler) ReconcileAudiobookFiles(c *gin.Context) {
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
		// ModifyBook sets FileSize alone on the stored row. `book` was read
		// before the file stats above, so writing it back whole would revert
		// any column another writer committed meanwhile; the callback
		// re-checks the fresh row so an equal size is not rewritten.
		updated, upErr := store.ModifyBook(id, func(b *database.Book) error {
			if b.FileSize != nil && *b.FileSize == newTotal {
				return database.ErrSkipBookWrite
			}
			b.FileSize = &newTotal
			return nil
		})
		if upErr != nil {
			httputil.InternalError(c, "failed to update book", upErr)
			return
		}
		if updated == nil {
			httputil.RespondWithNotFound(c, "audiobook", id)
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

// ForceRescanAudiobook flags one book for a full re-read by the next scan.
// POST /audiobooks/:id/force-rescan.
//
// This sets NeedsRescan, which classifySkipFile treats as "never skip" (it is
// checked before the rescan-age gate, so a forced rescan is never deferred), and
// GetDirtyBookFolders then pulls the book's immediate parent directory into the
// next scan even when that directory sits outside the configured import paths.
// Unchanged siblings in that directory are still skipped, so the cost is one
// directory walk plus this one book.
//
// It deliberately does NOT trigger a scan. The flag is durable and the next
// scheduled incremental picks it up; triggering here would queue behind any
// running library.scan anyway, because they share ConcurrencyKey "library.scan".
func (h *Handler) ForceRescanAudiobook(c *gin.Context) {
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
	if markErr := store.MarkNeedsRescan(id); markErr != nil {
		// Never swallow this. A silent failure here looks identical to success
		// from the caller's side and the book would simply never be re-read.
		httputil.InternalError(c, "failed to flag audiobook for rescan", markErr)
		return
	}
	httputil.RespondWithOK(c, gin.H{
		"book_id":      id,
		"needs_rescan": true,
		"message":      "audiobook flagged for a full re-read by the next scan",
	})
}

// RestoreAudiobook handles POST /audiobooks/:id/restore.
func (h *Handler) RestoreAudiobook(c *gin.Context) {
	id := c.Param("id")
	updated, err := h.audiobookService.RestoreAudiobook(c.Request.Context(), id)
	if err != nil {
		switch {
		case errors.Is(err, merge.ErrMergeBusy):
			// A running combine holds the merge lock; nothing was restored.
			httputil.RespondWithError(c, http.StatusConflict, err.Error(), CodeMergeBusy)
		case errors.Is(err, database.ErrBookIsRetiredShell):
			// Refused on purpose (the book exists; its files are kept as a
			// retired version), so it is a conflict, not a 404. The stable code
			// is the one the delete and the discard-progress purge answer with.
			httputil.RespondWithError(c, http.StatusConflict, err.Error(), CodeRetiredShell)
		case errors.Is(err, versionprimary.ErrMembershipChanged):
			// A concurrent group change outlasted the lock's retries: transient,
			// nothing was written, and the same request succeeds when it settles.
			// Same answer as the split handler's (handlers/versions.go).
			httputil.RespondWithConflict(c, "the audiobook's version group kept changing while it was being restored; nothing was restored, retry: "+err.Error())
		case errors.Is(err, audiobookspkg.ErrAudiobookNotFound):
			httputil.RespondWithNotFound(c, "audiobook", id)
		default:
			// A lookup that failed (the retired-shell check fails closed, a
			// read error) says nothing about whether the book exists: a 404
			// here told the user their book was gone.
			httputil.InternalError(c, "failed to restore audiobook", err)
		}
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
//
// scoped=1 additionally returns scoped_tags ([{tag,count}], count desc) and
// scoped_total: the tags of EVERY book the same request would list on
// GET /audiobooks, counted over those books only. The request takes the
// list's own parameters (search, filters, tags[], library_state,
// is_primary_version, show_quarantined, author_id, series_id, ...) and is
// parsed by the list's own parseListRequest, so the two can never evaluate
// different predicates. The library-wide keys are unchanged.
func (h *Handler) AudiobookFacets(c *gin.Context) {
	if h.store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}
	scoped := c.Query("scoped") == "1" || c.Query("scoped") == "true"
	var req listRequest
	if scoped {
		var ok bool
		if req, ok = h.parseListRequest(c); !ok {
			return
		}
		// Mirrors buildAudiobookListResponse: the list excludes quarantined
		// books unless show_quarantined=true, so the facets must too.
		if c.Query("show_quarantined") != "true" {
			req.filters.ExcludeQuarantined = true
		}
	}
	base, ok := h.facetsCache.Get(facetsCacheKey)
	if !ok {
		// Cache miss (e.g. first request before warm-up goroutine completes, or after TTL expiry).
		result, err := h.buildFacetsResponse(c.Request.Context())
		if err != nil {
			httputil.InternalError(c, "failed to fetch facets", err)
			return
		}
		h.facetsCache.Set(facetsCacheKey, result)
		base = result
	}
	if !scoped {
		httputil.RespondWithOK(c, base)
		return
	}
	facets, err := h.audiobookService.ScopedTagFacets(c.Request.Context(), req.params.Search, req.authorID, req.seriesID, req.filters)
	if respondIdentIndexBuilding(c, err) || respondSearchLimit(c, err) {
		return
	}
	if err != nil {
		httputil.InternalError(c, "failed to compute scoped tag facets", err)
		return
	}
	// Copy: base is the shared cached map and must not grow per-request keys.
	resp := make(gin.H, len(base)+2)
	maps.Copy(resp, base)
	resp["scoped_tags"] = facets.Tags
	resp["scoped_total"] = facets.Total
	httputil.RespondWithOK(c, resp)
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

// respondSearchLimit answers the querygrammar evaluation limits and reports
// whether it did: a spent pattern budget, or a pattern that cannot be
// answered exactly on a long field, is a 400 (the search, as written,
// cannot be answered within the limit; nothing partial is returned), every
// pattern slot busy is a 503 with Retry-After. The review handler in package
// handlers has the same function; httputil and querygrammar are both leaf
// packages, so neither can host it.
// identIndexRetryAfter is the Retry-After, in seconds, of a 503 for an
// ident: query while the identification index is being built. A rebuild over
// a production-sized library takes seconds.
const identIndexRetryAfter = "5"

// respondIdentIndexBuilding answers an ident: query that arrived while the
// identification index is being built (database.ErrIdentIndexBuilding) with
// 503 and Retry-After, and reports whether it did. A count read off a
// half-built index would be a wrong number that reads as a fact.
func respondIdentIndexBuilding(c *gin.Context, err error) bool {
	if !errors.Is(err, database.ErrIdentIndexBuilding) {
		return false
	}
	c.Header("Retry-After", identIndexRetryAfter)
	httputil.RespondWithServiceUnavailable(c, err.Error())
	return true
}

func respondSearchLimit(c *gin.Context, err error) bool {
	// A per-user or tag: term the search cannot evaluate as written (the service refuses
	// it for every caller; parseListRequest already did for this one).
	if search.IsQueryTermError(err) {
		httputil.RespondWithBadRequest(c, "invalid search: "+search.QueryTermReason(err))
		return true
	}
	if slow, ok := errors.AsType[*querygrammar.TooSlowError](err); ok {
		httputil.RespondWithBadRequest(c, slow.Error())
		return true
	}
	if inexact, ok := errors.AsType[*querygrammar.InexactError](err); ok {
		httputil.RespondWithBadRequest(c, inexact.Error())
		return true
	}
	if busy, ok := errors.AsType[*querygrammar.BusyError](err); ok {
		c.Header("Retry-After", "2")
		httputil.RespondWithServiceUnavailable(c, busy.Error())
		return true
	}
	return false
}
