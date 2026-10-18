// file: internal/server/handlers/collections.go
// version: 1.3.0
// guid: 3e81c47a-95d2-4b06-a1f8-6c025d9b7413
// last-edited: 2026-10-18

package handlers

import (
	"errors"
	"net/http"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/playlist"
	"github.com/falkcorp/audiobook-organizer/internal/search"
	"github.com/gin-gonic/gin"
)

// ── Collections, native API ─────────────────────────────────────────────────
//
// The ABS-compatible surface (handlers/abs/collections.go) serves the app. This
// is the surface the web UI and scripts use, and it exists for one capability
// the ABS one structurally cannot provide:
//
//	🔴 DYNAMIC COLLECTIONS CAN ONLY BE CREATED HERE.
//
// Audiobookshelf has no concept of a query-backed collection, so its create
// payload has nowhere to put one. Everything created through the ABS surface is
// static by definition. Without these routes "collections support static and
// dynamic" would be true of the storage layer and false of the product — the
// store would accept a dynamic collection that no caller could ever construct.
//
// TWO SURFACES, ONE PERMISSION. Writes here are gated on the same
// PermCollectionsManage the ABS surface uses, applied as route middleware in
// wire_library_routes.go rather than in each handler. A second, looser rule on
// this surface would make the ABS gate decorative: anyone locked out there could
// simply call /api/v1 instead.
//
// COLLECTIONS ARE SERVER-WIDE — no ownedByCaller here. This file sits beside
// playlists.go, which scopes every read to CallingUserID and 404s other users'
// rows, and that difference is deliberate rather than an oversight: a playlist
// belongs to a person, a collection belongs to the server. VISIBILITY is
// server-wide; CreatedByUserID never decides who may see or edit a collection.
//
// A DYNAMIC COLLECTION IS EVALUATED AS ITS OWNER, NOT AS THE READER (D75).
// Per-user query fields (read_status, progress_pct, last_played) mean "for whom?",
// and a collection that answered with the signed-in reader's state would show
// different members to different people, store whichever answer was computed last
// as MaterializedBookIDs, and serve that to everyone on the ABS surface, which
// never evaluates. So the evaluating user is CreatedByUserID (see evaluatorFor).
// Smart PLAYLISTS stay per-user; they are a different feature.

// CollectionCreateReq is the payload for POST /api/v1/collections.
//
// Query/SortJSON/Limit are the dynamic half and are meaningless on a static
// collection; BookIDs is the static half and is meaningless on a dynamic one.
// validateCollectionCreate rejects the mismatch rather than silently keeping a
// field that will never be read — a stored query on a static collection is a
// promise the evaluator never keeps.
type CollectionCreateReq struct {
	Name        string   `json:"name" binding:"required"`
	Description string   `json:"description,omitempty"`
	Type        string   `json:"type" binding:"required"` // static|dynamic
	BookIDs     []string `json:"book_ids,omitempty"`
	Query       string   `json:"query,omitempty"`
	SortJSON    string   `json:"sort_json,omitempty"`
	Limit       int      `json:"limit,omitempty"`
}

// CollectionUpdateReq mirrors the create payload with every field optional, so
// an absent key leaves the stored value alone and an explicit empty one clears
// it. Type is deliberately absent: converting a static collection into a dynamic
// one would discard its members with no way back.
type CollectionUpdateReq struct {
	Name        *string   `json:"name,omitempty"`
	Description *string   `json:"description,omitempty"`
	BookIDs     *[]string `json:"book_ids,omitempty"`
	Query       *string   `json:"query,omitempty"`
	SortJSON    *string   `json:"sort_json,omitempty"`
	Limit       *int      `json:"limit,omitempty"`
}

// CollectionMembers is the locked member-list slice of the store: the two
// operations the add/remove-books routes call. Kept out of CollectionStore, and
// out of database.Store, so the wide store interface does not grow; the wiring
// resolves it with database.AsCapability.
type CollectionMembers interface {
	AddBooksToCollection(id string, bookIDs []string) (*database.Collection, error)
	RemoveBookFromCollection(id, bookID string) (*database.Collection, error)
}

