// file: internal/server/handlers/abs/playlists_write.go
// version: 1.2.0
// guid: 6ddbf78d-bfe3-47a7-946a-c677d6f16821
// last-edited: 2026-09-19

package abs

import (
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	servermiddleware "github.com/falkcorp/audiobook-organizer/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

// ── Playlist mutations ──────────────────────────────────────────────────────
//
// 🔴 WHAT WAS BROKEN, MEASURED (item-6 decode matrix, 2026-09-19). Every one of
// these six app actions answered 301 → /api/v1/playlists… on production. The
// client follows a 301 by re-issuing the request as a GET, so a create, rename,
// delete or add-to-playlist silently became a read of the app-API twin and
// nothing was written:
//
//	POST   /api/playlists                      create
//	PATCH  /api/playlists/:id                  rename / edit / reorder
//	DELETE /api/playlists/:id                  delete
//	POST   /api/playlists/:id/batch/add        add books
//	POST   /api/playlists/:id/batch/remove     remove books
//	DELETE /api/playlists/:id/item/:itemId     remove one book
//
// ROUTING. /api/playlists has a live /api/v1 twin, so each route is claimed
// individually in absCollisionDetailRoutes (wire_abs_routes.go), and only while
// the ABS surface is enabled. POST /api/playlists and DELETE /api/playlists/:id
// are the two shapes BOTH APIs serve; with ABS on, the unversioned form belongs to
// ABS. The web UI is unaffected because it only ever calls /api/v1/playlists
// (web/src/services/playlistApi.ts), which never passes through the redirect.
//
// STORAGE. Same UserPlaylist store and same write methods as the native routes
// (handlers/playlists.go), so a playlist made in the app shows up in the web UI
// and vice versa. Every write marks the playlist Dirty exactly as the native
// routes do, so the two surfaces feed the iTunes sync identically.
//
// OWNERSHIP. Playlists belong to a person — the opposite of collections. Every
// route resolves the playlist through ownedPlaylist, which answers 404 (never
// 403) for another user's playlist, the same rule PlaylistDetail applies.
//
// IDS. The client sends libraryItemIds (36-char sync ids). They are translated to
// book ULIDs through resolveSyncIDs; storing them raw would produce a playlist
// whose members never match a book — a 200 followed by a permanently empty list.

// absPlaylistItemRef is one entry of an ABS playlist `items` array.
type absPlaylistItemRef struct {
	LibraryItemID string  `json:"libraryItemId"`
	EpisodeID     *string `json:"episodeId"`
}

// absPlaylistCreateReq is the ABS create payload.
type absPlaylistCreateReq struct {
	LibraryID   string               `json:"libraryId"`
	Name        string               `json:"name"`
	Description string               `json:"description"`
	Items       []absPlaylistItemRef `json:"items"`
}

// absPlaylistUpdateReq uses pointers so an absent key leaves a field alone; a
// value-typed struct would blank every field the client did not send.
type absPlaylistUpdateReq struct {
	Name        *string               `json:"name"`
	Description *string               `json:"description"`
	Items       *[]absPlaylistItemRef `json:"items"`
}

// absPlaylistBatchReq is the batch add/remove payload.
type absPlaylistBatchReq struct {
	Items []absPlaylistItemRef `json:"items"`
}

// playlistItemSyncIDs extracts the libraryItemIds from an items array. Podcast
// episode entries are skipped: this library holds no podcasts, so an entry that
// names an episode cannot refer to anything here.
func playlistItemSyncIDs(items []absPlaylistItemRef) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		if it.EpisodeID != nil && strings.TrimSpace(*it.EpisodeID) != "" {
			continue
		}
		out = append(out, it.LibraryItemID)
	}
	return out
}

// CreatePlaylist handles POST /api/playlists.
func (h *Handler) CreatePlaylist(c *gin.Context) {
	u, ok := h.playlistWriter(c)
	if !ok {
		return
	}
	var req absPlaylistCreateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, "invalid playlist payload")
		return
	}
	if lib := strings.TrimSpace(req.LibraryID); lib != "" && lib != h.libraryID() {
		respondError(c, http.StatusBadRequest, "unknown library")
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		respondError(c, http.StatusBadRequest, "playlist name is required")
		return
	}

	bookIDs, rerr := h.resolveSyncIDs(playlistItemSyncIDs(req.Items))
	if rerr != nil {
		respondResolveError(c)
		return
	}
	pl := &database.UserPlaylist{
		Name:            name,
		Description:     strings.TrimSpace(req.Description),
		Type:            database.UserPlaylistTypeStatic,
		BookIDs:         bookIDs,
		CreatedByUserID: u.ID,
		Dirty:           true, // same as the native create: new playlists need iTunes sync
	}
	created, err := h.playlists.CreateUserPlaylist(pl)
	if err != nil {
		if errors.Is(err, database.ErrUserPlaylistNameInUse) {
			// Generic on purpose: the name index is global across users, so the
			// message confirms only that the requested name is taken.
			respondError(c, http.StatusConflict, err.Error())
			return
		}
		respondError(c, http.StatusInternalServerError, "could not create playlist")
		return
	}
	respondJSON(c, http.StatusOK, h.playlistDTO(c, created))
}

