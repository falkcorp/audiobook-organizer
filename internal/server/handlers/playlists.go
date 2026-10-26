// file: internal/server/handlers/playlists.go
// version: 2.6.8
// guid: a7b8c9d0-e1f2-3456-abcd-456789012345
// last-edited: 2026-10-26

package handlers

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
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

// NewPlaylistHandler constructs a PlaylistHandler. indexFn is called at request
// time, so the handler works when the search index is opened after construction
// (in Start()). It may be nil-returning: smart-playlist evaluation then answers 503.
func NewPlaylistHandler(store PlaylistStore, indexFn func() *search.BleveIndex) *PlaylistHandler {
	return &PlaylistHandler{store: store, indexFn: indexFn}
}

// playlistLog carries this file's diagnostics through the log-injection barrier.
var playlistLog = logger.New("handlers.playlists")

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
		// A snapshot cut to the member bound (MaterializePlaylist) says how many
		// books its query matched. Only a list still at the bound is reported, and
		// the store drops the count when the books it holds change (an add and a
		// remove together keep the length, so the length alone would not show it).
		if pl.MaterializedTotal > len(pl.BookIDs) && len(pl.BookIDs) == database.MaxMaterializedMembers {
			resp["book_ids_total"] = pl.MaterializedTotal
		}
	case database.UserPlaylistTypeSmart:
		bookIDs, evalErr := playlist.EvaluateSmartPlaylist(
			c.Request.Context(), h.store, h.indexFn(),
			pl.Query, pl.SortJSON, pl.Limit,
			CallingUserID(c),
		)
		if evalErr != nil {
			if abortIfClientGone(c, evalErr, "GetPlaylist") {
				return // a client that went away is not a bad query
			}
			// Surface as 503 when the index is unavailable — this is
			// a transient condition during startup (errors.Is: the paging helper
			// wraps it). Actual query errors are 400 (user's smart-playlist DSL
			// is busted).
			if errors.Is(evalErr, playlist.ErrSearchIndexUnavailable) {
				httputil.RespondWithError(c, 503, evalErr.Error(), "SERVICE_UNAVAILABLE")
				return
			}
			// A stored definition the evaluator refuses (a per-user term saved before
			// the check existed, a query that no longer parses, a sort spec that is
			// not JSON) is not a bad request: the playlist is fine, its answer is
			// not available. It reads as no members, with the reason, and the list
			// cached by the previous evaluation is dropped so nothing (the ABS
			// surface serves it) keeps answering the old, wrong way.
			if playlist.IsDefinitionError(evalErr) {
				if len(pl.MaterializedBookIDs) > 0 || pl.MaterializedTotal != 0 {
					pl.MaterializedBookIDs, pl.MaterializedTotal = nil, 0
					_ = h.store.UpdateUserPlaylist(pl)
				}
				resp["book_ids"] = []string{}
				resp["members_unavailable"] = true
				resp["members_unavailable_message"] = playlistUnavailableMessage(evalErr)
				httputil.RespondWithOK(c, resp)
				return
			}
			// Anything else (a state read that failed, a store error) is the
			// server's failure, not a bad request: it was answered 400 with the
			// error text. The text is logged through evalCause, which withholds the
			// viewer's id and a book id, and the client gets a plain 500.
			playlistLog.Error("playlists: evaluating %s failed: %s", id, evalCause(evalErr))
			httputil.RespondWithInternalError(c, "this playlist's books could not be worked out; see the server log")
			return
		}
		// A query is evaluated with no result window, so a broad one answers with
		// the whole library. The answer and the cached list are both cut to
		// database.MaxMaterializedMembers, and book_ids_total / materialized_total
		// carry the real count when they were.
		kept, total := database.CapMembers(bookIDs)
		resp["book_ids"] = kept
		if total > 0 {
			resp["book_ids_total"] = total
		}
		// Cache for iTunes sync worker. Persist only if changed.
		if !stringSlicesEqual(pl.MaterializedBookIDs, kept) || pl.MaterializedTotal != total {
			pl.MaterializedBookIDs, pl.MaterializedTotal = kept, total
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
			// A list that omits a stored member is refused, not silently
			// kept (see database.MemberListOmitted).
			if omitted := database.MemberListOmitted(pl.BookIDs, *req.BookIDs, nil); len(omitted) > 0 {
				return &playlistConflictError{msg: "book_ids omits books currently in this playlist (it may have changed since you loaded it); reload and retry, and use DELETE /playlists/:id/books/:bookID to remove books"}
			}
			pl.BookIDs = database.MergeMemberListNoLoss(pl.BookIDs, *req.BookIDs)
		}
		if req.Query != nil {
			if pl.Type != database.UserPlaylistTypeSmart {
				return playlistBadRequest("query only valid for smart playlists")
			}
			// Only a CHANGED query is checked, as UpdateCollection does: a client
			// that resends the stored query with a rename must not be refused for a
			// query it did not touch.
			if *req.Query != pl.Query {
				if err := validatePlaylistQuery(*req.Query); err != nil {
					return playlistBadRequest(err.Error())
				}
			}
			pl.Query = *req.Query
		}
		if req.SortJSON != nil {
			if pl.Type == database.UserPlaylistTypeSmart && *req.SortJSON != pl.SortJSON {
				if err := playlist.ValidateSortJSON(*req.SortJSON); err != nil {
					return playlistBadRequest(err.Error())
				}
			}
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

// playlistConflictError aborts a mutation with 409.
type playlistConflictError struct{ msg string }

func (e *playlistConflictError) Error() string { return e.msg }

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
	var conflictErr *playlistConflictError
	switch {
	case err == nil:
		httputil.RespondWithOK(c, updated)
	case errors.As(err, &reqErr):
		httputil.RespondWithBadRequest(c, reqErr.msg)
	case errors.As(err, &conflictErr):
		httputil.RespondWithConflict(c, conflictErr.msg)
	case errors.Is(err, database.ErrUserPlaylistNotFound), errors.Is(err, errPlaylistNotOwned):
		httputil.RespondWithNotFound(c, "playlist", id)
	case errors.Is(err, database.ErrUserPlaylistNameInUse), errors.Is(err, database.ErrUserPlaylistVersionConflict):
		httputil.RespondWithConflict(c, err.Error())
	default:
		httputil.InternalError(c, failMsg, err)
	}
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
		c.Request.Context(), h.store, h.indexFn(),
		src.Query, src.SortJSON, src.Limit,
		CallingUserID(c),
	)
	if evalErr != nil {
		if abortIfClientGone(c, evalErr, "MaterializeSmartPlaylist") {
			return // nothing is created for a request nobody is waiting on
		}
		if errors.Is(evalErr, playlist.ErrSearchIndexUnavailable) {
			httputil.RespondWithError(c, 503, evalErr.Error(), "SERVICE_UNAVAILABLE")
			return
		}
		if !playlist.IsDefinitionError(evalErr) {
			playlistLog.Error("playlists: materializing %s failed: %s", id, evalCause(evalErr))
			httputil.RespondWithInternalError(c, "this playlist's books could not be worked out; see the server log")
			return
		}
		httputil.RespondWithBadRequest(c, evalErr.Error())
		return
	}

	// The snapshot is a static list, which nothing downstream cuts (the ABS
	// surface expands every id of a static playlist), so it is cut here to the
	// same bound as every other member list. The true count is kept in
	// materialized_total and in the description, so a snapshot that stops short of
	// its query's answer says so.
	snapIDs, snapTotal := database.CapMembers(bookIDs)
	description := fmt.Sprintf("Materialized from smart playlist %q at %s", src.Name, time.Now().Format(time.RFC3339))
	if snapTotal > 0 {
		description += fmt.Sprintf("; the query matched %d books and the snapshot holds the first %d", snapTotal, len(snapIDs))
	}
	snapshot := &database.UserPlaylist{
		Name:              fmt.Sprintf("%s (snapshot %s)", src.Name, time.Now().Format("2006-01-02")),
		Description:       description,
		Type:              database.UserPlaylistTypeStatic,
		BookIDs:           snapIDs,
		MaterializedTotal: snapTotal,
		CreatedByUserID:   CallingUserID(c),
		Dirty:             true,
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
		if err := validatePlaylistQuery(req.Query); err != nil {
			return err
		}
		if err := playlist.ValidateSortJSON(req.SortJSON); err != nil {
			return err
		}
	default:
		return fmt.Errorf("type must be static or smart")
	}
	return nil
}

