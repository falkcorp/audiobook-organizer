// file: internal/server/handlers/collections_test.go
// version: 1.1.0
// guid: 5d17e903-2b48-4c81-96af-70e3c5a12b8d
// last-edited: 2026-08-16

package handlers_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/database/mocks"
	"github.com/falkcorp/audiobook-organizer/internal/search"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
	servermiddleware "github.com/falkcorp/audiobook-organizer/internal/server/middleware"
)

// ── fake store ──────────────────────────────────────────────────────────────

type colFakeStore struct {
	cols    []database.Collection
	created *database.Collection
	updates int
}

func (f *colFakeStore) CreateCollection(col *database.Collection) (*database.Collection, error) {
	if col.ID == "" {
		col.ID = "col-1"
	}
	cp := *col
	f.created = &cp
	f.cols = append(f.cols, cp)
	return col, nil
}

func (f *colFakeStore) GetCollection(id string) (*database.Collection, error) {
	for i := range f.cols {
		if f.cols[i].ID == id {
			cp := f.cols[i]
			return &cp, nil
		}
	}
	return nil, nil
}

func (f *colFakeStore) ListCollections(t string, _, _ int) ([]database.Collection, int, error) {
	if t == "" {
		return f.cols, len(f.cols), nil
	}
	var out []database.Collection
	for _, c := range f.cols {
		if c.Type == t {
			out = append(out, c)
		}
	}
	return out, len(out), nil
}

func (f *colFakeStore) UpdateCollection(col *database.Collection) error {
	f.updates++
	for i := range f.cols {
		if f.cols[i].ID == col.ID {
			f.cols[i] = *col
			return nil
		}
	}
	return nil
}

func (f *colFakeStore) DeleteCollection(id string) error {
	kept := f.cols[:0]
	for _, c := range f.cols {
		if c.ID != id {
			kept = append(kept, c)
		}
	}
	f.cols = kept
	return nil
}

// colRouter mounts the native collection routes with NO permission middleware.
// Gating is asserted separately in TestCollectionWrites_RequirePermission —
// mixing the two would make a validation failure and an authorization failure
// indistinguishable in the output.
func colRouter(store handlers.CollectionStore) (*gin.Engine, *handlers.CollectionHandler) {
	gin.SetMode(gin.TestMode)
	// A nil index getter is the honest fixture here: these tests assert
	// validation and routing, not query evaluation, and a dynamic collection is
	// specified to survive an unavailable index rather than fail creation.
	h := handlers.NewCollectionHandler(store, nil, nil)
	r := gin.New()
	g := r.Group("/api/v1")
	g.POST("/collections", h.CreateCollection)
	g.GET("/collections", h.ListCollections)
	g.GET("/collections/:id", h.GetCollection)
	g.PUT("/collections/:id", h.UpdateCollection)
	g.DELETE("/collections/:id", h.DeleteCollection)
	g.POST("/collections/:id/materialize", h.MaterializeCollection)
	return r, h
}

