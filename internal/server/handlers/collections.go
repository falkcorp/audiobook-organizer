// file: internal/server/handlers/collections.go
// version: 2.0.0
// guid: 3e81c47a-95d2-4b06-a1f8-6c025d9b7413
// last-edited: 2026-10-25

package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/playlist"
	"github.com/falkcorp/audiobook-organizer/internal/search"
	"github.com/gin-gonic/gin"
	"golang.org/x/sync/errgroup"
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
// A DYNAMIC COLLECTION IS EVALUATED FOR THE VIEWER, NEVER FOR ITS CREATOR (owner
// decision 2026-10-25, replacing D75's owner evaluation).
// Per-user query fields (read_status, progress_pct, last_played) mean "for whom?",
// and the answer is: for whoever is looking. "Unfinished" means unfinished for the
// signed-in reader, so two readers see different members of the same collection
// and nobody can infer what another user has read from a collection's membership.
// D75 evaluated as CreatedByUserID and served that one answer to everyone, which
// published the creator's listening state to every reader. Even an admin gets
// their own state: reading another user's listening state needs users.manage and
// its own tooling, and nothing on this surface takes an "as user" parameter.
//
// A PER-VIEWER MEMBER LIST IS NEVER STORED. MaterializedBookIDs would bake one
// viewer's answer into a row every other viewer (and the ABS surface) reads, so
// for a dynamic collection whose query uses a per-user field it is always empty
// on the stored row: create, update, materialize and GET evaluate for the caller,
// return the answer in the response only, and never write it. Rows written before
// this change are scrubbed at boot (the collection-peruser-purge step) and again
// on any write here, and every read path replaces the stored list with the
// viewer's own, so a leftover list is never served. A per-viewer answer is not
// cached at all: it changes on every position heartbeat, a per-user cache would
// need invalidation hooked into every state write plus a users x collections
// refresh loop, and a library has tens of collections, so one search plus a state
// read per hit at read time is cheap.
//
// NO VIEWER, NO MATCH. An evaluation with no viewer (a background job, an
// unresolved caller) matches nothing for a per-user rule; it never falls back to
// the creator or to an admin. See playlist.EvaluateSmartPlaylist.
//
// A query WITHOUT a per-user field is unchanged: evaluated with no user, stored as
// MaterializedBookIDs, served from the stored list by the ABS surface, and kept
// current by materialize and the scheduled relative-date refresh.
//
// Because the evaluating user is always the caller, anyone with collections.manage
// may put a per-user field in a query: it can only ever read their own state.

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
//
// Version is required when BookIDs is present (a whole-list replace is only safe
// against the list the caller last read).
type CollectionUpdateReq struct {
	Name        *string   `json:"name,omitempty"`
	Description *string   `json:"description,omitempty"`
	BookIDs     *[]string `json:"book_ids,omitempty"`
	Query       *string   `json:"query,omitempty"`
	SortJSON    *string   `json:"sort_json,omitempty"`
	Limit       *int      `json:"limit,omitempty"`
	Version     *int      `json:"version,omitempty"`
}

// CollectionMembers is the locked member-list slice of the store: the two
// operations the add/remove-books routes call. Kept out of CollectionStore, and
// out of database.Store, so the wide store interface does not grow; the wiring
// resolves it with database.AsCapability.
type CollectionMembers interface {
	AddBooksToCollection(id string, bookIDs []string) (*database.Collection, error)
	RemoveBookFromCollection(id, bookID string) (*database.Collection, error)
}

// maxCollectionBooksPerRequest bounds one add-books request.
const maxCollectionBooksPerRequest = 500

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
	// evalStore is the wider store the smart-query evaluator needs. Separate
	// from store because CollectionStore is deliberately narrow and the
	// evaluator's requirements are not this handler's business.
	evalStore CollectionEvalStore
	// indexFn is resolved at request time, not construction time, so the handler
	// works when the search index is opened later in Start(). A dynamic
	// collection answers 503 rather than 500 while it is nil.
	indexFn func() *search.BleveIndex
	// log receives scheduled-refresh warnings.
	log collectionWarner
}

// collectionLog is the logger the handler's scheduled refresh writes to.
var collectionLog = logger.New("handlers.collections")

// collectionWarner is the part of a logger the handler writes warnings to.
type collectionWarner interface {
	Warn(msg string, args ...any)
}