// validatePlaylistQuery reports why a smart playlist's query cannot be saved: it
// does not parse, or it puts a per-user term (read_status, progress_pct,
// last_played) where the evaluator cannot give it its boolean meaning (under an
// OR with a sibling, or under a NOT over a group; search.CheckPerUserBoolean) or
// with a value it cannot evaluate (a status that does not exist, a pattern, a
// number or a date that does not parse; search.CheckPerUserValues).
func validatePlaylistQuery(query string) error {
	ast, err := search.ParseQuery(query)
	if err != nil {
		// A /regex/ holds parentheses and pipes the grammar cannot read
		// (title:/^(the|a) /): say what is wrong, not only that it did not parse.
		if rerr := search.UnparsableRegexQuery(query, err, search.IsIndexedField); rerr != nil {
			return fmt.Errorf("invalid query: %w", rerr)
		}
		return fmt.Errorf("invalid query: %w", err)
	}
	if err := search.CheckQueryTerms(ast, time.Now()); err != nil {
		return fmt.Errorf("invalid query: %w", err)
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

// playlistUnavailableMessage is the text of members_unavailable_message for a
// playlist whose stored definition the evaluator refuses. A per-user refusal is
// the sentence the person got when they tried to save such a query and says what
// to write instead; any other failure keeps a generic text, without the internal
// error.
func playlistUnavailableMessage(err error) string {
	if search.IsQueryTermError(err) {
		return "This playlist's query can no longer be evaluated, so it shows no books: " + search.QueryTermReason(err) + ". Edit the query to fix it."
	}
	return "This playlist's query or sort order can no longer be evaluated, so it shows no books. Edit the playlist to fix it."
}
