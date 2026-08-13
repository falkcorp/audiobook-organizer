// file: internal/server/handlers/abs/playlists.go
// version: 1.1.0
// guid: c41e97b2-0d85-4f36-a7e9-1b620c8ad573
// last-edited: 2026-08-13

package abs

import (
	"net/http"
	"sort"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	servermiddleware "github.com/falkcorp/audiobook-organizer/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

// ── GET /api/libraries/:libraryId/playlists ─────────────────────────────────
//
// Replaces the h.EmptyPage stub on the playlist route with the real user-playlist
// list.
//
// 🔴 THE MODEL IS UserPlaylist, NOT Playlist. internal/database carries two
// unrelated types with confusingly similar names. `Playlist` (store.go:380, int
// ids, SeriesID, FilePath) is the LEGACY series M3U auto-generator. `UserPlaylist`
// (store.go:389, ULID ids, static|smart, BookIDs, CreatedByUserID) is the spec-3.4
// user-facing feature, and it is what the nine app routes under /api/v1/playlists
// (wire_library_routes.go:77-85) actually serve. Mapping the legacy type here would
// produce a playlist list unrelated to anything the web UI shows.
//
// SCOPE, AND WHAT IT IS NOT. This file serves the LIST and the DETAIL routes.
// Upstream ABS has ~12 playlist routes (create, update, delete, item add/remove,
// batch add/remove, create-from-collection); none of those WRITE routes are
// implemented and this change does not add them.
//
// The bare /api/playlists namespace continues to 301 into the app-API twin via
// absAppAPICollisions — deliberate, and pinned by
// TestCollidingNamespacesStillRedirect. Only "/api/playlists/" WITH a trailing
// segment is reserved for ABS (absReservedPathPrefixes), so the list redirect and
// the native detail route coexist. Nothing here touches engine-level routing —
// doing that to solve this collision has broken 46 live app routes twice.
//
// ⚠️ RETRACTED 2026-08-13: this block previously read "NO CLIENT IS KNOWN TO CALL
// THIS", resting on zero playlist paths appearing in the 28 captures and on
// contract §11 listing playlists as safe to stub. A user opened a playlist in the
// app and got an empty screen, which is direct evidence a client DOES call this
// surface. The captures were taken before any playlist existed — absence in a
// fixture set bounds what the fixtures prove, never what the client does. Treating
// "not in the corpus" as "not called" is what left the detail route unimplemented
// while the list route shipped.
//
// Still true and worth keeping: there is no capture to conform to, so the DTO is
// modelled on the upstream ABS 2.36.0 reference and the tests here assert OUR
// shape rather than an oracle's.
//
// PRODUCTION IS EMPTY EITHER WAY. /api/v1/playlists returned {"items":[],"count":0}
// on 2026-08-13, so this route still renders an empty list until playlists exist —
// which is blocked on the separate iTunes importer gap (the ITL parser extracts 0
// of 292 smart playlists). "Playlists implemented" must not be read as "playlists
// appear".

// PlaylistStore reads user playlists for the ABS playlist list. Optional: with a
// nil store the route keeps answering the empty page, which is a valid Page<T> and
// exactly the previous behaviour, rather than 500-ing.
//
// The per-user variant is the only one exposed on purpose. ListUserPlaylists (no
// user) would disclose every user's playlists to every caller the moment this
// server has more than one account.
type PlaylistStore interface {
	ListUserPlaylistsForUser(userID, playlistType string, limit, offset int) ([]database.UserPlaylist, int, error)
	// GetUserPlaylist is NOT user-scoped — it resolves any playlist by id. The
	// ownership check therefore lives in PlaylistDetail and is not optional; see
	// the note on this interface about disclosing other users' playlists.
	GetUserPlaylist(id string) (*database.UserPlaylist, error)
}

// absPlaylistPageSize bounds one page of playlists. Playlists are few and the
// client has no paging UI for them, so this is a safety bound rather than a
// pagination scheme.
const absPlaylistPageSize = 500

// LibraryPlaylists handles GET /api/libraries/:libraryId/playlists.
func (h *Handler) LibraryPlaylists(c *gin.Context) {
	if !h.knownLibrary(c) {
		return
	}
	// No store wired: answer exactly what the stub answered. An empty Page<T> is
	// valid; `{}` would throw in the Dart client (§6.6).
	if h.playlists == nil {
		respondJSON(c, http.StatusOK, pageResponse{Results: []any{}})
		return
	}

	u, found := servermiddleware.CurrentUser(c)
	if !found || u == nil {
		respondError(c, http.StatusUnauthorized, "authentication required")
		return
	}

	// "" = both static and smart.
	lists, _, err := h.playlists.ListUserPlaylistsForUser(u.ID, "", absPlaylistPageSize, 0)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "could not list playlists")
		return
	}

	results := make([]any, 0, len(lists))
	for i := range lists {
		results = append(results, h.playlistDTO(c, &lists[i]))
	}
	// Total is the number RETURNED, not the store's total count. Returning a total
	// larger than results without a working page parameter would tell the client
	// there are more pages it cannot fetch.
	respondJSON(c, http.StatusOK, pageResponse{Results: results, Total: len(results)})
}

// playlistDTO maps one UserPlaylist onto the upstream ABS playlist shape.
func (h *Handler) playlistDTO(c *gin.Context, pl *database.UserPlaylist) gin.H {
	// A smart playlist's membership is its LAST EVALUATION, not its query: the
	// query needs the Bleve index and this is a read path that must not depend on
	// the index being open. An unmaterialized smart playlist therefore renders as
	// an empty playlist rather than erroring — the client shows it with no items,
	// which is recoverable, instead of red-screening the library tab.
	bookIDs := pl.BookIDs
	if pl.Type == database.UserPlaylistTypeSmart {
		bookIDs = pl.MaterializedBookIDs
	}

	var description any
	if pl.Description != "" {
		description = pl.Description
	}

	return gin.H{
		"id":          pl.ID,
		"libraryId":   h.libraryID(),
		"userId":      pl.CreatedByUserID,
		"name":        pl.Name,
		"description": description,
		"coverPath":   nil,
		"items":       h.playlistItems(c, pl.ID, bookIDs),
		"lastUpdate":  msEpoch(pl.UpdatedAt),
		"createdAt":   msEpoch(pl.CreatedAt),
	}
}

