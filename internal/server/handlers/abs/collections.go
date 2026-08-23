// file: internal/server/handlers/abs/collections.go
// version: 1.1.0
// guid: 6b3d81f0-4a27-4e95-8c16-0d75be2439af
// last-edited: 2026-08-22

package abs

import (
	"context"
	"net/http"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	servermiddleware "github.com/falkcorp/audiobook-organizer/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

// ── Collections ─────────────────────────────────────────────────────────────
//
// 🔴 WHAT WAS BROKEN, MEASURED. The app's Collections tab listed nothing and its
// create sheet failed silently. Both halves were unimplemented, in different ways:
//
//	GET  /api/libraries/:libraryId/collections → h.EmptyPage, a stub that returns
//	                                             a valid, permanently empty Page<T>
//	POST /api/collections                      → no route at all
//
// Production journal, 2026-08-16 15:45:10-15:45:12, five attempts in two seconds
// from a single client (the user pressing Create repeatedly):
//
//	WARN http request method=POST path=/api/collections status=404
//	     message="endpoint not found" clientIP=…
//
// The list stub is the more dangerous of the two, and it is the same defect class
// as the series bug fixed in #2496: an empty 200 is indistinguishable from "you
// have no collections". Nothing errors, nothing logs, and the feature reads as
// working-but-unused for as long as it ships.
//
// ✅ THE NAMESPACE WAS ALREADY RESERVED, so this file adds no collision-table
// entry. "/api/collections" is in absUnimplementedNamespaces
// (wire_abs_routes.go:193), which absReservedPath() matches by prefix, so these
// paths already skip the /api/* → /api/v1/* compatibility redirect. That matters
// more for collections than it did for playlists: a 301 on a POST drops the body
// on many HTTP clients, so an unreserved create route would have "succeeded" with
// an empty payload. Verified there is no /api/v1/collections twin to redirect to
// — this namespace has exactly one implementation, the one below.
//
// 🔴 COLLECTIONS ARE SERVER-WIDE. DO NOT COPY THE PLAYLIST OWNERSHIP CHECK.
// playlists.go 404s when CreatedByUserID != the caller, and that is correct there:
// a playlist belongs to a person. A collection belongs to the server — every user
// sees every collection, by explicit product decision. CreatedByUserID survives
// only as the DTO's `userId` attribution field and is NEVER an access decision.
// Reintroducing an ownership filter here would hide most collections from most
// users while looking like a security improvement.
//
// WRITES ARE GATED, READS ARE NOT. Any authenticated user may list and open
// collections; creating, editing and deleting requires PermCollectionsManage.
// The admin role is seeded from All() and SeedRoles recomputes existing roles on
// every boot (internal/auth/seed.go), so admins acquire this permission on the
// next restart with no backfill — which is why a single permission gate satisfies
// "admins, plus anyone granted the collections permission" without an
// admin-OR-permission special case.

// CollectionStore is the narrow slice of the store this surface needs.
//
// Optional, exactly like PlaylistStore: with a nil store the list route keeps
// answering the empty page it answered before (a valid Page<T>) rather than
// 500-ing, and the write routes report the feature as unavailable.
type CollectionStore interface {
	ListCollections(collectionType string, limit, offset int) ([]database.Collection, int, error)
	GetCollection(id string) (*database.Collection, error)
	CreateCollection(col *database.Collection) (*database.Collection, error)
	UpdateCollection(col *database.Collection) error
	DeleteCollection(id string) error
}

// absCollectionPageSize bounds one page of collections. Like playlists, the
// client has no paging UI for this list, so it is a safety bound rather than a
// pagination scheme.
const absCollectionPageSize = 500

// ── GET /api/libraries/:libraryId/collections ───────────────────────────────

// LibraryCollections handles the collection list, replacing the EmptyPage stub.
func (h *Handler) LibraryCollections(c *gin.Context) {
	if !h.knownLibrary(c) {
		return
	}
	if h.collections == nil {
		respondJSON(c, http.StatusOK, pageResponse{Results: []any{}})
		return
	}

	// "" = both static and dynamic. A dynamic collection is a first-class
	// collection to this surface; see collectionBookIDs.
	cols, _, err := h.collections.ListCollections("", absCollectionPageSize, 0)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "could not list collections")
		return
	}

	// One batched expansion for the whole page rather than one per collection —
	// membership is user-controlled and can reach library scale, which is the
	// loop shape CLAUDE.md's concurrency section exists to prevent.
	byCollection := h.collectionsPageBooks(c.Request.Context(), cols)

	results := make([]any, 0, len(cols))
	for i := range cols {
		results = append(results, h.collectionDTO(&cols[i], byCollection[cols[i].ID]))
	}
	// Total is what was RETURNED. A total larger than results would tell the
	// client there are pages it has no parameter to fetch.
	respondJSON(c, http.StatusOK, pageResponse{Results: results, Total: len(results)})
}

