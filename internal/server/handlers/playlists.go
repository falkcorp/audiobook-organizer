// file: internal/server/handlers/playlists.go
// version: 2.1.0
// guid: a7b8c9d0-e1f2-3456-abcd-456789012345
// last-edited: 2026-06-02

package handlers

import (
	"fmt"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/playlist"
	"github.com/falkcorp/audiobook-organizer/internal/search"
	"github.com/gin-gonic/gin"
)

// -----------------------------------------------------------------------
// Request types (Phase 1 — kept from initial migration)
// -----------------------------------------------------------------------

// PlaylistCreateReq is the payload for POST /api/v1/playlists.
type PlaylistCreateReq struct {
	Name        string   `json:"name" binding:"required"`
	Description string   `json:"description,omitempty"`
	Type        string   `json:"type" binding:"required"` // static|smart
	BookIDs     []string `json:"book_ids,omitempty"`
	Query       string   `json:"query,omitempty"`
	SortJSON    string   `json:"sort_json,omitempty"`
	Limit       int      `json:"limit,omitempty"`
}

// PlaylistUpdateReq mirrors PlaylistCreateReq but all fields are
// optional — only set ones are applied.
type PlaylistUpdateReq struct {
	Name        *string   `json:"name,omitempty"`
	Description *string   `json:"description,omitempty"`
	BookIDs     *[]string `json:"book_ids,omitempty"`
	Query       *string   `json:"query,omitempty"`
	SortJSON    *string   `json:"sort_json,omitempty"`
	Limit       *int      `json:"limit,omitempty"`
}

// PlaylistBooksAddReq is the payload for POST /api/v1/playlists/:id/books.
type PlaylistBooksAddReq struct {
	BookIDs []string `json:"book_ids" binding:"required"`
}

// PlaylistReorderReq is the payload for PUT /api/v1/playlists/:id/books/order.
type PlaylistReorderReq struct {
	BookIDs []string `json:"book_ids" binding:"required"`
}

// -----------------------------------------------------------------------
// Narrow interface
// -----------------------------------------------------------------------

// PlaylistStore is the narrow database interface PlaylistHandler requires.
// It embeds database.UserPlaylistStore for CRUD, and additionally includes
// the methods needed by playlist.EvaluateSmartPlaylist internally.
type PlaylistStore interface {
	database.UserPlaylistStore
	// Methods needed by playlist.EvaluateSmartPlaylist (satisfies playlist.playlistEvalStore):
	database.BookReader
	database.UserPositionStore
}

// -----------------------------------------------------------------------
// Handler struct
// -----------------------------------------------------------------------

// PlaylistHandler handles all /playlists routes.
type PlaylistHandler struct {
	store   PlaylistStore
	indexFn func() *search.BleveIndex // lazily resolved — smart playlists return 503 when nil
}

// NewPlaylistHandler constructs a PlaylistHandler.
// searchIndexFn is called at request time so the handler works correctly
// when the search index is opened after construction (e.g. in Start()).
// Pass nil or a function that returns nil to disable smart-playlist evaluation.
func NewPlaylistHandler(store PlaylistStore, searchIndex *search.BleveIndex) *PlaylistHandler {
	fn := func() *search.BleveIndex { return searchIndex }
	return &PlaylistHandler{store: store, indexFn: fn}
}

// NewPlaylistHandlerWithGetter constructs a PlaylistHandler with a lazy index getter.
// Use this when the search index is set after construction (e.g. in tests).
func NewPlaylistHandlerWithGetter(store PlaylistStore, indexFn func() *search.BleveIndex) *PlaylistHandler {
	return &PlaylistHandler{store: store, indexFn: indexFn}
}

// ownedByCaller reports whether pl is accessible to the calling user. Playlists
// are per-user; without this check any authenticated user could read or mutate
// another user's playlist by guessing its ID (IDOR). A nil playlist is treated
// as not-owned. Callers respond 404 (not 403) on failure so the existence of
// another user's playlist is not disclosed.
//
// A playlist with an empty CreatedByUserID is treated as legacy/unowned and is
// accessible to any caller. Such rows predate ownership tracking (e.g. iTunes
// smart-playlist imports created before this field was stamped); refusing them
// would silently hide pre-existing data. New playlists always carry an owner.
func ownedByCaller(c *gin.Context, pl *database.UserPlaylist) bool {
	if pl == nil {
		return false
	}
	if pl.CreatedByUserID == "" {
		return true
	}
	return pl.CreatedByUserID == CallingUserID(c)
}

// -----------------------------------------------------------------------
// HTTP handlers
// -----------------------------------------------------------------------

