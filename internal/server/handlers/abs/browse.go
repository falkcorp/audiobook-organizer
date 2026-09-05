// file: internal/server/handlers/abs/browse.go
// version: 1.18.0
// guid: 5e0b83c7-2a41-4d96-b7e8-1c53fd90a2b4
// last-edited: 2026-09-05

package abs

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

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
	// A mapped field is ORDERED whether or not its memdb sort index is enabled:
	// without an index the store materialises the match set and sorts it before
	// paginating. The index is a speed choice now, not a correctness one.
	//
	// This note said the opposite until 2026-08-25 -- that an unindexed field
	// "falls back to unordered iteration" -- and it was true when written. It
	// stopped being true without anything here changing, which is the reason
	// the store-side default no longer has to list every field this map can
	// produce. See config.EnabledSortIndexes.
	f.SortBy = absSortField(c.Query("sort"))
	warnUnindexedSort(f.SortBy, c.Query("sort"))
	warnUnsupportedSort(f.SortBy, c.Query("sort"))
	return f
}

// absUnindexedSortWarned rate-limits the warning below to once per field per
// process, so a client polling the library cannot flood the log.
var absUnindexedSortWarned sync.Map

// warnUnindexedSort reports a sort the store must materialise rather than
// stream from an index. It is a COST warning, not a correctness one.
//
// It used to say the results would be unordered, and that was true when
// written: without an index the store iterated in whatever order it walked.
// The store now materialises the match set and sorts it before paginating, so
// an unindexed sort is correct and merely more expensive.
//
// The old remediation -- "add it to enabled_sort_indexes" -- was worse than
// useless for a while: for year and bitrate, enabling the index moved the
// request onto a branch that re-sorted the store's ordered page into insertion
// order, so following the advice broke the sort it was meant to fix. That is
// fixed, but do not restore the old wording: a message that names a symptom
// the code no longer has sends operators to change config for no reason.
func warnUnindexedSort(field, raw string) {
	if field == "" || field == "title" || database.CanPushDownSort(field) {
		return
	}
	if _, seen := absUnindexedSortWarned.LoadOrStore(field, true); seen {
		return
	}
	slog.Warn("abs: sort has no memdb index; the store must materialise the match set to order it (results are correct, but this costs more per request)",
		"sort_param", absTruncateForLog(raw, absSortRawLogMax),
		"sort_field", field,
		"remediation", "add it to enabled_sort_indexes and restart ONLY if this sort is hot enough to justify the index's memory and insert-throughput cost")
}

// absUnsupportedSortLastWarn holds the unix second at which this warning last
// fired, and is the entire rate limiter.
//
// 🚨 It deliberately remembers NOTHING about the value the client sent. The
// first version keyed a sync.Map on the raw query parameter and capped the
// number of distinct keys at 64. The cap hid three separate defects rather
// than fixing any of them:
//
//   - It bounded key COUNT, not bytes. The key was the UNTRUNCATED raw
//     parameter and MaxHeaderBytes is 1 MB, so 64 keys could retain ~64 MB for
//     the life of the process. Truncating only the log line did nothing for
//     the map.
//   - Check-then-act. Load, then LoadOrStore, then Add let concurrent callers
//     all pass the gate before any increment landed: a burst of 5,000 measured
//     961 keys held against a cap of 64.
//   - Worst, it could be SILENCED. 64 distinct junk spellings -- or a client
//     that appends a cache-buster -- permanently exhausted the cap, after
//     which every genuine future gap was answered in exactly the silence this
//     warning exists to end.
//
// A time limiter has no key to poison, no cap to exhaust, and O(1) memory
// whatever the client sends. It cannot be permanently silenced: the next
// window always reopens.
//
// On severity: the earlier fix called this an UNAUTHENTICATED DoS. That was
// wrong. handler.go registers this route behind ABSRequireAuth, and the
// surface is Cloudflare-Access-authenticated at the edge as well. The bug was
// real -- any authenticated client, or a leaked token, reached it -- but do
// not restate the unauthenticated claim; it is false in both files that
// carried it.
var absUnsupportedSortLastWarn atomic.Int64

// absUnsupportedSortWarnEvery is the minimum gap in seconds between two of
// these warnings. A gap in sort coverage is a standing condition, not an
// event: once a minute is ample to notice it, and it bounds the log whatever
// a client does.
const absUnsupportedSortWarnEvery = 60

// absSortRawLogMax bounds what reaches a log line. Both sort warnings echo the
// client's raw parameter back, so this is the one place a 1 MB query string
// could bloat the log.
const absSortRawLogMax = 64

// warnUnsupportedSort reports a sort the client asked for that this server has
// no field for at all.
//
// absSortField returns "" for anything not in absSortFields, and "" means "no
// ordering requested" everywhere downstream -- so the request is answered in
// the store's default order, with a 200 and, until this existed, complete
// silence. warnUnindexedSort cannot cover it: its first line returns early on
// field == "", so the case it most needed to report was the one case it
// skipped.
//
// absSortFields holds 11 accepted parameter spellings that resolve to 9
// distinct store fields (title and author each have two spellings). Six client
// sorts are known to resolve to "" instead, for three different reasons, and
// the log line is the only way anyone finds out which:
//
//   - File Modified -- Book.LastScanMtime exists; this is a mapping away, and
//     is filed rather than done here because adding a sort is a feature, not a
//     fix for the silence.
//   - Progress (×3) -- per-user state (UserBookState.ProgressPct), not a Book
//     field. Sorting by it needs a per-user join the summary path has no shape
//     for.
//   - File Birthtime -- no field exists on Book at all.
//   - Randomly -- deliberately unimplemented; a stable page order is required
//     for pagination to mean anything.
//
// An earlier version of this comment claimed the client menu offers 14 sorts
// and that absSortFields covered 8 of them. Neither number was reconstructible
// from the code, so both are gone: what is stated above is what the map
// actually contains.
func warnUnsupportedSort(field, raw string) {
	if field != "" || strings.TrimSpace(raw) == "" {
		return
	}
	// The zero value means "never warned": now-0 is ~1.7e9, far past the
	// window, so the first call always fires. A backwards wall-clock step
	// makes this negative and silences the warning until the clock catches
	// up -- an acceptable trade for a log limiter, and not worth a monotonic
	// redesign.
	now := time.Now().Unix()
	last := absUnsupportedSortLastWarn.Load()
	if now-last < absUnsupportedSortWarnEvery {
		return
	}
	// Exactly one concurrent caller wins the window and the losers return.
	// This is precisely what the old Load/LoadOrStore/Add sequence failed to
	// do: there, every caller that passed the first gate went on to log.
	if !absUnsupportedSortLastWarn.CompareAndSwap(last, now) {
		return
	}
	slog.Warn("abs: client requested a sort this server has no field for; the page is returned in the store's default order",
		"sort_param", absTruncateForLog(raw, absSortRawLogMax),
		"supported", absSupportedSortParams(),
		"remediation", "none available to an operator -- this needs a mapping in absSortFields, or a field that does not exist yet")
}

