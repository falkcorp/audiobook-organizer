// file: internal/audiobooks/service_query.go
// version: 1.11.0
// guid: c5f9d4e3-f6a7-8b90-ac1d-2e3f4a5b6c7d
// last-edited: 2026-08-12

package audiobooks

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/search"
)

// GetAudiobooks retrieves audiobooks with optional filtering.
// Supports search, author_id, series_id, is_primary_version, and library_state filters.
func (svc *AudiobookService) GetAudiobooks(ctx context.Context, limit int, offset int, search string, authorID *int, seriesID *int, filters ...ListFilters) ([]database.Book, error) {
	books, _, err := svc.GetAudiobooksWithTotal(ctx, limit, offset, search, authorID, seriesID, filters...)
	return books, err
}

// GetAudiobooksWithTotal is GetAudiobooks plus the number of MATCHES for the
// request, which is not the same thing as the length of the page returned.
//
// The int is -1 when this layer cannot establish a true total (the generic
// unfiltered list path, where the caller already has cheaper dedicated count
// queries, and the non-Bleve substring fallback which exposes no count).
// Callers must treat -1 as "unknown" and fall back to their previous
// behaviour rather than rendering it.
//
// A caveat worth stating rather than burying: when a search is combined with
// post-filters, the total is exact only while the match set fits inside
// searchPostFilterWindow. Past that it is a lower bound and a warning is
// logged. It is not silently capped.
func (svc *AudiobookService) GetAudiobooksWithTotal(ctx context.Context, limit int, offset int, search string, authorID *int, seriesID *int, filters ...ListFilters) ([]database.Book, int, error) {
	if svc.store == nil {
		return nil, 0, fmt.Errorf("database not initialized")
	}

	// Normalize limit and offset
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
	hasSorting := f.SortBy != ""
	// Sorts backed by a memdb sorted index are pushed down and streamed;
	// everything else still needs the heavy materialise-the-whole-set path.
	//
	// This used to be hardcoded as `f.SortBy == "title"`. It now asks the
	// database package, because that is where the indexes are declared —
	// with the hardcoded string, adding an index would have silently built a
	// structure that nothing ever queried. Nine sort keys qualify today
	// (author, narrator, series, year, created_at, updated_at, duration,
	// file_size, bitrate, plus their alias spellings).
	sortPushdownable := database.CanPushDownSort(f.SortBy)
	heavySorting := hasSorting && !sortPushdownable
	hasPerUser := len(f.PerUserFilters) > 0 && f.UserID != ""
	hasFingerprintingFilters := f.FingerprintStatus != "" || f.CoveragePercentMin != nil || f.CoveragePercentMax != nil
	// "Heavy" post-filters require fetching all rows to apply in Go.
	// IsPrimaryVersion is intentionally NOT in this set because the store
	// (memdb-backed) can push it down via an indexed iteration — fetching
	// all 68K rows to satisfy ?is_primary_version=true was the prod
	// "library spins forever" bug.
	hasHeavyPostFilters := f.LibraryState != "" || f.Tag != "" || len(f.Tags) > 0 || len(f.FieldFilters) > 0 || hasPerUser || heavySorting || hasFingerprintingFilters
	hasPostFilters := hasHeavyPostFilters || f.IsPrimaryVersion != nil || sortPushdownable

	// When heavy post-filters are active, fetch all and filter in memory.
	storeLimit := limit
	storeOffset := offset
	if hasHeavyPostFilters {
		storeLimit = 0
		storeOffset = 0
	}

	// Initialize as empty slice to ensure we return [] instead of null
	books := []database.Book{}
	var err error
	// alreadySortedAndPaginated is set true only by the didPushdown+heavySorting
	// branch below, which sorts and paginates the pushdown result itself. It
	// skips the trailing applySorting call so an already-paginated (≤limit-sized)
	// page isn't needlessly re-sorted.
	alreadySortedAndPaginated := false

	// resultTotal is the number of MATCHES for this request, as distinct from
	// the length of the page returned. It stays -1 until a branch below can
	// establish it; -1 means "not known here", and the caller substitutes the
	// page length exactly as it always did.
	resultTotal := -1

	// Apply filters in order of precedence
	if search != "" {
		if svc.searchIndex != nil {
			// When post-filters will run below, the index must hand back the
			// whole candidate set, NOT one page.
			//
			// Fetching a single page here and then post-filtering it produced
			// two compounding faults, both measured against production on
			// 2026-08-12 with search=honour&is_primary_version=true&limit=5:
			// offset=0 returned 1 row instead of a full page, because the
			// filter deleted most of an already-cut page and nothing refilled
			// it; and offset=5/10/20 each returned 0 rows, because
			// paginateFilteredBooks then re-sliced that short page by the
			// ORIGINAL offset, which is out of range for a <=limit slice.
			// The same query without the filter paged correctly (5/5/5), which
			// is what made this look like a search-quality problem rather than
			// a pagination one.
			//
			// The non-search pushdown path already guards the identical hazard
			// by clearing hasPostFilters once the store has filtered AND
			// paginated (see the didPushdown branch below, and its comment
			// naming the same "page 2 returns zero rows" symptom). The search
			// path never got that guard. Over-fetching is the equivalent fix
			// for a path that cannot push the filter down: filter the full set
			// first, then let the existing post-filter block paginate it.
			fetchLimit, fetchOffset := limit, offset
			if hasPostFilters {
				fetchLimit, fetchOffset = searchPostFilterWindow, 0
			}
			books, resultTotal, err = svc.searchWithBleve(search, fetchLimit, fetchOffset, f.UserID)
			if hasPostFilters && len(books) >= searchPostFilterWindow {
				// Truncated: rows past the window were never considered, so any
				// count derived below is a lower bound. Say so rather than
				// reporting a confident wrong number.
				slog.Warn("search: post-filter over-fetch window exhausted; count is a lower bound",
					"window", searchPostFilterWindow, "query", search)
			}
		} else {
			books, err = svc.store.SearchBooks(search, limit, offset)
		}
	} else if authorID != nil {
		// GetBooksByAuthorIDCore is Core-typed (STOREFID P3-W2); GetAudiobooks'
		// public signature ([]database.Book, shared with SearchBooks/
		// GetBooksBySeriesIDCore below and the caller's downstream filtering) is
		// out of scope to retype here, so convert back via BookCore.ToBook().
		var booksCore []database.BookCore
		booksCore, err = svc.store.GetBooksByAuthorIDCore(*authorID)
		if err == nil {
			books = make([]database.Book, len(booksCore))
			for i := range booksCore {
				books[i] = booksCore[i].ToBook()
			}
		}
	} else if seriesID != nil {
		// GetBooksBySeriesIDCore is Core-typed (STOREFID W4); same rationale
		// as the authorID branch above.
		var booksCore []database.BookCore
		booksCore, err = svc.store.GetBooksBySeriesIDCore(*seriesID)
		if err == nil {
			books = make([]database.Book, len(booksCore))
			for i := range booksCore {
				books[i] = booksCore[i].ToBook()
			}
		}
	}

	// Fall back to generic list only when no filter was applied
	if search == "" && authorID == nil && seriesID == nil {
		// Pushdown path: only "light" filters (is_primary_version, title
		// sort) — let the store paginate using its index, no in-memory
		// full-scan.
		sortAsc := !strings.EqualFold(f.SortOrder, "desc")
		if !hasHeavyPostFilters {
			// primaryKey renders the *bool as a stable token. Formatting the
			// pointer itself with %v prints its memory address, which is unique
			// per request — so the cache key never matched and the list cache
			// never hit for is_primary_version queries (every library page).
			primaryKey := "nil"
			if f.IsPrimaryVersion != nil {
				primaryKey = strconv.FormatBool(*f.IsPrimaryVersion)
			}
			cacheKey := fmt.Sprintf("all:%d:%d:p=%s:sb=%s:asc=%v:noq=%v",
				limit, offset, primaryKey, f.SortBy, sortAsc, f.ExcludeQuarantined)
			if cached, ok := svc.listCache.Get(cacheKey); ok {
				return cached, -1, nil
			}
			summaries, didPushdown, sErr := svc.summariesPushdown(storeLimit, storeOffset, f.IsPrimaryVersion, f.SortBy, sortAsc, f.ExcludeQuarantined)
			if sErr == nil && summaries != nil {
				books = bookSummariesToBooks(summaries)
				svc.listCache.Set(cacheKey, books)
			}
			// When the store applied the filter AND paginated, the page is
			// final. Running the post-filter block would re-slice this
			// already-paginated page by the original offset — which is out of
			// bounds for a ≤limit slice, so every offset>0 ("page 2") returned
			// zero rows. Skip it. If the store fell back to the unfiltered path
			// (didPushdown=false), keep the post-filter so IsPrimary is applied.
			if didPushdown {
				hasPostFilters = false
			}
		} else {
			// Heavy-filter pushdown: build a BookSummaryFilter that
			// captures every predicate the walker can apply in-loop, then
			// call summariesPushdownFiltered with REAL limit/offset. The
			// walker stops at limit+offset matches — no full-corpus
			// materialization, no 1GB working set per query.
			//
			// Falls back to the legacy fetch-all-then-filter path when the
			// filter set contains something we can't push down. Previously
			// non-title sorts and fingerprint filters always fell back; now they
			// go through pushdown with predicates, reducing fetched rows from
			// ~68K (unfiltered) to only the filtered subset (e.g. ~38K primary).
			if bsf, pushdownOK, pebbleLookups := svc.buildBookSummaryFilterWithLookupCount(f, sortAsc); pushdownOK {
				// Non-title sorts can't be pushed to memdb's title radix index,
				// so fetch the FULL filtered set (pdLimit/pdOffset zeroed) and
				// sort+paginate in application memory below.
				pdLimit, pdOffset := limit, offset
				if heavySorting {
					pdLimit, pdOffset = 0, 0
				}
				summaries, didPushdown, sErr := svc.summariesPushdownFiltered(pdLimit, pdOffset, bsf)
				if sErr == nil && summaries != nil {
					books = bookSummariesToBooks(summaries)
				}
				if pebbleLookups != nil && *pebbleLookups > 0 {
					slog.Debug("GetAudiobooks: stripped-field predicate Pebble fallback",
						"lookups", *pebbleLookups, "books_returned", len(books))
				}
				if didPushdown {
					// The memdb walker already applied every predicate carried
					// on bsf — tags (RestrictToIDs), IsPrimaryVersion,
					// LibraryState, ReviewStatus, and the closure covering
					// FieldFilters/FingerprintStatus/CoveragePercent/
					// PerUserFilters — against the real memdb *Book. Re-running
					// those same filters below would evaluate them against
					// bookSummariesToBooks(summaries) projections instead, and
					// BookSummary doesn't carry every Book field (Language,
					// Genre, Publisher, Edition, Codec, Quality,
					// FingerprintStatus, CoveragePercent are all
					// BookSummary-absent — see database.BookSummary). A
					// re-check would read those as "" / zero and drop every
					// row, so a heavy filter combined with a non-title sort
					// came back with 0 books despite CountAudiobooksFiltered
					// (which never re-filters) reporting the correct N. Skip
					// the post-filter pass unconditionally once the store has
					// pushed the filter down — never re-apply it.
					hasPostFilters = false
					if heavySorting {
						// The fetch above returned the full filtered set,
						// unpaginated and unsorted (pdLimit/pdOffset were
						// zeroed). Sort it now, then paginate ourselves —
						// mirrors the title-sort branch's filter-then-sort-
						// then-paginate semantics. alreadySortedAndPaginated
						// skips the redundant trailing applySorting call
						// below.
						applySorting(books, f)
						books = paginateFilteredBooks(books, limit, offset)
						alreadySortedAndPaginated = true
					}
				}
			} else {
				// pushdownOK == false is only reachable when tag→ID
				// resolution (GetBooksByTag) errored inside
				// buildBookSummaryFilterWithLookupCount. The old behavior
				// silently fetched the entire corpus (zero-value filter =
				// no pushdown), which is a full-library scan hidden behind
				// an error. Make it loud so the fetch-all is visible in logs;
				// the subsequent post-filter tag pass re-calls GetBooksByTag
				// and surfaces the same error to the caller, so this branch's
				// result is discarded anyway — the warn is the observable
				// signal that the fallback fired.
				slog.Warn("GetAudiobooks: pushdown filter construction failed; falling back to full fetch",
					"tag", f.Tag, "tags", f.Tags, "library_state", f.LibraryState)
				summaries, _, sErr := svc.summariesPushdownFiltered(storeLimit, storeOffset, database.BookSummaryFilter{})
				if sErr == nil && summaries != nil {
					books = bookSummariesToBooks(summaries)
				}
			}
		}
	}

	if err != nil {
		return nil, 0, err
	}

	// Apply post-filters
	if hasPostFilters {
		// If tag filter is set, build a set of matching book IDs (intersection of all tags)
		var tagBookIDs map[string]struct{}
		tagsToMatch := f.Tags
		if len(tagsToMatch) == 0 && f.Tag != "" {
			tagsToMatch = []string{f.Tag}
		}
		if len(tagsToMatch) > 0 {
			for _, tag := range tagsToMatch {
				if tag == "" {
					continue
				}
				ids, tagErr := svc.store.GetBooksByTag(tag)
				if tagErr != nil {
					return nil, 0, tagErr
				}
				curSet := make(map[string]struct{}, len(ids))
				for _, id := range ids {
					curSet[id] = struct{}{}
				}
				if tagBookIDs == nil {
					tagBookIDs = curSet
				} else {
					for id := range tagBookIDs {
						if _, ok := curSet[id]; !ok {
							delete(tagBookIDs, id)
						}
					}
				}
				if len(tagBookIDs) == 0 {
					break
				}
			}
		}

		filtered := make([]database.Book, 0, len(books))
		for _, b := range books {
			if len(tagsToMatch) > 0 {
				if tagBookIDs == nil {
					continue
				}
				if _, ok := tagBookIDs[b.ID]; !ok {
					continue
				}
			}
			if f.IsPrimaryVersion != nil {
				bPrimary := b.IsPrimaryVersion != nil && *b.IsPrimaryVersion
				if *f.IsPrimaryVersion != bPrimary {
					continue
				}
			}
			if f.LibraryState != "" {
				bState := ""
				if b.LibraryState != nil {
					bState = *b.LibraryState
				}
				if bState != f.LibraryState {
					continue
				}
			}
			filtered = append(filtered, b)
		}

		// Apply field-specific filters (advanced search). Books here came
		// from BookSummary projections, which never carry stripped fields
		// (description / version_notes / book_sig_v1). Route those filters
		// through the Pebble fallback so they don't silently miss.
		if len(f.FieldFilters) > 0 {
			cheapFF, strippedFF := splitFieldFilters(f.FieldFilters)
			var pebbleLookups int64
			var warnOnce sync.Once
			warnFn := func(id string, err error) {
				warnOnce.Do(func() {
					slog.Warn("post-filter stripped-field Pebble fallback: GetBookByID failed; dropping row",
						"book_id", id, "err", err)
				})
			}
			fetchFull := func(id string) (*database.Book, error) {
				return svc.store.GetBookByID(id)
			}
			// author/series names are never persisted on the Book row itself
			// (populated only via joins), so this fallback path needs the
			// same id→name hydration as the memdb pushdown predicate — see
			// buildAuthorSeriesNameMaps / hydrateAuthorSeriesNames in
			// service_filtering.go (TODO 16b).
			authorNames, seriesNames := svc.buildAuthorSeriesNameMaps(f.FieldFilters)
			fieldFiltered := make([]database.Book, 0, len(filtered))
			for i := range filtered {
				b := filtered[i]
				if matchesFieldFiltersWithStrippedFallback(&b, cheapFF, strippedFF, fetchFull, &pebbleLookups, warnFn, authorNames, seriesNames) {
					fieldFiltered = append(fieldFiltered, b)
				}
			}
			if pebbleLookups > 0 {
				slog.Debug("GetAudiobooks post-filter: stripped-field Pebble fallback",
					"lookups", pebbleLookups, "matched", len(fieldFiltered))
			}
			filtered = fieldFiltered
		}

		// Apply fingerprinting filters
		if hasFingerprintingFilters {
			fpFiltered := make([]database.Book, 0, len(filtered))
			for _, b := range filtered {
				if f.FingerprintStatus != "" {
					// Filter by fingerprint status
					if b.FingerprintStatus != f.FingerprintStatus {
						continue
					}
				}
				if f.CoveragePercentMin != nil {
					// Filter by minimum coverage percentage
					if b.CoveragePercent < *f.CoveragePercentMin {
						continue
					}
				}
				if f.CoveragePercentMax != nil {
					// Filter by maximum coverage percentage
					if b.CoveragePercent > *f.CoveragePercentMax {
						continue
					}
				}
				fpFiltered = append(fpFiltered, b)
			}
			filtered = fpFiltered
		}

		// Apply per-user filters (read_status / progress_pct / last_played).
		// Requires a caller user ID; without one we skip rather than
		// silently dropping every book.
		if hasPerUser {
			perUserFiltered := make([]database.Book, 0, len(filtered))
			for _, b := range filtered {
				state, _ := svc.store.GetUserBookState(f.UserID, b.ID)
				if matchesAllPerUserFilters(state, f.PerUserFilters) {
					perUserFiltered = append(perUserFiltered, b)
				}
			}
			filtered = perUserFiltered
		}

		// The post-filtered set is the real match set for this request, so its
		// length is the real total — but only if `filtered` was built from the
		// full candidate set rather than from one page. That holds for the
		// search path because the branch above over-fetches when
		// hasPostFilters, and for the author/series paths because those fetch
		// every row up front. Capture it BEFORE paginating; taking it after is
		// the bug.
		resultTotal = len(filtered)

		// Apply pagination after filtering
		books = paginateFilteredBooks(filtered, limit, offset)
	}

	// Apply sorting after all filtering but before returning. Skipped when the
	// didPushdown+heavySorting branch above already sorted (and paginated) the
	// pushdown result — re-sorting an already-paginated ≤limit-sized page is
	// wasted work, not a correctness issue, but there is no reason to pay it.
	if !alreadySortedAndPaginated {
		applySorting(books, f)
	}

	// Ensure we never return null - always return empty array
	if books == nil {
		books = []database.Book{}
	}

	return books, resultTotal, nil
}

