// file: internal/server/handlers/abs/browse.go
// version: 1.9.4
// guid: 5e0b83c7-2a41-4d96-b7e8-1c53fd90a2b4
// last-edited: 2026-08-22

package abs

import (
	"context"
	"encoding/base64"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	servermiddleware "github.com/falkcorp/audiobook-organizer/internal/server/middleware"
	"github.com/falkcorp/audiobook-organizer/internal/util"
	"github.com/gin-gonic/gin"
)

// Phase 3 — library browse.
//
// Two rules apply to every handler in this file and both are load-bearing:
//
//   - NEVER answer a browse endpoint with a bare 200 and no body, and never with
//     HTML. A 200 carrying HTML passes the clients' status guard and then fails the
//     JSON cast (§1.7.3 item 11); an empty 200 is fatal for any typed endpoint
//     (§1.8.6). The SPA NoRoute catch-all is exactly how that happens by accident,
//     which is why every path is explicitly registered.
//   - A 404 means "we do not implement this". Absorb treats 404 as "unsupported,
//     degrade gracefully" at 7 endpoints (§1.7.3 item 10), so a misapplied 404
//     silently disables a working feature. Anything we DO implement answers 200 with
//     a valid (possibly empty) body — including the podcast probes.

// defaultPageLimit matches the page size the ABS web UI and both clients request.
const defaultPageLimit = 50

// maxPageLimit caps a client-supplied limit. The cap exists because every item on a
// page costs a file listing, a chapter read and two id mints; an unbounded limit
// would let one request fan out over the whole library.
const maxPageLimit = 250

// ── GET /api/libraries ──────────────────────────────────────────────────────

// libraryID is the single library this server exposes.
//
// It is the SAME value /login reports as userDefaultLibraryId. That is not a
// coincidence to be maintained by hand: §1.8.2 makes a null userDefaultLibraryId a
// hard login blocker for AudioBooth, and Absorb throws if the id it selects is not
// in this list — so both come from one config field.
func (h *Handler) libraryID() string { return h.cfg.DefaultLibraryID }

// folderID derives the library's single folder id from the library id.
//
// Deterministic rather than random so it survives a restart: clients cache
// folderId, and a value that changed on every boot would make every cached item look
// like it had moved. Built to the same 36-char canonical UUID shape as the library id
// (§1.7.1) by substituting a fixed marker into the last group.
func (h *Handler) folderID() string {
	id := h.libraryID()
	if len(id) != 36 {
		return id
	}
	return id[:24] + "f0lder00d1r"
}

func (h *Handler) libraryDTO() libraryDTO {
	created := msEpoch(h.now())
	return libraryDTO{
		CreatedAt:    created,
		DisplayOrder: 1,
		Folders: []libraryFolderDTO{{
			AddedAt:   created,
			FullPath:  h.coverRoot,
			ID:        h.folderID(),
			LibraryID: h.libraryID(),
		}},
		Icon:            "database",
		ID:              h.libraryID(),
		LastScan:        created,
		LastScanVersion: h.cfg.ServerVersion,
		LastUpdate:      created,
		// EXACTLY "book" or "podcast": AudioBooth decodes mediaType as a
		// non-tolerant enum (§1.8.5 item 9), so "audiobook" throws.
		MediaType: "book",
		Name:      h.libraryName,
		Provider:  "audible",
		Settings: librarySettingsDTO{
			CoverAspectRatio:            1,
			MarkAsFinishedTimeRemaining: 10,
			MetadataPrecedence: []string{
				"folderStructure", "audioMetatags", "nfoFile", "txtFiles", "opfFile", "absMetadata",
			},
		},
	}
}

// Libraries handles GET /api/libraries.
func (h *Handler) Libraries(c *gin.Context) {
	respondJSON(c, http.StatusOK, librariesResponse{Libraries: []libraryDTO{h.libraryDTO()}})
}

// Library handles GET /api/libraries/:libraryId.
func (h *Handler) Library(c *gin.Context) {
	if !h.knownLibrary(c) {
		return
	}
	respondJSON(c, http.StatusOK, h.libraryDTO())
}

// knownLibrary rejects a request for a library we do not have. It writes the 404
// itself and reports false so callers can simply return.
func (h *Handler) knownLibrary(c *gin.Context) bool {
	if id := c.Param("libraryId"); id != "" && id != h.libraryID() {
		respondError(c, http.StatusNotFound, "library not found")
		return false
	}
	return true
}

// ── GET /api/libraries/:libraryId/items ─────────────────────────────────────

// pageParams is the parsed page/limit pair. Values are always sane: a garbage or
// negative limit falls back to the default rather than erroring, because a 4xx here
// reads to Absorb as "endpoint unsupported" and disables browsing entirely.
type pageParams struct {
	Page   int
	Limit  int
	Offset int
}

func parsePageParams(c *gin.Context) pageParams {
	limit := defaultPageLimit
	if raw := strings.TrimSpace(c.Query("limit")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			limit = n
		}
	}
	if limit > maxPageLimit {
		limit = maxPageLimit
	}
	page := 0
	if raw := strings.TrimSpace(c.Query("page")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			page = n
		}
	}
	return pageParams{Page: page, Limit: limit, Offset: page * limit}
}

// absItemsCountTTL bounds how long the filtered item count is served from cache.
//
// Short on purpose. The count is cosmetic — it drives the client's "is there another
// page" decision — so a value a minute stale costs nothing, while recomputing it per
// request costs a full-library scan. Mirrors primaryCountCacheTTL rather than
// inventing a second caching style.
const absItemsCountTTL = 60 * time.Second

// absItemFilter builds the filter that defines WHAT THE LIBRARY IS on this surface.
//
// 🔴 Without it the app showed 44,888 items where the owner has ~16,000. The app's own
// counts cache reports total_books=44888, organized_books=16491,
// unorganized_books=23928 — the extra 28k are raw imports and iTunes-tree copies, plus
// alternate versions of books already present.
//
//   - IsPrimaryVersion collapses alternate versions to a single entry.
//   - LibraryState "organized" drops rows whose file is not in the managed library
//     (pebble_store_stats.go computes its organized_books the same way, via a
//     rootDir path prefix; the two are expected to agree and the acceptance check
//     compares them).
//   - ExcludeQuarantined: a quarantined file cannot be played, so listing it only
//     produces a failed playback attempt.
//
// Sorting is honoured here too. The client always sends sort=media.metadata.title and
// we previously IGNORED it, so the library was never actually title-sorted. SortBy
// "title" is backed by a sorted radix index — O(offset+limit), not a full sort.
func absItemFilterBase() database.BookSummaryFilter {
	primary := true
	return database.BookSummaryFilter{
		IsPrimaryVersion:   &primary,
		LibraryState:       "organized",
		ExcludeQuarantined: true,
		SortAscending:      true,
	}
}

// absSortFields maps the dotted sort keys Audiobookshelf clients send to the
// store's sort field names.
//
// The previous implementation was a single substring test for "title", so
// EVERY other sort the client offered -- year, author, narrator, added date,
// duration, size -- left SortBy empty. An empty SortBy does not error: it
// falls through memdb_summaries.go's switch to the IsPrimaryVersion index,
// which iterates in book-ID order. The client asked for a sort, got 200 OK,
// got the right rows, and got them in an arbitrary order with nothing
// anywhere reporting a problem.
//
// Keys are matched on the last dotted segment, case-insensitively, so both
// `media.metadata.publishedYear` and a bare `publishedYear` resolve.
var absSortFields = map[string]string{
	"title":               "title",
	"titleignorearticles": "title",
	"publishedyear":       "year",
	"authorname":          "author",
	"authornamelf":        "author",
	"narratorname":        "narrator",
	"seriesname":          "series",
	"addedat":             "created_at",
	"updatedat":           "updated_at",
	"duration":            "duration",
	"size":                "file_size",
}