// absTruncateForLog bounds a client-supplied string before it reaches a log
// line, marking it when it has been cut so a reader does not mistake the
// truncation for the value the client actually sent.
func absTruncateForLog(s string, max int) string {
	if len(s) <= max {
		return s
	}
	// Cutting at a byte offset can land inside a multi-byte rune, which puts
	// invalid UTF-8 into the log stream -- a slog JSON handler silently
	// rewrites it to U+FFFD, so the corruption is not even visible downstream.
	// Back off to the last whole rune.
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…(truncated)"
}

// absSupportedSortParams lists the sort parameters absSortFields recognises,
// so the warning names the alternatives instead of only the failure.
func absSupportedSortParams() []string {
	out := make([]string, 0, len(absSortFields))
	for k := range absSortFields {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
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
		start := min(page*limit, total)
		end := min(start+limit, total)
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
	results := h.seriesRows(c.Request.Context(), series, bySeries, counts)
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

// SeriesDetail handles GET /api/series/:id. The full series row is deliberately
// built through seriesRows so it remains byte-compatible with the series listing
// the client navigated from.
func (h *Handler) SeriesDetail(c *gin.Context) {
	seriesID, err := strconv.Atoi(strings.TrimSpace(c.Param("id")))
	if err != nil {
		respondError(c, http.StatusNotFound, "series not found")
		return
	}

	byID, err := h.library.GetSeriesByIDs([]int{seriesID})
	if err != nil {
		respondError(c, http.StatusInternalServerError, "could not load series")
		return
	}
	series := byID[seriesID]
	if series == nil {
		respondError(c, http.StatusNotFound, "series not found")
		return
	}

	counts, err := h.library.GetAllSeriesBookCounts()
	if err != nil {
		slog.Warn("abs: series book counts unavailable for detail, reporting 0", "err", err)
		counts = map[int]int{}
	}
	bySeries, err := h.seriesBooksCached()
	if err != nil {
		slog.Warn("abs: series books unavailable for detail, reporting empty list", "err", err)
		bySeries = map[int]seriesBooksBuilt{}
	}
	rows := h.seriesRows(c.Request.Context(), []database.Series{*series}, bySeries, counts)
	respondJSON(c, http.StatusOK, rows[0])
}

// seriesRows builds the client-facing projection for both the paginated list and
// an individual detail route. Keeping one renderer prevents their books, counts,
// or identity fields from drifting apart.
func (h *Handler) seriesRows(
	ctx context.Context,
	series []database.Series,
	bySeries map[int]seriesBooksBuilt,
	counts map[int]int,
) []any {
	enriched := h.seriesPageBooks(ctx, series, bySeries)
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
			"totalDuration":    total,
			"numBooks":         len(items),
		})
		if want := counts[s.ID]; want != len(items) {
			h.logSeriesBookCountMismatch(s.ID, s.Name, want, len(items))
		}
	}
	return results
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
	if idx := h.contributorsFresh(); idx != nil {
		return idx, nil
	}

	// Built OUTSIDE authorsCacheMu. Holding it across a full-library scan would
	// serialize every concurrent page request behind one rebuild — which is
	// precisely the 93-requests-in-a-row access pattern this exists to serve.
	//
	// 🔴 THAT ALONE LET N COLD CALLERS START N FULL SCANS. The lock was released
	// before the build and reacquired after, so nothing marked a rebuild as
	// already in flight. singleflight gives BOTH properties at once: a warm
	// reader never enters the group, and concurrent cold callers share ONE scan.
	//
	// 🔴 context.WithoutCancel IS LOAD-BEARING. The shared build runs on whichever
	// caller won the race, and its result is handed to every waiter. Without this,
	// that one caller disconnecting cancels the build for all of them — a client
	// closing a tab would fail the library page for everyone else waiting on it.
	v, err, _ := h.authorsCacheSF.Do("contributors", func() (any, error) {
		// Re-checked INSIDE the group: a build may have finished between the miss
		// above and this call winning, and a second full scan would be pure waste.
		if idx := h.contributorsFresh(); idx != nil {
			return idx, nil
		}
		idx, err := h.contributorDTOs(context.WithoutCancel(ctx))
		if err != nil {
			return nil, err
		}
		h.authorsCacheMu.Lock()
		// Stamped AFTER the build, not before. The old code captured the time at
		// entry and stored that, which charged the scan's own duration against the
		// TTL: a 9.5s build left the entry 9.5s stale the moment it was published.
		//
		// 9.5s is a cold /authors call measured against production on 2026-08-25.
		// WarmContributors below carries an older, undated "~6s" for the same
		// build; both are plausible for a library that has grown, so neither is
		// corrected here — but do not read either as current without re-measuring.
		h.authorsCache, h.authorsCacheAt = idx, h.now()
		h.authorsCacheMu.Unlock()
		return idx, nil
	})
	if err != nil {
		return nil, err
	}
	return v.(*contributorIndex), nil
}

