// file: internal/audiobooks/service_search_cache.go
// version: 2.1.0
// guid: c3572a50-dc5f-4325-a58a-c578d837cde8
// last-edited: 2026-09-25

package audiobooks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/search"
	"github.com/falkcorp/audiobook-organizer/internal/searchcache"
)

var searchCacheLog = logger.New("search-cache")

// searchFullLimit is the "whole match set" limit the result cache builds with.
// It is past the 100000 clamp GetAudiobooksPage applies to callers, which is
// why the cache calls queryAudiobooks directly. Every search branch is bounded
// by the library, so this is a ceiling, never an allocation size.
const searchFullLimit = 1 << 30

// searchWindow is the over-fetch the post-filter search branches take: the
// searchPostFilterWindow cap per request, and no cap for a cache build (see
// queryAudiobooks' build parameter).
func searchWindow(build bool) int {
	if build {
		return searchFullLimit
	}
	return searchPostFilterWindow
}

// SetSearchPostFilterWindowForTesting sets the per-request post-filter
// over-fetch window and returns a func restoring the old value. Tests use it
// to reach the windowed branch without seeding 10,000 books; it must not be
// called while searches run.
func SetSearchPostFilterWindowForTesting(n int) (restore func()) {
	old := searchPostFilterWindow
	searchPostFilterWindow = n
	return func() { searchPostFilterWindow = old }
}

// hydrateMode says how a search turns its hit IDs into books.
type hydrateMode int

const (
	// hydratePage is the per-request read: GetBooksByIDs, fail-open (a read
	// error serves the rows read so far, with a warning).
	hydratePage hydrateMode = iota
	// hydrateBuild is a cache build or re-evaluation: every row is read, an
	// undecodable row is skipped by itself (it cannot be shown by any path),
	// and a storage read error FAILS the build, because what it returned
	// would be cached as the complete list.
	hydrateBuild
	// hydrateIDsOnly returns the hit IDs as bare books without reading them:
	// a cache build with no post-filter, whose served pages are hydrated
	// exactly like the per-request page.
	hydrateIDsOnly
)

// hydrateSearchHits reads ids for a search in the given mode.
func (svc *AudiobookService) hydrateSearchHits(ids []string, mode hydrateMode) ([]database.Book, error) {
	switch mode {
	case hydrateIDsOnly:
		books := make([]database.Book, len(ids))
		for i, id := range ids {
			books[i].ID = id
		}
		return books, nil
	case hydrateBuild:
		if rs := database.AsSearchResultStore(svc.store); rs != nil {
			books, bad, err := rs.GetBooksForSearch(ids, false)
			if err != nil {
				return nil, fmt.Errorf("search build: hydrate: %w", err)
			}
			if len(bad) > 0 {
				searchCacheLog.Warn("search build: skipped %d unreadable book rows (first %s)", len(bad), bad[0])
			}
			return books, nil
		}
		books, err := svc.store.GetBooksByIDs(ids)
		if err != nil {
			return nil, fmt.Errorf("search build: hydrate: %w", err)
		}
		return books, nil
	default:
		// FAIL-OPEN at the call site (spec §C3): a non-nil error from
		// GetBooksByIDs must not fail the whole search page — warn and keep
		// serving the rows hydrated so far.
		books, err := svc.store.GetBooksByIDs(ids)
		if err != nil {
			searchCacheLog.Warn("search: batch hydrate failed; serving partial page hydrated=%d err=%v", len(books), err)
		}
		return books, nil
	}
}

// pageBooks hydrates one served page of a cached list with the same fields
// the per-request search page carries (the signature sidecar included: the
// list shows book_sig_coverage_pct), skipping each unreadable row by itself
// rather than truncating the page at the first one.
func (svc *AudiobookService) pageBooks(ids []string) []database.Book {
	if rs := database.AsSearchResultStore(svc.store); rs != nil {
		books, bad, err := rs.GetBooksForSearch(ids, true)
		if len(bad) > 0 {
			searchCacheLog.Warn("search cache: skipped %d unreadable rows on a page (first %s)", len(bad), bad[0])
		}
		if err != nil {
			searchCacheLog.Warn("search cache: page hydrate failed; serving partial page: %v", err)
		}
		return books
	}
	books, err := svc.store.GetBooksByIDs(ids)
	if err != nil {
		searchCacheLog.Warn("search cache: page hydrate failed; serving partial page: %v", err)
	}
	return books
}

// SearchMeta describes how a page was produced.
type SearchMeta struct {
	// Cached is true when the page was sliced from the search result cache.
	Cached bool
	// Stale is true when the cached list predates a change the cache could not
	// patch in place; a rebuild is running and a later request will see it.
	Stale bool
}