// CountAudiobooksFiltered returns the count of audiobooks matching the
// given filters. Uses the memdb count-only pushdown (no projection
// allocations, no full-corpus materialization) for the common filter set.
// Falls back to materialize-and-count when the filter set contains
// something we can't push down (non-title sort doesn't matter for counts;
// fingerprint filters depend on BookFile data).
func (svc *AudiobookService) CountAudiobooksFiltered(ctx context.Context, filters ListFilters) (int, error) {
	if svc.store == nil {
		return 0, fmt.Errorf("database not initialized")
	}
	// SortBy doesn't affect counts; clear it so buildBookSummaryFilter
	// doesn't reject the filter for non-title sort. Counts don't depend
	// on iteration order.
	filtersForCount := filters
	filtersForCount.SortBy = ""
	bsf, pushdownOK := svc.buildBookSummaryFilter(filtersForCount, true)
	if pushdownOK {
		return svc.countSummariesPushdownFiltered(bsf)
	}

	// Unreachable in practice — buildBookSummaryFilter now returns pushdownOK=true
	// for fingerprint filters (FingerprintStatus/CoveragePercent are denormalized
	// on Book). Kept as a defensive fallback.
	books, err := svc.store.GetAllBooksCore(0, 0)
	if err != nil {
		return 0, err
	}
	tagsToMatch := filters.Tags
	if len(tagsToMatch) == 0 && filters.Tag != "" {
		tagsToMatch = []string{filters.Tag}
	}
	var tagBookIDs map[string]struct{}
	for _, tag := range tagsToMatch {
		if tag == "" {
			continue
		}
		ids, tagErr := svc.store.GetBooksByTag(tag)
		if tagErr != nil {
			return 0, tagErr
		}
		curSet := make(map[string]struct{}, len(ids))
		for _, id := range ids {
			curSet[id] = struct{}{}
		}
		if tagBookIDs == nil {
			tagBookIDs = curSet
		} else {
			for id := range tagBookIDs {
				if _, ok := curSet[id]; !ok {
					delete(tagBookIDs, id)
				}
			}
		}
		if len(tagBookIDs) == 0 {
			break
		}
	}

	count := 0
	for _, b := range books {
		if filters.IsPrimaryVersion != nil {
			bPrimary := b.IsPrimaryVersion != nil && *b.IsPrimaryVersion
			if *filters.IsPrimaryVersion != bPrimary {
				continue
			}
		}
		if filters.LibraryState != "" {
			bState := ""
			if b.LibraryState != nil {
				bState = *b.LibraryState
			}
			if bState != filters.LibraryState {
				continue
			}
		}
		if len(tagsToMatch) > 0 {
			if tagBookIDs == nil {
				continue
			}
			if _, ok := tagBookIDs[b.ID]; !ok {
				continue
			}
		}
		if filters.FingerprintStatus != "" && b.FingerprintStatus != filters.FingerprintStatus {
			continue
		}
		if filters.CoveragePercentMin != nil && b.CoveragePercent < *filters.CoveragePercentMin {
			continue
		}
		if filters.CoveragePercentMax != nil && b.CoveragePercent > *filters.CoveragePercentMax {
			continue
		}
		count++
	}
	return count, nil
}