// ── GET /api/collections/:id ────────────────────────────────────────────────

// CollectionDetail handles opening one collection.
//
// This route exists for the same reason PlaylistDetail does: the list and the
// detail are separate paths, and shipping only the list is what made every
// playlist open empty on 2026-08-13.
func (h *Handler) CollectionDetail(c *gin.Context) {
	col, ok := h.lookupCollection(c)
	if !ok {
		return
	}
	books := h.collectionsPageBooks(c.Request.Context(), []database.Collection{*col})
	respondJSON(c, http.StatusOK, h.collectionDTO(col, books[col.ID]))
}

// ── POST /api/collections ───────────────────────────────────────────────────

// absCollectionCreateReq is the ABS create payload.
//
// `books` carries libraryItemIds (sync ids), NOT our internal book ULIDs — every
// client-visible id on this surface comes from the sync_item keyspace. They are
// translated back on the way in; see resolveSyncIDs.
type absCollectionCreateReq struct {
	LibraryID   string   `json:"libraryId"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Books       []string `json:"books"`
}

// CreateCollection handles POST /api/collections — the request that 404'd.
func (h *Handler) CreateCollection(c *gin.Context) {
	if !h.canManageCollections(c) {
		return
	}

	var req absCollectionCreateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, "invalid collection payload")
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		respondError(c, http.StatusBadRequest, "collection name is required")
		return
	}

	u, _ := servermiddleware.CurrentUser(c)
	createdBy := ""
	if u != nil {
		createdBy = u.ID
	}

	// ABS has no notion of a dynamic collection, so anything created through this
	// surface is static. Dynamic collections are created on the native API, and
	// this surface renders them as ordinary collections of their materialized
	// members — the client never needs to know the difference.
	col := &database.Collection{
		Name:            name,
		Description:     strings.TrimSpace(req.Description),
		Type:            database.CollectionTypeStatic,
		BookIDs:         h.resolveSyncIDs(req.Books),
		CreatedByUserID: createdBy,
	}

	created, err := h.collections.CreateCollection(col)
	if err != nil {
		// A duplicate name is the user's mistake, not the server's; reporting it
		// as 500 would send the app's generic "something went wrong" for a
		// condition the user can fix by typing a different name.
		if strings.Contains(err.Error(), "already in use") {
			respondError(c, http.StatusConflict, err.Error())
			return
		}
		respondError(c, http.StatusInternalServerError, "could not create collection")
		return
	}

	books := h.collectionsPageBooks(c.Request.Context(), []database.Collection{*created})
	respondJSON(c, http.StatusOK, h.collectionDTO(created, books[created.ID]))
}

// ── PATCH /api/collections/:id ──────────────────────────────────────────────

// absCollectionUpdateReq uses pointers so an absent field is distinguishable
// from a cleared one: `{"description":""}` clears the description, whereas
// omitting the key leaves it alone. A value-typed struct would silently blank
// every field the client did not send.
type absCollectionUpdateReq struct {
	Name        *string   `json:"name,omitempty"`
	Description *string   `json:"description,omitempty"`
	Books       *[]string `json:"books,omitempty"`
}

// UpdateCollection handles PATCH /api/collections/:id.
func (h *Handler) UpdateCollection(c *gin.Context) {
	if !h.canManageCollections(c) {
		return
	}
	col, ok := h.lookupCollection(c)
	if !ok {
		return
	}

	var req absCollectionUpdateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, "invalid collection payload")
		return
	}
	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		if name == "" {
			respondError(c, http.StatusBadRequest, "collection name cannot be empty")
			return
		}
		col.Name = name
	}
	if req.Description != nil {
		col.Description = strings.TrimSpace(*req.Description)
	}
	if req.Books != nil {
		// Editing membership through this surface only makes sense for a static
		// collection: a dynamic collection's membership is the query's answer,
		// and accepting a book list would produce a set the next evaluation
		// silently discards — a write that appears to work and does not persist.
		if col.Type == database.CollectionTypeDynamic {
			respondError(c, http.StatusConflict,
				"this is a dynamic collection; its members come from its query")
			return
		}
		col.BookIDs = h.resolveSyncIDs(*req.Books)
	}

	if err := h.collections.UpdateCollection(col); err != nil {
		if strings.Contains(err.Error(), "already in use") || strings.Contains(err.Error(), "version conflict") {
			respondError(c, http.StatusConflict, err.Error())
			return
		}
		respondError(c, http.StatusInternalServerError, "could not update collection")
		return
	}

	books := h.collectionsPageBooks(c.Request.Context(), []database.Collection{*col})
	respondJSON(c, http.StatusOK, h.collectionDTO(col, books[col.ID]))
}

// ── DELETE /api/collections/:id ─────────────────────────────────────────────

// DeleteCollection handles DELETE /api/collections/:id.
func (h *Handler) DeleteCollection(c *gin.Context) {
	if !h.canManageCollections(c) {
		return
	}
	col, ok := h.lookupCollection(c)
	if !ok {
		return
	}
	if err := h.collections.DeleteCollection(col.ID); err != nil {
		respondError(c, http.StatusInternalServerError, "could not delete collection")
		return
	}
	respondJSON(c, http.StatusOK, gin.H{"id": col.ID})
}

// ── POST /api/collections/:id/book, DELETE /api/collections/:id/book/:bookId ─

// absCollectionBookReq is the single-book add payload.
type absCollectionBookReq struct {
	ID string `json:"id"`
}

// AddBookToCollection handles POST /api/collections/:id/book.
func (h *Handler) AddBookToCollection(c *gin.Context) {
	if !h.canManageCollections(c) {
		return
	}
	col, ok := h.lookupCollection(c)
	if !ok {
		return
	}
	if col.Type == database.CollectionTypeDynamic {
		respondError(c, http.StatusConflict,
			"this is a dynamic collection; its members come from its query")
		return
	}

	var req absCollectionBookReq
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, "invalid payload")
		return
	}
	ids := h.resolveSyncIDs([]string{req.ID})
	if len(ids) == 0 {
		respondError(c, http.StatusNotFound, "book not found")
		return
	}
	// Adding a book already present is a no-op rather than an error or a
	// duplicate entry: the client's own list is the user's mental model, and a
	// collection holding the same book twice has no meaning.
	for _, existing := range col.BookIDs {
		if existing == ids[0] {
			books := h.collectionsPageBooks(c.Request.Context(), []database.Collection{*col})
			respondJSON(c, http.StatusOK, h.collectionDTO(col, books[col.ID]))
			return
		}
	}
	col.BookIDs = append(col.BookIDs, ids[0])

	if err := h.collections.UpdateCollection(col); err != nil {
		// col was read via h.lookupCollection above with no lock held until this
		// write, so a concurrent add/remove/edit on the same collection can win
		// the race and make this call's read stale — the classic
		// read-modify-write clobber the Version compare-and-swap exists to
		// catch. Surface it as a 409 so the client re-reads and retries rather
		// than getting a 500 for what is really "try again."
		if strings.Contains(err.Error(), "version conflict") {
			respondError(c, http.StatusConflict, err.Error())
			return
		}
		respondError(c, http.StatusInternalServerError, "could not update collection")
		return
	}
	books := h.collectionsPageBooks(c.Request.Context(), []database.Collection{*col})
	respondJSON(c, http.StatusOK, h.collectionDTO(col, books[col.ID]))
}

// RemoveBookFromCollection handles DELETE /api/collections/:id/book/:bookId.
func (h *Handler) RemoveBookFromCollection(c *gin.Context) {
	if !h.canManageCollections(c) {
		return
	}
	col, ok := h.lookupCollection(c)
	if !ok {
		return
	}
	if col.Type == database.CollectionTypeDynamic {
		respondError(c, http.StatusConflict,
			"this is a dynamic collection; its members come from its query")
		return
	}

	target := h.resolveSyncIDs([]string{strings.TrimSpace(c.Param("bookId"))})
	if len(target) == 0 {
		// The sync id does not resolve. The collection cannot contain it, so the
		// requested end state already holds; report the collection unchanged
		// rather than an error the user cannot act on.
		books := h.collectionsPageBooks(c.Request.Context(), []database.Collection{*col})
		respondJSON(c, http.StatusOK, h.collectionDTO(col, books[col.ID]))
		return
	}

	kept := make([]string, 0, len(col.BookIDs))
	for _, id := range col.BookIDs {
		if id != target[0] {
			kept = append(kept, id)
		}
	}
	col.BookIDs = kept

	if err := h.collections.UpdateCollection(col); err != nil {
		// Same race as AddBookToCollection above: surface a stale-Version
		// conflict as 409 rather than a generic 500.
		if strings.Contains(err.Error(), "version conflict") {
			respondError(c, http.StatusConflict, err.Error())
			return
		}
		respondError(c, http.StatusInternalServerError, "could not update collection")
		return
	}
	books := h.collectionsPageBooks(c.Request.Context(), []database.Collection{*col})
	respondJSON(c, http.StatusOK, h.collectionDTO(col, books[col.ID]))
}

// ── shared plumbing ─────────────────────────────────────────────────────────

// canManageCollections enforces the write gate, writing the response itself and
// reporting whether the caller may proceed.
//
// It answers 403 rather than 404 on purpose, unlike the playlist ownership
// check: collection ids are not secret here (every user can list every
// collection), so there is nothing for a 404 to conceal, and 403 tells the user
// the true reason their Create button failed.
func (h *Handler) canManageCollections(c *gin.Context) bool {
	if h.collections == nil {
		respondError(c, http.StatusServiceUnavailable, "collections are not available")
		return false
	}
	u, found := servermiddleware.CurrentUser(c)
	if !found || u == nil {
		respondError(c, http.StatusUnauthorized, "authentication required")
		return false
	}
	// ABSRequireAuth's Bind() loads the caller's effective permissions into the
	// request context (narrowed by API-key scope), so auth.Can is meaningful on
	// this surface — it is not a no-op the way a bare c.Get would be.
	if !auth.Can(c.Request.Context(), auth.PermCollectionsManage) {
		respondError(c, http.StatusForbidden,
			"permission denied: "+string(auth.PermCollectionsManage))
		return false
	}
	return true
}

// lookupCollection resolves :id, writing the error response itself.
func (h *Handler) lookupCollection(c *gin.Context) (*database.Collection, bool) {
	if h.collections == nil {
		// 404 rather than an empty object: {} is not a valid collection shape and
		// throws in the Dart client (§6.6).
		respondError(c, http.StatusNotFound, "collection not found")
		return nil, false
	}
	if _, found := servermiddleware.CurrentUser(c); !found {
		respondError(c, http.StatusUnauthorized, "authentication required")
		return nil, false
	}
	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		respondError(c, http.StatusNotFound, "collection not found")
		return nil, false
	}
	col, err := h.collections.GetCollection(id)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "could not read collection")
		return nil, false
	}
	if col == nil {
		respondError(c, http.StatusNotFound, "collection not found")
		return nil, false
	}
	// NO OWNERSHIP CHECK. See the file header: collections are server-wide.
	return col, true
}

// collectionBookIDs returns the ids whose books this collection currently shows.
//
// A dynamic collection's membership is its LAST EVALUATION, not its query —
// exactly as playlistDTO treats smart playlists. Evaluating the query here would
// make a read path depend on the Bleve index being open, and an unevaluated
// dynamic collection would then fail the whole library tab instead of rendering
// as empty.
func collectionBookIDs(col *database.Collection) []string {
	if col.Type == database.CollectionTypeDynamic {
		return col.MaterializedBookIDs
	}
	return col.BookIDs
}

// collectionsPageBooks expands every collection on the page in ONE batched load.
//
// Modelled on seriesPageBooks rather than playlistItems: playlistItems calls
// loadItemView per book, which is an N+1 over a list whose length the user
// controls. Collection membership is explicitly allowed to reach library scale,
// so the per-item form is not acceptable here.
//
// Each entry is a full ABS LibraryItem. That is the #2496 lesson: a typed client
// decodes books as [LibraryItem] as a unit, so ONE undecodable entry discards the
// entire response — which is how 23 of 50 series with real books rendered as
// "No Series Found". A book that fails to resolve is dropped rather than emitted
// as a partial object.
func (h *Handler) collectionsPageBooks(
	ctx context.Context,
	cols []database.Collection,
) map[string][]any {
	out := make(map[string][]any, len(cols))
	if len(cols) == 0 {
		return out
	}

	var ids []string
	seen := make(map[string]struct{})
	for i := range cols {
		for _, id := range collectionBookIDs(&cols[i]) {
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

	for i := range cols {
		member := collectionBookIDs(&cols[i])
		items := make([]any, 0, len(member))
		for _, id := range member {
			dto, ok := byID[id]
			if !ok {
				continue // deleted book, or one whose sync id could not be minted
			}
			items = append(items, dto)
		}
		out[cols[i].ID] = items
	}
	return out
}

// resolveSyncIDs translates client-supplied libraryItemIds into internal book
// ids, dropping any that do not resolve.
//
// 🔴 THIS TRANSLATION IS NOT OPTIONAL. Every id the client holds is a 36-char
// sync_item UUID; our books are 26-char ULIDs. Storing the client's ids directly
// would produce a collection whose members never match any book — it would
// create successfully, return 200, and then render permanently empty. That is
// the same "succeeds and shows nothing" failure this file exists to remove, so
// it would be a particularly cruel way to reintroduce it.
//
// Duplicates are collapsed: a collection holding the same book twice has no
// meaning, and the client's own UI treats membership as a set.
func (h *Handler) resolveSyncIDs(syncIDs []string) []string {
	out := make([]string, 0, len(syncIDs))
	if h.identity == nil {
		return out
	}
	seen := make(map[string]struct{}, len(syncIDs))
	for _, raw := range syncIDs {
		id := strings.TrimSpace(raw)
		if id == "" {
			continue
		}
		bookID := h.bookIDForSyncID(id)
		if bookID == "" {
			continue
		}
		if _, dup := seen[bookID]; dup {
			continue
		}
		seen[bookID] = struct{}{}
		out = append(out, bookID)
	}
	return out
}

// bookIDForSyncID resolves one sync id to a book id, following a single merge
// redirect.
//
// A sync item whose book was merged into another carries RedirectTo rather than
// a CurrentBookID. Not following it would silently drop books the user picked —
// they exist, they are reachable in the app under the id the client is holding,
// and only this lookup would disagree. One hop, not a loop: a redirect chain
// would mean the merge bookkeeping is itself broken, and spinning here would
// turn that into a hung request instead of a dropped book.
func (h *Handler) bookIDForSyncID(syncID string) string {
	item, err := h.identity.ResolveSyncItem(syncID)
	if err != nil || item == nil {
		return ""
	}
	if item.CurrentBookID != "" {
		return item.CurrentBookID
	}
	if item.RedirectTo == "" || item.RedirectTo == syncID {
		return ""
	}
	next, err := h.identity.ResolveSyncItem(item.RedirectTo)
	if err != nil || next == nil {
		return ""
	}
	return next.CurrentBookID
}

// collectionDTO maps one Collection onto the upstream ABS collection shape.
//
// 🔴 books IS THE SERVED LIST AND THE COUNT IS ITS LENGTH. Reporting
// len(col.BookIDs) while serving fewer books is the self-inconsistency measured
// on the series route: 9 of 50 series on page 0 reported numBooks >= 1 while
// carrying books: [], because members are dropped after the count would be
// taken. A row that reports a count it cannot list forces the client to guess
// which half to believe.
func (h *Handler) collectionDTO(col *database.Collection, books []any) gin.H {
	if books == nil {
		books = []any{}
	}

	var description any
	if col.Description != "" {
		description = col.Description
	}

	return gin.H{
		"id":        col.ID,
		"libraryId": h.libraryID(),
		// Attribution only — never an access decision. See the file header.
		"userId":        col.CreatedByUserID,
		"name":          col.Name,
		"description":   description,
		"cover":         nil,
		"coverFullPath": nil,
		"books":         books,
		"numBooks":      len(books),
		"lastUpdate":    msEpoch(col.UpdatedAt),
		"createdAt":     msEpoch(col.CreatedAt),
	}
}
