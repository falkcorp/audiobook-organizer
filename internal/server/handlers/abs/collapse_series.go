// file: internal/server/handlers/abs/collapse_series.go
// version: 1.0.0
// guid: 9a4d2e71-5c3b-4f86-b0e2-7d19c6a8f354
// last-edited: 2026-09-26

package abs

import (
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/gin-gonic/gin"
)

// ── ?collapseseries=1 on GET /api/libraries/:id/items ───────────────────────
//
// AudioBooth sends collapseseries=1 on the root library list when the user turns
// on "Collapse series" (LibraryPageModel.swift:337, UserPreferences
// collapseSeriesInLibrary). We ignored it until 2026-09-26, so the toggle did
// nothing: every book of every series came back as its own tile.
//
// What real Audiobookshelf does (server/utils/queries/libraryItemsBookFilters.js,
// getFilteredLibraryItems + getCollapseSeriesBooksToExclude, read 2026-09-26):
//
//   - Every series with at least one book in the filtered set is shown as ONE
//     entry: its first book in sequence order (ASC, NULLS LAST) stands in for it
//     and carries a `collapsedSeries` object. Every other book of that series is
//     left out of the list. A series with one matching book still collapses.
//   - Books with no series are listed as usual.
//   - `total` counts the list AFTER the collapse, and paging (limit/page) is cut
//     from the collapsed list, so the client never pages past the end.
//   - Sort is the requested sort over the collapsed list, the representative
//     book's own value standing in for the series. The one exception is the
//     title sort: a collapsed entry sorts by its SERIES NAME (display_title), so
//     "Mistborn" files under M, not under its first book's title.
//   - It is not applied when the list is filtered BY series (filterGroup
//     'series'): the drill-down into a series shows its books.
//   - collapsedSeries is {id, name, nameIgnorePrefix, sequence, numBooks,
//     libraryItemIds}. numBooks counts the series' books; libraryItemIds lists
//     the books of the series that are in the filtered set. AudioBooth decodes
//     exactly these six (API/Models/Book.swift CollapsedSeries; name, numBooks
//     and libraryItemIds are non-optional) and renders the entry as a series
//     card built from them (SeriesCardModel.swift:32).
//
// seriesSequenceList is NOT emitted. Real ABS builds it only on the sub-series
// path (libraryHelpers.handleCollapseSubseries), which runs only for a SERIES
// filter, the one case where this collapse does not apply; AudioBooth's model
// has no such field.
//
// Ours differs from real ABS in one documented way: a book belongs to at most
// one series here (Book.SeriesID), so the "book in two series" case ABS has to
// arbitrate cannot arise. numBooks counts the series' VISIBLE books (the same
// set the Series tab and the series drill-down serve, seriesBooksCached), not
// every stored row: a count naming books the client can never open would
// disagree with the drill-down one tap away.

var collapseLog = logger.New("abs")

// wantsCollapseSeries reports whether the request asked for series collapsing.
// Real ABS tests `req.query.collapseseries === '1'` and nothing else.
func wantsCollapseSeries(c *gin.Context) bool {
	return c.Query("collapseseries") == "1"
}

// collapsedSeriesDTO is the collapsedSeries object real ABS attaches to the
// item that stands in for a series.
type collapsedSeriesDTO struct {
	ID               string   `json:"id"`
	Name             string   `json:"name"`
	NameIgnorePrefix string   `json:"nameIgnorePrefix"`
	Sequence         *string  `json:"sequence"`
	NumBooks         int      `json:"numBooks"`
	LibraryItemIDs   []string `json:"libraryItemIds"`
}

// collapsedItemDTO is a minified library item carrying collapsedSeries.
type collapsedItemDTO struct {
	libraryItemDTO
	CollapsedSeries *collapsedSeriesDTO `json:"collapsedSeries,omitempty"`
}

// collapseGroup is one series collapsed into its representative book.
type collapseGroup struct {
	seriesID int
	// memberSyncIDs are the client ids of the series' books present in the
	// filtered set, in reading order; the first is the representative's.
	memberSyncIDs []string
	// numBooks is the series' visible size, whatever the filter.
	numBooks int
}