// contributorsBuiltAt reports when the cached contributor index was built.
//
// /filterdata stamps its own document with this so the two expire TOGETHER.
// Matching the TTL LENGTHS is not enough — see the comment in filterDataCached.
func (h *Handler) contributorsBuiltAt() time.Time {
	h.authorsCacheMu.Lock()
	defer h.authorsCacheMu.Unlock()
	return h.authorsCacheAt
}

// contributorsFresh returns the cached contributor index while it is still within
// absAuthorsCacheTTL, or nil when a rebuild is due.
func (h *Handler) contributorsFresh() *contributorIndex {
	h.authorsCacheMu.Lock()
	defer h.authorsCacheMu.Unlock()
	if h.authorsCache != nil && h.now().Sub(h.authorsCacheAt) < absAuthorsCacheTTL {
		return h.authorsCache
	}
	return nil
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

	// 🔴 SORTED BEFORE THE SHAPE SPLIT, NOT INSIDE THE PAGINATED BRANCH. Both
	// shapes accept ?sort=/?desc=, and pageSlice has to window an already-sorted
	// list. Sorting only where SortBy/SortDesc are echoed would leave
	// …/authors?sort=numBooks&desc=1 (no limit/page) answered in name order.
	sortBy := strings.TrimSpace(c.Query("sort"))
	desc := c.Query("desc") == "1"
	authors, sortApplied := sortAuthors(authors, sortBy, desc)

	_, hasLimit := c.GetQuery("limit")
	_, hasPage := c.GetQuery("page")
	if !hasLimit && !hasPage {
		respondJSON(c, http.StatusOK, authorsResponse{Authors: authors})
		return
	}

	total := len(authors)
	limit := queryInt(c, "limit", total)
	page := queryInt(c, "page", 0)

	echoSort, echoDesc := sortBy, desc
	if !sortApplied {
		echoSort, echoDesc = "", false
	}
	respondJSON(c, http.StatusOK, authorsPageResponse{
		Limit:    limit,
		Minified: c.Query("minified") == "1",
		Page:     page,
		// total is the FULL count, not the size of this slice — the client uses it to
		// decide whether more pages exist.
		Results: pageSlice(authors, limit, page),
		// 🔴 THE ORDER WE ACTUALLY APPLIED, NOT THE ONE WE WERE ASKED FOR. These
		// two fields are the server telling the client how the list is ordered.
		// Echoing back a key we had no field for made the response ASSERT an
		// ordering it had not applied — the same defect, in the same handler, as
		// the sort that was parsed and then ignored.
		//
		// A 400 was the other candidate and is the wrong trade here. These are
		// third-party clients we do not control, and this package's own notes
		// record that a response a client cannot use BLANKS the Authors tab.
		// Refusing to serve a list because we do not implement someone's sort key
		// is a bigger harm than serving it in a different order and saying so.
		//
		// An unsupported key therefore reports the empty string — "the store's
		// default order" — which is exactly what the client received, and which
		// it can tell apart from the key it sent.
		SortBy:   echoSort,
		SortDesc: echoDesc,
		Total:    total,
	})
}

// authorDetailDTO is the author row plus the two optional expansions real ABS
// serves for ?include=items,series. omitzero, not omitempty: an author whose
// include was requested but who has no visible books must still get
// "libraryItems": [] — the client asked for the list, and an absent key reads
// as "this server has no such list", which is the black page this fixes.
type authorDetailDTO struct {
	authorDTO
	LibraryItems []any `json:"libraryItems,omitzero"`
	Series       []any `json:"series,omitzero"`
}

// AuthorDetail handles GET /api/authors/:id.
//
// Book items reference authors by this endpoint rather than by the library
// listing route. Resolving from the same cached projection keeps the identity
// and fields consistent across both UI paths without adding another store read.
//
// 🔴 THE APP'S AUTHOR PAGE IS BUILT FROM THE INCLUDES. Tapping an author on a
// book requests ?include=items,series and renders libraryItems; the bare
// author row was a 200 that drew nothing. The items come from the SAME
// contributor index the /authors tile count and the ?filter=authors.<id>
// drill-down read, so all three agree on which books this author has.
func (h *Handler) AuthorDetail(c *gin.Context) {
	authorID := strings.TrimSpace(c.Param("id"))
	if authorID == "" {
		respondError(c, http.StatusNotFound, "author not found")
		return
	}

	idx, err := h.contributorsCached(c.Request.Context())
	if err != nil {
		slog.Warn("abs: author detail: contributor index unavailable", "author_id", authorID, "err", err)
		respondError(c, http.StatusInternalServerError, "could not load author")
		return
	}
	var found *authorDTO
	for i := range idx.authors {
		if idx.authors[i].ID == authorID {
			found = &idx.authors[i]
			break
		}
	}
	if found == nil {
		respondError(c, http.StatusNotFound, "author not found")
		return
	}

	includeItems, includeSeries := false, false
	for _, part := range strings.Split(c.Query("include"), ",") {
		switch strings.TrimSpace(strings.ToLower(part)) {
		case "items":
			includeItems = true
		case "series":
			includeSeries = true
		}
	}
	resp := authorDetailDTO{authorDTO: *found}
	if !includeItems && !includeSeries {
		respondJSON(c, http.StatusOK, resp)
		return
	}

	id, err := strconv.Atoi(authorID)
	if err != nil {
		// Unreachable while ids are minted by strconv.Itoa above, and a 404
		// rather than a silent authorBooks[0] lookup if that ever changes.
		respondError(c, http.StatusNotFound, "author not found")
		return
	}
	views, err := h.itemViewsByIDs(c.Request.Context(), idx.authorBooks[id])
	if err != nil {
		// 🔴 NOT an empty list. The same body carries numBooks from the index;
		// "12 books" beside "libraryItems: []" is a contradiction the client
		// renders as an empty page with no error, which is the report this fixes.
		slog.Warn("abs: author detail: could not hydrate items", "author_id", authorID, "err", err)
		respondError(c, http.StatusInternalServerError, "could not load author's items")
		return
	}
	if includeItems {
		resp.LibraryItems = make([]any, 0, len(views))
		for i := range views {
			resp.LibraryItems = append(resp.LibraryItems, h.minifiedItem(&views[i]))
		}
	}
	if includeSeries {
		resp.Series = h.authorSeriesGroups(views)
	}
	respondJSON(c, http.StatusOK, resp)
}