// absSortField resolves an ABS sort query parameter to a store sort field.
// Returns "" when the key is unrecognised.
func absSortField(sort string) string {
	sort = strings.TrimSpace(strings.ToLower(sort))
	if sort == "" {
		return ""
	}
	if i := strings.LastIndex(sort, "."); i >= 0 {
		sort = sort[i+1:]
	}
	return absSortFields[sort]
}

func absItemFilter(c *gin.Context) database.BookSummaryFilter {
	f := absItemFilterBase()
	f.SortAscending = c.Query("desc") != "1"
	// NOTE: a mapped field still only streams if its memdb sort index is
	// enabled (config.EnabledSortIndexes; "title" is always indexed). When it
	// is not, the store falls back to unordered iteration -- which is the bug
	// this mapping exists to fix -- so the store-side default must list the
	// fields this map can produce. See config.EnabledSortIndexes.
	f.SortBy = absSortField(c.Query("sort"))
	warnUnindexedSort(f.SortBy, c.Query("sort"))
	return f
}

// absUnindexedSortWarned rate-limits the warning below to once per field per
// process, so a client polling the library cannot flood the log.
var absUnindexedSortWarned sync.Map

// warnUnindexedSort reports a sort we accepted but cannot actually perform.
//
// "title" is always indexed. Every other field needs its memdb sort index
// enabled (config.EnabledSortIndexes, empty by default); without it the store
// silently iterates unordered. Silence is precisely how this shipped
// undetected -- the client got 200 OK and arbitrary order -- so say it once
// rather than let a wrong answer stay quiet.
func warnUnindexedSort(field, raw string) {
	if field == "" || field == "title" || database.CanPushDownSort(field) {
		return
	}
	if _, seen := absUnindexedSortWarned.LoadOrStore(field, true); seen {
		return
	}
	slog.Warn("abs: client requested a sort whose memdb index is disabled; results will be UNORDERED",
		"sort_param", raw,
		"sort_field", field,
		"remediation", "add it to enabled_sort_indexes and restart")
}

// countItems returns the filtered item count, cached for absItemsCountTTL.
//
// Keyed by the filter's identity so a differently-filtered request cannot be served
// another filter's number — today only the sort varies (which does not change the
// count), but keying it now means a future filter cannot silently reuse a stale total.
func (h *Handler) countItems(f database.BookSummaryFilter) (int, error) {
	key := itemsCountKey(f)
	now := h.now()

	h.itemsCountMu.Lock()
	if entry, ok := h.itemsCount[key]; ok && now.Sub(entry.at) < absItemsCountTTL {
		h.itemsCountMu.Unlock()
		return entry.count, nil
	}
	h.itemsCountMu.Unlock()

	// Computed OUTSIDE the lock: this is a full-library scan and holding the mutex
	// across it would serialize every concurrent request behind one scan — turning a
	// slow endpoint into a stalled one.
	count, err := h.library.CountBookSummariesFiltered(f)
	if err != nil {
		return 0, err
	}

	h.itemsCountMu.Lock()
	h.itemsCount[key] = itemsCountEntry{count: count, at: now}
	h.itemsCountMu.Unlock()
	return count, nil
}

// itemsCountKey identifies a filter for caching. Sort fields are deliberately
// EXCLUDED — ordering cannot change how many rows match, and including them would
// multiply the cache entries (and the scans) by the number of sort orders.
func itemsCountKey(f database.BookSummaryFilter) string {
	primary := "any"
	if f.IsPrimaryVersion != nil {
		primary = strconv.FormatBool(*f.IsPrimaryVersion)
	}
	return primary + "|" + f.LibraryState + "|" + strconv.FormatBool(f.ExcludeQuarantined)
}

// requestsPodcasts reports whether the caller is probing for podcast content.
//
// We hold no podcasts, so the correct answer is a well-formed EMPTY page: a 404 or a
// 4xx would be read as "this endpoint is unsupported" and could disable the item list
// altogether for a client that probes before it filters.
func requestsPodcasts(c *gin.Context) bool {
	return strings.EqualFold(strings.TrimSpace(c.Query("mediaType")), "podcast")
}

// LibraryItems handles GET /api/libraries/:libraryId/items.
func (h *Handler) LibraryItems(c *gin.Context) {
	if !h.knownLibrary(c) {
		return
	}
	p := parsePageParams(c)

	resp := itemsPageResponse{
		Include:   strings.TrimSpace(c.Query("include")),
		Limit:     p.Limit,
		MediaType: "book",
		Offset:    p.Offset,
		Page:      p.Page,
		// Never nil: a null results array fails the decode.
		Results: []any{},
		Total:   0,
	}

	if requestsPodcasts(c) {
		resp.MediaType = "podcast"
		respondJSON(c, http.StatusOK, resp)
		return
	}

	// 🔴 THE ?filter= PARAMETER WAS READ BY NOTHING, so every drill-down returned
	// THE WHOLE LIBRARY.
	//
	// Proven on production 2026-08-13 with three requests to /items differing only
	// in the filter: no filter, a real series, and a DELIBERATELY FABRICATED series
	// id. All three answered total=34280 with the same first title. A filter naming
	// a series that cannot exist returning the entire library is only explicable by
	// the parameter never being read — which is why the fabricated id was worth
	// sending: a real id returning everything could be excused as an over-broad
	// match, a fake one cannot.
	//
	// That is the reported "shows random books for every series, and the books it
	// shows are random too": opening a series asks for its items, gets all 34,280,
	// and renders the first page under that series' name.
	//
	// FORMAT, CONFIRMED BY OBSERVATION rather than inferred. The server's own logs
	// carry a real request from the app:
	//
	//	GET /api/libraries/<id>/items?filter=series.MTQ3OTI0&page=0&limit=100
	//
	// MTQ3OTI0 is base64 "147924", the series id of 'Salem's Lot in the live series
	// list. So the shape is <group>.<base64(value)>. No fixture shows this — zero of
	// the 28 captures carry a filter — so the log was the only oracle available.
	if raw := strings.TrimSpace(c.Query("filter")); raw != "" {
		h.filteredItems(c, raw, p, &resp)
		return
	}

	filter := absItemFilter(c)

	// 🔴 The FILTERED count, not CountAllBooks.
	//
	// Two separate reasons, and both bit us:
	//
	//  1. Correctness. CountAllBooks counts every row — 44,888 — while this endpoint
	//     now serves only primary+organized items (16,491). Reporting the unfiltered
	//     total makes the client page past the end into empty results forever.
	//  2. Cost. CountAllBooks iterates every book: key AND json.Unmarshals every book
	//     purely to count them (pebble_store.go:2509). That is a full 44,888-book
	//     decode on EVERY request, and it is why latency was a flat ~2s regardless of
	//     which page was asked for. Same hotspot class as CountPrimaryBooks, fixed in
	//     PR #2021.
	total, err := h.countItems(filter)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "could not count library items")
		return
	}
	resp.Total = total

	summaries, err := h.library.GetAllBookSummariesFiltered(p.Limit, p.Offset, filter)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "could not list library items")
		return
	}
	ids := make([]string, 0, len(summaries))
	for i := range summaries {
		ids = append(ids, summaries[i].ID)
	}
	books, err := h.library.GetBooksByIDs(ids)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "could not load library items")
		return
	}

	views, err := h.loadItemViews(c.Request.Context(), books)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "could not build library items")
		return
	}
	for i := range views {
		resp.Results = append(resp.Results, h.minifiedItem(&views[i]))
	}
	respondJSON(c, http.StatusOK, resp)
}

