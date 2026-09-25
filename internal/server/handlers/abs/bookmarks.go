// file: internal/server/handlers/abs/bookmarks.go
// version: 1.1.0
// guid: 8d3c15af-6e29-4b70-91c8-4a05f7e2b3d6
// last-edited: 2026-09-25

package abs

import (
	"net/http"
	"slices"
	"sort"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/syncapi/progress"
	"github.com/gin-gonic/gin"
)

// Phase 6 — bookmarks CRUD, over the keyspace TASK-09 built
// (internal/database/pebble_store_bookmarks.go). Shapes captured from real
// Audiobookshelf 2.36.0 on 2026-08-02:
//
//	GET    /api/me/bookmarks/:id              -> 200 {"bookmarks":[…]}
//	POST   /api/me/item/:id/bookmark          -> 200 <bare bookmark object>
//	PATCH  /api/me/item/:id/bookmark          -> 200 <bare bookmark object>
//	DELETE /api/me/item/:id/bookmark/:time    -> 200 text/plain "OK" | 404
//
// 🔑 A BOOKMARK IS KEYED BY (item, time), NOT by an opaque id. The delete surface
// puts the time value itself in the URL path and update matches on it, which is why
// progress.CanonicalTimeKey exists: AudioBooth sends `time` as an Int in the path and
// round-trips it as a Double in bodies, so "100", "100.0" and float noise must all
// address the SAME stored row or a user's bookmark becomes undeletable.
//
// 🔑 MERGED ITEMS (ABS-ALIAS-HELPER, 2026-09-25). The keyspace is keyed by
// libraryItemId, and a dedup merge does not move bookmarks: those made before the
// merge stay under the LOSER's id, which now redirects to the survivor. So every
// route here reads the item's bookmark SET, the canonical id plus every alias
// that still redirects to it (bookmarkItemIDs). A new bookmark is written under
// the canonical id; a rename or delete acts on whichever ids hold that time.
// Bodies render every bookmark under the id the client addressed (itemRef.echo),
// because AudioBooth lists an item page's bookmarks by that page's id.

// bookmarkRequest is the POST/PATCH body. Time is a JSON number (Int or Double —
// both decode into float64, which is the whole point) and Title is required, matching
// real ABS and progress.ValidateBookmark.
type bookmarkRequest struct {
	Time  float64 `json:"time"`
	Title string  `json:"title"`
}

// ── GET /api/me/bookmarks/:id ───────────────────────────────────────────────

// ListItemBookmarks handles GET /api/me/bookmarks/:id.
//
// A read failure is a 5xx, never `{"bookmarks":[]}`. This list is item-scoped so it
// carries none of §1.8.1's whole-library delete risk, but the same reasoning applies
// in miniature: a client told "this book has no bookmarks" cannot distinguish that
// from "we could not read them", and one of those answers is destructive to act on.
func (h *Handler) ListItemBookmarks(c *gin.Context) {
	user, ref, ok := h.resolveTarget(c)
	if !ok {
		return
	}
	stored, err := h.itemBookmarks(user.ID, ref)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "could not load bookmarks")
		return
	}
	respondJSON(c, http.StatusOK, gin.H{"bookmarks": bookmarkDTOs(stored, ref)})
}

// ── POST /api/me/item/:id/bookmark ──────────────────────────────────────────

