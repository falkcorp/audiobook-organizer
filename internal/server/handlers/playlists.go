// file: internal/server/handlers/playlists.go
// version: 2.4.0
// guid: a7b8c9d0-e1f2-3456-abcd-456789012345
// last-edited: 2026-09-19

package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/playlist"
	"github.com/falkcorp/audiobook-organizer/internal/search"
	"github.com/falkcorp/audiobook-organizer/internal/security/pathvalidation"
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

// PlaylistStore is five direct calls plus two for the evaluator, measured by emptying the interface and reading the
// compiler's enumeration. It was 52 methods of database.* embeds.
type PlaylistStore interface {
	GetUserPlaylist(id string) (*database.UserPlaylist, error)
	ListUserPlaylistsForUser(userID, playlistType string, limit, offset int) ([]database.UserPlaylist, int, error)
	CreateUserPlaylist(pl *database.UserPlaylist) (*database.UserPlaylist, error)
	UpdateUserPlaylist(pl *database.UserPlaylist) error
	DeleteUserPlaylist(id string) error

	// Carried so this value satisfies playlist.EvaluateSmartPlaylist.
	GetBookByID(id string) (*database.Book, error)
	GetUserBookState(userID, bookID string) (*database.UserBookState, error)
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
		// errors.Is, not the message: the store's text said "already in use"
		// while this matched "already exists", so a duplicate name was a 500.
		if errors.Is(err, database.ErrUserPlaylistNameInUse) {
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
	var req PlaylistUpdateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}
	if req.Query != nil {
		if _, err := search.ParseQuery(*req.Query); err != nil {
			httputil.RespondWithBadRequest(c, "invalid query: "+err.Error())
			return
		}
	}
	h.mutatePlaylist(c, "failed to update playlist", func(pl *database.UserPlaylist) error {
		if req.Name != nil {
			pl.Name = strings.TrimSpace(*req.Name)
		}
		if req.Description != nil {
			pl.Description = *req.Description
		}
		if req.BookIDs != nil {
			if pl.Type != database.UserPlaylistTypeStatic {
				return playlistBadRequest("book_ids only valid for static playlists")
			}
			// Never a blind replace: the retry re-applies this against the
			// FRESH row, and a list the client built from an older read would
			// silently drop members added since. See MergeMemberListNoLoss;
			// removal goes through DELETE /playlists/:id/books/:bookID.
			pl.BookIDs = database.MergeMemberListNoLoss(pl.BookIDs, *req.BookIDs)
		}
		if req.Query != nil {
			if pl.Type != database.UserPlaylistTypeSmart {
				return playlistBadRequest("query only valid for smart playlists")
			}
			pl.Query = *req.Query
		}
		if req.SortJSON != nil {
			pl.SortJSON = *req.SortJSON
		}
		if req.Limit != nil {
			pl.Limit = *req.Limit
		}
		return nil
	})
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
	var req PlaylistBooksAddReq
	if err := c.ShouldBindJSON(&req); err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}
	h.mutatePlaylist(c, "failed to add books", func(pl *database.UserPlaylist) error {
		if pl.Type != database.UserPlaylistTypeStatic {
			return playlistBadRequest("cannot add books to smart playlist")
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
		return nil
	})
}

// RemoveBookFromPlaylist — DELETE /api/v1/playlists/:id/books/:bookID
func (h *PlaylistHandler) RemoveBookFromPlaylist(c *gin.Context) {
	bookID := c.Param("bookID")
	h.mutatePlaylist(c, "failed to remove book", func(pl *database.UserPlaylist) error {
		if pl.Type != database.UserPlaylistTypeStatic {
			return playlistBadRequest("cannot remove books from smart playlist")
		}
		filtered := make([]string, 0, len(pl.BookIDs))
		for _, b := range pl.BookIDs {
			if b != bookID {
				filtered = append(filtered, b)
			}
		}
		pl.BookIDs = filtered
		return nil
	})
}

// ReorderPlaylist — POST /api/v1/playlists/:id/reorder
// Replaces book order. Rejects if the payload changes the set of
// books (use add/remove endpoints for that).
func (h *PlaylistHandler) ReorderPlaylist(c *gin.Context) {
	var req PlaylistReorderReq
	if err := c.ShouldBindJSON(&req); err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}
	h.mutatePlaylist(c, "failed to reorder", func(pl *database.UserPlaylist) error {
		if pl.Type != database.UserPlaylistTypeStatic {
			return playlistBadRequest("cannot reorder smart playlist")
		}
		// Checked against the CURRENT row on every attempt: if a concurrent add
		// changed the set, this reorder is rejected rather than dropping the add.
		if !sameBookSet(pl.BookIDs, req.BookIDs) {
			return playlistBadRequest("reorder must keep the same book set")
		}
		pl.BookIDs = append([]string(nil), req.BookIDs...)
		return nil
	})
}

// playlistRequestError aborts a mutation with a client error.
type playlistRequestError struct{ msg string }

func (e *playlistRequestError) Error() string { return e.msg }

