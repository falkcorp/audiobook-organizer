<!-- file: docs/plans/2026-10-06-scoped-tag-facets.md -->
<!-- version: 1.0.0 -->
<!-- guid: 5b1e7c2a-9d3f-4e8a-b6c1-2f0a7d9e4c31 -->
<!-- last-edited: 2026-10-06 -->

# Scoped tag facets for the Library "Browse by Tag" panel

## Goal

Owner: "The tag chips should be scoped to current results, otherwise they're
useless." With a search/filter active, the Browse-by-Tag panel must list only
tags present in the CURRENT result set, with counts over that set, and a chip
click must narrow the current query. Also: `GET /api/v1/audiobooks/facets` is
slow in prod (mean 5.8 s, sometimes >10 s) — target < 1 s warm.

## Current flow (file:line at b6c3e5a38)

- Chip panel: `web/src/components/library/TagCloud.tsx:95` renders
  `availableTags`; chip click toggles `selectedTags` (`:144`).
- Data source is **not** `/facets`: `web/src/hooks/useLibraryFilters.ts:138`
  calls `api.listAllUserTags()` (`web/src/services/api.ts:6153`) ->
  `GET /api/v1/tags` -> `handlers/audiobooks/handler_tags.go:46` ->
  `AudiobookService.ListAllUserTags` (`internal/audiobooks/service_tags.go:16`)
  -> `PebbleStore.ListAllTags` (`internal/database/pebble_store_tags.go:296`),
  a full `tag_idx:` scan counting EVERY tag row (soft-deleted, non-primary,
  quarantined books included). Fetched once on mount; never re-scoped.
- `/facets`: `useLibraryFilters.ts:147` -> `api.getBookFacets` (`api.ts:1350`)
  -> `handlers/audiobooks/handler.go:1120 AudiobookFacets` -> single cache key
  `"all"` (24 h TTL) -> `server/audiobooks_helpers.go:158 buildFacetsResponse`
  -> `GetDistinctGenres` + `GetDistinctLanguages` (two full Pebble
  JSON-unmarshal walks, `pebble_store.go:4364/4397`) + Bleve `FacetCounts`
  (capped at 200 values per facet — cannot carry 431 tags).
- List request: `useLibraryQuery.ts:283` -> `api.getBooks` (`api.ts:1229`)
  -> `handler.go:430 ListAudiobooks` (param parsing `:470-695`) ->
  `buildAudiobookListResponse` (`audiobooks_helpers.go:52`, adds
  `ExcludeQuarantined`) -> `GetAudiobooksPage` -> `queryAudiobooks`
  (`service_query.go:78`) / search result cache (`service_search_cache.go`).
- Multiple `tag:` terms in the search box do not narrow: `useLibraryQuery.ts:232`
  takes only the FIRST parsed `tag:` and ignores parsed tags entirely when
  `selectedTags` is non-empty.

### Why /facets is slow

Nothing invalidates the `"all"` facets cache (only the operator cache-clear
endpoint), so the 5.8 s mean is the cold path: every restart/deploy, every
24 h TTL expiry, and every request that lands before the startup warmer
finishes pays two full Pebble JSON walks over ~68K book rows plus a Bleve
MatchAll, with no collapse of concurrent misses. A cold path of that cost is
the defect; the cache only hides it some of the time.

## Design

### Backend

1. **One request parser.** Extract the `ListFilters`-building section of
   `ListAudiobooks` (bare-param guard, unindexed-search guard,
   has_file_errors / quick-query / ids restrictions, tags[], fingerprint
   params, filters-JSON validation + per-user split, ScopedSort, UserID) into
   `parseListRequest(c) (listRequest, ok)` in the handler package. Both
   `ListAudiobooks` and `AudiobookFacets` call it, so facets evaluate exactly
   the list's predicate. `show_quarantined` -> `ExcludeQuarantined` is applied
   the same way the list builder applies it.
2. **Matched ID set through the list pipeline** —
   `AudiobookService.MatchingBookIDs(ctx, search, authorID, seriesID, f)`
   (new file `internal/audiobooks/service_tag_facets.go`):
   - cacheable search -> `resultCache.Lookup` with the SAME `searchCacheKey`
     and evaluator the list uses (instant hit right after the list loaded;
     maintained by the store change log);
   - otherwise -> `queryAudiobooks(ctx, searchFullLimit, 0, ..., build=true)`,
     the uncached list pipeline with no window. No filter logic is duplicated
     and `service_filtering.go` is not touched.
   - `queryAudiobooks`' light path must not write a whole-library "page" into
     `listCache`: gate that `Set` on `limit <= 100000` (one-line change in
     `service_query.go`).