// CreatePlaylist — POST /api/v1/playlists
func (h *PlaylistHandler) CreatePlaylist(c *gin.Context) {
	var req PlaylistCreateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}
	if err := validatePlaylistCreate(&req); err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}

	pl := &database.UserPlaylist{
		Name:            strings.TrimSpace(req.Name),
		Description:     req.Description,
		Type:            req.Type,
		BookIDs:         req.BookIDs,
		Query:           req.Query,
		SortJSON:        req.SortJSON,
		Limit:           req.Limit,
		CreatedByUserID: CallingUserID(c),
		Dirty:           true, // new playlists need iTunes sync
	}
	created, err := h.store.CreateUserPlaylist(pl)
	if err != nil {
		if strings.Contains(err.Error(), "already exists") || strings.Contains(err.Error(), "duplicate") {
			httputil.RespondWithConflict(c, err.Error())
			return
		}
		httputil.InternalError(c, "failed to create playlist", err)
		return
	}
	httputil.RespondWithCreated(c, created)
}

// ListPlaylists — GET /api/v1/playlists?type=static|smart&limit=N&offset=M
func (h *PlaylistHandler) ListPlaylists(c *gin.Context) {
	plType := c.Query("type")
	if plType != "" &&
		plType != database.UserPlaylistTypeStatic &&
		plType != database.UserPlaylistTypeSmart {
		httputil.RespondWithBadRequest(c, "type must be static, smart, or empty")
		return
	}
	p := httputil.ParsePaginationParams(c)
	// Scope to the calling user — a user must not see another user's playlists.
	lists, total, err := h.store.ListUserPlaylistsForUser(CallingUserID(c), plType, p.Limit, p.Offset)
	if err != nil {
		httputil.InternalError(c, "failed to list playlists", err)
		return
	}
	httputil.RespondWithList(c, lists, total, p.Limit, p.Offset)
}

// GetPlaylist — GET /api/v1/playlists/:id
// For static: returns playlist + the stored BookIDs.
// For smart: evaluates the query and returns the live book list
// alongside the playlist metadata. Caches evaluation into
// MaterializedBookIDs for the iTunes push worker.
func (h *PlaylistHandler) GetPlaylist(c *gin.Context) {
	id := c.Param("id")
	pl, err := h.store.GetUserPlaylist(id)
	if err != nil {
		httputil.InternalError(c, "failed to load playlist", err)
		return
	}
	if pl == nil {
		httputil.RespondWithNotFound(c, "playlist", id)
		return
	}

	if !ownedByCaller(c, pl) {
		httputil.RespondWithNotFound(c, "playlist", id)
		return
	}

	resp := gin.H{"playlist": pl}
	switch pl.Type {
	case database.UserPlaylistTypeStatic:
		resp["book_ids"] = pl.BookIDs
	case database.UserPlaylistTypeSmart:
		bookIDs, evalErr := playlist.EvaluateSmartPlaylist(
			h.store, h.indexFn(),
			pl.Query, pl.SortJSON, pl.Limit,
			CallingUserID(c),
		)
		if evalErr != nil {
			// Surface as 503 when the index is unavailable — this is
			// a transient condition during startup. Actual query
			// errors are 400 (user's smart-playlist DSL is busted).
			if evalErr == playlist.ErrSearchIndexUnavailable {
				httputil.RespondWithError(c, 503, evalErr.Error(), "SERVICE_UNAVAILABLE")
				return
			}
			httputil.RespondWithBadRequest(c, evalErr.Error())
			return
		}
		resp["book_ids"] = bookIDs
		// Cache for iTunes sync worker. Persist only if changed.
		if !stringSlicesEqual(pl.MaterializedBookIDs, bookIDs) {
			pl.MaterializedBookIDs = bookIDs
			_ = h.store.UpdateUserPlaylist(pl)
		}
	}
	httputil.RespondWithOK(c, resp)
}

// UpdatePlaylist — PUT /api/v1/playlists/:id
func (h *PlaylistHandler) UpdatePlaylist(c *gin.Context) {
	id := c.Param("id")
	pl, err := h.store.GetUserPlaylist(id)
	if err != nil {
		httputil.InternalError(c, "failed to load playlist", err)
		return
	}
	if pl == nil {
		httputil.RespondWithNotFound(c, "playlist", id)
		return
	}
	if !ownedByCaller(c, pl) {
		httputil.RespondWithNotFound(c, "playlist", id)
		return
	}

	var req PlaylistUpdateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}
	if req.Name != nil {
		pl.Name = strings.TrimSpace(*req.Name)
	}
	if req.Description != nil {
		pl.Description = *req.Description
	}
	if req.BookIDs != nil {
		if pl.Type != database.UserPlaylistTypeStatic {
			httputil.RespondWithBadRequest(c, "book_ids only valid for static playlists")
			return
		}
		pl.BookIDs = *req.BookIDs
	}
	if req.Query != nil {
		if pl.Type != database.UserPlaylistTypeSmart {
			httputil.RespondWithBadRequest(c, "query only valid for smart playlists")
			return
		}
		if _, err := search.ParseQuery(*req.Query); err != nil {
			httputil.RespondWithBadRequest(c, "invalid query: "+err.Error())
			return
		}
		pl.Query = *req.Query
	}
	if req.SortJSON != nil {
		pl.SortJSON = *req.SortJSON
	}
	if req.Limit != nil {
		pl.Limit = *req.Limit
	}
	pl.Dirty = true
	if err := h.store.UpdateUserPlaylist(pl); err != nil {
		httputil.InternalError(c, "failed to update playlist", err)
		return
	}
	httputil.RespondWithOK(c, pl)
}