func colDo(t *testing.T, r *gin.Engine, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf *bytes.Buffer
	if body != nil {
		b, err := json.Marshal(body)
		require.NoError(t, err)
		buf = bytes.NewBuffer(b)
	} else {
		buf = bytes.NewBuffer(nil)
	}
	req := httptest.NewRequest(method, path, buf)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// ── the capability that exists ONLY here ────────────────────────────────────

// TestCreateDynamicCollection is the reason this surface exists.
//
// Audiobookshelf has no concept of a query-backed collection, so its create
// payload has nowhere to put a query and everything made through the ABS routes
// is static. Without this route, "collections support static and dynamic" would
// be true of the storage layer and false of the product: the store would accept
// a dynamic collection that no caller could construct.
func TestCreateDynamicCollection(t *testing.T) {
	store := &colFakeStore{}
	r, _ := colRouter(store)

	w := colDo(t, r, http.MethodPost, "/api/v1/collections", map[string]any{
		"name":  "Recently Added",
		"type":  "dynamic",
		"query": "added:>2025-01-01",
		"limit": 50,
	})
	require.Equal(t, http.StatusCreated, w.Code, "body: %s", w.Body.String())

	require.NotNil(t, store.created, "nothing reached the store")
	assert.Equal(t, database.CollectionTypeDynamic, store.created.Type)
	assert.Equal(t, "added:>2025-01-01", store.created.Query,
		"the query is the entire point of a dynamic collection and must be persisted")
	assert.Equal(t, 50, store.created.Limit)
}

// TestCreateDynamicCollectionSurvivesAnUnavailableIndex pins the fail-soft rule.
//
// The handler evaluates a new dynamic collection once so it is populated the
// first time anyone looks at it. That evaluation needs the Bleve index, and this
// fixture has none — the collection must still be created. Refusing it because
// the index happens to be closed would lose what the user typed for a reason
// that has nothing to do with what they typed.
func TestCreateDynamicCollectionSurvivesAnUnavailableIndex(t *testing.T) {
	store := &colFakeStore{}
	r, _ := colRouter(store) // nil index getter

	w := colDo(t, r, http.MethodPost, "/api/v1/collections", map[string]any{
		"name": "No Index", "type": "dynamic", "query": "title:anything",
	})
	require.Equal(t, http.StatusCreated, w.Code,
		"an unavailable search index must not destroy the user's input: %s", w.Body.String())
	require.NotNil(t, store.created)
	assert.Empty(t, store.created.MaterializedBookIDs,
		"nothing could be evaluated, so the materialized set must be empty rather than invented")
}

// ── validation: each field belongs to exactly one type ──────────────────────

func TestCollectionCreateValidation(t *testing.T) {
	cases := []struct {
		name string
		body map[string]any
		want int
		why  string
	}{
		{
			name: "dynamic without a query",
			body: map[string]any{"name": "Empty Forever", "type": "dynamic"},
			want: http.StatusBadRequest,
			why: "it would create, list, and never contain anything — a bug report " +
				"rather than a visible typo",
		},
		{
			name: "dynamic carrying book_ids",
			body: map[string]any{"name": "Confused", "type": "dynamic",
				"query": "title:x", "book_ids": []string{"b1"}},
			want: http.StatusBadRequest,
			why:  "members come from the query; stored ids would be silently discarded",
		},
		{
			name: "static carrying a query",
			body: map[string]any{"name": "Confused", "type": "static", "query": "title:x"},
			want: http.StatusBadRequest,
			why:  "a stored query on a static collection is a promise the evaluator never keeps",
		},
		{
			name: "unknown type",
			body: map[string]any{"name": "Weird", "type": "magic"},
			want: http.StatusBadRequest,
			why:  "an unrecognized type must not fall through to a default",
		},
		{
			name: "static with book_ids is fine",
			body: map[string]any{"name": "Good", "type": "static", "book_ids": []string{"b1"}},
			want: http.StatusCreated,
			why:  "the ordinary case must still work",
		},
		{
			name: "dynamic with a query is fine",
			body: map[string]any{"name": "Good Dynamic", "type": "dynamic", "query": "title:x"},
			want: http.StatusCreated,
			why:  "the ordinary case must still work",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := colRouter(&colFakeStore{})
			w := colDo(t, r, http.MethodPost, "/api/v1/collections", tc.body)
			assert.Equal(t, tc.want, w.Code, "%s\nbody: %s", tc.why, w.Body.String())
		})
	}
}

// TestDynamicMembershipIsNotEditableNatively mirrors the ABS-surface rule.
//
// Accepting book_ids on a dynamic collection would store a set the next
// evaluation discards: a write that returns 200 and changes nothing.
func TestDynamicMembershipIsNotEditableNatively(t *testing.T) {
	store := &colFakeStore{cols: []database.Collection{{
		ID: "c1", Name: "Dyn", Type: database.CollectionTypeDynamic, Query: "title:x",
	}}}
	r, _ := colRouter(store)

	w := colDo(t, r, http.MethodPut, "/api/v1/collections/c1", map[string]any{
		"book_ids": []string{"b1", "b2"},
	})
	assert.Equal(t, http.StatusBadRequest, w.Code,
		"editing a dynamic collection's members must be refused, not ignored")
	assert.Zero(t, store.updates, "the refused edit still wrote to the store")
}

// TestStaticCollectionHasNoQueryToEdit is the mirror case.
func TestStaticCollectionHasNoQueryToEdit(t *testing.T) {
	store := &colFakeStore{cols: []database.Collection{{
		ID: "c1", Name: "Static", Type: database.CollectionTypeStatic,
	}}}
	r, _ := colRouter(store)

	w := colDo(t, r, http.MethodPut, "/api/v1/collections/c1", map[string]any{
		"query": "title:x",
	})
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Zero(t, store.updates)
}