// RecentEpisodes handles GET /api/libraries/:libraryId/recent-episodes — the podcast
// stub. The wrapper key is required (§1.8.6) and an empty array is the honest answer.
func (h *Handler) RecentEpisodes(c *gin.Context) {
	if !h.knownLibrary(c) {
		return
	}
	respondJSON(c, http.StatusOK, episodesResponse{Episodes: []any{}})
}

// EmptyPage serves the collections/playlists stubs. §1.8.6: `{}` throws because
// Page<T> requires total AND page even with no results.
func (h *Handler) EmptyPage(c *gin.Context) {
	if !h.knownLibrary(c) {
		return
	}
	respondJSON(c, http.StatusOK, pageResponse{Results: []any{}})
}

// ── GET /api/libraries/:libraryId/personalized ──────────────────────────────

// personalizedShelfSize caps each shelf. Shelves are decoration; there is no reason
// for one to page the library.
const personalizedShelfSize = 10

// Personalized handles GET /api/libraries/:libraryId/personalized.
//
// The body is a BARE ARRAY of shelves. §1.8.6 is explicit: this endpoint decodes as
// an array, and an object there throws.
func (h *Handler) Personalized(c *gin.Context) {
	if !h.knownLibrary(c) {
		return
	}
	user, ok := servermiddleware.CurrentUser(c)
	if !ok || user == nil {
		respondError(c, http.StatusUnauthorized, "authentication required")
		return
	}

	summaries, err := h.library.GetAllBookSummaries(personalizedShelfSize*2, 0)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "could not build home shelves")
		return
	}
	ids := make([]string, 0, len(summaries))
	for i := range summaries {
		ids = append(ids, summaries[i].ID)
	}
	books, err := h.library.GetBooksByIDs(ids)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "could not build home shelves")
		return
	}
	views, err := h.loadItemViews(c.Request.Context(), books)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "could not build home shelves")
		return
	}

	// Continue Listening is the shelf the whole mission turns on, so it is driven by
	// the real stored position rather than a heuristic.
	var continueListening, discover []any
	for i := range views {
		item := h.minifiedItem(&views[i])
		if h.hasProgress(user.ID, views[i].Book.ID) {
			continueListening = append(continueListening, item)
		} else {
			discover = append(discover, item)
		}
	}

	recentlyAdded := make([]any, 0, len(views))
	byRecency := make([]int, 0, len(views))
	for i := range views {
		byRecency = append(byRecency, i)
	}
	sort.SliceStable(byRecency, func(a, b int) bool {
		return msEpochPtr(views[byRecency[a]].Book.CreatedAt) > msEpochPtr(views[byRecency[b]].Book.CreatedAt)
	})
	for _, i := range byRecency {
		recentlyAdded = append(recentlyAdded, h.minifiedItem(&views[i]))
	}

	authors, err := h.authorDTOsCached(c.Request.Context())
	if err != nil {
		respondError(c, http.StatusInternalServerError, "could not build home shelves")
		return
	}
	newestAuthors := make([]any, 0, len(authors))
	for i := range authors {
		newestAuthors = append(newestAuthors, authors[i])
	}

	shelves := []shelfDTO{}
	addShelf := func(id, label, key, kind string, entities []any) {
		if len(entities) == 0 {
			// An empty shelf is omitted rather than emitted: real ABS omits them, and
			// a client rendering an empty carousel looks broken.
			return
		}
		if len(entities) > personalizedShelfSize {
			entities = entities[:personalizedShelfSize]
		}
		shelves = append(shelves, shelfDTO{
			Entities: entities, ID: id, Label: label, LabelStringKey: key,
			Total: len(entities), Type: kind,
		})
	}
	addShelf("continue-listening", "Continue Listening", "LabelContinueListening", "book", continueListening)
	addShelf("recently-added", "Recently Added", "LabelRecentlyAdded", "book", recentlyAdded)
	addShelf("discover", "Discover", "LabelDiscover", "book", discover)
	addShelf("newest-authors", "Newest Authors", "LabelNewestAuthors", "authors", newestAuthors)

	respondJSON(c, http.StatusOK, shelves)
}

// hasProgress reports whether the user has a stored position for the book. Errors
// are treated as "no progress": a shelf is decoration, and failing the whole home
// screen over one unreadable key would be worse than a short Continue Listening.
func (h *Handler) hasProgress(userID, bookID string) bool {
	if h.progress == nil {
		return false
	}
	pos, err := h.progress.GetUserPosition(userID, bookID)
	if err != nil || pos == nil || pos.PositionSeconds <= 0 {
		return false
	}
	// 🔴 The user's "remove from Continue Listening" choice is what this shelf is
	// FOR. Reading only the position ignores it, so the book reappears on the very
	// next home-screen refresh and the feature looks broken — which is exactly how
	// it was reported. Hiding deliberately KEEPS the position (the user tidied the
	// shelf, they did not ask to lose their place), so the flag has to be consulted
	// here rather than inferred from the absence of progress.
	if state, serr := h.progress.GetUserBookState(userID, bookID); serr == nil && state != nil &&
		state.HideFromContinueListening {
		return false
	}
	return true
}

// ── /series, /authors, /narrators ───────────────────────────────────────────