// CollectionOwnerStore is what the owner evaluator reads: the owning user by id,
// and the user list for the first-admin fallback.
type CollectionOwnerStore interface {
	GetUserByID(id string) (*database.User, error)
	ListUsers() ([]database.User, error)
}

// ErrNoCollectionEvaluator means a dynamic collection has no owner that still
// exists and the install has no active admin to fall back to, so there is no
// user whose per-user state the query could be evaluated against.
var ErrNoCollectionEvaluator = errors.New("no admin user to evaluate this collection as")

// maxCollectionBooksPerRequest bounds one add-books request.
const maxCollectionBooksPerRequest = 500

// collectionLog records the owner fallback so an operator can see which
// collections are being evaluated as someone other than their stored owner.
var collectionLog = logger.New("handlers.collections")

// CollectionStore is the narrow database interface CollectionHandler requires.
type CollectionStore interface {
	CreateCollection(col *database.Collection) (*database.Collection, error)
	GetCollection(id string) (*database.Collection, error)
	ListCollections(collectionType string, limit, offset int) ([]database.Collection, int, error)
	UpdateCollection(col *database.Collection) error
	DeleteCollection(id string) error
}

// CollectionEvalStore is only what playlist.EvaluateSmartPlaylist needs — it makes no calls of its own, measured by emptying the interface and reading the
// compiler's enumeration. It was 43 methods of database.* embeds.
type CollectionEvalStore interface {
	GetBookByID(id string) (*database.Book, error)
	GetUserBookState(userID, bookID string) (*database.UserBookState, error)
}

// CollectionHandler serves the native collection routes.
type CollectionHandler struct {
	store CollectionStore
	// members is nil when the store has no locked membership operations; the
	// add/remove-books routes then answer 503 rather than a read-modify-write.
	members CollectionMembers
	// owners resolves the user a dynamic collection is evaluated as.
	owners CollectionOwnerStore
	// evalStore is the wider store the smart-query evaluator needs. Separate
	// from store because CollectionStore is deliberately narrow and the
	// evaluator's requirements are not this handler's business.
	evalStore CollectionEvalStore
	// indexFn is resolved at request time, not construction time, so the handler
	// works when the search index is opened later in Start(). A dynamic
	// collection answers 503 rather than 500 while it is nil.
	indexFn func() *search.BleveIndex
}

// NewCollectionHandler constructs a CollectionHandler with a lazy index getter.
func NewCollectionHandler(
	store CollectionStore,
	members CollectionMembers,
	owners CollectionOwnerStore,
	evalStore CollectionEvalStore,
	indexFn func() *search.BleveIndex,
) *CollectionHandler {
	if indexFn == nil {
		indexFn = func() *search.BleveIndex { return nil }
	}
	return &CollectionHandler{
		store: store, members: members, owners: owners,
		evalStore: evalStore, indexFn: indexFn,
	}
}

// validateCollectionCreate enforces the type/field agreement.
func validateCollectionCreate(req *CollectionCreateReq) error {
	if strings.TrimSpace(req.Name) == "" {
		return errCollection("name is required")
	}
	switch req.Type {
	case database.CollectionTypeStatic:
		if strings.TrimSpace(req.Query) != "" {
			return errCollection("a static collection cannot carry a query; use type=dynamic")
		}
	case database.CollectionTypeDynamic:
		// A dynamic collection with no query would behave as a permanently empty
		// one: it would create successfully, list successfully, and never contain
		// anything. Rejecting it at the door is the difference between a typo the
		// user can see and a collection they will file a bug about.
		if strings.TrimSpace(req.Query) == "" {
			return errCollection("a dynamic collection requires a query")
		}
		if len(req.BookIDs) > 0 {
			return errCollection("a dynamic collection's members come from its query; do not send book_ids")
		}
	default:
		return errCollection("type must be static or dynamic")
	}
	return nil
}

type collectionError string

func (e collectionError) Error() string { return string(e) }
func errCollection(msg string) error    { return collectionError(msg) }