// UpdatePlaylist handles PATCH /api/playlists/:id.
//
// A present `items` array REPLACES the membership in the given order, which is
// how the app reorders a playlist.
func (h *Handler) UpdatePlaylist(c *gin.Context) {
	var req absPlaylistUpdateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, "invalid playlist payload")
		return
	}
	// Resolved ONCE, before the retry loop: translation does not depend on the
	// row, and a lookup error fails the request instead of dropping members.
	var incoming []string
	if req.Items != nil {
		ids, rerr := h.resolveSyncIDs(playlistItemSyncIDs(*req.Items))
		if rerr != nil {
			respondResolveError(c)
			return
		}
		incoming = ids
	}
	h.mutateOwnedPlaylist(c, func(pl *database.UserPlaylist) (int, string) {
		if req.Name != nil {
			name := strings.TrimSpace(*req.Name)
			if name == "" {
				return http.StatusBadRequest, "playlist name cannot be empty"
			}
			pl.Name = name
		}
		if req.Description != nil {
			pl.Description = strings.TrimSpace(*req.Description)
		}
		if req.Items != nil {
			if pl.Type != database.UserPlaylistTypeStatic {
				return http.StatusConflict, "this is a smart playlist; its members come from its query"
			}
			// 🔴 NOT a replace. The app builds `items` from the playlist it
			// last read; a replace (re-applied by the retry to the fresh row)
			// would drop anything added since. Reorder what it names, add what
			// is new, keep the rest; removal is batch/remove or item delete.
			pl.BookIDs = database.MergeMemberListNoLoss(pl.BookIDs, incoming)
		}
		return 0, ""
	})
}

// DeletePlaylist handles DELETE /api/playlists/:id.
func (h *Handler) DeletePlaylist(c *gin.Context) {
	if _, ok := h.playlistWriter(c); !ok {
		return
	}
	pl, ok := h.ownedPlaylist(c)
	if !ok {
		return
	}
	if err := h.playlists.DeleteUserPlaylist(pl.ID); err != nil {
		respondError(c, http.StatusInternalServerError, "could not delete playlist")
		return
	}
	// The app types this response as Data, so any non-empty 2xx body is success.
	respondJSON(c, http.StatusOK, gin.H{"id": pl.ID})
}

// BatchAddToPlaylist handles POST /api/playlists/:id/batch/add.
//
// Books already in the playlist are skipped rather than duplicated, and the new
// ones are appended in request order.
func (h *Handler) BatchAddToPlaylist(c *gin.Context) {
	var req absPlaylistBatchReq
	if err := c.ShouldBindJSON(&req); err != nil || len(req.Items) == 0 {
		respondError(c, http.StatusBadRequest, "items are required")
		return
	}
	adds, rerr := h.resolveSyncIDs(playlistItemSyncIDs(req.Items))
	if rerr != nil {
		respondResolveError(c)
		return
	}
	h.mutateOwnedPlaylist(c, func(pl *database.UserPlaylist) (int, string) {
		if pl.Type != database.UserPlaylistTypeStatic {
			return http.StatusConflict, "this is a smart playlist; its members come from its query"
		}
		for _, id := range adds {
			if !slices.Contains(pl.BookIDs, id) {
				pl.BookIDs = append(pl.BookIDs, id)
			}
		}
		return 0, ""
	})
}

// BatchRemoveFromPlaylist handles POST /api/playlists/:id/batch/remove.
//
// Unlike upstream ABS, an emptied playlist is KEPT rather than deleted: deleting
// a user's playlist as a side effect of removing its last book is data loss the
// user did not ask for, and the app renders an empty playlist without trouble.
func (h *Handler) BatchRemoveFromPlaylist(c *gin.Context) {
	var req absPlaylistBatchReq
	if err := c.ShouldBindJSON(&req); err != nil || len(req.Items) == 0 {
		respondError(c, http.StatusBadRequest, "items are required")
		return
	}
	drops, rerr := h.resolveSyncIDs(playlistItemSyncIDs(req.Items))
	if rerr != nil {
		respondResolveError(c)
		return
	}
	h.mutateOwnedPlaylist(c, func(pl *database.UserPlaylist) (int, string) {
		if pl.Type != database.UserPlaylistTypeStatic {
			return http.StatusConflict, "this is a smart playlist; its members come from its query"
		}
		pl.BookIDs = h.withoutMembers(pl.BookIDs, drops)
		return 0, ""
	})
}