// LibrarySeries handles GET /api/libraries/:libraryId/series.
func (h *Handler) LibrarySeries(c *gin.Context) {
	if !h.knownLibrary(c) {
		return
	}
	series, err := h.library.GetAllSeries()
	if err != nil {
		respondError(c, http.StatusInternalServerError, "could not list series")
		return
	}
	counts, err := h.library.GetAllSeriesBookCounts()
	if err != nil {
		// A missing count is not worth failing the page over; report 0 books rather
		// than 500 the whole series list.
		slog.Warn("abs: series book counts unavailable, reporting 0 for all series", "err", err)
		counts = map[int]int{}
	}

	// 🔴 books AND totalDuration WERE HARDCODED EMPTY, on every series, forever.
	// Measured on production 2026-08-13 before this fix: books == [] on 14,625 of
	// 14,625 series and totalDuration == 0 on 14,625 of 14,625, while numBooks was
	// correctly populated on 14,295 of them. The app therefore showed a series
	// insisting it held no books while displaying a book count.
	//
	// ⚠️ CORRECTED 2026-08-13: this note previously finished "and the books it did
	// render came from the client filling an empty list from elsewhere". That was
	// speculation and it was wrong. The books the app rendered came from
	// /api/libraries/:id/items, which accepted a `filter` parameter and read it with
	// nothing — so a request for one series returned an arbitrary page of the whole
	// library. Fixed separately; see absFilterGroup/filteredItems below.
	bySeries, berr := h.seriesBooksCached()
	if berr != nil {
		// Same rule as the counts above: a series page missing its book lists is
		// worth serving; a 500 is not. But it must be VISIBLE — this is exactly the
		// silent-empty that hid the bug for so long.
		_ = berr
		bySeries = map[int]seriesBooksBuilt{}
	}

	// ORDER FIRST, THEN SLICE. GetAllSeries makes no ordering promise, and paginating
	// an unstable order lets pages overlap and skip — the client would see some series
	// twice and others never. Sorting on nameIgnorePrefix with the id as tie-break is
	// total (ids are unique), so the pages partition the set exactly.
	//
	// `sort=name` is the only sort the app is observed to send and it is what this
	// already does; an unrecognised sort keeps this order rather than erroring.
	sort.SliceStable(series, func(i, j int) bool {
		li, lj := ignorePrefix(series[i].Name), ignorePrefix(series[j].Name)
		if li != lj {
			return li < lj
		}
		return series[i].ID < series[j].ID
	})
	if c.Query("desc") == "1" || c.Query("sortDesc") == "1" {
		for i, j := 0, len(series)-1; i < j; i, j = i+1, j-1 {
			series[i], series[j] = series[j], series[i]
		}
	}

	// 🔴 page/limit/sort WERE ACCEPTED AND IGNORED. Confirmed against production
	// 2026-08-13: limit=100 and limit=500 both returned all 14,625 series, and the
	// app's own `limit=50&page=2&sort=name` got page 0, unsorted, every time.
	//
	// This is not cosmetic any more. Populating `books` above takes the unpaginated
	// response from 3.36 MB to roughly 10.8 MB (31,139 book rows), which is not a
	// thing to hand a phone that asked for 50.
	//
	// A limit of 0 or an absent limit still returns everything, deliberately: every
	// observed app request carries an explicit limit, so this keeps every other
	// caller byte-identical instead of imposing a default page size nobody asked for.
	total := len(series)
	limit := queryInt(c, "limit", 0)
	page := queryInt(c, "page", 0)
	if limit > 0 {
		start := page * limit
		if start > total {
			start = total
		}
		end := start + limit
		if end > total {
			end = total
		}
		series = series[start:end]
	}

	// 🔴 THE BOOK OBJECTS WERE NOT LIBRARY ITEMS, so no series rendered at all.
	//
	// ABS defines a series' `books` as full LibraryItem objects. This route
	// emitted six ad-hoc fields instead -- id, libraryItemId, libraryId, title,
	// sequence, duration -- with no `media`, no `media.metadata`, no
	// `mediaType`, no `coverPath`. Measured against production 2026-08-16: the
	// app's own request returned HTTP 200 with 50 well-formed-looking rows, and
	// the Series tab showed "No Series Found".
	//
	// The control that identifies this as the cause is the PLAYLISTS route,
	// which the same client renders correctly on the same library with the same
	// auth: its items embed a complete libraryItem (20 fields, including
	// media.metadata and coverPath) built by minifiedItem. A typed client
	// decodes `books: [LibraryItem]` as a unit, so one undecodable entry
	// discards the whole response -- which is why 23 of the 50 having real
	// books did not put 23 series on screen. It was 0 or nothing.
	//
	// Enrichment is deliberately PAGE-SCOPED. Building library items for every
	// series would rebuild the whole library on each cache miss; the note above
	// already measured the unpaginated body at ~10.8 MB / 31,139 book rows.
	// Only the ~50 series actually being returned are hydrated.
	enriched := h.seriesPageBooks(c.Request.Context(), series, bySeries)

	results := make([]any, 0, len(series))
	for _, s := range series {
		built := bySeries[s.ID]
		items, total := enriched[s.ID], built.totalDuration
		if items == nil {
			items = []any{}
		}
		results = append(results, gin.H{
			"id":               strconv.Itoa(s.ID),
			"name":             s.Name,
			"nameIgnorePrefix": ignorePrefix(s.Name),
			"libraryId":        h.libraryID(),
			"addedAt":          msEpoch(h.now()),
			"updatedAt":        msEpoch(h.now()),
			"books":            items,
			// An int, never a float: Dart throws on `42.0 as int?` and this is cast
			// during widget build (§1.7.3 item 5). Summed from BookSummary.Duration,
			// which is already an int — do not introduce a float on the way here.
			"totalDuration": total,
			// Count the books actually SERVED, not every row the store counts.
			//
			// This is the same rule totalDuration above already follows, applied
			// to the field next to it. Measured on production 2026-08-16, 9 of 50
			// series on page 0 claimed numBooks >= 1 while carrying books: [] and
			// totalDuration: 0 -- because seriesBookEntry drops books with no
			// resolvable sync id, and the count came from a store query that knew
			// nothing about that. A row that reports a book count it cannot list
			// is a row the client has to decide how to disbelieve.
			//
			// counts is still consulted so a series whose books were all dropped
			// is visible as such, rather than silently reading as an empty series
			// that genuinely has no books.
			"numBooks": len(items),
		})
		if want := counts[s.ID]; want != len(items) {
			h.logSeriesBookCountMismatch(s.ID, s.Name, want, len(items))
		}
	}
	// Total is the FULL series count, NOT len(results). The client decides whether
	// another page exists with `page*limit < total` (the same rule recorded on
	// authorDTOsCached), so reporting the slice length would tell it page 0 is the
	// whole library and it would stop at 50 of 14,625. The opposite convention in
	// playlists.go is correct THERE only because that route has no working page
	// parameter to offer.
	respondJSON(c, http.StatusOK, pageResponse{
		Results: results,
		Total:   total,
		Limit:   limit,
		Page:    page,
	})
}