3. **Tag counting without per-item reads.**
   `AudiobookService.ScopedTagCounts(ctx, ...)`: for small match sets
   (<= 2,000 IDs) `store.GetBookTagsByBookIDs` (one iterator, one seek per
   book); for larger sets a new Pebble capability
   `CountTagsForBookIDs(set)` — ONE sequential `tag_idx:` key scan (no JSON
   decode) intersected with the set. Both are single sequential iterations,
   so no worker pool is needed (CLAUDE.md concurrency rule is about per-item
   work fanned over the library; this is one scan).
4. **Cache.** Small LRU (`cache.NewWithLimit`, 256 entries, 10 min TTL) keyed by
   `searchChanges.Generation()` + normalized request (search key, filters
   JSON, author/series ids, quarantine). Not cached (fail closed, mirroring
   `searchCacheKey`): per-user filters, RestrictToIDs, fingerprint/coverage
   filters, or no change log wired. Tag writes advance the generation via
   `notifyBooksNeedReindex`, so a tag edit invalidates. Concurrent identical
   misses are collapsed with `singleflight`.
5. **/facets response (additive).** Unchanged keys (`genres`, `languages`,
   `genre_counts`, `language_counts`, `tag_counts`) still come from the
   `"all"` cache. New when the request carries `scoped=1`:
   `scoped_tags: [{tag,count}]` (sorted count desc, tag asc) and
   `scoped_total` (match-set size).
6. **Cold path for genres/languages.** `PebbleStore.GetGenreCounts` and
   `GetDistinctLanguages` delegate to new memdb walkers when memdb is
   serving (same pattern as `GetDistinctPublishedYears`), same semantics as
   the Pebble walk. Benefits ABS `/filterdata` too (same functions).

### Frontend

- `api.ts`: extract `buildBookListParams(options)` from `getBooks`;
  `getBooks` and new `getScopedTagFacets(options)` both use it, so the facets
  request cannot drift from the list request.
- New hook `useScopedTagFacets` (debounced 250 ms after the list's own 300 ms
  search debounce, aborts in-flight request on change; refetches on
  search/filters/tags only, not page or sort).
- `Library.tsx` feeds TagCloud from `scoped_tags`, falling back to the
  library-wide list while the first scoped response is pending / on error.
- Chip click: if the tag is active (in `selectedTags` or a parsed `tag:` term)
  remove it; else append `tag:"<value>"` to the search box text.
- `useLibraryQuery.ts`: send the UNION of `selectedTags` and every
  non-negated parsed `tag:` term as `tags[]` (AND), fixing the
  only-first-tag bug so a second chip actually narrows.
- Empty state: with a query that matches books with no tags, the panel says
  "No tags in these results" instead of disappearing.
- Not touched (other branch feat/unified-search-re2): `searchParser.ts`,
  `service_filtering.go`, `SearchBar.tsx`.

## Test strategy

- Go: `ScopedTagCounts` / handler test with a real Pebble store and synthetic
  books: a filter (library_state, tags[], search) counts only matching books;
  quarantined/non-primary excluded per params; a tag write invalidates the
  cache; `CountTagsForBookIDs` vs `GetBookTagsByBookIDs` agree; benchmark on
  synthetic 20K books.
- Vitest: facets refetch with the query (search change -> new request with
  same params as getBooks); chip click appends `tag:"x"` and a second chip
  sends both tags to getBooks; `buildBookListParams` parity.
- `go build ./... && go vet` on touched packages, `go test -race` on
  `internal/audiobooks`, `internal/server/handlers/audiobooks`,
  `internal/database` (targeted), `npm run typecheck`, vitest on touched dirs.

## Rollback

Single PR, additive. Frontend falls back to the library-wide `/tags` list when
`scoped_tags` is absent, so reverting the backend alone is safe; reverting the
PR restores prior behaviour exactly.

## Decisions for owner to validate

1. **Counts drop even with no search.** Scoped counts use the list's own
   predicate (primary versions only, quarantine excluded) — e.g.
   `language: en` will read lower than today's 28,697, which counted every
   tag row including non-primary/soft-deleted copies. WHY: a chip count that
   disagrees with the number of books the click shows is the bug being fixed.
2. **Chip click edits the search text** (`tag:"value"` appended) rather than
   the hidden `selectedTags` state. WHY: owner asked that the click "add it to
   the current query"; the query box becomes the single visible source of
   truth. The quoted form may need revisiting once the RE2 grammar
   (feat/unified-search-re2) lands.
3. **FilterSidebar's tag list stays library-wide.** WHY: it is a picker for
   building a query from scratch; scoping it would hide tags you want to
   switch to. Say if you want it scoped too.
4. **genres/languages dropdown lists stay library-wide** (same reasoning);
   only the tag cloud is scoped.
5. Hidden namespaces (`dedup:*`, `metadata:source:*`) stay hidden in the cloud.