// collapseSeriesIDs collapses ordered (the COMPLETE filtered set, already in the
// requested order) so that each series keeps only its first book in reading
// order. It returns the collapsed order and the groups keyed by representative
// book id. ordered is not modified.
//
// series is the cached series grouping (seriesBooksCached), whose bookIDs are in
// reading order: sequence ascending, books without a sequence last, then title,
// which is the order real ABS picks its representative in.
func collapseSeriesIDs(ordered []string, series map[int]seriesBooksBuilt) ([]string, map[string]collapseGroup) {
	inSet := make(map[string]struct{}, len(ordered))
	for _, id := range ordered {
		inSet[id] = struct{}{}
	}
	// represented maps every filtered book that belongs to a series to its
	// series' representative.
	represented := make(map[string]string)
	groups := make(map[string]collapseGroup)
	for seriesID, built := range series {
		rep := ""
		var syncIDs []string
		for i, id := range built.bookIDs {
			if _, ok := inSet[id]; !ok {
				continue
			}
			if rep == "" {
				rep = id
			}
			represented[id] = rep
			syncIDs = append(syncIDs, built.syncIDs[i])
		}
		if rep != "" {
			groups[rep] = collapseGroup{seriesID: seriesID, memberSyncIDs: syncIDs, numBooks: len(built.bookIDs)}
		}
	}
	out := make([]string, 0, len(ordered))
	for _, id := range ordered {
		if rep, ok := represented[id]; ok && rep != id {
			continue
		}
		out = append(out, id)
	}
	return out, groups
}

// sortCollapsedByTitle re-orders a collapsed list for the title sort: a
// collapsed entry sorts by its series name, every other book by its title.
// titleKeys holds each id's database.TitleSortKey. Keys compare with the
// store's own string comparison (database.CompareSortStrings), ties by book id,
// and desc negates the whole comparison, tie-break included, exactly as
// database.SortBooks orders a title sort.
func sortCollapsedByTitle(ids []string, groups map[string]collapseGroup, titleKeys map[string]string, seriesNames map[int]string, desc bool) {
	keys := make(map[string]string, len(ids))
	for _, id := range ids {
		keys[id] = titleKeys[id]
		if g, ok := groups[id]; ok {
			if k := database.TitleSortKey(seriesNames[g.seriesID], nil); k != "" {
				keys[id] = k
			}
		}
	}
	sort.SliceStable(ids, func(a, b int) bool {
		r := database.CompareSortStrings(keys[ids[a]], keys[ids[b]])
		if r == 0 {
			r = strings.Compare(ids[a], ids[b])
		}
		if desc {
			return r > 0
		}
		return r < 0
	})
}

// collapsedItemsPage collapses ordered (the COMPLETE filtered, sorted set),
// re-sorts it by series name for the title sort, and renders the requested page.
// titleKeys must hold every id's database.TitleSortKey when titleSort is set; it
// is unused otherwise.
func (h *Handler) collapsedItemsPage(c *gin.Context, ordered []string, titleKeys map[string]string, titleSort, desc bool, p pageParams, resp *itemsPageResponse) {
	series, err := h.seriesBooksCached()
	if err != nil {
		collapseLog.Warn("abs: collapseseries: series grouping unavailable: %v", err)
		respondError(c, http.StatusInternalServerError, "could not group library items by series")
		return
	}
	out, groups := collapseSeriesIDs(ordered, series)

	names := map[int]string{}
	if len(groups) > 0 {
		seriesIDs := make([]int, 0, len(groups))
		for _, g := range groups {
			seriesIDs = append(seriesIDs, g.seriesID)
		}
		seriesRows, err := h.library.GetSeriesByIDs(seriesIDs)
		if err != nil {
			respondError(c, http.StatusInternalServerError, "could not load series")
			return
		}
		for id, s := range seriesRows {
			if s != nil {
				names[id] = s.Name
			}
		}
	}
	if titleSort {
		sortCollapsedByTitle(out, groups, titleKeys, names, desc)
	}

	resp.Total = len(out)
	start := min(p.Offset, len(out))
	end := min(start+p.Limit, len(out))
	page := out[start:end]

	books, err := h.library.GetBooksByIDs(page)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "could not load library items")
		return
	}
	pos := make(map[string]int, len(page))
	for i, id := range page {
		pos[id] = i
	}
	sort.SliceStable(books, func(a, b int) bool { return pos[books[a].ID] < pos[books[b].ID] })
	views, err := h.loadItemViews(c.Request.Context(), books)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "could not build library items")
		return
	}
	for i := range views {
		item := h.minifiedItem(&views[i])
		if g, ok := groups[views[i].Book.ID]; ok {
			resp.Results = append(resp.Results, collapsedItemDTO{
				libraryItemDTO:  item,
				CollapsedSeries: collapsedSeries(views[i].Book, g, names[g.seriesID]),
			})
			continue
		}
		resp.Results = append(resp.Results, item)
	}
	respondJSON(c, http.StatusOK, *resp)
}