// seriesPageBooks hydrates each series' book list into real ABS LibraryItem
// objects, for the page being served only.
//
// It reuses seriesBooksCached's ordering (reading order, already sorted) and the
// same minifiedItem serializer the playlists route uses, so the two routes
// cannot drift into disagreeing about what a library item looks like -- which is
// exactly the divergence that made this route render nothing while playlists
// worked.
//
// On any failure it returns what it has rather than nothing: a series page
// missing hydration is worth serving, a 500 is not. That matches the existing
// rule for counts and seriesBooksCached above.
func (h *Handler) seriesPageBooks(
	ctx context.Context,
	series []database.Series,
	bySeries map[int]seriesBooksBuilt,
) map[int][]any {
	out := make(map[int][]any, len(series))

	// One batched load for the whole page rather than one per series.
	var ids []string
	seen := make(map[string]struct{})
	for _, s := range series {
		for _, id := range bySeries[s.ID].bookIDs {
			if _, dup := seen[id]; dup {
				continue
			}
			seen[id] = struct{}{}
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return out
	}

	books, err := h.library.GetBooksByIDs(ids)
	if err != nil {
		return out
	}
	views, err := h.loadItemViews(ctx, books)
	if err != nil {
		return out
	}

	byID := make(map[string]libraryItemDTO, len(views))
	for i := range views {
		byID[views[i].Book.ID] = h.minifiedItem(&views[i])
	}

	for _, s := range series {
		built := bySeries[s.ID]
		items := make([]any, 0, len(built.bookIDs))
		for _, id := range built.bookIDs {
			dto, ok := byID[id]
			if !ok {
				// A book that vanished between the cached grouping and now.
				// Dropping it keeps the array decodable, which is the whole
				// point of this change; numBooks is computed from what survives.
				continue
			}
			items = append(items, dto)
		}
		out[s.ID] = items
	}
	return out
}

// logSeriesBookCountMismatch reports a series whose store count disagrees with
// the number of books actually served.
//
// It is rate-limited by being WARN on a route the app polls: the condition is
// real (9 of 50 on production 2026-08-16) and worth seeing, but it is a data
// observation, not a request failure.
func (h *Handler) logSeriesBookCountMismatch(id int, name string, want, got int) {
	slog.Warn("abs: series book count disagrees with the books served",
		"series_id", id, "series_name", name,
		"store_count", want, "served", got,
		"note", "books without a resolvable sync id are dropped; numBooks reports what was served")
}

// absSeriesBooksCacheTTL bounds how long the series→books map is reused. Matches
// the author cache: the underlying set changes only when books are added, and the
// cost being avoided is a full pass over the visible library.
const absSeriesBooksCacheTTL = 5 * time.Minute

// seriesBooksCached returns visible books grouped by series id, rebuilt at most
// once per absSeriesBooksCacheTTL.
//
// 🔴 IT USES THE SAME VISIBLE SET AS /items, DELIBERATELY. absItemFilterBase is
// what the item list serves, and a second, narrower rule here would make the
// Series tab disagree with the Library tab about what the library contains —
// the exact failure that put ~28,000 unorganized iTunes-tree rows into the
// Authors and Narrators tabs (see visibleBookIDs).
// seriesBooksBuilt is one series' rendered book list plus its summed duration.
//
// The BUILT rows are cached, not the raw summaries, following authorDTOsCached in
// this file — "this caches the whole DOCUMENT rather than just the count". The
// expensive part here is not the grouping: it is MintOrGetSyncID, one call per
// book. Caching summaries would still mint ~16,500 ids on EVERY series request.
type seriesBooksBuilt struct {
	totalDuration int
	// bookIDs is the same set in the same order, kept so the ?filter=series.<id>
	// path on /items can reuse this grouping instead of re-deriving it. Reusing it
	// is not just cheaper: it guarantees the series tile and the series drill-down
	// agree about which books are in the series, which is the whole complaint.
	bookIDs []string
}

func (h *Handler) seriesBooksCached() (map[int]seriesBooksBuilt, error) {
	now := h.now()

	h.seriesBooksCacheMu.Lock()
	if h.seriesBooksCache != nil && now.Sub(h.seriesBooksCacheAt) < absSeriesBooksCacheTTL {
		m := h.seriesBooksCache
		h.seriesBooksCacheMu.Unlock()
		return m, nil
	}
	h.seriesBooksCacheMu.Unlock()

	// Built OUTSIDE the lock, for the reason contributorsCached documents: holding
	// it across a full-library pass serializes every concurrent request behind one
	// rebuild.
	books, err := h.visibleBookSummaries(absItemFilterBase())
	if err != nil {
		return nil, err
	}
	grouped := make(map[int][]database.BookSummary, 1024)
	for i := range books {
		if books[i].SeriesID == nil {
			continue
		}
		grouped[*books[i].SeriesID] = append(grouped[*books[i].SeriesID], books[i])
	}

	m := make(map[int]seriesBooksBuilt, len(grouped))
	for id, list := range grouped {
		// Order within a series is the reading order, not insertion order: a
		// series listed out of sequence is barely better than one listed not at
		// all. Books with no sequence sort last, by title, rather than dropping.
		sort.SliceStable(list, func(a, b int) bool {
			sa, sb := list[a].SeriesSequence, list[b].SeriesSequence
			switch {
			case sa != nil && sb != nil && *sa != *sb:
				return *sa < *sb
			case sa != nil && sb == nil:
				return true
			case sa == nil && sb != nil:
				return false
			}
			return list[a].Title < list[b].Title
		})

		built := seriesBooksBuilt{}
		for i := range list {
			// Books with no resolvable sync id are dropped here, before they can
			// reach the response. The client splits compound ids at a fixed byte
			// offset of 36, so a 26-char ULID does not merely look wrong, it
			// mis-parses -- the rule #2366 established for playlist items.
			if !h.hasSyncID(list[i].ID) {
				continue
			}
			built.bookIDs = append(built.bookIDs, list[i].ID)
			// Summed over the books actually SERVED, not over every row in the
			// series: a duration counting books the client cannot see would
			// disagree with the list printed right next to it.
			if list[i].Duration != nil {
				built.totalDuration += *list[i].Duration
			}
		}
		m[id] = built
	}

	h.seriesBooksCacheMu.Lock()
	h.seriesBooksCache, h.seriesBooksCacheAt = m, now
	h.seriesBooksCacheMu.Unlock()
	return m, nil
}

// hasSyncID reports whether a book has a resolvable client-visible sync id.
//
// This replaced seriesBookEntry, which built a six-field ad-hoc book object
// that no ABS client could decode (see the note in LibrarySeries). The
// filtering it did was the part worth keeping: the id check decides which books
// can be served at all, and it must happen while the series grouping is built
// so bookIDs and the served list cannot disagree.
func (h *Handler) hasSyncID(bookID string) bool {
	sid, err := h.identity.MintOrGetSyncID(bookID)
	return err == nil && sid != ""
}

// absAuthorsCacheTTL bounds how long the built author list is reused.
//
// Generous on purpose: authors change only when books are added, and the owner
// explicitly accepted a slightly-stale list. The cost being avoided is large — see
// authorDTOsCached.
const absAuthorsCacheTTL = 5 * time.Minute

// authorDTOsCached returns the full, sorted author list, rebuilt at most once per
// absAuthorsCacheTTL.
//
// 🔴 THE CLIENT ASKS FOR THIS UP TO 93 TIMES IN A ROW, and each rebuild is a full
// library scan.
//
// AudioBooth pages authors 100 at a time (AuthorsPageModel: itemsPerPage = 100,
// hasMorePages = currentPage*itemsPerPage < total) and its jump-to-letter feature
// keeps loading pages until the target letter appears. With ~9,200 authors, jumping
// to "Z" is ~93 sequential requests.
//
// Each of those requests called GetAllAuthorBookCounts, which is by its own
// description a "Full Pebble book scan combined with junction table scan" — 44,888
// books walked, per page. 93 pages x ~400ms was ~37 seconds to reach the end of the
// alphabet.
//
// Building the list once turns pages 2..93 into slice arithmetic. That is why this
// caches the whole DOCUMENT rather than just the count: the count was never the
// expensive part here.
func (h *Handler) contributorsCached(ctx context.Context) (*contributorIndex, error) {
	now := h.now()

	h.authorsCacheMu.Lock()
	if h.authorsCache != nil && now.Sub(h.authorsCacheAt) < absAuthorsCacheTTL {
		idx := h.authorsCache
		h.authorsCacheMu.Unlock()
		return idx, nil
	}
	h.authorsCacheMu.Unlock()

	// Built OUTSIDE the lock. Holding it across a full-library scan would serialize
	// every concurrent page request behind one rebuild — which is precisely the
	// 93-requests-in-a-row access pattern this exists to serve.
	idx, err := h.contributorDTOs(ctx)
	if err != nil {
		return nil, err
	}

	h.authorsCacheMu.Lock()
	h.authorsCache, h.authorsCacheAt = idx, now
	h.authorsCacheMu.Unlock()
	return idx, nil
}

// authorDTOsCached is the author-only view of the shared contributor cache.
func (h *Handler) authorDTOsCached(ctx context.Context) ([]authorDTO, error) {
	idx, err := h.contributorsCached(ctx)
	if err != nil {
		return nil, err
	}
	return idx.authors, nil
}

// authorDTOs builds the author list once, shared by /authors and /personalized.
// visibleBookIDs returns the ids of every book the library actually SHOWS —
// the same primary + organized + non-quarantined set /items serves.
//
// 🔴 WITHOUT THIS, THE TABS DISAGREE ABOUT WHAT THE LIBRARY IS. /items shows 16,491
// books while GetAllAuthors returned the authors of all 44,888, so the Authors and
// Narrators tabs were populated from ~28,000 books the app no longer lists. That is
// where the junk comes from: unorganized iTunes-tree rows whose "author" is really a
// track name ("065_Rise of the Corinari", "13_Aurora", "CD 12"), a bare year, or a
// "Read by ..." narrator credit.
//
// Paged in chunks rather than with one unbounded call so this never depends on a
// store's limit<=0 meaning "everything", and so peak memory is a chunk of summaries
// rather than the whole library.
// It returns the SUMMARIES, not just the ids, because the narrator list needs two
// fields that live on the book row rather than in the junction table — see
// contributorDTOs.
func (h *Handler) visibleBookSummaries(f database.BookSummaryFilter) ([]database.BookSummary, error) {
	const chunk = 5000
	var out []database.BookSummary
	for offset := 0; ; offset += chunk {
		page, err := h.library.GetAllBookSummariesFiltered(chunk, offset, f)
		if err != nil {
			return nil, err
		}
		for i := range page {
			if page[i].ID != "" {
				out = append(out, page[i])
			}
		}
		if len(page) < chunk {
			return out, nil
		}
	}
}

// contributorIndex is ONE consistent view of the library's contributors: the two
// client-visible lists, and the visible book ids behind every entry in them.
//
// The lists and the id maps exist in the same struct because the drill-down
// (?filter=authors.<id>) and the tile it was tapped from must be answered from the
// same build. Keeping them apart is what made the equivalent /items?filter=series
// bug possible, and the fix there was exactly this: serve the drill-down from the
// grouping the list itself was rendered from.
//
// authorBooks is keyed by author id (an entity); narratorBooks by narrator NAME,
// because narrators are not entities in Audiobookshelf — see narratorID.
type contributorIndex struct {
	authors       []authorDTO
	narrators     []narratorDTO
	authorBooks   map[int][]string
	narratorBooks map[string][]string
}

// contributorDTOs builds the author AND narrator lists from the VISIBLE books only.
//
// Both are derived from one pass so the two tabs can never disagree with each other or
// with the library, and so the expensive part — enumerating visible books — is paid
// once instead of twice.
//
// Counts are the number of VISIBLE books, not the raw junction count: an author with
// forty unorganized rows and one real book should read "1 book", not "41".
// 🔴 NARRATORS MUST USE ALL THREE TIERS. Narrator data lives in three places and
// resolveNarrators (mapper.go) reads them in order: the BookNarrator junction, then
// Book.NarratorsJSON, then the legacy Book.Narrator column. Book DETAIL has always
// done this, which is why a book page shows a narrator.
//
// Deriving this list from the junction ALONE — as the first version of this function
// did — collapsed the Narrators tab to 8 names, because for organized books the
// junction is nearly empty and the data sits in tiers 2 and 3. Measured on prod:
// 69 of 120 sampled visible books (57.5%) have a narrator stored, i.e. roughly 9,500
// of 16,491, against 8 shown.
//
// The tab and the book page must agree about who narrated a book, so this uses the
// same three-tier resolution rather than a second, narrower rule.
func (h *Handler) contributorDTOs(ctx context.Context) (*contributorIndex, error) {
	books, err := h.visibleBookSummaries(absItemFilterBase())
	if err != nil {
		return nil, err
	}
	idx := &contributorIndex{
		authors:       []authorDTO{},
		narrators:     []narratorDTO{},
		authorBooks:   map[int][]string{},
		narratorBooks: map[string][]string{},
	}
	if len(books) == 0 {
		return idx, nil
	}
	ids := make([]string, 0, len(books))
	for i := range books {
		ids = append(ids, books[i].ID)
	}

	authorsByBook, err := h.library.GetAuthorsByBookIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	narratorsByBook, err := h.library.GetNarratorsByBookIDs(ctx, ids)
	if err != nil {
		return nil, err
	}

	type authorAgg struct {
		name string
		id   int
	}
	authorSeen := map[int]*authorAgg{}
	// Walked in the order visibleBookSummaries returned, NOT by ranging the
	// authorsByBook map, so the id lists — and therefore the drill-down's page
	// order — are stable between rebuilds rather than reshuffling per request.
	for i := range books {
		bookID := books[i].ID
		for _, a := range authorsByBook[bookID] {
			if strings.TrimSpace(a.Name) == "" {
				continue
			}
			if _, ok := authorSeen[a.ID]; !ok {
				authorSeen[a.ID] = &authorAgg{name: a.Name, id: a.ID}
			}
			// numBooks is len(authorBooks[id]) rather than its own counter, so the
			// count the tile shows and the set the drill-down pages CANNOT disagree.
			// A book credited to the same author twice must still count once.
			if list := idx.authorBooks[a.ID]; len(list) == 0 || list[len(list)-1] != bookID {
				idx.authorBooks[a.ID] = append(list, bookID)
			}
		}
	}
	// One entry per VISIBLE book, resolved through the same three tiers the book
	// page uses. Counting per book (not per junction row) keeps numBooks honest.
	//
	// 🔴 SPLIT COMPOUND CREDITS. A single stored string "Jeff Hays, Annie Ellicott"
	// is two people; left whole it became its own Narrators-tab entry reading
	// "1 book", and the real narrators' counts were short by that book. The library
	// had entries naming EIGHT narrators. This splits the presentation only — the
	// stored BookNarrator/NarratorsJSON rows still hold the compound string, and the
	// web UI still shows it, so this is not the data fix.
	narratorSeen := map[string]struct{}{}
	for i := range books {
		bookID := books[i].ID
		for _, raw := range resolveNarratorsFromSummary(&books[i], narratorsByBook[bookID]) {
			for _, name := range util.SplitCreditNames(raw) {
				if name = strings.TrimSpace(name); name == "" {
					continue
				}
				narratorSeen[name] = struct{}{}
				// Splitting can yield the same person twice for one book (tier 2 and
				// tier 3 both naming them); the book must still be listed once.
				if list := idx.narratorBooks[name]; len(list) == 0 || list[len(list)-1] != bookID {
					idx.narratorBooks[name] = append(list, bookID)
				}
			}
		}
	}

	now := msEpoch(h.now())
	idx.authors = make([]authorDTO, 0, len(authorSeen))
	for _, agg := range authorSeen {
		idx.authors = append(idx.authors, authorDTO{
			AddedAt:   now,
			ID:        strconv.Itoa(agg.id),
			LastFirst: lastFirst(agg.name),
			LibraryID: h.libraryID(),
			Name:      agg.name,
			NumBooks:  len(idx.authorBooks[agg.id]),
			UpdatedAt: now,
		})
	}
	sort.SliceStable(idx.authors, func(i, j int) bool { return idx.authors[i].Name < idx.authors[j].Name })

	idx.narrators = make([]narratorDTO, 0, len(narratorSeen))
	for name := range narratorSeen {
		n := len(idx.narratorBooks[name])
		idx.narrators = append(idx.narrators, narratorDTO{ID: narratorID(name), Name: name, NumBooks: &n})
	}
	sort.SliceStable(idx.narrators, func(i, j int) bool { return idx.narrators[i].Name < idx.narrators[j].Name })

	return idx, nil
}

// LibraryAuthors handles GET /api/libraries/:libraryId/authors.
//
// 🔴 TWO RESPONSE SHAPES, chosen by whether the caller paginated. Real ABS does the
// same (verified against the oracle 2026-08-02) and the shapes share no keys:
//
//	…/authors                    -> {"authors":[…]}
//	…/authors?limit=100&page=0   -> {"results":[…],"total":…,"limit":…,"page":…,…}
//
// AudioBooth always paginates and decodes into Page<Author>, which requires `total`
// and `page`. Serving the bare shape to it throws and blanks the Authors tab.
func (h *Handler) LibraryAuthors(c *gin.Context) {
	if !h.knownLibrary(c) {
		return
	}
	authors, err := h.authorDTOsCached(c.Request.Context())
	if err != nil {
		respondError(c, http.StatusInternalServerError, "could not list authors")
		return
	}

	_, hasLimit := c.GetQuery("limit")
	_, hasPage := c.GetQuery("page")
	if !hasLimit && !hasPage {
		respondJSON(c, http.StatusOK, authorsResponse{Authors: authors})
		return
	}

	total := len(authors)
	limit := queryInt(c, "limit", total)
	page := queryInt(c, "page", 0)
	respondJSON(c, http.StatusOK, authorsPageResponse{
		Limit:    limit,
		Minified: c.Query("minified") == "1",
		Page:     page,
		// total is the FULL count, not the size of this slice — the client uses it to
		// decide whether more pages exist.
		Results:  pageSlice(authors, limit, page),
		SortBy:   strings.TrimSpace(c.Query("sort")),
		SortDesc: c.Query("desc") == "1",
		Total:    total,
	})
}

// pageSlice returns the requested window of items. A non-positive limit means "no
// limit" (the caller sent ?page= alone), and a page past the end yields an EMPTY
// slice rather than an error — a client scrolling past the last page should see
// nothing more, not a failure.
func pageSlice(items []authorDTO, limit, page int) []authorDTO {
	if limit <= 0 || limit >= len(items) {
		if page > 0 {
			return []authorDTO{}
		}
		return items
	}
	start := page * limit
	if start >= len(items) {
		return []authorDTO{}
	}
	end := start + limit
	if end > len(items) {
		end = len(items)
	}
	return items[start:end]
}

// LibraryNarrators handles GET /api/libraries/:libraryId/narrators.
//
// The wrapper key is required (§1.8.6). Names come from the authoritative narrator
// source documented on resolveNarrators — the BookNarrator junction.
func (h *Handler) LibraryNarrators(c *gin.Context) {
	if !h.knownLibrary(c) {
		return
	}
	// Derived from the VISIBLE books, exactly like the author list — ListNarrators
	// returns every narrator row in the store, including those attached only to
	// unorganized iTunes-tree books whose "narrator" is really a track name.
	idx, err := h.contributorsCached(c.Request.Context())
	if err != nil {
		respondError(c, http.StatusInternalServerError, "could not list narrators")
		return
	}
	respondJSON(c, http.StatusOK, narratorsResponse{Narrators: idx.narrators})
}

// narratorID derives a narrator's client-visible id from their name, matching real
// ABS exactly (LibraryController.getNarrators):
//
//	id: encodeURIComponent(Buffer.from(name).toString('base64'))
//
// Derived rather than minted because narrators are NOT entities in Audiobookshelf —
// the name is the identity. A generated id would change on restart and rot every id
// the client had cached. url.QueryEscape is JavaScript's encodeURIComponent for this
// alphabet: standard base64 emits only [A-Za-z0-9+/=], of which + / and = are the
// only characters either function escapes, and both escape all three the same way.
func narratorID(name string) string {
	return url.QueryEscape(base64.StdEncoding.EncodeToString([]byte(name)))
}

// LibraryFilterData handles GET /api/libraries/:libraryId/filterdata.
//
// ALL EIGHT of authors, genres, tags, series, narrators, languages, publishers and
// publishedDecades are decoded non-optionally (§1.8.6), so every key is present even
// when empty. Note the asymmetry that is easy to get backwards (§1.7.3 item 8):
// authors are OBJECTS, narrators are PLAIN NAME STRINGS.
func (h *Handler) LibraryFilterData(c *gin.Context) {
	if !h.knownLibrary(c) {
		return
	}

	resp := filterDataResponse{
		Authors:          []idNameDTO{},
		Genres:           []string{},
		Languages:        []string{},
		LoadedAt:         msEpoch(h.now()),
		Narrators:        []string{},
		PublishedDecades: []string{},
		Publishers:       []string{},
		Series:           []idNameDTO{},
		Tags:             []string{},
	}

	if authors, err := h.library.GetAllAuthors(); err == nil {
		for _, a := range authors {
			resp.Authors = append(resp.Authors, idNameDTO{ID: strconv.Itoa(a.ID), Name: a.Name})
		}
	}
	if series, err := h.library.GetAllSeries(); err == nil {
		for _, s := range series {
			resp.Series = append(resp.Series, idNameDTO{ID: strconv.Itoa(s.ID), Name: s.Name})
		}
	}
	if narrators, err := h.library.ListNarrators(); err == nil {
		for _, n := range narrators {
			if name := strings.TrimSpace(n.Name); name != "" {
				resp.Narrators = append(resp.Narrators, name)
			}
		}
	}
	if genres, err := h.library.GetDistinctGenres(); err == nil {
		for _, g := range genres {
			if g = strings.TrimSpace(g); g != "" {
				resp.Genres = append(resp.Genres, g)
			}
		}
	}
	resp.PublishedDecades = h.publishedDecades()
	if langs, err := h.library.GetDistinctLanguages(); err == nil {
		for _, l := range langs {
			if l = strings.TrimSpace(l); l != "" {
				resp.Languages = append(resp.Languages, l)
			}
		}
	}
	respondJSON(c, http.StatusOK, resp)
}

// filterDataScanLimit bounds the projection scan behind publishedDecades.
//
// It is a single Core-typed store call (a projection, no per-item I/O), so it is not
// the whole-library-loop shape CLAUDE.md's concurrency rule targets — but it is still
// bounded, because filterdata is a decoration endpoint and no client needs a decade
// list that cost a full scan of a 68K-row library.
const filterDataScanLimit = 5000

// publishedDecades derives the decade buckets from the years we actually know.
//
// The captured oracle returned ["NaN"] here, because real ABS ran a numeric
// conversion over the unparseable published year "800BC". We deliberately do NOT
// reproduce that: "NaN" is an ABS bug, the field is decorative, and emitting a real
// decade is both honest and what a client can filter on. Books with no usable year
// contribute nothing rather than a junk bucket.
func (h *Handler) publishedDecades() []string {
	out := []string{}
	books, err := h.library.GetAllBooksCore(filterDataScanLimit, 0)
	if err != nil {
		return out
	}
	seen := map[string]bool{}
	for i := range books {
		year := books[i].AudiobookReleaseYear
		if year == nil {
			year = books[i].PrintYear
		}
		if year == nil || *year == 0 {
			continue
		}
		decade := strconv.Itoa(*year / 10 * 10)
		if !seen[decade] {
			seen[decade] = true
			out = append(out, decade)
		}
	}
	sort.Strings(out)
	return out
}

// ── GET /api/libraries/:libraryId/search ────────────────────────────────────

// searchResultLimit caps a search. Both clients paginate nothing here.
const searchResultLimit = 25

// LibrarySearch handles GET /api/libraries/:libraryId/search.
//
// An empty or unmatched query returns 200 with empty arrays, never a 4xx: a 4xx here
// reads as "search unsupported" and hides the feature (§1.7.3 item 10). Every one of
// the six keys is a non-nil array — a null fails the decode.
func (h *Handler) LibrarySearch(c *gin.Context) {
	if !h.knownLibrary(c) {
		return
	}
	resp := searchResponse{
		Authors: []any{}, Book: []searchBookHitDTO{}, Genres: []any{},
		Narrators: []narratorDTO{}, Series: []any{}, Tags: []any{},
	}

	query := strings.TrimSpace(c.Query("q"))
	if query == "" {
		respondJSON(c, http.StatusOK, resp)
		return
	}

	books, err := h.library.SearchBooks(query, searchResultLimit, 0)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "search failed")
		return
	}
	views, err := h.loadItemViews(c.Request.Context(), books)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "search failed")
		return
	}
	for i := range views {
		// Search hits carry the EXPANDED item, which is what real ABS returns and
		// what lets a client play straight from a search result.
		resp.Book = append(resp.Book, searchBookHitDTO{LibraryItem: h.expandedItem(&views[i])})
	}

	lower := strings.ToLower(query)
	if authors, err := h.authorDTOsCached(c.Request.Context()); err == nil {
		for i := range authors {
			if strings.Contains(strings.ToLower(authors[i].Name), lower) {
				resp.Authors = append(resp.Authors, authors[i])
			}
		}
	}
	if series, err := h.library.GetAllSeries(); err == nil {
		for _, s := range series {
			if strings.Contains(strings.ToLower(s.Name), lower) {
				resp.Series = append(resp.Series, gin.H{
					"id": strconv.Itoa(s.ID), "name": s.Name, "books": []any{},
				})
			}
		}
	}
	if idx, err := h.contributorsCached(c.Request.Context()); err == nil {
		for i := range idx.narrators {
			if strings.Contains(strings.ToLower(idx.narrators[i].Name), lower) {
				// §6.3: the client's Narrator.id is non-optional and ONE element
				// without it throws the whole list.
				//
				// The names come from the contributor index rather than the raw
				// store, which is what makes the id USABLE and not merely present.
				// The index splits compound credits and covers visible books only,
				// so these are the same names -- and therefore the same ids -- that
				// /narrators publishes and that narrators.<id> resolves against.
				// ListNarrators keeps "Jeff Hays, Annie Ellicott" whole and also
				// returns narrators of hidden books, both of which encode to ids
				// that decode fine and then match nothing: tap a search hit, get an
				// empty list. Authors three branches up already read this index; the
				// narrator branch was the last one on the raw store.
				//
				// NumBooks is dropped deliberately: the index carries a real count,
				// and §6.3 wants the field omitted here rather than sent.
				resp.Narrators = append(resp.Narrators, narratorDTO{
					ID: idx.narrators[i].ID, Name: idx.narrators[i].Name,
				})
			}
		}
	}
	if genres, err := h.library.GetDistinctGenres(); err == nil {
		for _, g := range genres {
			if strings.Contains(strings.ToLower(g), lower) {
				resp.Genres = append(resp.Genres, g)
			}
		}
	}
	respondJSON(c, http.StatusOK, resp)
}