// TestMaterializeRejectsStaticCollections — a static collection has no query, so
// materializing it is a category error rather than a no-op.
func TestMaterializeRejectsStaticCollections(t *testing.T) {
	store := &colFakeStore{cols: []database.Collection{{
		ID: "c1", Name: "Static", Type: database.CollectionTypeStatic,
	}}}
	r, _ := colRouter(store)
	w := colDo(t, r, http.MethodPost, "/api/v1/collections/c1/materialize", nil)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// ── server-wide, not per-user ───────────────────────────────────────────────

// TestCollectionsAreNotScopedToTheCallingUser pins the divergence from
// playlists.
//
// This file sits beside playlists_test.go, whose handler scopes every read to
// CallingUserID and 404s another user's rows. Copying that here is the likely
// mistake, and it would hide most collections from most users while looking
// like a security fix.
func TestCollectionsAreNotScopedToTheCallingUser(t *testing.T) {
	store := &colFakeStore{cols: []database.Collection{
		{ID: "c1", Name: "Mine", Type: database.CollectionTypeStatic, CreatedByUserID: "user-a"},
		{ID: "c2", Name: "Theirs", Type: database.CollectionTypeStatic, CreatedByUserID: "user-b"},
	}}
	r, _ := colRouter(store)

	w := colDo(t, r, http.MethodGet, "/api/v1/collections", nil)
	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "Mine")
	assert.Contains(t, w.Body.String(), "Theirs",
		"collections are server-wide; an ownership filter would drop this row")

	w = colDo(t, r, http.MethodGet, "/api/v1/collections/c2", nil)
	assert.Equal(t, http.StatusOK, w.Code,
		"another user's collection must be readable — server-wide is the product rule")
}

// ── the write gate ──────────────────────────────────────────────────────────

// TestCollectionWrites_RequirePermission asserts the gate with the REAL
// middleware.
//
// 🔴 WHY THIS DOES NOT USE setupTestServer. That fixture leaves EnableAuth
// false, and s.perm() returns a pass-through no-op when auth is disabled — so a
// gating test written against the production router there would pass with the
// gate removed. RequirePermission has a second bypass for the same reason:
// CountUsers()==0 lets the first-run caller through. A test that trips either
// one measures the fixture, not the rule.
//
// So this mounts servermiddleware.RequirePermission directly — the same
// middleware s.perm() returns when auth IS enabled — over a store with a user
// in it, and injects the caller's permissions the way the auth middleware does.
// The precedent is newAdminInvalidateRouter in list_cache_generation_test.go.
func TestCollectionWrites_RequirePermission(t *testing.T) {
	store, cleanup := colAuthStore(t)
	defer cleanup()

	writes := []struct {
		method, path string
		body         any
	}{
		{http.MethodPost, "/api/v1/collections", map[string]any{"name": "x", "type": "static"}},
		{http.MethodPut, "/api/v1/collections/c1", map[string]any{"name": "y"}},
		{http.MethodDelete, "/api/v1/collections/c1", nil},
		{http.MethodPost, "/api/v1/collections/c1/materialize", nil},
	}

	t.Run("without the permission", func(t *testing.T) {
		for _, w := range writes {
			fake := &colFakeStore{cols: []database.Collection{{
				ID: "c1", Name: "Existing", Type: database.CollectionTypeStatic,
			}}}
			r := colGatedRouter(store, fake, []auth.Permission{auth.PermLibraryView})
			rec := colDo(t, r, w.method, w.path, w.body)
			assert.Equal(t, http.StatusForbidden, rec.Code,
				"%s %s must be refused without %s", w.method, w.path, auth.PermCollectionsManage)
			assert.Nil(t, fake.created, "a refused request reached the store")
		}
	})

	// 🔴 THE NEGATIVE CONTROL. Without it, a router that refused EVERYTHING would
	// pass the block above — the gate would be "verified" by an instrument that
	// cannot distinguish a correct gate from a broken one.
	t.Run("with the permission", func(t *testing.T) {
		fake := &colFakeStore{}
		r := colGatedRouter(store, fake,
			[]auth.Permission{auth.PermLibraryView, auth.PermCollectionsManage})
		rec := colDo(t, r, http.MethodPost, "/api/v1/collections",
			map[string]any{"name": "Allowed", "type": "static"})
		require.Equal(t, http.StatusCreated, rec.Code,
			"a permitted caller must get through: %s", rec.Body.String())
		require.NotNil(t, fake.created)
		assert.Equal(t, "Allowed", fake.created.Name)
	})
}

// colAuthStore builds a real store holding one user, so RequirePermission's
// first-run bypass (CountUsers()==0) cannot silently pass every caller.
func colAuthStore(t *testing.T) (*database.PebbleStore, func()) {
	t.Helper()
	store, err := database.NewPebbleStoreInMemory(t.TempDir() + "/auth.pebble")
	require.NoError(t, err)
	_, err = store.CreateUser("gatekeeper", "gate@example.com", "argon2id", "hash",
		[]string{"viewer"}, "active")
	require.NoError(t, err)
	n, err := store.CountUsers()
	require.NoError(t, err)
	require.Positive(t, n, "the first-run bypass would make this test vacuous")
	return store, func() { _ = store.Close() }
}