// authorSeriesGroups groups an author's hydrated books by series, in the shape
// real ABS puts under author.series: one entry per series with its items. Books
// in no series are not listed here; they are in libraryItems already.
func (h *Handler) authorSeriesGroups(views []itemView) []any {
	order := []int{}
	byID := map[int][]any{}
	for i := range views {
		sid := views[i].Book.SeriesID
		if sid == nil {
			continue
		}
		if _, seen := byID[*sid]; !seen {
			order = append(order, *sid)
		}
		byID[*sid] = append(byID[*sid], h.minifiedItem(&views[i]))
	}
	out := make([]any, 0, len(order))
	if len(order) == 0 {
		return out
	}
	names, err := h.library.GetSeriesByIDs(order)
	if err != nil {
		slog.Warn("abs: series names unavailable for author detail", "err", err)
		names = map[int]*database.Series{}
	}
	for _, sid := range order {
		name := ""
		if s := names[sid]; s != nil {
			name = s.Name
		}
		out = append(out, gin.H{
			"id":               strconv.Itoa(sid),
			"name":             name,
			"nameIgnorePrefix": ignorePrefix(name),
			"items":            byID[sid],
		})
	}
	return out
}

// itemViewsByIDs hydrates a list of book ids into item views, in the order
// given, dropping only ids that no longer resolve. A store or hydration
// failure is returned, not swallowed: the caller decides whether an empty
// list is an honest answer, and for an author whose numBooks says otherwise
// it is not.
func (h *Handler) itemViewsByIDs(ctx context.Context, ids []string) ([]itemView, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	books, err := h.library.GetBooksByIDs(ids)
	if err != nil {
		return nil, fmt.Errorf("load %d books by id: %w", len(ids), err)
	}
	views, err := h.loadItemViews(ctx, books)
	if err != nil {
		return nil, fmt.Errorf("hydrate %d items: %w", len(books), err)
	}
	byID := make(map[string]int, len(views))
	for i := range views {
		byID[views[i].Book.ID] = i
	}
	out := make([]itemView, 0, len(ids))
	for _, id := range ids {
		if i, ok := byID[id]; ok {
			out = append(out, views[i])
		}
	}
	return out, nil
}

// authorLess returns the comparator for an author-list sort, or nil when this
// server has no field for the requested key.
//
// 🔴 A DIFFERENT NAMESPACE FROM absSortFields, and routing author sorts through
// that map would be worse than useless rather than merely inert. absSortField
// lowercases the key and strips the last dotted segment, so "addedAt" resolves
// to "created_at" and "updatedAt" to "updated_at" — real BOOK columns, applied
// to an authorDTO. A wrong non-empty column is a wrong ANSWER; an unresolved ""
// would at least fall through to a documented default order.
//
// These five are the fields the DTO carries that a client can meaningfully
// order by.
//
// An empty sort means "name": the index is built name-ascending
// (contributorDTOs), so ?sort=name is a no-op against the default order and
// ?desc=1 alone correctly means name-DESCENDING rather than "unsorted".
func authorLess(sortBy string) func(a, b authorDTO) bool {
	switch strings.TrimSpace(sortBy) {
	case "", "name":
		return func(a, b authorDTO) bool { return a.Name < b.Name }
	case "lastFirst":
		return func(a, b authorDTO) bool { return a.LastFirst < b.LastFirst }
	case "numBooks":
		return func(a, b authorDTO) bool { return a.NumBooks < b.NumBooks }
	case "addedAt":
		return func(a, b authorDTO) bool { return a.AddedAt < b.AddedAt }
	case "updatedAt":
		return func(a, b authorDTO) bool { return a.UpdatedAt < b.UpdatedAt }
	}
	return nil
}

// sortAuthors returns a sorted COPY of the author list.
//
// 🔴 IT MUST COPY, AND THE COPY IS THE WHOLE POINT. contributorsCached hands
// every caller the SAME cached *contributorIndex, so sorting idx.authors in
// place would both race with concurrent readers of that shared slice and
// permanently reorder the cache — the next request that asked for no particular
// order would be answered in whatever order the last ?desc=1 caller left behind,
// for the rest of the TTL.
//
// A stable sort over a name-ascending input gives ties a deterministic order
// (two authors with the same numBooks stay in name order) rather than an
// arbitrary one that shifts between requests and breaks pagination.
// The second return reports whether the REQUESTED order was applied. The caller
// needs it to describe the response honestly; see the echo in LibraryAuthors.
func sortAuthors(authors []authorDTO, sortBy string, desc bool) ([]authorDTO, bool) {
	// The index is ALREADY name-ascending, so the no-parameter request — by far
	// the most common, and the one the 93-page jump-to-letter burst issues — can
	// skip both the allocation and the sort. At 12,854 authors that is a fresh
	// slice plus a full stable sort per request, on a path that previously did
	// neither.
	//
	// Returning the shared slice is safe HERE for the same reason it was safe
	// before this function existed: every caller only reads it. It is the
	// SORTING that must not touch the shared slice, not the returning.
	if !desc && (sortBy == "" || sortBy == "name") {
		return authors, true
	}
	less := authorLess(sortBy)
	if less == nil {
		warnUnsupportedAuthorSort(sortBy)
		return authors, false
	}
	out := make([]authorDTO, len(authors))
	copy(out, authors)
	sort.SliceStable(out, func(i, j int) bool {
		if desc {
			return less(out[j], out[i])
		}
		return less(out[i], out[j])
	})
	return out, true
}