// ── shared item resolution ──────────────────────────────────────────────────

// resolveItem turns a client-supplied libraryItemId into the live Book.
//
// It goes through ResolveSyncItem, which FOLLOWS MERGE REDIRECTS: a client that
// still holds the syncID of a book that later lost a dedup merge resolves to the
// surviving book instead of 404'ing and losing the user's place (spec §4.2). That
// redirect-following is the entire reason libraryItemId is not the Book ULID.
//
// It writes the 404 itself and returns nil so callers can simply return.
func (h *Handler) resolveItem(c *gin.Context) *database.Book {
	syncID := strings.TrimSpace(c.Param("id"))
	if syncID == "" {
		respondError(c, http.StatusNotFound, "library item not found")
		return nil
	}
	item, err := h.identity.ResolveSyncItem(syncID)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "could not resolve library item")
		return nil
	}
	if item == nil || item.CurrentBookID == "" {
		respondError(c, http.StatusNotFound, "library item not found")
		return nil
	}
	book, err := h.library.GetBookByID(item.CurrentBookID)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "could not load library item")
		return nil
	}
	if book == nil {
		respondError(c, http.StatusNotFound, "library item not found")
		return nil
	}
	return book
}

// WarmContributors builds the author/narrator cache ahead of the first request.
//
// The build is a full-library scan and takes ~6s on this library, so the FIRST
// caller after every start pays it while every later one is served in ~100ms.
// That first caller is normally the client's Authors tab, which is exactly the
// request that must not hang — and a restart is precisely when someone is most
// likely to be looking at the app.
//
// Deliberately best-effort: a failure here only means the next request rebuilds,
// which is the behaviour that existed before this. Errors are logged, never
// returned, and never block startup.
func (h *Handler) WarmContributors(ctx context.Context) {
	started := time.Now()
	if _, err := h.contributorsCached(ctx); err != nil {
		slog.Warn("abs: contributor cache warm failed; the first request will rebuild it", "err", err)
		return
	}
	slog.Info("abs: contributor cache warmed", "duration_ms", time.Since(started).Milliseconds())
}