func playlistBadRequest(msg string) error { return &playlistRequestError{msg: msg} }

// errPlaylistNotOwned makes another user's playlist indistinguishable from a
// missing one (404, never 403), as ownedByCaller's doc requires.
var errPlaylistNotOwned = errors.New("playlist not owned by caller")

// mutatePlaylist is the read-modify-write behind every native playlist edit.
//
// 🔴 IT MUST GO THROUGH database.UpdateUserPlaylistWithRetry. These handlers
// used to read the row, change it, and write the WHOLE record back with no
// version check, so a concurrent edit from any other writer (the ABS surface's
// playlist routes since 2026-09-19, the iTunes dirty-clear) was silently erased
// by whichever write landed second. The store now compares Version and the
// helper re-reads and re-applies mutate on a conflict, so concurrent edits
// compose.
func (h *PlaylistHandler) mutatePlaylist(c *gin.Context, failMsg string, mutate func(pl *database.UserPlaylist) error) {
	id := c.Param("id")
	updated, err := database.UpdateUserPlaylistWithRetry(h.store, id, func(pl *database.UserPlaylist) error {
		if !ownedByCaller(c, pl) {
			return errPlaylistNotOwned
		}
		if err := mutate(pl); err != nil {
			return err
		}
		pl.Dirty = true
		return nil
	})
	var reqErr *playlistRequestError
	switch {
	case err == nil:
		httputil.RespondWithOK(c, updated)
	case errors.As(err, &reqErr):
		httputil.RespondWithBadRequest(c, reqErr.msg)
	case errors.Is(err, database.ErrUserPlaylistNotFound), errors.Is(err, errPlaylistNotOwned):
		httputil.RespondWithNotFound(c, "playlist", id)
	case errors.Is(err, database.ErrUserPlaylistNameInUse), errors.Is(err, database.ErrUserPlaylistVersionConflict):
		httputil.RespondWithConflict(c, err.Error())
	default:
		httputil.InternalError(c, failMsg, err)
	}
}

// ExportPlaylistM3U — GET /api/v1/playlists/:id/export.m3u
// Writes the playlist's resolved membership as a standard #EXTM3U file: a
// #EXTINF duration+title comment followed by the file's path, one pair per
// book, in playlist order.
//
// Static playlists use BookIDs; smart playlists use MaterializedBookIDs —
// the last evaluation, not a live re-query — matching the read-mostly
// convention playlistDTO already uses in abs/playlists.go. A smart playlist
// that has never been materialized therefore exports a header-only file
// (just "#EXTM3U") rather than erroring.
//
// Paths are emitted as Book.FilePath, i.e. absolute, matching the scanner's
// own M3U importer: parseM3UFile in internal/scanner/scanner.go takes an
// absolute entry as-is and only resolves a relative one against the .m3u
// file's own directory. Emitting relative paths would only round-trip if the
// exported file were saved back into that exact source directory, which a
// downloaded file cannot guarantee — so this trades "opens on any machine"
// for "round-trips through this repo's own importer and resolves without
// the client having to know the library root."
//
// A book ID that no longer resolves, or whose FilePath is empty, is dropped
// rather than written as a blank/placeholder line — the same "stale
// reference" handling playlistItems already applies in abs/playlists.go.
func (h *PlaylistHandler) ExportPlaylistM3U(c *gin.Context) {
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

	bookIDs := pl.BookIDs
	if pl.Type == database.UserPlaylistTypeSmart {
		bookIDs = pl.MaterializedBookIDs
	}

	var buf strings.Builder
	buf.WriteString("#EXTM3U\n")
	for _, bid := range bookIDs {
		book, err := h.store.GetBookByID(bid)
		if err != nil || book == nil || book.FilePath == "" {
			continue // stale/unresolved reference — dropped, not a placeholder line
		}
		duration := 0
		if book.Duration != nil {
			duration = *book.Duration
		}
		fmt.Fprintf(&buf, "#EXTINF:%d,%s\n%s\n", duration, extinfTitle(book.Title), book.FilePath)
	}

	// filename= comes from the user-chosen playlist name — sanitize it so a
	// name like `../../etc/passwd`, or one containing quotes or CRLF, cannot
	// escape the attachment filename or inject extra response headers.
	filename := pathvalidation.SanitizeFilename(pl.Name) + ".m3u"
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	c.Data(http.StatusOK, "audio/x-mpegurl; charset=utf-8", []byte(buf.String()))
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

// extinfTitle returns title with any CR/LF stripped so it cannot inject an
// extra line into the #EXTINF/path pair it is written into. Commas are left
// untouched: the #EXTINF format is "#EXTINF:<duration>,<title>", parsed by
// splitting on the FIRST comma only, so an embedded comma in the title
// cannot be mistaken for the duration/title separator or break the format.
func extinfTitle(title string) string {
	title = strings.ReplaceAll(title, "\r\n", " ")
	title = strings.ReplaceAll(title, "\n", " ")
	title = strings.ReplaceAll(title, "\r", " ")
	return title
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