// CreateBookmark handles POST /api/me/item/:id/bookmark and answers with the created
// bookmark, matching the oracle capture.
//
// CreateBookmark in the store is an UPSERT at (user, item, canonical time): saving
// twice at the same spot updates the title rather than producing a duplicate, and the
// original CreatedAt survives. That mirrors real ABS, where the create endpoint is
// also the natural "re-save at the same spot" path a client may replay.
//
// The upsert is under the CANONICAL id. When an alias id already holds a bookmark
// at that time (made before a merge), that one is renamed instead, so re-saving a
// spot never produces a second bookmark at it.
func (h *Handler) CreateBookmark(c *gin.Context) {
	user, ref, ok := h.resolveTarget(c)
	if !ok {
		return
	}
	var req bookmarkRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, "invalid bookmark body")
		return
	}

	bookmark := progress.Bookmark{
		UserID:  user.ID,
		ItemID:  ref.SyncID,
		TimeSec: req.Time,
		Title:   strings.TrimSpace(req.Title),
	}
	if err := progress.ValidateBookmark(bookmark); err != nil {
		respondError(c, http.StatusBadRequest, "invalid bookmark: "+err.Error())
		return
	}
	holders, err := h.bookmarkHolders(user.ID, ref, req.Time)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "could not save bookmark")
		return
	}
	if len(holders) > 0 && !slices.Contains(holders, ref.SyncID) {
		for _, itemID := range holders {
			if err := h.bookmarks.UpdateBookmarkTitle(user.ID, itemID, req.Time, bookmark.Title); err != nil {
				respondError(c, http.StatusInternalServerError, "could not save bookmark")
				return
			}
		}
		respondJSON(c, http.StatusOK, h.readBackBookmark(user.ID, ref, holders[0], req.Time, bookmark))
		return
	}
	if err := h.bookmarks.CreateBookmark(bookmark); err != nil {
		respondError(c, http.StatusInternalServerError, "could not save bookmark")
		return
	}

	// Read back rather than echo the request: CreatedAt is assigned by the store
	// (and PRESERVED across an upsert), so echoing the input would report a fresh
	// creation time for a bookmark that already existed.
	respondJSON(c, http.StatusOK, h.readBackBookmark(user.ID, ref, ref.SyncID, req.Time, bookmark))
}

// ── PATCH /api/me/item/:id/bookmark ─────────────────────────────────────────

// UpdateBookmark handles PATCH /api/me/item/:id/bookmark — rename in place.
//
// The bookmark is addressed by the `time` in the BODY (there is no time path segment
// on this route), and a time with no bookmark is a 404 rather than an implicit
// create: the store's UpdateBookmarkTitle refuses to conjure a row, and silently
// creating one would put a bookmark at a position the user never marked.
func (h *Handler) UpdateBookmark(c *gin.Context) {
	user, ref, ok := h.resolveTarget(c)
	if !ok {
		return
	}
	var req bookmarkRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, "invalid bookmark body")
		return
	}
	title := strings.TrimSpace(req.Title)
	if title == "" {
		respondError(c, http.StatusBadRequest, "bookmark title is required")
		return
	}

	// Existence is read first rather than inferred from the store's error: the
	// store reports "no such bookmark" as an untyped error, and a genuine store
	// failure must not be reported to the client as a 404, which reads as "your
	// bookmark is gone".
	holders, err := h.bookmarkHolders(user.ID, ref, req.Time)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "could not update bookmark")
		return
	}
	if len(holders) == 0 {
		respondNotFoundPlain(c)
		return
	}
	for _, itemID := range holders {
		if err := h.bookmarks.UpdateBookmarkTitle(user.ID, itemID, req.Time, title); err != nil {
			respondError(c, http.StatusInternalServerError, "could not update bookmark")
			return
		}
	}
	respondJSON(c, http.StatusOK, h.readBackBookmark(user.ID, ref, holders[0], req.Time, progress.Bookmark{
		UserID: user.ID, ItemID: holders[0], TimeSec: req.Time, Title: title,
	}))
}

// ── DELETE /api/me/item/:id/bookmark/:time ──────────────────────────────────

// DeleteBookmark handles DELETE /api/me/item/:id/bookmark/:time.
//
// The time arrives as a URL PATH SEGMENT, parsed with progress.ParseTimeSec
// (ParseFloat, never ParseInt) because clients send "100" here and "100.0" elsewhere
// for the same bookmark. A malformed time is a 404 rather than a 400: from the
// client's perspective there is no bookmark at an unparseable position, and 404 is
// what real ABS answers for a time with no bookmark.
//
// Every id in the item's set that holds the time loses it: the client sees one
// bookmark there (bookmarkDTOs collapses them), so deleting only one would leave
// the "deleted" bookmark on screen.
func (h *Handler) DeleteBookmark(c *gin.Context) {
	user, ref, ok := h.resolveTarget(c)
	if !ok {
		return
	}
	timeSec, err := progress.ParseTimeSec(c.Param("time"))
	if err != nil {
		respondNotFoundPlain(c)
		return
	}
	holders, err := h.bookmarkHolders(user.ID, ref, timeSec)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "could not delete bookmark")
		return
	}
	if len(holders) == 0 {
		respondNotFoundPlain(c)
		return
	}
	for _, itemID := range holders {
		if err := h.bookmarks.DeleteBookmark(user.ID, itemID, timeSec); err != nil {
			respondError(c, http.StatusInternalServerError, "could not delete bookmark")
			return
		}
	}
	respondPlainOK(c)
}