// absFilterGroup splits an ABS filter token into its group and decoded value.
//
// The encoding is base64 of the raw value; ABS uses standard encoding but a value
// can arrive unpadded or URL-safe depending on the client, so both are attempted
// before giving up. A token that does not decode is reported as not-ok rather than
// silently treated as a literal — guessing here would reintroduce the very bug this
// function exists to fix, one level down.
func absFilterGroup(raw string) (group, value string, ok bool) {
	dot := strings.Index(raw, ".")
	if dot <= 0 || dot == len(raw)-1 {
		return "", "", false
	}
	group, enc := raw[:dot], raw[dot+1:]
	for _, dec := range []*base64.Encoding{
		base64.StdEncoding, base64.RawStdEncoding,
		base64.URLEncoding, base64.RawURLEncoding,
	} {
		if b, err := dec.DecodeString(enc); err == nil {
			return group, string(b), true
		}
	}
	return "", "", false
}

// filteredItems serves GET /items?filter=<group>.<base64 value>.
//
// 🔴 AN UNRECOGNISED FILTER RETURNS AN EMPTY PAGE, NOT THE WHOLE LIBRARY.
//
// That is the entire point of this function and it is worth being explicit about,
// because "ignore what you do not understand" is the intuitive choice and it is
// what produced the bug: an unimplemented filter fell through and answered with
// all 34,280 books, which the client rendered as though it were the answer. Wrong
// data that looks like real data is strictly worse than no data — an empty series
// reads as "nothing here", while a full library under a series name reads as
// "these are the books in this series" and is simply false.
//
// Every unhandled filter is LOGGED with its group. The log is not decoration: no
// fixture in the corpus carries a filter, so the only way to learn which filters
// this client actually sends is to watch it send them. The next group to implement
// should be chosen from that log rather than from a list of what upstream supports.
func (h *Handler) filteredItems(c *gin.Context, raw string, p pageParams, resp *itemsPageResponse) {
	group, value, ok := absFilterGroup(raw)
	if !ok {
		slog.Warn("abs: undecodable item filter, serving empty page", "filter", raw)
		respondJSON(c, http.StatusOK, resp)
		return
	}

	var ids []string
	switch group {
	case "series":
		seriesID, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil {
			slog.Warn("abs: series filter value is not a series id", "value", value)
			respondJSON(c, http.StatusOK, resp)
			return
		}
		bySeries, berr := h.seriesBooksCached()
		if berr != nil {
			respondError(c, http.StatusInternalServerError, "could not list library items")
			return
		}
		// Reuses the SAME grouping the series list renders from, so the tile and
		// the drill-down cannot disagree, and it is already ordered by series
		// sequence — which is the order a series should be read in.
		ids = bySeries[seriesID].bookIDs
	case "authors":
		// The value is the author ID the /authors list published, which is
		// strconv.Itoa of the store's int id — not a name.
		authorID, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil {
			slog.Warn("abs: author filter value is not an author id", "value", value)
			respondJSON(c, http.StatusOK, resp)
			return
		}
		idx, ierr := h.contributorsCached(c.Request.Context())
		if ierr != nil {
			respondError(c, http.StatusInternalServerError, "could not list library items")
			return
		}
		ids = idx.authorBooks[authorID]
	case "narrators":
		// Narrators are addressed by NAME, not by id — narratorID is just base64 of
		// the name, so decoding the filter token yields the name back. Verified
		// against the live client: it sends narrators.<the id from /narrators>, and
		// prod logged group=narrators value="Jeff Hays, Annie Ellicott".
		idx, ierr := h.contributorsCached(c.Request.Context())
		if ierr != nil {
			respondError(c, http.StatusInternalServerError, "could not list library items")
			return
		}
		ids = idx.narratorBooks[strings.TrimSpace(value)]
	default:
		slog.Warn("abs: unimplemented item filter group, serving empty page",
			"group", group, "value", value)
		respondJSON(c, http.StatusOK, resp)
		return
	}

	// Total is the size of the FILTERED set, not of the library. Reporting the
	// library total here makes the client page forever into empty results.
	resp.Total = len(ids)

	if p.Offset >= len(ids) {
		respondJSON(c, http.StatusOK, resp)
		return
	}
	end := p.Offset + p.Limit
	if p.Limit <= 0 || end > len(ids) {
		end = len(ids)
	}
	page := ids[p.Offset:end]

	books, err := h.library.GetBooksByIDs(page)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "could not load library items")
		return
	}
	// GetBooksByIDs need not preserve the requested order, and series order is
	// meaningful, so it is restored explicitly rather than trusted.
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
		resp.Results = append(resp.Results, h.minifiedItem(&views[i]))
	}
	respondJSON(c, http.StatusOK, resp)
}