// DeletePlaylist — DELETE /api/v1/playlists/:id
func (h *PlaylistHandler) DeletePlaylist(c *gin.Context) {
	id := c.Param("id")
	// Load first to enforce ownership — without this any user could delete
	// another user's playlist by ID (IDOR).
	pl, err := h.store.GetUserPlaylist(id)
	if err != nil {
		httputil.InternalError(c, "failed to load playlist", err)
		return
	}
	if !ownedByCaller(c, pl) {
		httputil.RespondWithNotFound(c, "playlist", id)
		return
	}
	if err := h.store.DeleteUserPlaylist(id); err != nil {
		httputil.InternalError(c, "failed to delete playlist", err)
		return
	}
	httputil.RespondWithOK(c, gin.H{"deleted": id})
}

// AddBooksToPlaylist — POST /api/v1/playlists/:id/books
// Appends book IDs to a static playlist, de-duplicating against
// existing entries. No-op on smart playlists.
func (h *PlaylistHandler) AddBooksToPlaylist(c *gin.Context) {
	id := c.Param("id")
	var req PlaylistBooksAddReq
	if err := c.ShouldBindJSON(&req); err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}
	pl, err := h.store.GetUserPlaylist(id)
	if err != nil {
		httputil.InternalError(c, "failed to load playlist", err)
		return
	}
	if pl == nil {
		httputil.RespondWithNotFound(c, "playlist", id)
		return
	}
	if !ownedByCaller(c, pl) {
		httputil.RespondWithNotFound(c, "playlist", id)
		return
	}
	if pl.Type != database.UserPlaylistTypeStatic {
		httputil.RespondWithBadRequest(c, "cannot add books to smart playlist")
		return
	}
	existing := make(map[string]bool, len(pl.BookIDs))
	for _, bid := range pl.BookIDs {
		existing[bid] = true
	}
	for _, bid := range req.BookIDs {
		if bid == "" || existing[bid] {
			continue
		}
		pl.BookIDs = append(pl.BookIDs, bid)
		existing[bid] = true
	}
	pl.Dirty = true
	if err := h.store.UpdateUserPlaylist(pl); err != nil {
		httputil.InternalError(c, "failed to add books", err)
		return
	}
	httputil.RespondWithOK(c, pl)
}

// RemoveBookFromPlaylist — DELETE /api/v1/playlists/:id/books/:bookID
func (h *PlaylistHandler) RemoveBookFromPlaylist(c *gin.Context) {
	id := c.Param("id")
	bookID := c.Param("bookID")
	pl, err := h.store.GetUserPlaylist(id)
	if err != nil {
		httputil.InternalError(c, "failed to load playlist", err)
		return
	}
	if pl == nil {
		httputil.RespondWithNotFound(c, "playlist", id)
		return
	}
	if !ownedByCaller(c, pl) {
		httputil.RespondWithNotFound(c, "playlist", id)
		return
	}
	if pl.Type != database.UserPlaylistTypeStatic {
		httputil.RespondWithBadRequest(c, "cannot remove books from smart playlist")
		return
	}
	filtered := pl.BookIDs[:0]
	for _, b := range pl.BookIDs {
		if b != bookID {
			filtered = append(filtered, b)
		}
	}
	pl.BookIDs = filtered
	pl.Dirty = true
	if err := h.store.UpdateUserPlaylist(pl); err != nil {
		httputil.InternalError(c, "failed to remove book", err)
		return
	}
	httputil.RespondWithOK(c, pl)
}