// CreateCollection — POST /api/v1/collections
func (h *CollectionHandler) CreateCollection(c *gin.Context) {
	var req CollectionCreateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}
	if err := validateCollectionCreate(&req); err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}

	col := &database.Collection{
		Name:            strings.TrimSpace(req.Name),
		Description:     strings.TrimSpace(req.Description),
		Type:            req.Type,
		BookIDs:         req.BookIDs,
		Query:           req.Query,
		SortJSON:        req.SortJSON,
		Limit:           req.Limit,
		CreatedByUserID: CallingUserID(c),
	}

	// Evaluate a dynamic collection ONCE at creation so it is populated the first
	// time anyone looks at it. Read paths deliberately serve MaterializedBookIDs
	// without re-evaluating (they must not depend on the index being open), so a
	// collection created and never materialized would render empty forever —
	// indistinguishable from a query that matches nothing.
	//
	// A failure here does NOT fail the create: the collection is valid, the query
	// is stored, and an explicit materialize can populate it later. Refusing to
	// create it because the index happens to be closed would lose the user's work
	// for a reason that has nothing to do with what they typed.
	if col.Type == database.CollectionTypeDynamic {
		if ids, err := h.evaluate(col); err == nil {
			col.MaterializedBookIDs = ids
		}
	}

	created, err := h.store.CreateCollection(col)
	if err != nil {
		if errors.Is(err, database.ErrCollectionNameInUse) {
			httputil.RespondWithConflict(c, err.Error())
			return
		}
		httputil.InternalError(c, "failed to create collection", err)
		return
	}
	httputil.RespondWithCreated(c, created)
}

// ListCollections — GET /api/v1/collections?type=static|dynamic&limit=N&offset=M
//
// NOT scoped to the calling user, unlike ListPlaylists directly above it in the
// sibling file. See the file header: that is the product rule, not a missing
// filter.
func (h *CollectionHandler) ListCollections(c *gin.Context) {
	colType := c.Query("type")
	if colType != "" &&
		colType != database.CollectionTypeStatic &&
		colType != database.CollectionTypeDynamic {
		httputil.RespondWithBadRequest(c, "type must be static, dynamic, or empty")
		return
	}
	p := httputil.ParsePaginationParams(c)
	cols, total, err := h.store.ListCollections(colType, p.Limit, p.Offset)
	if err != nil {
		httputil.InternalError(c, "failed to list collections", err)
		return
	}
	// total is the count BEFORE paging, so the client can tell whether another
	// page exists. Returning len(cols) would say page 0 is always everything.
	httputil.RespondWithList(c, cols, total, p.Limit, p.Offset)
}

// GetCollection — GET /api/v1/collections/:id
//
// For a dynamic collection this re-evaluates the query and returns the live
// membership, refreshing MaterializedBookIDs as a side effect. That is the
// opposite of the ABS read path, on purpose: this surface is the one that keeps
// the materialized set current, so the ABS surface can stay index-independent.
func (h *CollectionHandler) GetCollection(c *gin.Context) {
	col, ok := h.load(c)
	if !ok {
		return
	}
	if col.Type != database.CollectionTypeDynamic {
		httputil.RespondWithOK(c, col)
		return
	}

	ids, err := h.evaluate(col)
	if err != nil {
		// Serve the last known membership rather than an error. A search index
		// that is closed or rebuilding is a transient condition, and failing the
		// read would black out every dynamic collection in the UI while it
		// recovers.
		httputil.RespondWithOK(c, col)
		return
	}
	// Persist ONLY on an actual change. UpdateCollection sets UpdatedAt=now,
	// Version=prev+1 and commits with pebble.Sync, so an unconditional write here
	// would make every READ an fsync plus a version bump — and Version would stop
	// meaning "times this collection changed". It is also client-visible: the ABS
	// DTO exposes UpdatedAt as lastUpdate, which clients use for cache
	// invalidation, so an idle collection would look freshly-modified on every
	// unrelated read and be re-fetched forever.
	changed := !sameIDs(col.MaterializedBookIDs, ids)
	col.MaterializedBookIDs = ids
	if changed {
		// Best-effort persist: the response is already correct without it, so a
		// write failure must not turn a successful read into an error. This now
		// also swallows a Version compare-and-swap conflict, which is fine here
		// specifically: a conflict just means some other writer changed the
		// collection between this read and this cache-refresh write, so the
		// stale MaterializedBookIDs this call would have written are no more
		// current than what is already stored — there is nothing worth retrying
		// for a cache write the next read recomputes anyway.
		_ = h.store.UpdateCollection(col)
	}
	httputil.RespondWithOK(c, col)
}

