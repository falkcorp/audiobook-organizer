// file: internal/server/handlers/abs/item_ref.go
// version: 1.0.0
// guid: 6c1e9a47-2b58-4f3d-8e70-a94d3c51b2f8
// last-edited: 2026-09-25

package abs

import (
	"errors"
	"net/http"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	servermiddleware "github.com/falkcorp/audiobook-organizer/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

// ── The one item-id resolver (ABS-ALIAS-HELPER) ─────────────────────────────
//
// Every ABS request that names a library item (:id in the path, or a
// libraryItemId in a body) resolves it HERE, and nowhere else.
//
// Why one place. A dedup merge leaves the loser's libraryItemId redirecting to
// the survivor. A client can go on addressing the loser id (an ALIAS) for as
// long as it has it cached: an item page, a download, a playlist entry. Three
// rules follow, and #3558 applied them endpoint by endpoint, which is how
// bookmarks were missed:
//
//  1. Storage keys by the CANONICAL item: ref.BookID and ref.SyncID. A write
//     through an alias lands on the survivor's record, never on a second one.
//  2. Response bodies the client files under the id it asked with echo
//     ref.RequestedID (ref.echo). AudioBooth keys its item page, its local
//     progress row and its bookmark list by the id it opened; a body naming
//     another id never reaches that page.
//  3. The use is RECORDED per user (noteAliasUse). /api/me carries alias
//     progress rows only for aliases the user's client has addressed
//     (userdata.go ClientMediaProgress), so a finished merged book is not
//     counted once per id in the client's stats.
//
// "Rewrite item-id fields back to the requested id" is done at render time
// from the ref, not by rewriting serialized JSON: the handler knows which
// fields are item ids, a byte-level rewrite would not.

// itemRef is a resolved client item id.
type itemRef struct {
	// RequestedID is the libraryItemId the client addressed (a row-id prefix
	// stripped). Equal to SyncID unless the client holds an alias.
	RequestedID string
	// SyncID is the canonical (live) libraryItemId.
	SyncID string
	// BookID is the internal book the canonical item points at.
	BookID string
}

// isAlias reports whether the client addressed a merge loser's id.
func (r itemRef) isAlias() bool { return r.RequestedID != "" && r.RequestedID != r.SyncID }

// echo maps an item id about to be rendered: the canonical id becomes the id
// the client asked with; any other id is returned unchanged.
func (r itemRef) echo(id string) string {
	if r.isAlias() && id == r.SyncID {
		return r.RequestedID
	}
	return id
}

// errItemNotFound: no candidate id names a live item.
var errItemNotFound = errors.New("abs: library item not found")

// lookupItemRef resolves raw to an itemRef.
//
// ResolveSyncItem FOLLOWS MERGE REDIRECTS: a client still holding the syncID
// of a book that lost a dedup merge resolves to the surviving book instead of
// a 404 that loses the user's place (spec §4.2). That is the reason
// libraryItemId is not the Book ULID.
//
// With allowRowID, raw may also be a mediaProgress ROW id, "<userID>-<syncID>".
// Our read half renders row ids that way (userdata.go / item.go), and real ABS
// keys DELETE /api/me/progress/:id by the ROW id (verified against the oracle
// 2026-08-02, where deleting by libraryItemId answers 404), so a client that
// read the list and handed the row id back must be understood or reset-progress
// silently does nothing. The prefix stripped is always the AUTHENTICATED
// user's own id: the parse does not depend on whether a user id can contain
// the separator, and one user can never address another user's row by
// constructing an id. The row-id reading is tried first: it is the more
// specific one, and a bare syncID can never carry the prefix.
//
// A store error on a candidate is returned only when no other candidate
// resolves; a 404 reads to the client as "this item/row does not exist", and
// that must not be said about an item we could not read.
func (h *Handler) lookupItemRef(userID, raw string, allowRowID bool) (itemRef, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || h.identity == nil {
		return itemRef{}, errItemNotFound
	}
	candidates := []string{raw}
	if prefix := userID + "-"; allowRowID && userID != "" && strings.HasPrefix(raw, prefix) {
		candidates = []string{strings.TrimPrefix(raw, prefix), raw}
	}
	var firstErr error
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		item, err := h.identity.ResolveSyncItem(candidate)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if item == nil || item.CurrentBookID == "" {
			continue
		}
		syncID := item.SyncID
		if syncID == "" {
			syncID = candidate
		}
		return itemRef{RequestedID: candidate, SyncID: syncID, BookID: item.CurrentBookID}, nil
	}
	if firstErr != nil {
		return itemRef{}, firstErr
	}
	return itemRef{}, errItemNotFound
}