// EnrichAudiobooksWithNames adds author and series names to audiobook details.
// Also aggregates duration and file size from individual files.
// Batch-fetches authors and series by unique IDs to avoid N+1 DB lookups.
//
// This is the fetch-then-enrich wrapper. Callers that have already fetched
// the book_files map should use EnrichAudiobooksWithNamesAndFiles to avoid
// a duplicate GetBookFilesForIDsCore call.
func (svc *AudiobookService) EnrichAudiobooksWithNames(books []database.Book) []AudiobookDetail {
	filesByBookID := svc.FetchBookFilesForBooks(books)
	return svc.EnrichAudiobooksWithNamesAndFiles(books, filesByBookID)
}

// EnrichAudiobooksWithNamesAndFiles is the variant that accepts a pre-fetched
// files map and threads it into aggregation, avoiding a duplicate
// GetBookFilesForIDsCore call when the caller already has the map (e.g. the
// list handler uses it again for fingerprint compute).
func (svc *AudiobookService) EnrichAudiobooksWithNamesAndFiles(books []database.Book, filesByBookID map[string][]database.BookFileCore) []AudiobookDetail {
	// Aggregate file metadata for all books at once (avoids N+1)
	svc.aggregateFileMetadataWithFiles(books, filesByBookID)

	// Collect unique IDs that need DB lookups (skip pre-loaded objects).
	authorIDs := make([]int, 0)
	seriesIDs := make([]int, 0)
	for i := range books {
		b := &books[i]
		if b.Author == nil && b.AuthorID != nil {
			authorIDs = append(authorIDs, *b.AuthorID)
		}
		if b.Series == nil && b.SeriesID != nil {
			seriesIDs = append(seriesIDs, *b.SeriesID)
		}
	}

	var authorsMap map[int]*database.Author
	var seriesMap map[int]*database.Series
	if len(authorIDs) > 0 && svc.store != nil {
		authorsMap, _ = svc.store.GetAuthorsByIDs(authorIDs)
	}
	if len(seriesIDs) > 0 && svc.store != nil {
		seriesMap, _ = svc.store.GetSeriesByIDs(seriesIDs)
	}

	enrichedBooks := make([]AudiobookDetail, 0, len(books))
	for i := range books {
		b := &books[i]
		detail := AudiobookDetail{Book: b}

		authorName := ""
		if b.Author != nil {
			authorName = b.Author.Name
		} else if b.AuthorID != nil && authorsMap != nil {
			if a, ok := authorsMap[*b.AuthorID]; ok {
				authorName = a.Name
			}
		}

		seriesName := ""
		if b.Series != nil {
			seriesName = b.Series.Name
		} else if b.SeriesID != nil && seriesMap != nil {
			if s, ok := seriesMap[*b.SeriesID]; ok {
				seriesName = s.Name
			}
		}

		if authorName != "" {
			detail.AuthorName = &authorName
		}
		if seriesName != "" {
			detail.SeriesName = &seriesName
		}
		enrichedBooks = append(enrichedBooks, detail)
	}
	return enrichedBooks
}