// sameIDs reports whether two materialized-membership slices are equal, order
// included. Order matters: it is the collection's sort order, so a reorder with
// identical members is a real change worth persisting.
func sameIDs(a, b []string) bool {
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

// UpdateCollection — PUT /api/v1/collections/:id
func (h *CollectionHandler) UpdateCollection(c *gin.Context) {
	col, ok := h.load(c)
	if !ok {
		return
	}
	var req CollectionUpdateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}

	if req.Name != nil {
		if strings.TrimSpace(*req.Name) == "" {
			httputil.RespondWithBadRequest(c, "name cannot be empty")
			return
		}
		col.Name = strings.TrimSpace(*req.Name)
	}
	if req.Description != nil {
		col.Description = strings.TrimSpace(*req.Description)
	}

	// Each half is rejected on the wrong type rather than ignored. Silently
	// dropping book_ids on a dynamic collection is a write that returns 200 and
	// changes nothing — the failure mode this whole feature was built to remove.
	if req.BookIDs != nil {
		if col.Type == database.CollectionTypeDynamic {
			httputil.RespondWithBadRequest(c,
				"a dynamic collection's members come from its query; update the query instead")
			return
		}
		col.BookIDs = *req.BookIDs
	}
	if req.Query != nil {
		if col.Type != database.CollectionTypeDynamic {
			httputil.RespondWithBadRequest(c, "only a dynamic collection has a query")
			return
		}
		if strings.TrimSpace(*req.Query) == "" {
			httputil.RespondWithBadRequest(c, "a dynamic collection requires a query")
			return
		}
		col.Query = *req.Query
	}
	if req.SortJSON != nil {
		col.SortJSON = *req.SortJSON
	}
	if req.Limit != nil {
		col.Limit = *req.Limit
	}

	// Re-materialize when the query or its shaping changed, so the next reader —
	// including the ABS surface, which never evaluates — sees the new answer
	// rather than the previous query's members.
	if col.Type == database.CollectionTypeDynamic &&
		(req.Query != nil || req.SortJSON != nil || req.Limit != nil) {
		if ids, err := h.evaluate(col); err == nil {
			col.MaterializedBookIDs = ids
		}
	}

	if err := h.store.UpdateCollection(col); err != nil {
		if errors.Is(err, database.ErrCollectionVersionConflict) || errors.Is(err, database.ErrCollectionNameInUse) {
			httputil.RespondWithConflict(c, err.Error())
			return
		}
		httputil.InternalError(c, "failed to update collection", err)
		return
	}
	httputil.RespondWithOK(c, col)
}

// DeleteCollection — DELETE /api/v1/collections/:id
func (h *CollectionHandler) DeleteCollection(c *gin.Context) {
	col, ok := h.load(c)
	if !ok {
		return
	}
	if err := h.store.DeleteCollection(col.ID); err != nil {
		httputil.InternalError(c, "failed to delete collection", err)
		return
	}
	httputil.RespondWithOK(c, gin.H{"id": col.ID, "deleted": true})
}

