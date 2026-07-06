// file: internal/audiobooks/service_query.go
// version: 1.2.0
// guid: c5f9d4e3-f6a7-8b90-ac1d-2e3f4a5b6c7d
// last-edited: 2026-07-05

package audiobooks

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/search"
)

// GetAudiobooks retrieves audiobooks with optional filtering.
// Supports search, author_id, series_id, is_primary_version, and library_state filters.
func (svc *AudiobookService) GetAudiobooks(ctx context.Context, limit int, offset int, search string, authorID *int, seriesID *int, filters ...ListFilters) ([]database.Book, error) {
	if svc.store == nil {
		return nil, fmt.Errorf("database not initialized")
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
	// "title" sort can be pushed down to memdb (sorted radix index) — every
	// other sort key still needs the heavy in-memory path. This is the
	// dominant case (library page default), so the pushdown matters.
	titleSortPushdownable := f.SortBy == "title"
	heavySorting := hasSorting && !titleSortPushdownable
	hasPerUser := len(f.PerUserFilters) > 0 && f.UserID != ""
	hasFingerprintingFilters := f.FingerprintStatus != "" || f.CoveragePercentMin != nil || f.CoveragePercentMax != nil
	// "Heavy" post-filters require fetching all rows to apply in Go.
	// IsPrimaryVersion is intentionally NOT in this set because the store
	// (memdb-backed) can push it down via an indexed iteration — fetching
	// all 68K rows to satisfy ?is_primary_version=true was the prod
	// "library spins forever" bug.
	hasHeavyPostFilters := f.LibraryState != "" || f.Tag != "" || len(f.Tags) > 0 || len(f.FieldFilters) > 0 || hasPerUser || heavySorting || hasFingerprintingFilters
	hasPostFilters := hasHeavyPostFilters || f.IsPrimaryVersion != nil || titleSortPushdownable

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

	// Apply filters in order of precedence
	if search != "" {
		if svc.searchIndex != nil {
			books, err = svc.searchWithBleve(search, limit, offset)
		} else {
			books, err = svc.store.SearchBooks(search, limit, offset)
		}
	} else if authorID != nil {
		books, err = svc.store.GetBooksByAuthorID(*authorID)
	} else if seriesID != nil {
		books, err = svc.store.GetBooksBySeriesID(*seriesID)
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
				return cached, nil
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
				// Non-title sorts need the post-filter pass for pagination
				// (sort happens after paginate — pre-existing design). Fetch all
				// filtered books so the service can slice after sorting.
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
				// When the store pushed down AND the walker already handled
				// pagination (no heavy sort), the post-filter pass would
				// double-apply pagination. Skip it. For heavy sorts, keep
				// hasPostFilters = true so the pagination block runs after
				// applySorting recombines the filtered set.
				if didPushdown && !heavySorting {
					hasPostFilters = false
				}
			} else {
				summaries, _, sErr := svc.summariesPushdownFiltered(storeLimit, storeOffset, database.BookSummaryFilter{})
				if sErr == nil && summaries != nil {
					books = bookSummariesToBooks(summaries)
				}
			}
		}
	}

	if err != nil {
		return nil, err
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
					return nil, tagErr
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
			fieldFiltered := make([]database.Book, 0, len(filtered))
			for i := range filtered {
				b := filtered[i]
				if matchesFieldFiltersWithStrippedFallback(&b, cheapFF, strippedFF, fetchFull, &pebbleLookups, warnFn) {
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

		// Apply pagination after filtering
		if offset > 0 && offset < len(filtered) {
			filtered = filtered[offset:]
		} else if offset >= len(filtered) {
			filtered = nil
		}
		if limit > 0 && limit < len(filtered) {
			filtered = filtered[:limit]
		}
		books = filtered
	}

	// Apply sorting after all filtering but before returning
	applySorting(books, f)

	// Ensure we never return null - always return empty array
	if books == nil {
		books = []database.Book{}
	}

	return books, nil
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
	books, err := svc.store.GetAllBooks(0, 0)
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

// searchWithBleve parses the query via the DSL, translates to a
// Bleve native query, and returns the matching books. Per-user
// filters produced by the translator (read_status / progress_pct /
// last_played) are currently dropped here — the library-list route
// doesn't carry user state. Spec 3.6 will wire them back in at the
// handler layer once the user context is plumbed.
//
// Falls back to an empty slice (not nil) on zero matches so callers
// get consistent JSON shape.
func (svc *AudiobookService) searchWithBleve(query string, limit, offset int) ([]database.Book, error) {
	ast, err := search.ParseQuery(query)
	if err != nil {
		// Parser failure: fall back to the substring search path so
		// users still see results for simple queries the DSL parser
		// rejects (e.g. punctuation-heavy book titles).
		return svc.store.SearchBooks(query, limit, offset)
	}
	bleveQ, _, err := search.Translate(ast)
	if err != nil {
		return svc.store.SearchBooks(query, limit, offset)
	}
	hits, _, err := svc.searchIndex.SearchNative(bleveQ, offset, limit)
	if err != nil {
		return nil, fmt.Errorf("bleve search: %w", err)
	}
	books := make([]database.Book, 0, len(hits))
	for _, h := range hits {
		b, _ := svc.store.GetBookByID(h.BookID)
		if b != nil {
			books = append(books, *b)
		}
	}
	return books, nil
}