// SetSearchResultCache wires the shared search result cache. nil disables it:
// every search runs the per-request pipeline, which is the rollback path.
func (svc *AudiobookService) SetSearchResultCache(c *searchcache.Cache) {
	svc.resultCache = c
}

// GetAudiobooksPage is GetAudiobooksWithTotal plus how the page was produced.
//
// A cacheable search (see searchCacheKey) is answered from the shared result
// cache: the FULL ranked, filtered ID list is computed once per distinct
// (query, filters, sort, engine) and every page is a slice of it, so page N
// costs one hydration of `limit` books however deep it is, and the count is
// exact. A search that has not finished within the configured wait returns a
// *searchcache.PendingError; the search keeps running and lands in the cache.
func (svc *AudiobookService) GetAudiobooksPage(ctx context.Context, limit int, offset int, search string, authorID *int, seriesID *int, filters ...ListFilters) ([]database.Book, int, SearchMeta, error) {
	if svc.store == nil {
		return nil, 0, SearchMeta{}, fmt.Errorf("database not initialized")
	}
	if limit <= 0 || limit > 100000 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	var f ListFilters
	if len(filters) > 0 {
		f = filters[0]
	}
	if key, ok := svc.searchCacheKey(search, authorID, seriesID, f); ok {
		books, total, meta, err := svc.cachedSearchPage(ctx, key, limit, offset, search, authorID, seriesID, f)
		var pending *searchcache.PendingError
		switch {
		case err == nil, errors.As(err, &pending):
			return books, total, meta, err
		case ctx.Err() != nil:
			// The caller has gone; running the search again helps nobody.
			return nil, 0, SearchMeta{}, err
		case errors.Is(err, searchcache.ErrNotCurrent), errors.Is(err, searchcache.ErrBusy):
			// The cache has no current list for a caller that must see every
			// change, or its build queue is full. A rebuild has been queued
			// for later lookups where possible.
		default:
			// The shared build failed: a storage read error in its fail-closed
			// hydration, a recovered panic, or an abandoned job. That error
			// reaches every caller joined on the key, and none of them asked
			// for the cache: they asked for a search, which the per-request
			// pipeline below can still answer (fail-open, as it always did).
			searchCacheLog.Warn("search cache: shared search failed, running it uncached: %v", err)
		}
		// Run the search as it always ran.
	}
	books, total, err := svc.queryAudiobooks(ctx, limit, offset, search, authorID, seriesID, f, nil, false)
	return books, total, SearchMeta{}, err
}

// SearchIsCached reports whether this request is served by the search result
// cache. The list handler's response cache defers to it: a request the result
// cache serves must not ALSO be answered from the response cache, which renames
// and tag writes do not invalidate; every other request keeps it.
func (svc *AudiobookService) SearchIsCached(search string, authorID, seriesID *int, f ListFilters) bool {
	_, ok := svc.searchCacheKey(search, authorID, seriesID, f)
	return ok
}

// cachedSearchPage serves one page of a cacheable search from the cache.
func (svc *AudiobookService) cachedSearchPage(ctx context.Context, key string, limit, offset int, search string, authorID, seriesID *int, f ListFilters) ([]database.Book, int, SearchMeta, error) {
	ev := &listSearchEvaluator{svc: svc, search: search, authorID: authorID, seriesID: seriesID, f: f, substring: !svc.bleveSearchable()}
	res, err := svc.resultCache.Lookup(ctx, key, ev, svc.lookupOptions(ctx))
	if err != nil {
		return nil, 0, SearchMeta{}, err
	}
	page := []database.Book{}
	if offset < len(res.IDs) {
		end := min(offset+limit, len(res.IDs))
		if books := svc.pageBooks(res.IDs[offset:end]); books != nil {
			page = books
		}
	}
	normalizeEffectivePrimaryVersion(page)
	return page, len(res.IDs), SearchMeta{Cached: true, Stale: res.Stale}, nil
}

type pendingOKKey struct{}

type staleOKKey struct{}

// WithPendingSearchResponse marks ctx as belonging to an interactive caller
// that asked for, and can handle, a *searchcache.PendingError when a new
// search outlives the wait: the web list handler answers 202 and the client
// polls. The handler sets it only when the request itself opted in
// (Prefer: respond-async). It does NOT admit a stale list: see
// WithStaleSearchResponse.
//
// Every other caller of GetAudiobooksPage/GetAudiobooks — batch operations,
// metadata tools, an HTTP client that did not opt in — gets a result that
// reflects every change recorded before its call, waiting as long as that
// takes (or its ctx allows), or the uncached search when the cache cannot
// produce one.
func WithPendingSearchResponse(ctx context.Context) context.Context {
	return context.WithValue(ctx, pendingOKKey{}, true)
}