// MaterializeCollection — POST /api/v1/collections/:id/materialize
//
// Re-runs a dynamic collection's query and stores the result. The ABS surface
// serves MaterializedBookIDs without evaluating, so this is how a collection
// created while the index was down becomes populated without waiting for
// somebody to open it on the native API.
func (h *CollectionHandler) MaterializeCollection(c *gin.Context) {
	col, ok := h.load(c)
	if !ok {
		return
	}
	if col.Type != database.CollectionTypeDynamic {
		httputil.RespondWithBadRequest(c, "only a dynamic collection can be materialized")
		return
	}
	ids, err := h.evaluate(col)
	if err != nil {
		if errors.Is(err, playlist.ErrSearchIndexUnavailable) || errors.Is(err, ErrNoCollectionEvaluator) {
			httputil.RespondWithError(c, http.StatusServiceUnavailable, err.Error(), "SERVICE_UNAVAILABLE")
			return
		}
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}
	// Same no-op guard as the read path, for the same reason: this endpoint is what
	// a periodic refresh would call, so writing unconditionally would bump
	// Version/UpdatedAt on every cycle whether or not membership moved. Unlike the
	// read path a write failure IS reported here — the caller asked for a write.
	changed := !sameIDs(col.MaterializedBookIDs, ids)
	col.MaterializedBookIDs = ids
	if changed {
		if uerr := h.store.UpdateCollection(col); uerr != nil {
			// Same conflict class as UpdateCollection above: col was read via
			// h.load at the top of this handler, so a version conflict here
			// means something else wrote the collection in between — a real,
			// expected race under concurrent access, not a server fault.
			if errors.Is(uerr, database.ErrCollectionVersionConflict) {
				httputil.RespondWithConflict(c, uerr.Error())
				return
			}
			httputil.InternalError(c, "failed to store materialized collection", uerr)
			return
		}
	}
	httputil.RespondWithOK(c, col)
}

// evaluate runs a dynamic collection's query as the collection's owner.
//
// It reuses playlist.EvaluateSmartPlaylist rather than growing a second query
// engine: the two features ask the same question of the same index, and a
// private copy would drift — a query that worked in a smart playlist and failed
// in a dynamic collection would be indistinguishable from a bad query.
//
// The user passed to it is evaluatorFor's answer, never the caller: see the file
// header. That is what makes a materialize, a native read and the ABS surface
// (which serves the stored set) agree whoever is signed in.
func (h *CollectionHandler) evaluate(col *database.Collection) ([]string, error) {
	userID, err := h.evaluatorFor(col)
	if err != nil {
		return nil, err
	}
	return playlist.EvaluateSmartPlaylist(
		h.evalStore, h.indexFn(),
		col.Query, col.SortJSON, col.Limit,
		userID,
	)
}

// evaluatorFor names the user whose per-user state a dynamic collection is
// evaluated against: CreatedByUserID when that user still exists, otherwise the
// first active admin (logged, because the stored owner is stale), otherwise
// ErrNoCollectionEvaluator.
//
// "Exists" is a row lookup, not an active check: a deactivated owner's reading
// state is still theirs, and moving a collection to a different user's state
// because its owner was deactivated would silently change its members. The
// fallback admin, by contrast, must be active (auth.FirstActiveAdmin).
func (h *CollectionHandler) evaluatorFor(col *database.Collection) (string, error) {
	if h.owners == nil {
		return "", ErrNoCollectionEvaluator
	}
	if col.CreatedByUserID != "" {
		u, err := h.owners.GetUserByID(col.CreatedByUserID)
		if err != nil {
			return "", err
		}
		if u != nil {
			return u.ID, nil
		}
	}
	users, err := h.owners.ListUsers()
	if err != nil {
		return "", err
	}
	admin := auth.FirstActiveAdmin(users)
	if admin == nil {
		return "", ErrNoCollectionEvaluator
	}
	collectionLog.Warn("collections: collection %s has owner %q which is not a user; evaluating as first admin %s",
		logger.SanitizeLogValue(col.ID), logger.SanitizeLogValue(col.CreatedByUserID), logger.SanitizeLogValue(admin.ID))
	return admin.ID, nil
}

// load resolves :id, writing the error response itself.
//
// There is no ownership check, by design. See the file header.
func (h *CollectionHandler) load(c *gin.Context) (*database.Collection, bool) {
	id := c.Param("id")
	col, err := h.store.GetCollection(id)
	if err != nil {
		httputil.InternalError(c, "failed to load collection", err)
		return nil, false
	}
	if col == nil {
		httputil.RespondWithNotFound(c, "collection", id)
		return nil, false
	}
	return col, true
}

