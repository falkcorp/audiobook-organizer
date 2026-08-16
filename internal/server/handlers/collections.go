// file: internal/server/handlers/collections.go
// version: 1.0.0
// guid: 3e81c47a-95d2-4b06-a1f8-6c025d9b7413
// last-edited: 2026-08-16

package handlers

import (
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
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
// belongs to a person, a collection belongs to the server. CreatedByUserID is
// attribution only.

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

// CollectionStore is the narrow database interface CollectionHandler requires.
type CollectionStore interface {
	CreateCollection(col *database.Collection) (*database.Collection, error)
	GetCollection(id string) (*database.Collection, error)
	ListCollections(collectionType string, limit, offset int) ([]database.Collection, int, error)
	UpdateCollection(col *database.Collection) error
	DeleteCollection(id string) error
}

// CollectionEvalStore is what the query evaluator needs of the store.
//
// Declared here rather than imported because internal/playlist keeps its
// equivalent unexported (playlistEvalStore). Go's structural typing means a
// value satisfying this satisfies that, so restating the method set costs
// nothing and avoids exporting an interface for one caller. The two must stay
// in step: a drift shows up as a compile error at the EvaluateSmartPlaylist
// call, not as a runtime surprise.
type CollectionEvalStore interface {
	database.BookReader
	database.UserPositionStore
}

// CollectionHandler serves the native collection routes.
type CollectionHandler struct {
	store CollectionStore
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
	evalStore CollectionEvalStore,
	indexFn func() *search.BleveIndex,
) *CollectionHandler {
	if indexFn == nil {
		indexFn = func() *search.BleveIndex { return nil }
	}
	return &CollectionHandler{store: store, evalStore: evalStore, indexFn: indexFn}
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
		if ids, err := h.evaluate(c, col); err == nil {
			col.MaterializedBookIDs = ids
		}
	}

	created, err := h.store.CreateCollection(col)
	if err != nil {
		if strings.Contains(err.Error(), "already in use") || strings.Contains(err.Error(), "duplicate") {
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

	ids, err := h.evaluate(c, col)
	if err != nil {
		// Serve the last known membership rather than an error. A search index
		// that is closed or rebuilding is a transient condition, and failing the
		// read would black out every dynamic collection in the UI while it
		// recovers.
		httputil.RespondWithOK(c, col)
		return
	}
	col.MaterializedBookIDs = ids
	// Best-effort persist: the response is already correct without it, so a write
	// failure must not turn a successful read into an error.
	_ = h.store.UpdateCollection(col)
	httputil.RespondWithOK(c, col)
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
		if ids, err := h.evaluate(c, col); err == nil {
			col.MaterializedBookIDs = ids
		}
	}

	if err := h.store.UpdateCollection(col); err != nil {
		if strings.Contains(err.Error(), "already in use") {
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
	ids, err := h.evaluate(c, col)
	if err != nil {
		if err == playlist.ErrSearchIndexUnavailable {
			httputil.RespondWithError(c, 503, err.Error(), "SERVICE_UNAVAILABLE")
			return
		}
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}
	col.MaterializedBookIDs = ids
	if uerr := h.store.UpdateCollection(col); uerr != nil {
		httputil.InternalError(c, "failed to store materialized collection", uerr)
		return
	}
	httputil.RespondWithOK(c, col)
}

// evaluate runs a dynamic collection's query.
//
// It reuses playlist.EvaluateSmartPlaylist rather than growing a second query
// engine: the two features ask the same question of the same index, and a
// private copy would drift — a query that worked in a smart playlist and failed
// in a dynamic collection would be indistinguishable from a bad query.
func (h *CollectionHandler) evaluate(c *gin.Context, col *database.Collection) ([]string, error) {
	return playlist.EvaluateSmartPlaylist(
		h.evalStore, h.indexFn(),
		col.Query, col.SortJSON, col.Limit,
		CallingUserID(c),
	)
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
