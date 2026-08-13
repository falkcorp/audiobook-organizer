// file: internal/server/handlers/abs/playlists.go
// version: 1.0.0
// guid: c41e97b2-0d85-4f36-a7e9-1b620c8ad573
// last-edited: 2026-08-13

package abs

import (
	"net/http"
	"sort"

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
// SCOPE, AND WHAT IT IS NOT. This is the LIST route only. Upstream ABS has ~12
// playlist routes (create, update, delete, item add/remove, batch add/remove,
// create-from-collection); none of those are implemented and this change does not
// add them. The bare /api/playlists namespace continues to 301 into the app-API
// twin via absAppAPICollisions — that is deliberate and pinned by
// TestCollidingNamespacesStillRedirect. Nothing here touches engine-level routing.
//
// 🔴 NO CLIENT IS KNOWN TO CALL THIS. Zero of the 28 captures in
// testdata/abs-fixtures/ request any playlist path, and the target-client contract
// §11 lists playlists among the surfaces explicitly SAFE TO STUB. So the empty page
// this replaces was contract-correct, not a defect. This is completeness work: it
// makes the route honest if a client ever does ask, and it is why the DTO below is
// modelled on the upstream ABS 2.36.0 reference rather than on a capture — there is
// no capture to conform to, and the tests here therefore assert OUR shape, not an
// oracle's.
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
