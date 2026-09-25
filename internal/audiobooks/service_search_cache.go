// file: internal/audiobooks/service_search_cache.go
// version: 1.0.0
// guid: c3572a50-dc5f-4325-a58a-c578d837cde8
// last-edited: 2026-09-25

package audiobooks

import (
	"context"
	"encoding/json"
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
// by the library (and the post-filter branch by searchPostFilterWindow), so
// this is a ceiling, never an allocation size.
const searchFullLimit = 1 << 30

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
		return svc.cachedSearchPage(ctx, key, limit, offset, search, authorID, seriesID, f)
	}
	books, total, err := svc.queryAudiobooks(ctx, limit, offset, search, authorID, seriesID, f, nil)
	return books, total, SearchMeta{}, err
}

// cachedSearchPage serves one page of a cacheable search from the cache.
func (svc *AudiobookService) cachedSearchPage(ctx context.Context, key string, limit, offset int, search string, authorID, seriesID *int, f ListFilters) ([]database.Book, int, SearchMeta, error) {
	ev := &listSearchEvaluator{svc: svc, search: search, authorID: authorID, seriesID: seriesID, f: f, substring: !svc.bleveSearchable()}
	res, err := svc.resultCache.Lookup(ctx, key, ev, svc.searchWait(ctx))
	if err != nil {
		return nil, 0, SearchMeta{}, err
	}
	page := []database.Book{}
	if offset < len(res.IDs) {
		end := min(offset+limit, len(res.IDs))
		books, hErr := svc.store.GetBooksByIDs(res.IDs[offset:end])
		if hErr != nil {
			// Same fail-open contract as the uncached search hydration.
			searchCacheLog.Warn("search cache: page hydrate failed; serving partial page: %v", hErr)
		}
		if books != nil {
			page = books
		}
	}
	normalizeEffectivePrimaryVersion(page)
	return page, len(res.IDs), SearchMeta{Cached: true, Stale: res.Stale}, nil
}

type pendingOKKey struct{}

// WithPendingSearchResponse marks ctx as belonging to a caller that can
// handle *searchcache.PendingError (the web list handler, which answers 202).
// Every other caller of GetAudiobooksPage/GetAudiobooks blocks until the
// search finishes or its ctx ends, as it always did: a batch op or the
// metadata tools must never receive "still running" instead of results.
func WithPendingSearchResponse(ctx context.Context) context.Context {
	return context.WithValue(ctx, pendingOKKey{}, true)
}

// blockForever is the wait for callers that cannot handle a pending search:
// they wait for the build or for their own ctx.
const blockForever = time.Duration(1<<63 - 1)

// searchWait is how long a request waits for a new search: for a caller that
// accepts a pending answer, search.result_cache.wait_seconds (or the cache's
// default); for everyone else, until the build finishes or ctx ends.
func (svc *AudiobookService) searchWait(ctx context.Context) time.Duration {
	if ok, _ := ctx.Value(pendingOKKey{}).(bool); !ok {
		return blockForever
	}
	if s := config.AppConfig.Search.ResultCache.WaitSeconds; s > 0 {
		return time.Duration(s) * time.Second
	}
	return svc.resultCache.DefaultWait()
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
	q := strings.TrimSpace(search)
	if q == "" {
		return "", false
	}
	if len(f.PerUserFilters) > 0 || f.RestrictToIDs != nil ||
		f.FingerprintStatus != "" || f.CoveragePercentMin != nil || f.CoveragePercentMax != nil {
		return "", false
	}
	if SearchHasPerUserFilters(q) {
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
//   - substring engine (store.SearchBooks and friends): the full
//     database.searchFold semantics — lowercase, '_' to space, runs of spaces
//     collapsed. That path applies exactly that fold to the query.
//   - Bleve engine, plain queries (letters, digits and spaces only, and no
//     upper-case AND/OR/NOT operator): lowercase and whitespace collapse. Each
//     word becomes analyzed match queries, and the analyzer lowercases.
//   - Bleve engine, anything else: the trimmed query as sent. DSL field names,
//     quoted values, wildcards and operators are case- or spacing-sensitive in
//     places, and '_' is NOT folded for Bleve: a token containing '_' is
//     translated differently from the same words separated by spaces
//     (underscorePatternQuery vs separate ANDed terms), so the scores and
//     therefore the order can differ.
func NormalizeSearchKey(q string, substring bool) string {
	q = strings.TrimSpace(q)
	if substring {
		return collapseSpaces(strings.ReplaceAll(strings.ToLower(q), "_", " "))
	}
	for _, r := range q {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && !unicode.IsSpace(r) {
			return q
		}
	}
	for _, w := range strings.Fields(q) {
		if w == "AND" || w == "OR" || w == "NOT" {
			return q
		}
	}
	return strings.ToLower(strings.Join(strings.Fields(q), " "))
}

func collapseSpaces(s string) string {
	var b strings.Builder
	prev := false
	for _, r := range s {
		if r == ' ' {
			if prev {
				continue
			}
			prev = true
		} else {
			prev = false
		}
		b.WriteRune(r)
	}
	return b.String()
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
	books, _, err := e.svc.queryAudiobooks(ctx, limit, 0, e.search, e.authorID, e.seriesID, e.f, restrict)
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

// Less orders two matching books exactly as the full result does. With a
// sort_by it is the pipeline's own comparator on the pair; in relevance order
// it is the same search confined to the two books, whose relative order is
// the one the full search gives them (both scores come from the same query
// over the same index statistics).
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