// CollectionAddBooksReq is the payload for POST /api/v1/collections/:id/books.
type CollectionAddBooksReq struct {
	BookIDs []string `json:"book_ids"`
}

// AddBooksToCollection — POST /api/v1/collections/:id/books
//
// Adds books to a STATIC collection through the store's locked membership
// operation, so two requests adding different books to one collection both land
// (a PUT with the whole book_ids list cannot promise that: it replaces what the
// caller last read). Ids already in the collection are skipped, order is kept.
//
// Statuses: 400 for a payload that is not 1 to 500 ids or names a book that does
// not exist (the first unknown id is named), 404 for an unknown collection, 409
// for a dynamic one, 503 when the store has no membership operations. The
// response is the updated collection.
func (h *CollectionHandler) AddBooksToCollection(c *gin.Context) {
	if h.members == nil {
		httputil.RespondWithServiceUnavailable(c, "collection membership is not available")
		return
	}
	var req CollectionAddBooksReq
	if err := c.ShouldBindJSON(&req); err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}
	if n := len(req.BookIDs); n < 1 || n > maxCollectionBooksPerRequest {
		httputil.RespondWithBadRequest(c, "book_ids must contain 1 to 500 ids")
		return
	}
	col, ok := h.load(c)
	if !ok {
		return
	}
	if col.Type == database.CollectionTypeDynamic {
		httputil.RespondWithConflict(c, errDynamicMembership)
		return
	}
	// Every id must be a real book: a collection holding an id no book answers to
	// would create successfully and render a hole. Bounded by the 500 cap above,
	// so this is request-scale work, not a library-scale loop.
	for _, id := range req.BookIDs {
		b, err := h.evalStore.GetBookByID(id)
		if err != nil {
			httputil.InternalError(c, "failed to check book", err)
			return
		}
		if b == nil {
			httputil.RespondWithBadRequest(c, "unknown book id: "+logger.SanitizeLogValue(id))
			return
		}
	}
	updated, err := h.members.AddBooksToCollection(col.ID, req.BookIDs)
	if err != nil {
		h.respondMembershipError(c, col.ID, err)
		return
	}
	httputil.RespondWithOK(c, updated)
}

// RemoveBookFromCollection — DELETE /api/v1/collections/:id/books/:bookId
//
// Removes one book from a STATIC collection through the same locked operation.
// 404 for an unknown collection or a book that is not a member, 409 for a
// dynamic collection; the response is the updated collection.
func (h *CollectionHandler) RemoveBookFromCollection(c *gin.Context) {
	if h.members == nil {
		httputil.RespondWithServiceUnavailable(c, "collection membership is not available")
		return
	}
	col, ok := h.load(c)
	if !ok {
		return
	}
	if col.Type == database.CollectionTypeDynamic {
		httputil.RespondWithConflict(c, errDynamicMembership)
		return
	}
	updated, err := h.members.RemoveBookFromCollection(col.ID, c.Param("bookId"))
	if err != nil {
		h.respondMembershipError(c, col.ID, err)
		return
	}
	httputil.RespondWithOK(c, updated)
}

const errDynamicMembership = "a dynamic collection's members come from its query; update the query instead"

// respondMembershipError maps the locked membership operations' sentinels. The
// pre-checks in the handlers answer the common cases; these also cover the row
// changing between the pre-check and the locked write (deleted, or the type
// differing).
func (h *CollectionHandler) respondMembershipError(c *gin.Context, id string, err error) {
	switch {
	case errors.Is(err, database.ErrCollectionNotFound):
		httputil.RespondWithNotFound(c, "collection", id)
	case errors.Is(err, database.ErrCollectionNotStatic):
		httputil.RespondWithConflict(c, errDynamicMembership)
	case errors.Is(err, database.ErrCollectionBookNotMember):
		httputil.RespondWithNotFound(c, "collection member", c.Param("bookId"))
	default:
		httputil.InternalError(c, "failed to update collection membership", err)
	}
}
