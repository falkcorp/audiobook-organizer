// file: internal/server/handlers/collections.go
// version: 1.7.0
// guid: 3e81c47a-95d2-4b06-a1f8-6c025d9b7413
// last-edited: 2026-10-18

package handlers

import (
	"errors"
	"net/http"
	"strings"
	"sync"

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
//
// ONLY THE EVALUATOR MAY WRITE A PER-USER FIELD INTO A QUERY. Because the query is
// evaluated as the owner and the answer (MaterializedBookIDs) is served to every
// reader, anyone allowed to edit a collection could otherwise set
// read_status:finished or last_played:>2026-01-01 on someone else's collection and
// read that person's private listening state back out of its members. So a query
// that references a per-user field (search.QueryUsesPerUserField) is accepted
// only from the user evaluatorFor resolves to; anyone else with collections.manage
// still edits every other part of the collection, and any query without one. A
// collection with no usable owner evaluates as the first active admin, so only
// that admin may set one there. See requirePerUserQueryEvaluator.
//
// AN EVALUATOR IS RESOLVED ONLY FOR A PER-USER QUERY. A query with no per-user
// field is evaluated with no user at all, as it always was, so it never needs an
// owner and never answers 503 because the install has no admin.
//
// WITH AUTH OFF, "_local" IS THE EVALUATOR. A single-user install (EnableAuth
// false) has no user rows to speak of: every request is the caller "_local", and
// it is the one person whose listening state exists. A collection owned by
// "_local" is therefore evaluated as "_local" (not as a stand-in), and per-user
// queries work there. With auth on, "_local" is not a user and is treated like any
// other owner that no longer exists.
//
// A STAND-IN ADMIN NEVER EVALUATES A PER-USER QUERY. The first-admin fallback
// exists so a collection whose owner was deleted keeps working, but for a query
// that uses a per-user field it would publish the admin's listening state (the
// members are served to every reader) for a query the admin did not write, and a
// per-user query on an ownerless collection may have been written by someone else
// before the evaluator gate existed. So evaluate refuses it with
// ErrCollectionOwnerMissingPerUserQuery and the stored members stay as they were:
// materialize answers 409 COLLECTION_OWNER_MISSING_PER_USER_QUERY, and a native
// read answers 200 with the stored collection and an evaluation_blocked flag (a
// read has always served the last known members rather than failing, see
// GetCollection). A query without a per-user field still falls back to the admin.
// The first active admin takes the collection over by saving its query again
// WITH "adopt_owner": true in the PUT body (UpdateCollection stores that admin as
// the owner). The flag is required: re-sending a query, changed or not, never
// adopts, so an admin who edits a query for another reason cannot become the
// evaluator of per-user state they did not mean to claim.

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
// against the list the caller last read), and AdoptOwner is the explicit opt-in
// for the first active admin to take an ownerless per-user collection over; see
// the file header.
type CollectionUpdateReq struct {
	Name        *string   `json:"name,omitempty"`
	Description *string   `json:"description,omitempty"`
	BookIDs     *[]string `json:"book_ids,omitempty"`
	Query       *string   `json:"query,omitempty"`
	SortJSON    *string   `json:"sort_json,omitempty"`
	Limit       *int      `json:"limit,omitempty"`
	Version     *int      `json:"version,omitempty"`
	AdoptOwner  bool      `json:"adopt_owner,omitempty"`
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

// ErrCollectionOwnerMissingPerUserQuery means a dynamic collection's owner no
// longer exists and its query uses a per-user field, so it must not be evaluated
// as the first-admin stand-in (see the file header). The stored members are kept.
var ErrCollectionOwnerMissingPerUserQuery = errors.New(
	"the collection's owner no longer exists and its query uses per-user fields; it is not evaluated as the first admin")

// ErrCodeCollectionOwnerMissingPerUserQuery is the error code and the
// evaluation_blocked flag value for ErrCollectionOwnerMissingPerUserQuery.
const ErrCodeCollectionOwnerMissingPerUserQuery = "COLLECTION_OWNER_MISSING_PER_USER_QUERY"

// collectionBlockedResponse is a collection read whose evaluation was refused:
// the stored collection, flattened, plus why it was not re-evaluated.
type collectionBlockedResponse struct {
	*database.Collection
	EvaluationBlocked        string `json:"evaluation_blocked"`
	EvaluationBlockedMessage string `json:"evaluation_blocked_message"`
}

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
	// authEnabled reports whether authentication is on, read per request so a
	// config change applies without a restart. Off means single-user mode, where
	// "_local" is a legitimate evaluator. Defaults to on, the strict reading.
	authEnabled func() bool
	// log receives the owner-fallback warning; fallbackLogged holds the ids it
	// has already been written for, so each collection warns once.
	log            collectionWarner
	fallbackLogged sync.Map
}

// collectionWarner is the part of a logger the handler writes warnings to.
type collectionWarner interface {
	Warn(msg string, args ...any)
}

// CollectionOption configures optional CollectionHandler behaviour.
type CollectionOption func(*CollectionHandler)

// WithAuthEnabled tells the handler whether authentication is on. The server
// passes the live config value; without it the handler assumes auth is on.
func WithAuthEnabled(fn func() bool) CollectionOption {
	return func(h *CollectionHandler) {
		if fn != nil {
			h.authEnabled = fn
		}
	}
}

// NewCollectionHandler constructs a CollectionHandler with a lazy index getter.
func NewCollectionHandler(
	store CollectionStore,
	members CollectionMembers,
	owners CollectionOwnerStore,
	evalStore CollectionEvalStore,
	indexFn func() *search.BleveIndex,
	opts ...CollectionOption,
) *CollectionHandler {
	if indexFn == nil {
		indexFn = func() *search.BleveIndex { return nil }
	}
	h := &CollectionHandler{
		store: store, members: members, owners: owners,
		evalStore: evalStore, indexFn: indexFn,
		authEnabled: func() bool { return true },
		log:         collectionLog,
	}
	for _, opt := range opts {
		opt(h)
	}
	return h
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

	// The creator is the owner and the evaluator, never a stand-in: a per-user
	// query is refused unless evaluatorFor resolves to the creator, which it does
	// whenever the creator is a user row, or is "_local" with auth off. A creator
	// who is neither (a row deleted mid-request) would otherwise be evaluated as
	// the first admin and read the admin's state.
	if col.Type == database.CollectionTypeDynamic && !h.requirePerUserQueryEvaluator(c, col, col.Query) {
		return
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
	if errors.Is(err, ErrCollectionOwnerMissingPerUserQuery) {
		// Not a transient condition, so say so: the stored members are served
		// unchanged (nothing is re-materialized from the stand-in admin's state)
		// with a flag a client can show. A 409 would black out the collection for
		// every reader, which is what the paragraph below exists to avoid.
		httputil.RespondWithOK(c, collectionBlockedResponse{
			Collection:               col,
			EvaluationBlocked:        ErrCodeCollectionOwnerMissingPerUserQuery,
			EvaluationBlockedMessage: ownerMissingMessage(),
		})
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
	if req.AdoptOwner && req.Query == nil {
		httputil.RespondWithBadRequest(c, "adopt_owner applies to a query: send the per-user query with it")
		return
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
		// Only a CHANGED query is checked: renaming a collection whose stored query
		// already has a per-user field, or re-sending that query unchanged, writes
		// nothing new into what the evaluator's state is asked about.
		if *req.Query != origQuery && !h.requirePerUserQueryEvaluator(c, col, *req.Query) {
			return
		}
		// The first active admin takes an ownerless per-user collection over, but
		// only by asking: the stand-in never evaluates such a query (see the file
		// header), so without adoption the one user the gate lets write it could
		// not make it work, and with adoption implied by a re-save an admin editing
		// the query for another reason would claim someone else's per-user state.
		if req.AdoptOwner && !h.adoptOwner(c, col, *req.Query) {
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
	blocked := false
	if col.Type == database.CollectionTypeDynamic &&
		(req.Query != nil || req.SortJSON != nil || req.Limit != nil) {
		ids, err := h.evaluate(col)
		switch {
		case err == nil:
			col.MaterializedBookIDs = ids
		case errors.Is(err, ErrCollectionOwnerMissingPerUserQuery):
			// The edit is saved (name, sort, limit, even the query) but the members
			// stay as they were, and the caller is told so instead of being handed
			// a 200 that reads as a fresh evaluation.
			blocked = true
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
	if blocked {
		httputil.RespondWithOK(c, collectionBlockedResponse{
			Collection:               col,
			EvaluationBlocked:        ErrCodeCollectionOwnerMissingPerUserQuery,
			EvaluationBlockedMessage: ownerMissingMessage(),
		})
		return
	}
	httputil.RespondWithOK(c, col)
}

// adoptOwner handles "adopt_owner": true on a PUT carrying query: the caller must
// be the first active admin standing in for an ownerless collection, and the query
// must use a per-user field (adopting for any other query would change nothing the
// evaluator reads). It stores the caller as the owner on col, or writes the error
// response and reports false. Every refusal is explicit; nothing is a silent no-op.
func (h *CollectionHandler) adoptOwner(c *gin.Context, col *database.Collection, query string) bool {
	if !search.QueryUsesPerUserField(query) {
		httputil.RespondWithBadRequest(c, "adopt_owner applies only to a query that uses a per-user field ("+
			strings.Join(search.PerUserFieldNames(), ", ")+")")
		return false
	}
	id, fallback, err := h.resolveEvaluator(col)
	switch {
	case errors.Is(err, ErrNoCollectionEvaluator):
		httputil.RespondWithError(c, http.StatusForbidden,
			"only the first active admin can adopt a collection, and there is none",
			ErrCodeCollectionPerUserQueryOwnerOnly)
		return false
	case err != nil:
		httputil.InternalError(c, "failed to resolve the collection's evaluating user", err)
		return false
	case !fallback:
		httputil.RespondWithConflict(c, "the collection already has an owner; adopt_owner applies only to an ownerless collection")
		return false
	case id != CallingUserID(c):
		httputil.RespondWithError(c, http.StatusForbidden,
			"only the first active admin can adopt an ownerless collection",
			ErrCodeCollectionPerUserQueryOwnerOnly)
		return false
	}
	col.CreatedByUserID = id
	return true
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
		if errors.Is(err, ErrCollectionOwnerMissingPerUserQuery) {
			// An explicit request to re-evaluate cannot be honoured, and the
			// caller should be told why rather than handed the old members as if
			// they were fresh.
			httputil.RespondWithError(c, http.StatusConflict, ownerMissingMessage(),
				ErrCodeCollectionOwnerMissingPerUserQuery)
			return
		}
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
	// Only a per-user query has anyone to be evaluated as. Resolving an evaluator
	// for every query would make a plain one depend on the owner row and on an
	// active admin existing, and answer 503 for neither reason being its business.
	userID := ""
	if search.QueryUsesPerUserField(col.Query) {
		id, fallback, err := h.resolveEvaluator(col)
		if err != nil {
			return nil, err
		}
		if fallback {
			h.logOwnerFallback(col, id)
			return nil, ErrCollectionOwnerMissingPerUserQuery
		}
		userID = id
	}
	return playlist.EvaluateSmartPlaylist(
		h.evalStore, h.indexFn(),
		col.Query, col.SortJSON, col.Limit,
		userID,
	)
}

// ErrCodeCollectionPerUserQueryOwnerOnly is the error code of the 403 a caller
// gets for writing a per-user field into a query they do not evaluate as.
const ErrCodeCollectionPerUserQueryOwnerOnly = "COLLECTION_PER_USER_QUERY_OWNER_ONLY"

// requirePerUserQueryEvaluator reports whether the caller may put query on col,
// writing the 403 itself when not. A query with no per-user field is always fine.
// One with one is allowed only when the caller IS the collection's evaluator
// (evaluatorFor), so nobody can read another user's state through the members
// the owner-evaluated collection stores. No evaluator at all (no owner row, no
// active admin) means nobody qualifies.
func (h *CollectionHandler) requirePerUserQueryEvaluator(c *gin.Context, col *database.Collection, query string) bool {
	if !search.QueryUsesPerUserField(query) {
		return true
	}
	evaluator, err := h.evaluatorFor(col)
	if err != nil && !errors.Is(err, ErrNoCollectionEvaluator) {
		httputil.InternalError(c, "failed to resolve the collection's evaluating user", err)
		return false
	}
	if err == nil && evaluator == CallingUserID(c) {
		return true
	}
	httputil.RespondWithError(c, http.StatusForbidden,
		"only the collection's owner can use per-user fields ("+strings.Join(search.PerUserFieldNames(), ", ")+
			") in its query",
		ErrCodeCollectionPerUserQueryOwnerOnly)
	return false
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
	userID, fallback, err := h.resolveEvaluator(col)
	if err == nil && fallback {
		h.logOwnerFallback(col, userID)
	}
	return userID, err
}

// localCallerID is the caller id a request carries when nobody is authenticated
// (CallingUserID), and so the owner of a collection created with auth off.
const localCallerID = "_local"

// resolveEvaluator is evaluatorFor that also says whether the answer is the
// first-admin stand-in (the stored owner is empty, "_local" or not a user row)
// rather than the owner. It does not log; callers that evaluate do.
func (h *CollectionHandler) resolveEvaluator(col *database.Collection) (userID string, fallback bool, err error) {
	// Single-user mode: "_local" is the one user whose state exists, not a
	// stand-in. With auth on it is not a user row and falls through like any other
	// owner that does not exist.
	if col.CreatedByUserID == localCallerID && !h.authEnabled() {
		return localCallerID, false, nil
	}
	if h.owners == nil {
		return "", false, ErrNoCollectionEvaluator
	}
	if col.CreatedByUserID != "" {
		u, err := h.owners.GetUserByID(col.CreatedByUserID)
		if err != nil {
			return "", false, err
		}
		if u != nil {
			return u.ID, false, nil
		}
	}
	users, err := h.owners.ListUsers()
	if err != nil {
		return "", false, err
	}
	admin := auth.FirstActiveAdmin(users)
	if admin == nil {
		return "", false, ErrNoCollectionEvaluator
	}
	return admin.ID, true, nil
}

// logOwnerFallback records that a collection has no usable owner and its first
// active admin stand-in applies. It warns once per collection per process, and
// never for a collection with no id (a create in flight has none yet, and an
// empty id names nothing an operator could act on).
func (h *CollectionHandler) logOwnerFallback(col *database.Collection, adminID string) {
	if col.ID == "" {
		return
	}
	if _, seen := h.fallbackLogged.LoadOrStore(col.ID, struct{}{}); seen {
		return
	}
	h.log.Warn("collections: collection %s has owner %q which is not a user; first admin %s stands in for it and its per-user query is not evaluated",
		logger.SanitizeLogValue(col.ID), logger.SanitizeLogValue(col.CreatedByUserID), logger.SanitizeLogValue(adminID))
}

// ownerMissingMessage is the text of the 409 and of the evaluation_blocked flag.
func ownerMissingMessage() string {
	return "this collection's owner no longer exists and its query uses per-user fields (" +
		strings.Join(search.PerUserFieldNames(), ", ") +
		"), so it is not evaluated as the first admin and keeps its stored members; " +
		"the first active admin can save its query again with adopt_owner set to true to take ownership, or the per-user fields can be removed"
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