// ── helpers ─────────────────────────────────────────────────────────────────

// bookmarkItemIDs is the item's bookmark set: the canonical id first, then every
// alias that still redirects to it. An alias lookup error is an error: a set
// missing an id would answer "no bookmark" for one the user can see.
func (h *Handler) bookmarkItemIDs(ref itemRef) ([]string, error) {
	aliases, err := h.identity.ListSyncAliases(ref.SyncID)
	if err != nil {
		return nil, err
	}
	return append([]string{ref.SyncID}, aliases...), nil
}

// itemBookmarks reads every bookmark stored under the item's set, in set order.
func (h *Handler) itemBookmarks(userID string, ref itemRef) ([]progress.Bookmark, error) {
	ids, err := h.bookmarkItemIDs(ref)
	if err != nil {
		return nil, err
	}
	var out []progress.Bookmark
	for _, id := range ids {
		stored, err := h.bookmarks.ListBookmarks(userID, id)
		if err != nil {
			return nil, err
		}
		out = append(out, stored...)
	}
	return out, nil
}

// bookmarkHolders returns the ids in the item's set holding a bookmark at
// timeSec, canonical first, comparing through CanonicalTimeKey so an Int and a
// Double form of the same instant are recognised as the same row.
func (h *Handler) bookmarkHolders(userID string, ref itemRef, timeSec float64) ([]string, error) {
	ids, err := h.bookmarkItemIDs(ref)
	if err != nil {
		return nil, err
	}
	want := progress.CanonicalTimeKey(timeSec)
	var holders []string
	for _, id := range ids {
		stored, err := h.bookmarks.ListBookmarks(userID, id)
		if err != nil {
			return nil, err
		}
		for i := range stored {
			if progress.CanonicalTimeKey(stored[i].TimeSec) == want {
				holders = append(holders, id)
				break
			}
		}
	}
	return holders, nil
}

// readBackBookmark returns the stored row for (user, itemID, time) rendered for
// ref, falling back to the value just written when the read-back fails. The
// fallback keeps the response non-empty — an empty 200 is fatal to these decoders
// (§1.8.6) — at the cost of a possibly-wrong CreatedAt, which is strictly the
// lesser failure.
func (h *Handler) readBackBookmark(userID string, ref itemRef, itemID string, timeSec float64, fallback progress.Bookmark) bookmarkDTO {
	want := progress.CanonicalTimeKey(timeSec)
	if stored, err := h.bookmarks.ListBookmarks(userID, itemID); err == nil {
		for i := range stored {
			if progress.CanonicalTimeKey(stored[i].TimeSec) == want {
				return toBookmarkDTO(stored[i], ref)
			}
		}
	}
	return toBookmarkDTO(fallback, ref)
}

// toBookmarkDTO renders one bookmark in the oracle's exact four-field shape: no id,
// no userId, no updatedAt. Adding fields real ABS does not send is not free — a
// strict decoder that tolerates them today may not after a client update.
//
// libraryItemId is the id the client addressed (ref.RequestedID), whichever id of
// the item's set the row is stored under.
func toBookmarkDTO(b progress.Bookmark, ref itemRef) bookmarkDTO {
	return bookmarkDTO{
		CreatedAt:     b.CreatedAt,
		LibraryItemID: ref.RequestedID,
		Time:          b.TimeSec,
		Title:         b.Title,
	}
}

// bookmarkDTOs renders an item's list, ordered by time so the client sees a stable
// sequence across refreshes rather than the store's iteration order. Rows at the
// same instant under different ids of the set collapse to the first one, which is
// the canonical id's when it has one (itemBookmarks reads it first).
func bookmarkDTOs(stored []progress.Bookmark, ref itemRef) []bookmarkDTO {
	out := make([]bookmarkDTO, 0, len(stored))
	seen := make(map[string]bool, len(stored))
	for i := range stored {
		key := progress.CanonicalTimeKey(stored[i].TimeSec)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, toBookmarkDTO(stored[i], ref))
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Time < out[j].Time })
	return out
}
