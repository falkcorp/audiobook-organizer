// file: internal/server/handlers/abs/browse.go
// version: 1.3.0
// guid: 5e0b83c7-2a41-4d96-b7e8-1c53fd90a2b4
// last-edited: 2026-08-03

package abs

import (
	"encoding/base64"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	servermiddleware "github.com/falkcorp/audiobook-organizer/internal/server/middleware"
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
func absItemFilter(c *gin.Context) database.BookSummaryFilter {
	primary := true
	f := database.BookSummaryFilter{
		IsPrimaryVersion:   &primary,
		LibraryState:       "organized",
		ExcludeQuarantined: true,
		SortAscending:      c.Query("desc") != "1",
	}
	// Only "title" is index-backed; anything else falls through to store default
	// ordering rather than pretending to honour a sort we cannot do cheaply.
	if strings.Contains(strings.ToLower(c.Query("sort")), "title") {
		f.SortBy = "title"
	}
	return f
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

	authors, err := h.authorDTOs()
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
		counts = map[int]int{}
	}

	results := make([]any, 0, len(series))
	for _, s := range series {
		results = append(results, gin.H{
			"id":               strconv.Itoa(s.ID),
			"name":             s.Name,
			"nameIgnorePrefix": ignorePrefix(s.Name),
			"libraryId":        h.libraryID(),
			"addedAt":          msEpoch(h.now()),
			"updatedAt":        msEpoch(h.now()),
			"books":            []any{},
			// An int, never a float: Dart throws on `42.0 as int?` and this is cast
			// during widget build (§1.7.3 item 5).
			"totalDuration": 0,
			"numBooks":      counts[s.ID],
		})
	}
	respondJSON(c, http.StatusOK, pageResponse{Results: results, Total: len(results)})
}

// authorDTOs builds the author list once, shared by /authors and /personalized.
func (h *Handler) authorDTOs() ([]authorDTO, error) {
	authors, err := h.library.GetAllAuthors()
	if err != nil {
		return nil, err
	}
	counts, err := h.library.GetAllAuthorBookCounts()
	if err != nil {
		counts = map[int]int{}
	}
	now := msEpoch(h.now())
	out := make([]authorDTO, 0, len(authors))
	for _, a := range authors {
		out = append(out, authorDTO{
			AddedAt:   now,
			ID:        strconv.Itoa(a.ID),
			LastFirst: lastFirst(a.Name),
			LibraryID: h.libraryID(),
			Name:      a.Name,
			NumBooks:  counts[a.ID],
			UpdatedAt: now,
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
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
	authors, err := h.authorDTOs()
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
	narrators, err := h.library.ListNarrators()
	if err != nil {
		respondError(c, http.StatusInternalServerError, "could not list narrators")
		return
	}
	out := make([]narratorDTO, 0, len(narrators))
	for _, n := range narrators {
		if name := strings.TrimSpace(n.Name); name != "" {
			out = append(out, narratorDTO{ID: narratorID(name), Name: name})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	respondJSON(c, http.StatusOK, narratorsResponse{Narrators: out})
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
		Narrators: []any{}, Series: []any{}, Tags: []any{},
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
	if authors, err := h.authorDTOs(); err == nil {
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
	if narrators, err := h.library.ListNarrators(); err == nil {
		for _, n := range narrators {
			if strings.Contains(strings.ToLower(n.Name), lower) {
				resp.Narrators = append(resp.Narrators, gin.H{"name": n.Name, "numBooks": 0})
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