// absUnsupportedAuthorSortLastWarn rate-limits the author-sort warning.
//
// Deliberately NOT shared with absUnsupportedSortLastWarn: the two namespaces
// are unrelated, and one limiter would let a steady stream of unsupported book
// sorts silence the author warning entirely (and vice versa).
var absUnsupportedAuthorSortLastWarn atomic.Int64

// warnUnsupportedAuthorSort reports an author sort this server has no field for.
//
// Same reasoning as warnUnsupportedSort: the request is answered 200 in the
// index's default order, so without a log nobody finds out that ?sort= was
// ignored. The list above is what authorDTO can support, not a survey of what
// clients ask for.
//
// ⚠️ This log is a SAMPLE, not a census. The limiter is one line per 60s across
// all clients and all values, so only the first distinct bad key in each window
// is ever recorded. Do not read an absence here as "nobody asked for it".
func warnUnsupportedAuthorSort(raw string) {
	// No empty-string guard, deliberately: this is only reached when authorLess
	// returned nil, and authorLess maps "" to the name comparator. A guard here
	// could never fire, and an unreachable guard reads as load-bearing.
	now := time.Now().Unix()
	last := absUnsupportedAuthorSortLastWarn.Load()
	if now-last < absUnsupportedSortWarnEvery {
		return
	}
	if !absUnsupportedAuthorSortLastWarn.CompareAndSwap(last, now) {
		return
	}
	slog.Warn("abs: client requested an author sort this server has no field for; the page is returned in name order",
		"group", "authors", "value", raw)
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
	end := min(start+limit, len(items))
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
	respondJSON(c, http.StatusOK, h.filterDataCached(c.Request.Context()))
}

// absFilterDataCacheTTL bounds how long the built /filterdata document is reused.
//
// DERIVED from absAuthorsCacheTTL, not a matching literal: the two documents are
// built from the same contributor index, and a future retune of one would
// silently unmatch them while this comment went on claiming they agreed.
//
// Equal length is only half of it — filterDataCached also stamps the document
// with the INDEX's build time so the two expire together. See there.
const absFilterDataCacheTTL = absAuthorsCacheTTL

// filterDataCached builds the filter document at most once per TTL.
//
// Single-flighted for the same reason as contributorsCached, and more urgently:
// this endpoint is on the library page-load path, so every cold request that is
// not coalesced is another three full library scans.
func (h *Handler) filterDataCached(ctx context.Context) *filterDataResponse {
	if resp := h.filterDataFresh(); resp != nil {
		return resp
	}
	v, _, _ := h.filterDataSF.Do("filterdata", func() (any, error) {
		if resp := h.filterDataFresh(); resp != nil {
			return resp, nil
		}
		resp, builtAt, complete := h.buildFilterData(context.WithoutCancel(ctx))

		h.filterDataMu.Lock()
		defer h.filterDataMu.Unlock()

		// 🔴 A DEGRADED DOCUMENT IS NEVER PUBLISHED AS FRESH. buildFilterData
		// falls back to an empty list per source rather than failing the request,
		// which was the right cost when it was paid ONCE PER REQUEST. Caching
		// promoted it: stamping a degraded build would pin one transient store
		// error into a 200 for the whole TTL, silently, because filterDataFresh
		// then short-circuits and the error is never logged again.
		//
		// Worse, it would recreate the exact drift this endpoint was moved onto
		// the contributor index to remove — contributorsCached does NOT cache its
		// error, so /authors recovers on its very next request while /filterdata
		// would still be serving "this library has no authors".
		//
		// Serving the LAST GOOD document beats serving an empty one: an empty
		// list is not the absence of a filter, it is an affirmative claim that
		// the library has no authors, and LoadedAt would timestamp that claim as
		// current. A stale document is at least true of some moment, and it
		// carries the LoadedAt to say which.
		if !complete {
			if prev := h.filterDataCache; prev != nil {
				return prev, nil
			}
			return resp, nil
		}

		// 🔴 STAMPED WITH THE CONTRIBUTOR INDEX'S BUILD TIME, NOT h.now(). Equal
		// TTL LENGTHS do not align PHASES: stamping independently lets this
		// document outlive the index it was built from by up to a full TTL, so
		// /filterdata would serve the author list /authors had already replaced.
		// Expiring with the index is what actually makes the two agree.
		h.filterDataCache, h.filterDataCachedAt = resp, builtAt
		return resp, nil
	})
	if resp, ok := v.(*filterDataResponse); ok && resp != nil {
		return resp
	}
	// Unreachable today: the closure above returns a non-nil document on every
	// path. Guarded anyway because the bare assertion would panic through gin's
	// recovery the moment anyone adds a `return nil, err` to it.
	return emptyFilterData(msEpoch(h.now()))
}

// emptyFilterData is the all-keys-present, all-lists-empty document.
//
// Every one of the eight lists is decoded non-optionally by the client
// (§1.8.6), so a document is never allowed to omit a key.
func emptyFilterData(loadedAt int64) *filterDataResponse {
	return &filterDataResponse{
		Authors:          []idNameDTO{},
		Genres:           []string{},
		Languages:        []string{},
		LoadedAt:         loadedAt,
		Narrators:        []string{},
		PublishedDecades: []string{},
		Publishers:       []string{},
		Series:           []idNameDTO{},
		Tags:             []string{},
	}
}

// filterDataFresh returns the cached document while it is within the TTL.
func (h *Handler) filterDataFresh() *filterDataResponse {
	h.filterDataMu.Lock()
	defer h.filterDataMu.Unlock()
	if h.filterDataCache != nil && h.now().Sub(h.filterDataCachedAt) < absFilterDataCacheTTL {
		return h.filterDataCache
	}
	return nil
}