// NewCollectionHandler constructs a CollectionHandler with a lazy index getter.
//
// There is no owner store: a dynamic collection is evaluated for the requesting
// viewer, so no user has to be looked up to evaluate it (see the file header).
func NewCollectionHandler(
	store CollectionStore,
	members CollectionMembers,
	evalStore CollectionEvalStore,
	indexFn func() *search.BleveIndex,
) *CollectionHandler {
	if indexFn == nil {
		indexFn = func() *search.BleveIndex { return nil }
	}
	return &CollectionHandler{
		store: store, members: members,
		evalStore: evalStore, indexFn: indexFn,
		log: collectionLog,
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

	// A query that cannot be parsed or translated (a per-user date with no sign,
	// an unknown operator) is a 400 now, before anything is stored: the evaluate
	// below swallows its errors on purpose (a closed index must not block a
	// save), which used to let a query that can never work be stored and only
	// fail later on materialize.
	if col.Type == database.CollectionTypeDynamic {
		if err := validateCollectionQuery(col.Query); err != nil {
			httputil.RespondWithBadRequest(c, err.Error())
			return
		}
	}

	// Evaluate a dynamic collection ONCE at creation so it is populated the first
	// time anyone looks at it. Read paths deliberately serve MaterializedBookIDs
	// without re-evaluating (they must not depend on the index being open), so a
	// collection created and never materialized would render empty forever —
	// indistinguishable from a query that matches nothing.
	//
	// A per-viewer query (a per-user field) is evaluated for the creator as a
	// viewer, answered in the response only and NEVER stored: the stored row
	// carries no per-user members (see the file header).
	//
	// A failure here does NOT fail the create: the collection is valid, the query
	// is stored, and an explicit materialize can populate it later. Refusing to
	// create it because the index happens to be closed would lose the user's work
	// for a reason that has nothing to do with what they typed.
	var viewerIDs []string
	perViewer := collectionIsPerViewer(col)
	if col.Type == database.CollectionTypeDynamic {
		if ids, err := h.evaluate(col, CallingUserID(c)); err == nil {
			if perViewer {
				viewerIDs = ids
			} else {
				col.MaterializedBookIDs = ids
			}
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
	if perViewer {
		created = withViewerMembers(created, viewerIDs)
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
	// A per-viewer collection's stored list is never served (it is empty by
	// construction and may hold a pre-change leftover): the caller's own members
	// replace it, so a card's count is the viewer's, not anyone else's.
	// The slice is cloned first: it is the store's, and rewriting a member list on
	// it would write the viewer's answer into the store's own view of the row.
	cols = slices.Clone(cols)
	sets := h.ViewerMemberSets(cols, CallingUserID(c))
	for i := range cols {
		if collectionIsPerViewer(&cols[i]) {
			cols[i].MaterializedBookIDs = sets[cols[i].ID]
		}
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
//
// EXCEPT a per-viewer collection (a per-user field in its query): its members are
// evaluated for the caller, returned in the response, and never stored.
func (h *CollectionHandler) GetCollection(c *gin.Context) {
	col, ok := h.load(c)
	if !ok {
		return
	}
	if col.Type != database.CollectionTypeDynamic {
		httputil.RespondWithOK(c, col)
		return
	}

	ids, err := h.evaluate(col, CallingUserID(c))
	if collectionIsPerViewer(col) {
		// The viewer's own members, in the response only: nothing is persisted
		// (a stored list would be one viewer's answer served to all). An
		// evaluation that cannot run (index closed or rebuilding) serves no
		// members rather than the stored list, which is never trusted here.
		if err != nil {
			ids = nil
		}
		httputil.RespondWithOK(c, withViewerMembers(col, ids))
		return
	}
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
	origQuery := col.Query

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
		// A whole-list replace is only safe against the list the caller last read,
		// so the version it read is required and must still be current. Without it
		// a PUT silently dropped any book an atomic add landed since the read.
		if req.Version == nil {
			httputil.RespondWithBadRequest(c,
				"version is required with book_ids: send the version of the collection you read")
			return
		}
		if *req.Version != col.Version {
			httputil.RespondWithErrorFields(c, http.StatusConflict,
				"the collection changed since you read it; reload it and resend book_ids with its current version",
				"CONFLICT", gin.H{"current_version": col.Version})
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
		// Only a CHANGED query is checked, for syntax (400). Who may write a
		// per-user field is not a question any more: the collection is evaluated
		// for the viewer, so a per-user field in a query reads only the state of
		// whoever is looking, never the writer's.
		if *req.Query != origQuery {
			if err := validateCollectionQuery(*req.Query); err != nil {
				httputil.RespondWithBadRequest(c, err.Error())
				return
			}
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
	// rather than the previous query's members. A per-viewer query has nothing to
	// store: its stored list is cleared (also scrubbing a pre-change leftover) and
	// the caller's own members ride in the response only.
	perViewer := collectionIsPerViewer(col)
	var viewerIDs []string
	if col.Type == database.CollectionTypeDynamic {
		if perViewer {
			col.MaterializedBookIDs = nil
		}
		if req.Query != nil || req.SortJSON != nil || req.Limit != nil {
			ids, err := h.evaluate(col, CallingUserID(c))
			switch {
			case err != nil:
				// The edit is saved; the members stay as they were (stored) or
				// empty (per-viewer) until the next evaluation.
			case perViewer:
				viewerIDs = ids
			default:
				col.MaterializedBookIDs = ids
			}
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
	if perViewer {
		httputil.RespondWithOK(c, withViewerMembers(col, viewerIDs))
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
//
// A per-viewer collection (a per-user field in its query) stores nothing: the
// response is the caller's own members.
func (h *CollectionHandler) MaterializeCollection(c *gin.Context) {
	col, ok := h.load(c)
	if !ok {
		return
	}
	if col.Type != database.CollectionTypeDynamic {
		httputil.RespondWithBadRequest(c, "only a dynamic collection can be materialized")
		return
	}
	ids, err := h.evaluate(col, CallingUserID(c))
	if err != nil {
		if errors.Is(err, playlist.ErrSearchIndexUnavailable) {
			httputil.RespondWithError(c, http.StatusServiceUnavailable, err.Error(), "SERVICE_UNAVAILABLE")
			return
		}
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}
	if collectionIsPerViewer(col) {
		// Nothing to store: a per-viewer collection's members are the caller's
		// own and are evaluated on every read. This answers what THIS caller sees.
		httputil.RespondWithOK(c, withViewerMembers(col, ids))
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

// validateCollectionQuery reports whether query can be evaluated at all, without
// searching: it parses and translates it, the same pair playlist.EvaluateSmartPlaylist
// runs first, so every error EvaluateSmartPlaylist would return for the query's
// text is returned here (an unsigned per-user date such as last_played:>30d among
// them). It does not need the search index, so it works while the index is closed
// or rebuilding. It deliberately does not look at who is evaluating.
func validateCollectionQuery(query string) error {
	ast, err := search.ParseQuery(query)
	if err != nil {
		return fmt.Errorf("parse query: %w", err)
	}
	if _, _, err := search.TranslateAt(ast, time.Now()); err != nil {
		return fmt.Errorf("translate query: %w", err)
	}
	return nil
}

// collectionIsPerViewer reports whether who is looking decides col's members: a
// dynamic collection whose query uses a per-user field (read_status,
// progress_pct, last_played). Its members are evaluated for the viewer on every
// read and never stored.
func collectionIsPerViewer(col *database.Collection) bool {
	return col.Type == database.CollectionTypeDynamic && search.QueryUsesPerUserField(col.Query)
}

// withViewerMembers returns a copy of col whose dynamic member list is ids, for a
// response. It is a copy on purpose: col is the stored row's value, and a viewer's
// members must never be written back onto it. No ids is an empty list, never the
// stored one.
func withViewerMembers(col *database.Collection, ids []string) *database.Collection {
	out := *col
	out.MaterializedBookIDs = ids
	return &out
}

// evaluate runs a dynamic collection's query for viewerID.
//
// It reuses playlist.EvaluateSmartPlaylist rather than growing a second query
// engine: the two features ask the same question of the same index, and a
// private copy would drift — a query that worked in a smart playlist and failed
// in a dynamic collection would be indistinguishable from a bad query.
//
// viewerID reaches the evaluator only for a query with a per-user field, and an
// empty one (no viewer: a background job, an unresolved caller) matches nothing
// for such a query; it is never replaced by the collection's creator or an admin.
// A query with no per-user field is evaluated with no user at all.
func (h *CollectionHandler) evaluate(col *database.Collection, viewerID string) ([]string, error) {
	userID := ""
	if search.QueryUsesPerUserField(col.Query) {
		userID = viewerID
	}
	return playlist.EvaluateSmartPlaylist(
		h.evalStore, h.indexFn(),
		col.Query, col.SortJSON, col.Limit,
		userID,
	)
}

// ViewerMemberSetsConcurrency is how many per-viewer collections are evaluated at
// once for one request. A library has tens of dynamic collections at most, so the
// pool rarely fills; it is bounded because each evaluation is a search plus one
// state read per hit.
const ViewerMemberSetsConcurrency = 4

// ViewerMemberSets evaluates every per-viewer collection in cols for viewerID and
// returns collection id to ordered member ids. A collection that is not per-viewer
// is absent from the map (its members are the stored list). A per-viewer one whose
// evaluation fails (index closed or rebuilding, a query that no longer parses) or
// that has no viewer is present with an empty list: the answer is "none", never
// the stored list and never another user's.
//
// PARTITIONING: each collection is evaluated by exactly one worker and written to
// its own slot, so workers share no mutable state.
func (h *CollectionHandler) ViewerMemberSets(cols []database.Collection, viewerID string) map[string][]string {
	out := make(map[string][]string)
	var idx []int
	for i := range cols {
		if collectionIsPerViewer(&cols[i]) {
			idx = append(idx, i)
		}
	}
	if len(idx) == 0 {
		return out
	}
	results := make([][]string, len(idx))
	var g errgroup.Group
	g.SetLimit(ViewerMemberSetsConcurrency)
	for n, i := range idx {
		g.Go(func() error {
			if ids, err := h.evaluate(&cols[i], viewerID); err == nil {
				results[n] = ids
			}
			return nil // an evaluation that fails is "no members", it never aborts the rest
		})
	}
	_ = g.Wait()
	for n, i := range idx {
		if results[n] == nil {
			results[n] = []string{}
		}
		out[cols[i].ID] = results[n]
	}
	return out
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