// playlistItems expands the ordered book list into ABS playlist items.
//
// Books are fetched in ONE batch and then re-ordered to the playlist's own
// sequence. Fetching per id would be an N+1 over a list whose length the user
// controls, and playlist order is meaningful — it is the listening order — so the
// store's return order is never used directly.
//
// A book id that no longer resolves is DROPPED, not rendered as a null item: a
// playlist referencing a deleted book is a stale reference, and an item with no
// libraryItem is exactly the shape that red-screens a client expecting to expand
// it.
func (h *Handler) playlistItems(c *gin.Context, playlistID string, bookIDs []string) []any {
	items := make([]any, 0, len(bookIDs))
	if len(bookIDs) == 0 {
		return items
	}

	books, err := h.library.GetBooksByIDs(bookIDs)
	if err != nil {
		// Report the playlist with no items rather than failing the whole page —
		// one unreadable playlist must not take out the list.
		return items
	}
	byID := make(map[string]*database.Book, len(books))
	for i := range books {
		byID[books[i].ID] = &books[i]
	}

	order := make(map[string]int, len(bookIDs))
	for i, id := range bookIDs {
		if _, seen := order[id]; !seen {
			order[id] = i
		}
	}

	type positioned struct {
		pos int
		val gin.H
	}
	built := make([]positioned, 0, len(bookIDs))
	for id, pos := range order {
		book := byID[id]
		if book == nil {
			continue // stale reference to a deleted book
		}
		syncID, err := h.identity.MintOrGetSyncID(book.ID)
		if err != nil {
			continue
		}
		item := gin.H{
			// The playlist-item id is synthesized from (playlist, item): our store
			// has no PlaylistItem row for UserPlaylist — membership is an ordered
			// id slice — and ABS clients use this only as a list key.
			"id":            playlistID + "_" + syncID,
			"playlistId":    playlistID,
			"libraryItemId": syncID,
			"episodeId":     nil,
		}
		if v, verr := h.loadItemView(c.Request.Context(), book); verr == nil && v != nil {
			item["libraryItem"] = h.minifiedItem(v)
		}
		built = append(built, positioned{pos: pos, val: item})
	}
	sort.Slice(built, func(i, j int) bool { return built[i].pos < built[j].pos })

	for _, b := range built {
		items = append(items, b.val)
	}
	return items
}

// ── GET /api/playlists/:id ──────────────────────────────────────────────────
//
// 🔴 THIS IS WHY EVERY PLAYLIST OPENED EMPTY. Reported from the app 2026-08-13:
// the playlist list rendered (that route was already correct — it returned the
// 77-item cohort playlist on production), but opening one showed nothing.
//
// The list and the detail are served by different paths, and only the list was
// implemented. Opening a playlist calls GET /api/playlists/:id, which fell
// through absAppAPICollisions into a 301 to /api/v1/playlists/:id — the app-API
// twin. That answers {"id","name","book_ids":[...]}; ABS expects
// {"items":[{"libraryItem":…}]}. The client followed the redirect, got HTTP 200
// and valid JSON in the wrong shape, and rendered nothing.
//
// That failure mode is the one wire_abs_routes.go warns about in
// absReservedPathPrefixes: it "looks implemented and behaves broken". Nothing
// logs an error, because nothing errored. Only the empty screen showed it.
//
// 🔴 THE PREMISE OF THE COMMENT AT THE TOP OF THIS FILE IS NOW FALSE. It says
// "NO CLIENT IS KNOWN TO CALL THIS", resting on zero playlist paths appearing in
// the 28 captures and on contract §11 listing playlists as safe to stub. A user
// opening a playlist in the app is direct evidence a client does call it. The
// captures were simply taken before playlists existed — absence in a fixture set
// bounds what the fixtures prove, not what the client does.

// PlaylistDetail handles GET /api/playlists/:id.
func (h *Handler) PlaylistDetail(c *gin.Context) {
	if h.playlists == nil {
		// No store wired. 404 rather than an empty object: a playlist that does
		// not exist and a server that cannot read playlists are both "not here"
		// to the client, and {} is not a valid playlist shape (§6.6).
		respondError(c, http.StatusNotFound, "playlist not found")
		return
	}

	u, found := servermiddleware.CurrentUser(c)
	if !found || u == nil {
		respondError(c, http.StatusUnauthorized, "authentication required")
		return
	}

	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		respondError(c, http.StatusNotFound, "playlist not found")
		return
	}

	pl, err := h.playlists.GetUserPlaylist(id)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "could not read playlist")
		return
	}
	// 🔴 OWNERSHIP IS CHECKED HERE OR NOWHERE. GetUserPlaylist resolves ANY
	// playlist by id regardless of owner — it is the by-id twin of the
	// ListUserPlaylists this file deliberately does not expose. Without this
	// check, any authenticated user could read any other user's playlist, and
	// its book list, by guessing or observing an id. Answering 404 rather than
	// 403 keeps the id space opaque: 403 would confirm the playlist exists.
	if pl == nil || pl.CreatedByUserID != u.ID {
		respondError(c, http.StatusNotFound, "playlist not found")
		return
	}

	respondJSON(c, http.StatusOK, h.playlistDTO(c, pl))
}