// colGatedRouter mounts the collection writes behind the production permission
// middleware, with the caller's permission set injected ahead of it exactly as
// the auth middleware does.
func colGatedRouter(
	authStore *database.PebbleStore,
	colStore handlers.CollectionStore,
	perms []auth.Permission,
) *gin.Engine {
	gin.SetMode(gin.TestMode)
	h := handlers.NewCollectionHandler(colStore, nil, nil)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		ctx := auth.WithUser(c.Request.Context(), &database.User{ID: "u1", Username: "gatekeeper"})
		ctx = auth.WithPermissions(ctx, perms)
		c.Request = c.Request.WithContext(ctx)
		c.Set("auth_user", &database.User{ID: "u1", Username: "gatekeeper"})
		c.Next()
	})
	g := r.Group("/api/v1")
	gate := servermiddleware.RequirePermission(authStore, auth.PermCollectionsManage)
	g.POST("/collections", gate, h.CreateCollection)
	g.PUT("/collections/:id", gate, h.UpdateCollection)
	g.DELETE("/collections/:id", gate, h.DeleteCollection)
	g.POST("/collections/:id/materialize", gate, h.MaterializeCollection)
	return r
}

// ── the read path must not write ────────────────────────────────────────────

// colEvalRouter is colRouter with a REAL Bleve index and a store the smart-query
// evaluator can actually use, so GetCollection reaches its persist branch. The
// plain colRouter has a nil index on purpose, which makes evaluate() fail and
// returns from GetCollection before any write — with that fixture this test
// would pass no matter what the guard did.
func colEvalRouter(t *testing.T, store handlers.CollectionStore, bookIDs ...string) *gin.Engine {
	t.Helper()

	idx, err := search.Open(filepath.Join(t.TempDir(), "bleve"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = idx.Close() })

	docs := make([]search.BookDocument, 0, len(bookIDs))
	for _, id := range bookIDs {
		docs = append(docs, search.BookDocument{BookID: id, Title: id, Author: "sanderson"})
	}
	require.NoError(t, idx.IndexBookBatch(docs))

	evalStore := mocks.NewMockStore(t)
	evalStore.EXPECT().GetBooksByIDs(mock.Anything).RunAndReturn(func(ids []string) ([]database.Book, error) {
		books := make([]database.Book, 0, len(ids))
		for _, id := range ids {
			books = append(books, database.Book{ID: id})
		}
		return books, nil
	}).Maybe()

	h := handlers.NewCollectionHandler(store, evalStore, func() *search.BleveIndex { return idx })
	gin.SetMode(gin.TestMode)
	r := gin.New()
	g := r.Group("/api/v1")
	g.GET("/collections/:id", h.GetCollection)
	return r
}

// TestReadingADynamicCollectionTwiceWritesOnce is the regression test for a GET
// that mutated the database.
//
// GetCollection re-evaluates a dynamic collection and stored the result
// unconditionally. PebbleStore.UpdateCollection sets UpdatedAt=now,
// Version=prev+1 and commits with pebble.Sync — so every READ was an fsync and a
// version bump. Version stopped counting changes, and because the ABS DTO exposes
// UpdatedAt as lastUpdate, a client using it for cache invalidation would re-fetch
// an untouched collection after every unrelated read, forever.
//
// The assertion is a DOSE-RESPONSE, not "never writes": the first read must still
// persist, because membership genuinely changed from empty. A guard that simply
// disabled the write would pass "second read writes nothing" and fail here.
func TestReadingADynamicCollectionTwiceWritesOnce(t *testing.T) {
	store := &colFakeStore{cols: []database.Collection{{
		ID:    "c1",
		Name:  "Sanderson",
		Type:  database.CollectionTypeDynamic,
		Query: "author:sanderson",
		// Deliberately empty: the first read has real work to persist.
		MaterializedBookIDs: nil,
	}}}
	r := colEvalRouter(t, store, "b1", "b2")

	w := colDo(t, r, http.MethodGet, "/api/v1/collections/c1", nil)
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	require.Equal(t, 1, store.updates,
		"the first read found membership that differs from what is stored and must persist it")
	require.NotEmpty(t, store.cols[0].MaterializedBookIDs,
		"the fixture must actually evaluate to something, or the second half of this "+
			"test compares empty to empty and proves nothing")

	w = colDo(t, r, http.MethodGet, "/api/v1/collections/c1", nil)
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	assert.Equal(t, 1, store.updates,
		"nothing changed between the two reads, so the second must not write: an "+
			"unconditional persist here makes every GET an fsync and bumps Version")
}