// noteAliasUse records that userID's client addressed an alias (rule 3).
//
// FAIL OPEN: the request it rides on is served whatever happens here, because
// the record only widens a later /api/me list. A failure is logged and retried
// on the next request through the same alias. aliasUseSeen short-circuits
// repeats in this process so a steady stream of requests through one alias
// costs one store read, not one per request.
func (h *Handler) noteAliasUse(userID string, ref itemRef) {
	if h.aliasUses == nil || userID == "" || !ref.isAlias() {
		return
	}
	key := userID + "\x00" + ref.RequestedID
	if _, seen := h.aliasUseSeen.Load(key); seen {
		return
	}
	if err := h.aliasUses.RecordSyncAliasUse(userID, ref.RequestedID); err != nil {
		progressLog.Warn("abs: could not record an alias item id use; its /api/me row may be missing until the next request through it: user_id=%s alias=%s canonical=%s: %v",
			logger.SanitizeLogValue(userID), logger.SanitizeLogValue(ref.RequestedID),
			logger.SanitizeLogValue(ref.SyncID), err)
		return
	}
	h.aliasUseSeen.Store(key, struct{}{})
}

// currentUserID is the authenticated user's id, or "" on an unauthenticated
// route (the cover route has no user).
func currentUserID(c *gin.Context) string {
	if u, ok := servermiddleware.CurrentUser(c); ok && u != nil {
		return u.ID
	}
	return ""
}

// resolveItemAs resolves the :id path parameter to its book for the item
// routes (GET /api/items/:id, play, cover, file). It writes the 404 or 500
// itself and returns a nil book so callers can simply return.
func (h *Handler) resolveItemAs(c *gin.Context) (*database.Book, itemRef) {
	userID := currentUserID(c)
	ref, err := h.lookupItemRef(userID, c.Param("id"), false)
	if errors.Is(err, errItemNotFound) {
		respondError(c, http.StatusNotFound, "library item not found")
		return nil, itemRef{}
	}
	if err != nil {
		respondError(c, http.StatusInternalServerError, "could not resolve library item")
		return nil, itemRef{}
	}
	book, err := h.library.GetBookByID(ref.BookID)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "could not load library item")
		return nil, itemRef{}
	}
	if book == nil {
		respondError(c, http.StatusNotFound, "library item not found")
		return nil, itemRef{}
	}
	h.noteAliasUse(userID, ref)
	return book, ref
}

// resolveItem is resolveItemAs for callers that render nothing item-keyed.
func (h *Handler) resolveItem(c *gin.Context) *database.Book {
	book, _ := h.resolveItemAs(c)
	return book
}

// resolveTarget resolves the :id path parameter for the per-user routes
// (progress, bookmarks, remove-from-continue-listening), accepting the
// mediaProgress row-id form too. Those routes answer a missing item exactly
// as real ABS does, a plain-text 404; a store error is a 500, never a 404.
func (h *Handler) resolveTarget(c *gin.Context) (*database.User, itemRef, bool) {
	u, found := servermiddleware.CurrentUser(c)
	if !found || u == nil {
		respondError(c, http.StatusUnauthorized, "authentication required")
		return nil, itemRef{}, false
	}
	ref, err := h.lookupItemRef(u.ID, c.Param("id"), true)
	if errors.Is(err, errItemNotFound) {
		respondNotFoundPlain(c)
		return nil, itemRef{}, false
	}
	if err != nil {
		progressLog.Warn("abs: could not resolve item id: user_id=%s id=%s: %v",
			logger.SanitizeLogValue(u.ID), logger.SanitizeLogValue(c.Param("id")), err)
		respondError(c, http.StatusInternalServerError, "could not resolve library item")
		return nil, itemRef{}, false
	}
	h.noteAliasUse(u.ID, ref)
	return u, ref, true
}

// resolveBodyItem resolves a libraryItemId carried in a request BODY (batch
// progress update, offline session replay) for userID, recording an alias use.
func (h *Handler) resolveBodyItem(userID, raw string, allowRowID bool) (itemRef, error) {
	ref, err := h.lookupItemRef(userID, raw, allowRowID)
	if err == nil {
		h.noteAliasUse(userID, ref)
	}
	return ref, err
}