// buildFilterData assembles the filter document.
//
// Returns the document, the contributor index's build time to stamp it with, and
// whether EVERY source succeeded. The caller needs that third value: a degraded
// document must not be cached as fresh (see filterDataCached).
//
// 🔴 EVERY SOURCE DEGRADES TO AN EMPTY LIST RATHER THAN FAILING THE REQUEST, and
// that is deliberate even though the sibling /authors returns 500 on the same
// error. filterdata is decoration fetched during the library page load: a 500
// here blanks the page, while a missing filter dropdown costs the user one way
// to narrow a list they can still see.
//
// The asymmetry is the point — but a degraded document is a CLAIM, not a gap:
// "authors: []" says this library has no authors, and LoadedAt dates the claim.
// So every swallow logs, AND the result is marked incomplete so it is served for
// this request only rather than pinned for the TTL. Both halves are required;
// the logging alone was what the first version of this shipped with, and a log
// line nobody is watching does not stop a wrong answer being cached.
func (h *Handler) buildFilterData(ctx context.Context) (resp *filterDataResponse, builtAt time.Time, complete bool) {
	complete = true
	degraded := func(what string, err error) {
		complete = false
		// "err", not "error": the rest of this package keys error logs on "err"
		// (browse.go:714, browse.go:1824, stats.go), and a structured-log query
		// or alert rule on that field would not match a different key.
		slog.Warn("abs: filterdata source unavailable; serving the last good list for it, and NOT caching this build",
			"source", what, "err", err)
	}

	resp = emptyFilterData(0)
	builtAt = h.now()

	// 🔴 AUTHORS AND NARRATORS COME FROM THE CONTRIBUTOR INDEX, NOT FROM
	// GetAllAuthors/ListNarrators. Those return every row in the store, including
	// contributors attached only to books the library does not show — 4,975 of
	// 12,854 authors (38.7%) have no visible book at all. They populated this
	// dropdown, and picking one returned an empty shelf.
	//
	// PR #2512 built contributorIndex precisely so "the tile and the drill-down
	// cannot drift"; /filterdata was simply never moved onto it. Sourcing it here
	// makes the filter list exactly the list /authors and /narrators serve.
	if idx, err := h.contributorsCached(ctx); err != nil {
		degraded("contributor-index", err)
	} else {
		// The document expires with the index it was built from, not five
		// minutes after this request happened to arrive.
		builtAt = h.contributorsBuiltAt()
		for _, a := range idx.authors {
			resp.Authors = append(resp.Authors, idNameDTO{ID: a.ID, Name: a.Name})
		}
		for _, n := range idx.narrators {
			if name := strings.TrimSpace(n.Name); name != "" {
				resp.Narrators = append(resp.Narrators, name)
			}
		}
	}

	// Series is still row-derived: it has no entry in the contributor index, so
	// moving it is a separate change with its own measurement, not a drive-by.
	if series, err := h.library.GetAllSeries(); err != nil {
		degraded("series", err)
	} else {
		for _, s := range series {
			resp.Series = append(resp.Series, idNameDTO{ID: strconv.Itoa(s.ID), Name: s.Name})
		}
	}
	if genres, err := h.library.GetDistinctGenres(); err != nil {
		degraded("genres", err)
	} else {
		for _, g := range genres {
			if g = strings.TrimSpace(g); g != "" {
				resp.Genres = append(resp.Genres, g)
			}
		}
	}
	if decades, err := h.publishedDecades(); err != nil {
		degraded("published-decades", err)
	} else {
		resp.PublishedDecades = decades
	}
	if langs, err := h.library.GetDistinctLanguages(); err != nil {
		degraded("languages", err)
	} else {
		for _, l := range langs {
			if l = strings.TrimSpace(l); l != "" {
				resp.Languages = append(resp.Languages, l)
			}
		}
	}
	return resp, builtAt, complete
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
func (h *Handler) publishedDecades() ([]string, error) {
	out := []string{}
	// 🔴 THIS ONE USED TO SWALLOW SILENTLY, inside the very function whose
	// comment claimed every swallow logs. It returned an empty decade list with
	// no log at any level, and once /filterdata gained a cache that empty list
	// would have been served for the whole TTL with nothing in the logs at all.
	books, err := h.library.GetAllBooksCore(filterDataScanLimit, 0)
	if err != nil {
		return out, err
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
	return out, nil
}

// ── GET /api/libraries/:libraryId/search ────────────────────────────────────

// searchResultLimit caps a search. Both clients paginate nothing here.
const searchResultLimit = 25

// absSearchCacheTTL is how long a finished search document is replayed for the
// same (library, query). The user's number: "the same for the next minute ...
// or maybe 2-3 minutes". A library edit inside that window is invisible to a
// repeated search until it expires; that is the trade the user chose.
const absSearchCacheTTL = 2 * time.Minute

// absSearchCacheMax bounds the number of cached queries. The key is user input,
// so without a bound a client typing character by character grows the map
// forever; at the bound the oldest entry is dropped.
const absSearchCacheMax = 256

type searchCacheEntry struct {
	resp    *searchResponse
	builtAt time.Time
}

// searchCacheKey folds case and surrounding space so "Primal Hunter" and
// "primal hunter " share a document — the search itself is case-insensitive.
func searchCacheKey(libraryID, query string) string {
	return libraryID + "\x00" + strings.ToLower(strings.TrimSpace(query))
}

func (h *Handler) searchCached(key string) (*searchResponse, bool) {
	h.searchCacheMu.Lock()
	defer h.searchCacheMu.Unlock()
	e, ok := h.searchCache[key]
	if !ok || h.now().Sub(e.builtAt) >= absSearchCacheTTL {
		return nil, false
	}
	return e.resp, true
}

func (h *Handler) searchStore(key string, resp *searchResponse) {
	h.searchCacheMu.Lock()
	defer h.searchCacheMu.Unlock()
	if h.searchCache == nil {
		h.searchCache = map[string]searchCacheEntry{}
	}
	now := h.now()
	if len(h.searchCache) >= absSearchCacheMax {
		// Expired entries first; if none, the oldest. One pass either way —
		// the map is small by construction. The oldest is tracked from the
		// first survivor, not from now: seeded at now, entries built in the
		// same instant (a frozen test clock, or a burst) are never "before"
		// it and the map would grow past the bound.
		var oldestKey string
		var oldestAt time.Time
		for k, e := range h.searchCache {
			if now.Sub(e.builtAt) >= absSearchCacheTTL {
				delete(h.searchCache, k)
				continue
			}
			if oldestKey == "" || e.builtAt.Before(oldestAt) {
				oldestKey, oldestAt = k, e.builtAt
			}
		}
		if len(h.searchCache) >= absSearchCacheMax && oldestKey != "" {
			delete(h.searchCache, oldestKey)
		}
	}
	h.searchCache[key] = searchCacheEntry{resp: resp, builtAt: now}
}

// stripArticle drops one leading sort article ("the ", "a ", "an ") from an
// already-lowercased string, so "the primal hunter" and "primal hunter" match
// each other the way ABS's own nameIgnorePrefix sorting treats them.
func stripArticle(lower string) string {
	for _, p := range ignorePrefixes {
		if strings.HasPrefix(lower, p) {
			return strings.TrimSpace(lower[len(p):])
		}
	}
	return lower
}

// rankSeriesMatches returns up to limit series whose name contains the
// lowercased query, best matches first.
//
// Series with no books are left out when counts is known (non-nil). A series
// row nobody references renders as an empty tile — on 2026-09-05 "primal
// hunter" returned 25 series of which 16 had zero books, all duplicates of
// the one real row, and the phone showed a wall of black tiles above it. A
// nil counts means the count source failed; the caller serves the unfiltered
// list as a degraded document and does not cache it.
//
// Tiers: an exact name (article-insensitive: "The Primal Hunter" is exact for
// "primal hunter"), then names that start with the query, then the rest. Inside
// a tier, more books first, then the input (name) order. Ranked BEFORE the cap:
// GetAllSeries is name-sorted, so cutting the first 25 substring matches of a
// common word could drop the series the user typed behind twenty-four that
// merely contain it.
func rankSeriesMatches(all []database.Series, lower string, counts map[int]int, limit int) []database.Series {
	query := stripArticle(lower)
	type ranked struct {
		s    database.Series
		tier int
		pos  int
	}
	var out []ranked
	for i, s := range all {
		if counts != nil && counts[s.ID] == 0 {
			continue
		}
		name := strings.ToLower(s.Name)
		bare := stripArticle(name)
		var tier int
		switch {
		case name == lower || bare == query:
			tier = 0
		case strings.HasPrefix(name, lower) || strings.HasPrefix(bare, query):
			tier = 1
		case strings.Contains(name, lower) || strings.Contains(name, query):
			tier = 2
		default:
			continue
		}
		out = append(out, ranked{s: s, tier: tier, pos: i})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].tier != out[j].tier {
			return out[i].tier < out[j].tier
		}
		if counts != nil && counts[out[i].s.ID] != counts[out[j].s.ID] {
			return counts[out[i].s.ID] > counts[out[j].s.ID]
		}
		return out[i].pos < out[j].pos
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	res := make([]database.Series, 0, len(out))
	for _, r := range out {
		res = append(res, r.s)
	}
	return res
}

// LibrarySearch handles GET /api/libraries/:libraryId/search.
//
// An empty or unmatched query returns 200 with empty arrays, never a 4xx: a 4xx here
// reads as "search unsupported" and hides the feature (§1.7.3 item 10). Every one of
// the six keys is a non-nil array — a null fails the decode.
//
// The document is built once per (library, query) and replayed for
// absSearchCacheTTL. Readers only serialize the cached value, never mutate it.
func (h *Handler) LibrarySearch(c *gin.Context) {
	if !h.knownLibrary(c) {
		return
	}
	query := strings.TrimSpace(c.Query("q"))
	if query == "" {
		respondJSON(c, http.StatusOK, emptySearchResponse())
		return
	}

	key := searchCacheKey(h.libraryID(), query)
	if resp, ok := h.searchCached(key); ok {
		respondJSON(c, http.StatusOK, resp)
		return
	}
	v, err, _ := h.searchSF.Do(key, func() (any, error) {
		if resp, ok := h.searchCached(key); ok {
			return resp, nil
		}
		resp, complete, err := h.buildSearch(context.WithoutCancel(c.Request.Context()), query)
		if err != nil {
			return nil, err
		}
		// 🔴 A DEGRADED DOCUMENT IS NEVER CACHED. buildSearch serves a list
		// empty rather than failing the request when one of its optional
		// sources errors — the right cost per request, and the wrong thing to
		// pin for two minutes: the phone would retry into the same quietly
		// empty list with nothing in the log. Same rule as filterDataCached.
		if complete {
			h.searchStore(key, resp)
		}
		return resp, nil
	})
	if err != nil {
		slog.Warn("abs: search failed", "query", query, "err", err)
		respondError(c, http.StatusInternalServerError, "search failed")
		return
	}
	respondJSON(c, http.StatusOK, v.(*searchResponse))
}

func emptySearchResponse() *searchResponse {
	return &searchResponse{
		Authors: []any{}, Book: []searchBookHitDTO{}, Genres: []any{},
		Narrators: []narratorDTO{}, Series: []any{}, Tags: []any{},
	}
}

// buildSearch computes one search document. Profiled on production 2026-09-05
// (7.45s per call): 4.79s was GetDistinctGenres walking the whole book keyspace
// for one field, 1.6s the book search, 0.5s sorting every series name. Genres
// now come from the /filterdata document, which already caches exactly that
// scan; the series and book work is what the result cache amortizes.
//
// complete is false when any optional source degraded to an empty list; the
// document is still served, but the caller must not cache it.
func (h *Handler) buildSearch(ctx context.Context, query string) (resp *searchResponse, complete bool, err error) {
	resp = emptySearchResponse()
	complete = true
	degraded := func(what string, err error) {
		complete = false
		slog.Warn("abs: search source unavailable, serving that list empty", "source", what, "query", query, "err", err)
	}

	books, err := h.library.SearchBooks(query, searchResultLimit, 0)
	if err != nil {
		return nil, false, err
	}
	views, err := h.loadItemViews(ctx, books)
	if err != nil {
		return nil, false, err
	}
	for i := range views {
		// Search hits carry the EXPANDED item, which is what real ABS returns and
		// what lets a client play straight from a search result.
		resp.Book = append(resp.Book, searchBookHitDTO{LibraryItem: h.expandedItem(&views[i])})
	}

	lower := strings.ToLower(query)
	idx, err := h.contributorsCached(ctx)
	if err != nil {
		degraded("contributors", err)
	} else {
		for i := range idx.authors {
			if strings.Contains(strings.ToLower(idx.authors[i].Name), lower) {
				resp.Authors = append(resp.Authors, idx.authors[i])
			}
		}
	}
	var seriesOK bool
	resp.Series, seriesOK = h.searchSeriesHits(ctx, lower)
	if !seriesOK {
		complete = false
	}
	if idx != nil {
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
	// From the cached /filterdata document, NOT h.library.GetDistinctGenres():
	// that call is a full scan of the book keyspace and was 64% of every search.
	//
	// filterDataCached hands back a degraded, UNCACHED document when a source
	// failed and nothing fresh exists; filterDataFresh returns only a published
	// one. A document that is not the published one is degraded, and a search
	// built on it must not be cached either — /filterdata would self-heal on
	// its next request while /search kept asserting "no genres" for the TTL.
	fd := h.filterDataCached(ctx)
	if fd == nil || fd != h.filterDataFresh() {
		degraded("filterdata", errors.New("degraded or unpublished filterdata document"))
	}
	if fd != nil {
		for _, g := range fd.Genres {
			if strings.Contains(strings.ToLower(g), lower) {
				resp.Genres = append(resp.Genres, g)
			}
		}
	}
	return resp, complete, nil
}

// searchSeriesHits returns the series whose name contains the (lowercased)
// query, each carrying its books.
//
// 🔴 THE BOOKS ARE THE TILE. The client draws a series search hit from the
// covers of its books; an empty books array — which is what this served
// before — renders as a black tile that still opens the series when tapped,
// because the id was right and the books were not there. The rows come from
// seriesRows, the SAME renderer /series and /series/:id use, so a hit and the
// page it opens agree on the books. Each hit also carries a nested "series"
// object with the row's identity fields, which is where real ABS puts them,
// so a client reading either shape finds the id and name.
//
// Capped at searchResultLimit: every hit hydrates its books, and a one-word
// query can match thousands of the library's 43k series names.
//
// The bool is false when the list was degraded by a store failure (no series,
// or series with no books); the caller must not cache such a document.
func (h *Handler) searchSeriesHits(ctx context.Context, lower string) ([]any, bool) {
	out := []any{}
	all, err := h.library.GetAllSeries()
	if err != nil {
		slog.Warn("abs: series unavailable for search, serving none", "err", err)
		return out, false
	}
	complete := true
	// Counts come first: they decide which series are shown at all (a series
	// with no books is not a result) and how ties rank. Without them the list
	// is served unfiltered and NOT cached.
	counts, err := h.library.GetAllSeriesBookCounts()
	if err != nil {
		slog.Warn("abs: series book counts unavailable for search, serving unfiltered series", "err", err)
		counts = nil
		complete = false
	}
	matched := rankSeriesMatches(all, lower, counts, searchResultLimit)
	if len(matched) == 0 {
		return out, complete
	}
	bySeries, err := h.seriesBooksCached()
	if err != nil {
		// Served without books — the black tile — but NOT cached as such.
		slog.Warn("abs: series books unavailable for search, serving series without books", "err", err)
		bySeries = map[int]seriesBooksBuilt{}
		complete = false
	}
	if counts == nil {
		counts = map[int]int{}
	}
	for _, row := range h.seriesRows(ctx, matched, bySeries, counts) {
		hit, ok := row.(gin.H)
		if !ok {
			continue
		}
		nested := gin.H{}
		for k, v := range hit {
			if k != "books" {
				nested[k] = v
			}
		}
		hit["series"] = nested
		out = append(out, hit)
	}
	return out, complete
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

	// A sort has to be applied to the WHOLE filtered set before the page is
	// cut out of it. Sorting after slicing would only order the rows already
	// on screen, which looks like it works on page 1 and is wrong everywhere
	// else.
	//
	// That costs a fetch of every book in the group instead of one page, so it
	// is paid only when a sort is actually requested. With no sort the group's
	// own order stands -- for a series that is reading sequence, which is a
	// better default than any field the client could ask for.
	sortField := absSortField(c.Query("sort"))

	var (
		books []database.Book
		order []string
		err   error
	)
	if sortField == "" {
		order = ids[p.Offset:end]
		books, err = h.library.GetBooksByIDs(order)
	} else {
		var all []database.Book
		all, err = h.library.GetBooksByIDs(ids)
		if err == nil {
			database.SortBooks(all, sortField, c.Query("desc") != "1")
			if p.Offset < len(all) {
				hi := min(end, len(all))
				all = all[p.Offset:hi]
			} else {
				all = nil
			}
			books = all
			order = make([]string, len(books))
			for i := range books {
				order[i] = books[i].ID
			}
		}
	}
	if err != nil {
		respondError(c, http.StatusInternalServerError, "could not load library items")
		return
	}
	// GetBooksByIDs need not preserve the requested order, and both the group
	// order and the sorted order are meaningful, so it is restored explicitly
	// rather than trusted.
	pos := make(map[string]int, len(order))
	for i, id := range order {
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