// RemovePlaylistItem handles DELETE /api/playlists/:id/item/:libraryItemId.
//
// A libraryItemId that does not resolve cannot be in the playlist, so the
// requested end state already holds and the playlist is returned unchanged.
func (h *Handler) RemovePlaylistItem(c *gin.Context) {
	target, rerr := h.resolveSyncIDs([]string{c.Param("libraryItemId")})
	if rerr != nil {
		respondResolveError(c)
		return
	}
	h.mutateOwnedPlaylist(c, func(pl *database.UserPlaylist) (int, string) {
		if pl.Type != database.UserPlaylistTypeStatic {
			return http.StatusConflict, "this is a smart playlist; its members come from its query"
		}
		pl.BookIDs = h.withoutMembers(pl.BookIDs, target)
		return 0, ""
	})
}

// ── shared plumbing ─────────────────────────────────────────────────────────

// playlistWriter checks the store is wired and the caller is authenticated,
// writing the error response itself.
func (h *Handler) playlistWriter(c *gin.Context) (*database.User, bool) {
	if h.playlists == nil {
		respondError(c, http.StatusServiceUnavailable, "playlists are not available")
		return nil, false
	}
	u, found := servermiddleware.CurrentUser(c)
	if !found || u == nil {
		respondError(c, http.StatusUnauthorized, "authentication required")
		return nil, false
	}
	return u, true
}

// ownedPlaylist resolves :id to a playlist the caller owns, writing the error
// response itself. Another user's playlist is a 404, never a 403: a 403 would
// confirm the id exists (same rule as PlaylistDetail).
func (h *Handler) ownedPlaylist(c *gin.Context) (*database.UserPlaylist, bool) {
	u, ok := h.playlistWriter(c)
	if !ok {
		return nil, false
	}
	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		respondError(c, http.StatusNotFound, "playlist not found")
		return nil, false
	}
	pl, err := h.playlists.GetUserPlaylist(id)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "could not read playlist")
		return nil, false
	}
	if pl == nil || pl.CreatedByUserID != u.ID {
		respondError(c, http.StatusNotFound, "playlist not found")
		return nil, false
	}
	return pl, true
}

// absPlaylistReject aborts a mutation with a status.
type absPlaylistReject struct {
	status int
	msg    string
}

func (e *absPlaylistReject) Error() string { return e.msg }

// errABSPlaylistNotOwned: another user's playlist is a 404, like a missing one.
var errABSPlaylistNotOwned = errors.New("playlist not owned by caller")

// mutateOwnedPlaylist is the read-modify-write behind every membership/metadata
// mutation. It goes through database.UpdateUserPlaylistWithRetry — the same
// path the native /api/v1 handlers use — so the store's Version compare-and-swap
// makes a concurrent edit from EITHER surface compose with this one (re-read,
// re-apply, retry) instead of one whole-record write erasing the other. An
// ABS-only lock would not have protected against the native writers.
//
// mutate returns a non-zero status to reject the request without writing.
func (h *Handler) mutateOwnedPlaylist(c *gin.Context, mutate func(pl *database.UserPlaylist) (int, string)) {
	u, ok := h.playlistWriter(c)
	if !ok {
		return
	}
	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		respondError(c, http.StatusNotFound, "playlist not found")
		return
	}
	updated, err := database.UpdateUserPlaylistWithRetry(h.playlists, id, func(pl *database.UserPlaylist) error {
		if pl.CreatedByUserID != u.ID {
			return errABSPlaylistNotOwned
		}
		if status, msg := mutate(pl); status != 0 {
			return &absPlaylistReject{status: status, msg: msg}
		}
		pl.Dirty = true // same as every native playlist write: pending iTunes sync
		return nil
	})
	var rej *absPlaylistReject
	switch {
	case err == nil:
		respondJSON(c, http.StatusOK, h.playlistDTO(c, updated))
	case errors.As(err, &rej):
		respondError(c, rej.status, rej.msg)
	case errors.Is(err, database.ErrUserPlaylistNotFound), errors.Is(err, errABSPlaylistNotOwned):
		respondError(c, http.StatusNotFound, "playlist not found")
	case errors.Is(err, database.ErrUserPlaylistNameInUse), errors.Is(err, database.ErrUserPlaylistVersionConflict):
		respondError(c, http.StatusConflict, err.Error())
	default:
		respondError(c, http.StatusInternalServerError, "could not update playlist")
	}
}