// collapsedSeries renders the collapsedSeries object for rep, the book standing
// in for group's series. libraryItemIds come from the series grouping, which
// resolved every member's client id when it was built, so rendering reads no
// store.
func collapsedSeries(rep *database.Book, g collapseGroup, name string) *collapsedSeriesDTO {
	var seq *string
	if rep.SeriesSequence != nil {
		s := strconv.Itoa(*rep.SeriesSequence)
		seq = &s
	}
	return &collapsedSeriesDTO{
		ID:               strconv.Itoa(g.seriesID),
		Name:             name,
		NameIgnorePrefix: ignorePrefix(name),
		Sequence:         seq,
		NumBooks:         g.numBooks,
		LibraryItemIDs:   append([]string{}, g.memberSyncIDs...),
	}
}

// collapsedLibraryItems serves the UNFILTERED /items list with collapseseries=1.
// The collapse needs the whole ordered set, so this reads every visible summary
// (as clientSortedItems already does for handler-side sorts) instead of one page.
func (h *Handler) collapsedLibraryItems(c *gin.Context, p pageParams, resp *itemsPageResponse) {
	desc := c.Query("desc") == "1"
	var summaries []database.BookSummary
	var err error
	key := absHandlerSort(c.Query("sort"))
	if key != "" {
		base := absItemFilterBase()
		base.SortBy = "title"
		summaries, err = h.visibleBookSummaries(base)
	} else {
		summaries, err = h.visibleBookSummaries(absItemFilter(c))
	}
	if err != nil {
		respondError(c, http.StatusInternalServerError, "could not list library items")
		return
	}
	ids := make([]string, len(summaries))
	titles := make(map[string]string, len(summaries))
	for i := range summaries {
		ids[i] = summaries[i].ID
		titles[summaries[i].ID] = database.TitleSortKey(summaries[i].Title, summaries[i].OriginalFilename)
	}
	if key != "" {
		ids, err = h.orderIDsByClientSort(c, key, ids, desc)
		if err != nil {
			respondError(c, http.StatusInternalServerError, "could not sort library items")
			return
		}
	}
	titleSort := key == "" && absSortField(c.Query("sort")) == "title"
	h.collapsedItemsPage(c, ids, titles, titleSort, desc, p, resp)
}

// collapsedFilteredItems serves a ?filter= list (any group but series) with
// collapseseries=1. ids is the group's complete id set in the group's order;
// it is shared with the cached grouping and is never modified here.
//
// The order before the collapse mirrors filteredItems: a handler-side sort over
// the ids, a store field sort over the loaded books, or the group's own order.
func (h *Handler) collapsedFilteredItems(c *gin.Context, ids []string, p pageParams, resp *itemsPageResponse) {
	desc := c.Query("desc") == "1"
	if key := absHandlerSort(c.Query("sort")); key != "" {
		ordered, err := h.orderIDsByClientSort(c, key, ids, desc)
		if err != nil {
			respondError(c, http.StatusInternalServerError, "could not sort library items")
			return
		}
		h.collapsedItemsPage(c, ordered, nil, false, desc, p, resp)
		return
	}
	sortField := absSortField(c.Query("sort"))
	if sortField == "" {
		h.collapsedItemsPage(c, append([]string(nil), ids...), nil, false, desc, p, resp)
		return
	}
	books, err := h.library.GetBooksByIDs(ids)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "could not load library items")
		return
	}
	database.SortBooks(books, sortField, !desc)
	ordered := make([]string, len(books))
	titles := make(map[string]string, len(books))
	for i := range books {
		ordered[i] = books[i].ID
		titles[books[i].ID] = database.TitleSortKey(books[i].Title, books[i].OriginalFilename)
	}
	h.collapsedItemsPage(c, ordered, titles, sortField == "title", desc, p, resp)
}