// ReorderPlaylist — POST /api/v1/playlists/:id/reorder
// Replaces book order. Rejects if the payload changes the set of
// books (use add/remove endpoints for that).
func (h *PlaylistHandler) ReorderPlaylist(c *gin.Context) {
	id := c.Param("id")
	var req PlaylistReorderReq
	if err := c.ShouldBindJSON(&req); err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}
	pl, err := h.store.GetUserPlaylist(id)
	if err != nil {
		httputil.InternalError(c, "failed to load playlist", err)
		return
	}
	if pl == nil {
		httputil.RespondWithNotFound(c, "playlist", id)
		return
	}
	if !ownedByCaller(c, pl) {
		httputil.RespondWithNotFound(c, "playlist", id)
		return
	}
	if pl.Type != database.UserPlaylistTypeStatic {
		httputil.RespondWithBadRequest(c, "cannot reorder smart playlist")
		return
	}
	if !sameBookSet(pl.BookIDs, req.BookIDs) {
		httputil.RespondWithBadRequest(c, "reorder must keep the same book set")
		return
	}
	pl.BookIDs = req.BookIDs
	pl.Dirty = true
	if err := h.store.UpdateUserPlaylist(pl); err != nil {
		httputil.InternalError(c, "failed to reorder", err)
		return
	}
	httputil.RespondWithOK(c, pl)
}

// MaterializePlaylist — POST /api/v1/playlists/:id/materialize
// Evaluates a smart playlist and creates a new static playlist
// from the snapshot. The source smart playlist is left unchanged.
func (h *PlaylistHandler) MaterializePlaylist(c *gin.Context) {
	id := c.Param("id")
	src, err := h.store.GetUserPlaylist(id)
	if err != nil {
		httputil.InternalError(c, "failed to load playlist", err)
		return
	}
	if src == nil {
		httputil.RespondWithNotFound(c, "playlist", id)
		return
	}
	if !ownedByCaller(c, src) {
		httputil.RespondWithNotFound(c, "playlist", id)
		return
	}
	if src.Type != database.UserPlaylistTypeSmart {
		httputil.RespondWithBadRequest(c, "only smart playlists can be materialized")
		return
	}
	bookIDs, evalErr := playlist.EvaluateSmartPlaylist(
		h.store, h.indexFn(),
		src.Query, src.SortJSON, src.Limit,
		CallingUserID(c),
	)
	if evalErr != nil {
		if evalErr == playlist.ErrSearchIndexUnavailable {
			httputil.RespondWithError(c, 503, evalErr.Error(), "SERVICE_UNAVAILABLE")
			return
		}
		httputil.RespondWithBadRequest(c, evalErr.Error())
		return
	}

	snapshot := &database.UserPlaylist{
		Name:            fmt.Sprintf("%s (snapshot %s)", src.Name, time.Now().Format("2006-01-02")),
		Description:     fmt.Sprintf("Materialized from smart playlist %q at %s", src.Name, time.Now().Format(time.RFC3339)),
		Type:            database.UserPlaylistTypeStatic,
		BookIDs:         bookIDs,
		CreatedByUserID: CallingUserID(c),
		Dirty:           true,
	}
	created, err := h.store.CreateUserPlaylist(snapshot)
	if err != nil {
		// Name collision is the common case — retry with a counter.
		for i := 2; i < 10 && err != nil; i++ {
			snapshot.Name = fmt.Sprintf("%s (snapshot %s #%d)", src.Name, time.Now().Format("2006-01-02"), i)
			created, err = h.store.CreateUserPlaylist(snapshot)
		}
		if err != nil {
			httputil.InternalError(c, "failed to materialize", err)
			return
		}
	}
	httputil.RespondWithCreated(c, created)
}

// -----------------------------------------------------------------------
// Package-level helpers
// -----------------------------------------------------------------------

// validatePlaylistCreate checks required fields and type-specific
// shape of a PlaylistCreateReq.
func validatePlaylistCreate(req *PlaylistCreateReq) error {
	if strings.TrimSpace(req.Name) == "" {
		return fmt.Errorf("name is required")
	}
	switch req.Type {
	case database.UserPlaylistTypeStatic:
		if req.Query != "" {
			return fmt.Errorf("static playlist must not have a query")
		}
	case database.UserPlaylistTypeSmart:
		if len(req.BookIDs) > 0 {
			return fmt.Errorf("smart playlist must not have explicit book_ids")
		}
		if strings.TrimSpace(req.Query) == "" {
			return fmt.Errorf("smart playlist requires a query")
		}
		if _, err := search.ParseQuery(req.Query); err != nil {
			return fmt.Errorf("invalid query: %w", err)
		}
	default:
		return fmt.Errorf("type must be static or smart")
	}
	return nil
}

// stringSlicesEqual reports whether a and b are equal element-by-element.
func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// sameBookSet reports whether a and b contain the same elements,
// ignoring order.
func sameBookSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	counts := map[string]int{}
	for _, v := range a {
		counts[v]++
	}
	for _, v := range b {
		counts[v]--
		if counts[v] < 0 {
			return false
		}
	}
	for _, n := range counts {
		if n != 0 {
			return false
		}
	}
	return true
}