// WithStaleSearchResponse additionally admits a list flagged Stale while a
// rebuild runs: its membership can predate a bulk change the cache could not
// patch. The handler sets it only for a request that asked for it
// (Prefer: allow-stale), which the web client sends only from its quick-search
// pickers, where the user picks one freshly read row. The Library list never
// sends it: its rows feed bulk actions, which must never act on membership
// that predates a change.
func WithStaleSearchResponse(ctx context.Context) context.Context {
	return context.WithValue(ctx, staleOKKey{}, true)
}

// lookupOptions is what this caller accepts from the result cache.
func (svc *AudiobookService) lookupOptions(ctx context.Context) searchcache.LookupOptions {
	pending, _ := ctx.Value(pendingOKKey{}).(bool)
	stale, _ := ctx.Value(staleOKKey{}).(bool)
	if !pending && !stale {
		return searchcache.LookupOptions{}
	}
	wait := svc.resultCache.DefaultWait()
	if s := config.AppConfig.Search.ResultCache.WaitSeconds; s > 0 {
		wait = time.Duration(s) * time.Second
	}
	return searchcache.LookupOptions{Wait: wait, AllowPending: pending, AllowStale: stale}
}

// searchCacheKey returns the cache key for a search request, or false when the
// request must not be cached. It FAILS CLOSED: a request is cached only when
// everything its result depends on is either in the key or invalidated by the
// store change log.
//
// Not cached:
//   - per-user filters, from ListFilters or from the search DSL (read_status,
//     progress_pct, last_played). Their result depends on the caller's
//     listening state, and user-state writes do not advance the store change
//     log, so even a per-user key would serve stale matches. Owner question
//     2026-09-25; decision: do not cache them.
//   - RestrictToIDs (has_file_errors): the ID set comes from book-file error
//     state, which the change log does not track.
//   - fingerprint status / coverage filters: computed from book files, not the
//     book row, so a file write can change them without a book write.
func (svc *AudiobookService) searchCacheKey(search string, authorID, seriesID *int, f ListFilters) (string, bool) {
	if svc.resultCache == nil {
		return "", false
	}
	if strings.TrimSpace(search) == "" {
		return "", false
	}
	q := search
	if len(f.PerUserFilters) > 0 || f.RestrictToIDs != nil ||
		f.FingerprintStatus != "" || f.CoveragePercentMin != nil || f.CoveragePercentMax != nil {
		return "", false
	}
	if SearchHasPerUserFilters(strings.TrimSpace(q)) {
		return "", false
	}
	// Apply the same sort normalization queryAudiobooks does, so two requests
	// the pipeline treats identically share one entry.
	f.SortBy, f.SortOrder = ScopedSort(f.SortBy, f.SortOrder, authorID, seriesID)
	if f.SortBy != "" && !CanSortBy(f.SortBy) {
		f.SortBy = ""
	}
	if f.SortBy == "" {
		// Relevance order ignores sort_order.
		f.SortOrder = ""
	}
	f.UserID = "" // only per-user filters read it, and those are not cached
	fj, err := json.Marshal(f)
	if err != nil {
		return "", false
	}
	engine := "bleve"
	substring := !svc.bleveSearchable()
	if substring {
		engine = "substring"
	}
	var b strings.Builder
	b.WriteString("web\x00")
	b.WriteString(engine)
	b.WriteByte(0)
	b.WriteString(NormalizeSearchKey(q, substring))
	b.WriteByte(0)
	b.Write(fj)
	b.WriteByte(0)
	if authorID != nil {
		b.WriteString(strconv.Itoa(*authorID))
	}
	b.WriteByte(0)
	if seriesID != nil {
		b.WriteString(strconv.Itoa(*seriesID))
	}
	return b.String(), true
}

// SearchHasPerUserFilters reports whether a search string carries per-user
// DSL filters (read_status, progress_pct, last_played). A query that does not
// parse or translate has none: it takes the substring fallback.
func SearchHasPerUserFilters(q string) bool {
	ast, err := search.ParseQuery(q)
	if err != nil {
		return false
	}
	_, perUser, err := search.Translate(ast)
	return err == nil && len(perUser) > 0
}