// searchPostFilterWindow bounds the Bleve over-fetch used when
// per-user DSL filters (read_status / progress_pct / last_played)
// must be applied in Go after the index returns candidates. Mirrors
// the playlist evaluator's defaultEvalPageSize precedent
// (internal/playlist/evaluator.go) — kept as a private, package-local
// constant rather than importing the playlist one.
const searchPostFilterWindow = 10000

// searchWithBleve parses the query via the DSL, translates to a
// Bleve native query, and returns the matching books. Per-user
// filters produced by the translator (read_status / progress_pct /
// last_played) are applied here (INIT-4 T2) via
// search.MatchPerUserFilters: Bleve is over-fetched up to
// searchPostFilterWindow, each hit's per-user state is read and
// matched, then offset/limit slicing is applied to the filtered
// slice. Requires userID (the authenticated caller, plumbed in by
// GetAudiobooks via f.UserID); with no userID the filters are
// skipped and a warning is logged rather than silently
// over-suppressing results.
//
// Falls back to an empty slice (not nil) on zero matches so callers
// get consistent JSON shape.
// The int return is the number of MATCHES, not the length of the returned
// page. Bleve computes it on every query and hands it back as the second
// value of SearchNative; this function used to discard it with `_`, and the
// caller then substituted len(page) — which is always a plausible-looking
// number, which is why it survived so long. See searchTotalIsExact for when
// the figure is exact and when it is a lower bound.
func (svc *AudiobookService) searchWithBleve(query string, limit, offset int, userID string) ([]database.Book, int, error) {
	ast, err := search.ParseQuery(query)
	if err != nil {
		// Parser failure: fall back to the substring search path so
		// users still see results for simple queries the DSL parser
		// rejects (e.g. punctuation-heavy book titles).
		books, sErr := svc.store.SearchBooks(query, limit, offset)
		// SearchBooks exposes no match count, so the only honest figure
		// available here is the page length. Callers must not treat this as
		// a true total; it is the pre-existing behaviour for this fallback.
		return books, len(books), sErr
	}
	bleveQ, perUser, err := search.Translate(ast)
	if err != nil {
		books, sErr := svc.store.SearchBooks(query, limit, offset)
		return books, len(books), sErr
	}

	if len(perUser) > 0 && userID != "" && !config.AppConfig.DisablePerUserSearchFilters {
		hits, _, err := svc.searchIndex.SearchNative(bleveQ, 0, searchPostFilterWindow)
		if err != nil {
			return nil, 0, fmt.Errorf("bleve search: %w", err)
		}
		if len(hits) >= searchPostFilterWindow {
			slog.Warn("search: post-filter window exhausted; results beyond it are truncated",
				"window", searchPostFilterWindow)
		}
		ids := make([]string, 0, len(hits))
		for _, h := range hits {
			ids = append(ids, h.BookID)
		}
		// Batch-hydrate (INIT-4 T3): one store call instead of one per hit.
		// FAIL-OPEN at the call site (spec §C3): a non-nil error from
		// GetBooksByIDs must not fail the whole search page — warn and
		// keep serving the rows hydrated so far, mirroring the old
		// per-hit loop's silent-skip-on-error semantics.
		hydrated, hydrateErr := svc.store.GetBooksByIDs(ids)
		if hydrateErr != nil {
			slog.Warn("search: batch hydrate failed; serving partial page",
				"err", hydrateErr, "hydrated", len(hydrated))
		}
		filtered := make([]database.Book, 0, len(hydrated))
		for _, b := range hydrated {
			state, stateErr := svc.store.GetUserBookState(userID, b.ID)
			if stateErr != nil {
				// FAIL-OPEN (Decision 5): evaluate the zero-value state, loudly.
				slog.Warn("search: per-user state read failed; evaluating zero-value state",
					"book_id", b.ID, "err", stateErr)
				state = nil
			}
			if !search.MatchPerUserFilters(state, perUser) {
				continue
			}
			filtered = append(filtered, b)
		}
		// Capture the match count BEFORE the slice below cuts it to a page.
		// Taking it afterwards is exactly the bug this change fixes.
		perUserTotal := len(filtered)
		// Apply pagination after filtering — offset beyond len yields an
		// empty slice, not an error (mirrors GetAudiobooks' heavy
		// post-filter slicing above).
		if offset > 0 && offset < len(filtered) {
			filtered = filtered[offset:]
		} else if offset >= len(filtered) {
			filtered = nil
		}
		if limit > 0 && limit < len(filtered) {
			filtered = filtered[:limit]
		}
		if filtered == nil {
			filtered = []database.Book{}
		}
		// perUserTotal is counted BEFORE the offset/limit slice above, so it is
		// the number of matches, not the page length. It is exact unless the
		// over-fetch window was exhausted (warned about above), in which case
		// it is a lower bound — Bleve's own total cannot be used here because
		// the per-user predicate is evaluated in Go, outside the index.
		return filtered, perUserTotal, nil
	}

	if len(perUser) > 0 {
		reason := "no_user_context"
		if config.AppConfig.DisablePerUserSearchFilters {
			reason = "disabled_by_config"
		}
		slog.Warn("search: per-user filters dropped, no user context",
			"filters", len(perUser), "reason", reason)
	}

	// bleveTotal is the match count for the whole query, independent of the
	// offset/limit page requested. This value was previously discarded with
	// `_`, and the HTTP layer reported len(page) in its place.
	hits, bleveTotal, err := svc.searchIndex.SearchNative(bleveQ, offset, limit)
	if err != nil {
		return nil, 0, fmt.Errorf("bleve search: %w", err)
	}
	ids := make([]string, 0, len(hits))
	for _, h := range hits {
		ids = append(ids, h.BookID)
	}
	// Batch-hydrate (INIT-4 T3): one store call instead of one per hit.
	// FAIL-OPEN at the call site (spec §C3): a non-nil error from
	// GetBooksByIDs must not fail the whole search page — warn and keep
	// serving the rows hydrated so far, mirroring the old per-hit loop's
	// silent-skip-on-error semantics.
	books, hydrateErr := svc.store.GetBooksByIDs(ids)
	if hydrateErr != nil {
		slog.Warn("search: batch hydrate failed; serving partial page",
			"err", hydrateErr, "hydrated", len(books))
	}
	if books == nil {
		books = []database.Book{}
	}
	return books, int(bleveTotal), nil
}