// NormalizeSearchKey folds a query to the form two requests share an entry
// under. It folds ONLY what the search path itself folds, so a shared key can
// never merge two queries that return different results:
//
//   - substring engine (store.SearchBooks and friends): database.SearchQueryKey,
//     exactly the fold that path applies — lowercase, '_' to space, runs of
//     spaces collapsed, and NOTHING trimmed ("rock " matches fewer books than
//     "rock", so they must not share).
//   - Bleve engine, plain queries (letters, digits, ' ' and '\t' only, and no
//     upper-case AND/OR/NOT operator): lowercase and ' '/'\t' runs collapsed,
//     ends trimmed. Those are the only separators the DSL parser splits on;
//     each word becomes analyzed match queries ANDed together, and the
//     analyzer lowercases. Any other whitespace (a pasted no-break space, a
//     newline) is NOT a separator to the parser — "primal\u00a0hunter" is one
//     token, an OR of its analyzed terms — so such a query is not plain.
//   - Bleve engine, anything else: the query exactly as sent. DSL field
//     names, quoted values, wildcards and operators are case- or
//     spacing-sensitive in places, and a query that fails to parse falls back
//     to the untrimmed substring search. '_' is NOT folded for Bleve: a token
//     containing '_' is translated differently from the same words separated
//     by spaces (underscorePatternQuery vs separate ANDed terms).
func NormalizeSearchKey(q string, substring bool) string {
	if substring {
		return database.SearchQueryKey(q)
	}
	isSep := func(r rune) bool { return r == ' ' || r == '\t' }
	for _, r := range q {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && !isSep(r) {
			return q
		}
	}
	words := strings.FieldsFunc(q, isSep)
	for _, w := range words {
		if w == "AND" || w == "OR" || w == "NOT" {
			return q
		}
	}
	return strings.ToLower(strings.Join(words, " "))
}

// listSearchEvaluator computes and maintains one web list search entry through
// queryAudiobooks, the same pipeline an uncached request runs.
type listSearchEvaluator struct {
	svc                *AudiobookService
	search             string
	authorID, seriesID *int
	f                  ListFilters
	// substring is true when the entry was built on the substring fallback
	// (index missing or rebuilding). Such entries are not patched: that
	// engine has no cheap per-ID re-evaluation, and the index rebuild that
	// ends it advances the change log past every entry anyway.
	substring bool
}

func (e *listSearchEvaluator) run(ctx context.Context, limit int, restrict map[string]struct{}) ([]string, error) {
	books, _, err := e.svc.queryAudiobooks(ctx, limit, 0, e.search, e.authorID, e.seriesID, e.f, restrict, true)
	if err != nil {
		return nil, err
	}
	ids := make([]string, len(books))
	for i := range books {
		ids[i] = books[i].ID
	}
	return ids, nil
}

func (e *listSearchEvaluator) Build(ctx context.Context, progress func(int)) ([]string, error) {
	ids, err := e.run(ctx, searchFullLimit, nil)
	if err == nil {
		progress(len(ids))
	}
	return ids, err
}

func (e *listSearchEvaluator) Match(ctx context.Context, ids []string) ([]string, bool, error) {
	if e.substring {
		return nil, false, nil
	}
	set := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		set[id] = struct{}{}
	}
	out, err := e.run(ctx, searchFullLimit, set)
	return out, err == nil, err
}

// OrderDriftsOnPatch is true for a relevance-ordered Bleve entry. Bleve
// scores with corpus-wide statistics (TF-IDF: every write moves docCount and
// docFreq), so after any write the fresh order of UNCHANGED books can differ
// from the cached one, and a patch only re-places the changed books. The
// cache therefore serves the patched list (its membership is exact) and
// rebuilds it in the background to restore the fresh order. A sort_by order
// and the substring engine's rank are per-book and do not drift.
func (e *listSearchEvaluator) OrderDriftsOnPatch() bool {
	return !e.substring && e.f.SortBy == ""
}

// Less orders two matching books. With a sort_by it is the pipeline's own
// comparator on the pair, which is exact. In relevance order it is the same
// search confined to the two books: exact for the pair under the CURRENT
// index statistics, but the rest of a patched list keeps its build-time
// order, which is why OrderDriftsOnPatch schedules a rebuild.
func (e *listSearchEvaluator) Less(ctx context.Context, a, b string) (bool, error) {
	if e.f.SortBy != "" && CanSortBy(e.f.SortBy) {
		pair, err := e.svc.store.GetBooksByIDs([]string{a, b})
		if err != nil {
			return false, err
		}
		if len(pair) != 2 {
			// One of them vanished; put the survivor first.
			return len(pair) == 1 && pair[0].ID == a, nil
		}
		applySorting(pair, e.f)
		return pair[0].ID == a, nil
	}
	ids, err := e.run(ctx, 2, map[string]struct{}{a: {}, b: {}})
	if err != nil {
		return false, err
	}
	if len(ids) == 0 {
		return false, nil
	}
	return ids[0] == a, nil
}
