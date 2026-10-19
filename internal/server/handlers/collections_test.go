// file: internal/server/handlers/collections_test.go
// version: 1.6.0
// guid: 5d17e903-2b48-4c81-96af-70e3c5a12b8d
// last-edited: 2026-10-18

package handlers_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

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
	// mu guards UpdateCollection, the one write the scheduled refresh issues from
	// several workers at once.
	mu      sync.Mutex
	cols    []database.Collection
	created *database.Collection
	updates int
	// createErr / updateErr, when set, are returned by the matching write so a
	// test can drive the handler's error mapping with a real store error shape.
	createErr error
	updateErr error
	// membershipWrites counts calls into the locked add/remove operations, so a
	// gated test can prove a refused request never reached them.
	membershipWrites int
}

// AddBooksToCollection / RemoveBookFromCollection give the fake the locked
// membership operations with the real store's semantics (static only, skip
// duplicates, keep order, absent member is a sentinel), so handler tests assert
// the handler's mapping rather than a looser fake's behaviour.
func (f *colFakeStore) AddBooksToCollection(id string, bookIDs []string) (*database.Collection, error) {
	f.membershipWrites++
	for i := range f.cols {
		if f.cols[i].ID != id {
			continue
		}
		if f.cols[i].Type == database.CollectionTypeDynamic {
			return nil, database.ErrCollectionNotStatic
		}
		changed := false
		for _, b := range bookIDs {
			if !slices.Contains(f.cols[i].BookIDs, b) {
				f.cols[i].BookIDs = append(f.cols[i].BookIDs, b)
				changed = true
			}
		}
		if changed {
			f.cols[i].Version++ // the real store bumps Version on a change
		}
		cp := f.cols[i]
		return &cp, nil
	}
	return nil, fmt.Errorf("collection %s %w", id, database.ErrCollectionNotFound)
}

func (f *colFakeStore) RemoveBookFromCollection(id, bookID string) (*database.Collection, error) {
	f.membershipWrites++
	for i := range f.cols {
		if f.cols[i].ID != id {
			continue
		}
		if f.cols[i].Type == database.CollectionTypeDynamic {
			return nil, database.ErrCollectionNotStatic
		}
		at := slices.Index(f.cols[i].BookIDs, bookID)
		if at < 0 {
			return nil, database.ErrCollectionBookNotMember
		}
		f.cols[i].BookIDs = slices.Delete(slices.Clone(f.cols[i].BookIDs), at, at+1)
		f.cols[i].Version++
		cp := f.cols[i]
		return &cp, nil
	}
	return nil, fmt.Errorf("collection %s %w", id, database.ErrCollectionNotFound)
}

// colFakeUsers is the owner lookup. byID is what GetUserByID answers (nil for an
// id with no row), list is what the first-admin fallback scans.
type colFakeUsers struct {
	list []database.User
}

func (f *colFakeUsers) GetUserByID(id string) (*database.User, error) {
	for i := range f.list {
		if f.list[i].ID == id {
			cp := f.list[i]
			return &cp, nil
		}
	}
	return nil, nil
}

func (f *colFakeUsers) ListUsers() ([]database.User, error) { return f.list, nil }

// colFakeBooks answers the two reads the handler and the evaluator make. known
// is the set of book ids that exist; states is keyed "user/book".
type colFakeBooks struct {
	known  map[string]bool
	states map[string]*database.UserBookState
}

func (f *colFakeBooks) GetBookByID(id string) (*database.Book, error) {
	if f.known != nil && !f.known[id] {
		return nil, nil
	}
	return &database.Book{ID: id}, nil
}

func (f *colFakeBooks) GetUserBookState(userID, bookID string) (*database.UserBookState, error) {
	return f.states[userID+"/"+bookID], nil
}

func (f *colFakeStore) CreateCollection(col *database.Collection) (*database.Collection, error) {
	if f.createErr != nil {
		return nil, f.createErr
	}
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
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updates++
	if f.updateErr != nil {
		return f.updateErr
	}
	for i := range f.cols {
		if f.cols[i].ID == col.ID {
			// Same compare-and-swap as PebbleStore.UpdateCollection (plain equality,
			// no zero bypass), so a handler that writes a row it did not just read
			// is caught here and not only against a real store.
			if col.Version != f.cols[i].Version {
				return fmt.Errorf("collection %s: %w: expected %d, got %d",
					col.ID, database.ErrCollectionVersionConflict, f.cols[i].Version, col.Version)
			}
			col.Version = f.cols[i].Version + 1
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
	// A nil index getter is the plain fixture here: these tests assert
	// validation and routing, not query evaluation, and a dynamic collection is
	// specified to survive an unavailable index rather than fail creation.
	members, _ := store.(handlers.CollectionMembers)
	h := handlers.NewCollectionHandler(store, members, nil, &colFakeBooks{}, nil)
	r := gin.New()
	g := r.Group("/api/v1")
	g.POST("/collections", h.CreateCollection)
	g.GET("/collections", h.ListCollections)
	g.GET("/collections/:id", h.GetCollection)
	g.PUT("/collections/:id", h.UpdateCollection)
	g.DELETE("/collections/:id", h.DeleteCollection)
	g.POST("/collections/:id/materialize", h.MaterializeCollection)
	g.POST("/collections/:id/books", h.AddBooksToCollection)
	g.DELETE("/collections/:id/books/:bookId", h.RemoveBookFromCollection)
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
		{http.MethodPost, "/api/v1/collections/c1/books", map[string]any{"book_ids": []string{"b1"}}},
		{http.MethodDelete, "/api/v1/collections/c1/books/b1", nil},
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
			assert.Zero(t, fake.membershipWrites,
				"%s %s was refused but still reached the locked membership operation", w.method, w.path)
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

	// The same negative control for the two membership routes: a router that
	// refused them for EVERY caller (a typo in the route table, say) would pass
	// the "without the permission" block above.
	t.Run("membership routes with the permission", func(t *testing.T) {
		fake := &colFakeStore{cols: []database.Collection{{
			ID: "c1", Name: "Existing", Type: database.CollectionTypeStatic, BookIDs: []string{"b1"},
		}}}
		r := colGatedRouter(store, fake,
			[]auth.Permission{auth.PermLibraryView, auth.PermCollectionsManage})

		rec := colDo(t, r, http.MethodPost, "/api/v1/collections/c1/books",
			map[string]any{"book_ids": []string{"b2"}})
		require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
		assert.Equal(t, []string{"b1", "b2"}, fake.cols[0].BookIDs)

		rec = colDo(t, r, http.MethodDelete, "/api/v1/collections/c1/books/b1", nil)
		require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
		assert.Equal(t, []string{"b2"}, fake.cols[0].BookIDs)
		assert.Equal(t, 2, fake.membershipWrites)
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
	members, _ := colStore.(handlers.CollectionMembers)
	h := handlers.NewCollectionHandler(colStore, members, nil, &colFakeBooks{}, nil)
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
	g.POST("/collections/:id/books", gate, h.AddBooksToCollection)
	g.DELETE("/collections/:id/books/:bookId", gate, h.RemoveBookFromCollection)
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
	// Fixture indexes every book itself, so it is complete by construction.
	if err := idx.MarkRebuilt(); err != nil {
		t.Fatalf("MarkRebuilt: %v", err)
	}

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

	h := handlers.NewCollectionHandler(store, nil, colEvalOwners(), evalStore, func() *search.BleveIndex { return idx })
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

// TestCollectionNameInUse_Maps409 pins the duplicate-name mapping to the typed
// sentinel. The lookalike case is the point: an error that merely CONTAINS
// "already in use" without wrapping database.ErrCollectionNameInUse must NOT be
// read as a name conflict — the handler used to match that text, so a fake (or
// a reworded store message) could pass or fail for the wrong reason.
func TestCollectionNameInUse_Maps409(t *testing.T) {
	inUse := fmt.Errorf("%w: %q", database.ErrCollectionNameInUse, "Road Trip")
	lookalike := fmt.Errorf("collection name %q already in use", "Road Trip")
	seed := func() *colFakeStore {
		return &colFakeStore{cols: []database.Collection{{ID: "c1", Name: "Commute", Type: database.CollectionTypeStatic}}}
	}
	body := map[string]any{"name": "Road Trip", "type": "static"}

	t.Run("create with sentinel is 409", func(t *testing.T) {
		store := seed()
		store.createErr = inUse
		r, _ := colRouter(store)
		w := colDo(t, r, http.MethodPost, "/api/v1/collections", body)
		assert.Equal(t, http.StatusConflict, w.Code, "body: %s", w.Body.String())
	})
	t.Run("create with lookalike text is not 409", func(t *testing.T) {
		store := seed()
		store.createErr = lookalike
		r, _ := colRouter(store)
		w := colDo(t, r, http.MethodPost, "/api/v1/collections", body)
		assert.Equal(t, http.StatusInternalServerError, w.Code, "body: %s", w.Body.String())
	})
	t.Run("rename with sentinel is 409", func(t *testing.T) {
		store := seed()
		store.updateErr = inUse
		r, _ := colRouter(store)
		w := colDo(t, r, http.MethodPut, "/api/v1/collections/c1", map[string]any{"name": "Road Trip"})
		assert.Equal(t, http.StatusConflict, w.Code, "body: %s", w.Body.String())
		assert.Equal(t, 1, store.updates, "the handler must have reached the store write")
	})
	t.Run("rename with lookalike text is not 409", func(t *testing.T) {
		store := seed()
		store.updateErr = lookalike
		r, _ := colRouter(store)
		w := colDo(t, r, http.MethodPut, "/api/v1/collections/c1", map[string]any{"name": "Road Trip"})
		assert.Equal(t, http.StatusInternalServerError, w.Code, "body: %s", w.Body.String())
	})
}

// colEvalOwners is the owner lookup for tests that do not care who the owner is:
// one active admin, so the first-admin fallback always has an answer.
func colEvalOwners() *colFakeUsers {
	return &colFakeUsers{list: []database.User{{
		ID: "admin-1", Username: "admin", Roles: []string{auth.SeedRoleAdmin}, Status: "active",
		CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}}}
}

// ── add / remove books ──────────────────────────────────────────────────────

func staticCollection(ids ...string) *colFakeStore {
	return &colFakeStore{cols: []database.Collection{{
		ID: "c1", Name: "Shelf", Type: database.CollectionTypeStatic, BookIDs: ids,
	}}}
}

func TestAddBooksToCollection(t *testing.T) {
	t.Run("appends in order and skips duplicates", func(t *testing.T) {
		store := staticCollection("b1")
		r, _ := colRouter(store)
		w := colDo(t, r, http.MethodPost, "/api/v1/collections/c1/books",
			map[string]any{"book_ids": []string{"b2", "b1", "b3"}})
		require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
		assert.Equal(t, []string{"b1", "b2", "b3"}, store.cols[0].BookIDs)
		assert.Contains(t, w.Body.String(), `"b3"`, "the response is the updated collection")
		assert.Equal(t, 0, store.updates,
			"membership must go through the locked op, not a read-modify-write UpdateCollection")
	})

	bad := []struct {
		name string
		body any
	}{
		{"empty list", map[string]any{"book_ids": []string{}}},
		{"missing list", map[string]any{}},
		{"not json", "not-an-object"},
	}
	for _, tc := range bad {
		t.Run("400 for "+tc.name, func(t *testing.T) {
			store := staticCollection()
			r, _ := colRouter(store)
			w := colDo(t, r, http.MethodPost, "/api/v1/collections/c1/books", tc.body)
			assert.Equal(t, http.StatusBadRequest, w.Code, "body: %s", w.Body.String())
			assert.Zero(t, store.membershipWrites)
		})
	}

	t.Run("400 over the per-request cap, accepted at the cap", func(t *testing.T) {
		ids := func(n int) []string {
			out := make([]string, n)
			for i := range out {
				out[i] = fmt.Sprintf("book-%04d", i)
			}
			return out
		}
		store := staticCollection()
		r, _ := colRouter(store)
		w := colDo(t, r, http.MethodPost, "/api/v1/collections/c1/books",
			map[string]any{"book_ids": ids(501)})
		assert.Equal(t, http.StatusBadRequest, w.Code)
		assert.Zero(t, store.membershipWrites)

		w = colDo(t, r, http.MethodPost, "/api/v1/collections/c1/books",
			map[string]any{"book_ids": ids(500)})
		require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
		assert.Len(t, store.cols[0].BookIDs, 500)
	})

	t.Run("400 names the first unknown book and writes nothing", func(t *testing.T) {
		store := staticCollection("b1")
		gin.SetMode(gin.TestMode)
		h := handlers.NewCollectionHandler(store, store, nil,
			&colFakeBooks{known: map[string]bool{"b1": true, "b2": true}}, nil)
		r := gin.New()
		r.POST("/collections/:id/books", h.AddBooksToCollection)
		w := colDo(t, r, http.MethodPost, "/collections/c1/books",
			map[string]any{"book_ids": []string{"b2", "ghost-book", "other-ghost"}})
		require.Equal(t, http.StatusBadRequest, w.Code, "body: %s", w.Body.String())
		assert.Contains(t, w.Body.String(), "ghost-book")
		assert.NotContains(t, w.Body.String(), "other-ghost")
		assert.Zero(t, store.membershipWrites, "b2 is real, but one unknown id must reject the whole request")
		assert.Equal(t, []string{"b1"}, store.cols[0].BookIDs)
	})

	t.Run("404 for an unknown collection", func(t *testing.T) {
		store := staticCollection()
		r, _ := colRouter(store)
		w := colDo(t, r, http.MethodPost, "/api/v1/collections/nope/books",
			map[string]any{"book_ids": []string{"b1"}})
		assert.Equal(t, http.StatusNotFound, w.Code, "body: %s", w.Body.String())
	})

	t.Run("409 for a dynamic collection", func(t *testing.T) {
		store := &colFakeStore{cols: []database.Collection{{
			ID: "c1", Name: "Smart", Type: database.CollectionTypeDynamic, Query: "author:sanderson",
		}}}
		r, _ := colRouter(store)
		w := colDo(t, r, http.MethodPost, "/api/v1/collections/c1/books",
			map[string]any{"book_ids": []string{"b1"}})
		assert.Equal(t, http.StatusConflict, w.Code, "body: %s", w.Body.String())
		assert.Zero(t, store.membershipWrites)
	})

	t.Run("409 when the row turned dynamic after the pre-check", func(t *testing.T) {
		// The pre-check reads a static row; the locked op then reports the row is
		// not static. The mapping must still be 409 rather than a 500.
		store := &racingMembership{colFakeStore: staticCollection(), err: database.ErrCollectionNotStatic}
		gin.SetMode(gin.TestMode)
		h := handlers.NewCollectionHandler(store, store, nil, &colFakeBooks{}, nil)
		r := gin.New()
		r.POST("/collections/:id/books", h.AddBooksToCollection)
		w := colDo(t, r, http.MethodPost, "/collections/c1/books",
			map[string]any{"book_ids": []string{"b1"}})
		assert.Equal(t, http.StatusConflict, w.Code, "body: %s", w.Body.String())
	})

	t.Run("503 without a membership store", func(t *testing.T) {
		gin.SetMode(gin.TestMode)
		h := handlers.NewCollectionHandler(staticCollection(), nil, nil, &colFakeBooks{}, nil)
		r := gin.New()
		r.POST("/collections/:id/books", h.AddBooksToCollection)
		w := colDo(t, r, http.MethodPost, "/collections/c1/books",
			map[string]any{"book_ids": []string{"b1"}})
		assert.Equal(t, http.StatusServiceUnavailable, w.Code)
	})
}

// racingMembership returns err from the locked ops, standing in for a row that
// changed between the handler's pre-check and the write.
type racingMembership struct {
	*colFakeStore
	err error
}

func (r *racingMembership) AddBooksToCollection(string, []string) (*database.Collection, error) {
	return nil, r.err
}

func (r *racingMembership) RemoveBookFromCollection(string, string) (*database.Collection, error) {
	return nil, r.err
}

func TestRemoveBookFromCollection(t *testing.T) {
	t.Run("removes a member", func(t *testing.T) {
		store := staticCollection("b1", "b2", "b3")
		r, _ := colRouter(store)
		w := colDo(t, r, http.MethodDelete, "/api/v1/collections/c1/books/b2", nil)
		require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
		assert.Equal(t, []string{"b1", "b3"}, store.cols[0].BookIDs)
		assert.Zero(t, store.updates)
	})
	t.Run("404 for a book that is not a member", func(t *testing.T) {
		store := staticCollection("b1")
		r, _ := colRouter(store)
		w := colDo(t, r, http.MethodDelete, "/api/v1/collections/c1/books/b9", nil)
		assert.Equal(t, http.StatusNotFound, w.Code, "body: %s", w.Body.String())
		assert.Equal(t, []string{"b1"}, store.cols[0].BookIDs)
	})
	t.Run("404 for an unknown collection", func(t *testing.T) {
		r, _ := colRouter(staticCollection("b1"))
		w := colDo(t, r, http.MethodDelete, "/api/v1/collections/nope/books/b1", nil)
		assert.Equal(t, http.StatusNotFound, w.Code)
	})
	t.Run("409 for a dynamic collection", func(t *testing.T) {
		store := &colFakeStore{cols: []database.Collection{{
			ID: "c1", Name: "Smart", Type: database.CollectionTypeDynamic, Query: "author:sanderson",
			MaterializedBookIDs: []string{"b1"},
		}}}
		r, _ := colRouter(store)
		w := colDo(t, r, http.MethodDelete, "/api/v1/collections/c1/books/b1", nil)
		assert.Equal(t, http.StatusConflict, w.Code, "body: %s", w.Body.String())
		assert.Equal(t, []string{"b1"}, store.cols[0].MaterializedBookIDs)
	})
}

// ── owner evaluation (D75) ──────────────────────────────────────────────────

// ownerRig is a real Bleve index over b1..b3 plus fakes for the owner lookup and
// per-user state. The caller is injected from the X-Test-User header so a test
// can prove the answer does not depend on who is signed in.
type ownerRig struct {
	r     *gin.Engine
	store *colFakeStore
	h     *handlers.CollectionHandler
}

func newOwnerRig(t *testing.T, store *colFakeStore, users []database.User,
	states map[string]*database.UserBookState, opts ...handlers.CollectionOption) *ownerRig {
	t.Helper()
	idx, err := search.Open(filepath.Join(t.TempDir(), "bleve"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = idx.Close() })
	require.NoError(t, idx.MarkRebuilt())
	require.NoError(t, idx.IndexBookBatch([]search.BookDocument{
		{BookID: "b1", Title: "Alpha"}, {BookID: "b2", Title: "Bravo"}, {BookID: "b3", Title: "Charlie"},
	}))

	gin.SetMode(gin.TestMode)
	h := handlers.NewCollectionHandler(store, nil, &colFakeUsers{list: users},
		&colFakeBooks{states: states}, func() *search.BleveIndex { return idx }, opts...)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		if id := c.GetHeader("X-Test-User"); id != "" {
			u := &database.User{ID: id, Username: id}
			c.Request = c.Request.WithContext(auth.WithUser(c.Request.Context(), u))
			c.Set("auth_user", u)
		}
		c.Next()
	})
	g := r.Group("/api/v1")
	g.GET("/collections/:id", h.GetCollection)
	g.POST("/collections/:id/materialize", h.MaterializeCollection)
	g.POST("/collections", h.CreateCollection)
	g.PUT("/collections/:id", h.UpdateCollection)
	return &ownerRig{r: r, store: store, h: h}
}

func (o *ownerRig) do(t *testing.T, method, path, asUser string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	if asUser != "" {
		req.Header.Set("X-Test-User", asUser)
	}
	w := httptest.NewRecorder()
	o.r.ServeHTTP(w, req)
	return w
}

// send is do with a JSON body.
func (o *ownerRig) send(t *testing.T, method, path, asUser string, body any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	require.NoError(t, err)
	req := httptest.NewRequest(method, path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	if asUser != "" {
		req.Header.Set("X-Test-User", asUser)
	}
	w := httptest.NewRecorder()
	o.r.ServeHTTP(w, req)
	return w
}

func decodeMaterialized(t *testing.T, w *httptest.ResponseRecorder) []string {
	t.Helper()
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	var env struct {
		Data database.Collection `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &env), "body: %s", w.Body.String())
	return env.Data.MaterializedBookIDs
}

func ownerStates() map[string]*database.UserBookState {
	return map[string]*database.UserBookState{
		"alice/b1": {UserID: "alice", BookID: "b1", Status: database.UserBookStatusFinished},
		"alice/b2": {UserID: "alice", BookID: "b2", Status: database.UserBookStatusInProgress},
		"bob/b2":   {UserID: "bob", BookID: "b2", Status: database.UserBookStatusFinished},
	}
}

func ownerUsers() []database.User {
	return []database.User{
		{ID: "alice", Username: "alice", Roles: []string{auth.SeedRoleAdmin}, Status: "active",
			CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
		{ID: "bob", Username: "bob", Roles: []string{"viewer"}, Status: "active",
			CreatedAt: time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)},
	}
}

// TestDynamicCollectionIsEvaluatedAsItsOwner is the D75 regression: the answer
// follows CreatedByUserID, not whoever is signed in. Alice finished b1 and bob
// finished b2, so a reader-scoped evaluation would flip the members with the
// caller.
func TestDynamicCollectionIsEvaluatedAsItsOwner(t *testing.T) {
	newStore := func() *colFakeStore {
		return &colFakeStore{cols: []database.Collection{
			{ID: "owned-by-alice", Name: "A", Type: database.CollectionTypeDynamic,
				Query: "read_status:finished", CreatedByUserID: "alice"},
			{ID: "owned-by-bob", Name: "B", Type: database.CollectionTypeDynamic,
				Query: "read_status:finished", CreatedByUserID: "bob"},
		}}
	}

	for _, caller := range []string{"alice", "bob", ""} {
		t.Run("materialize as "+caller, func(t *testing.T) {
			rig := newOwnerRig(t, newStore(), ownerUsers(), ownerStates())
			w := rig.do(t, http.MethodPost, "/api/v1/collections/owned-by-alice/materialize", caller)
			assert.Equal(t, []string{"b1"}, decodeMaterialized(t, w),
				"alice owns it, so it holds what alice finished, whoever asked")
			w = rig.do(t, http.MethodPost, "/api/v1/collections/owned-by-bob/materialize", caller)
			assert.Equal(t, []string{"b2"}, decodeMaterialized(t, w))
		})
		t.Run("read as "+caller, func(t *testing.T) {
			rig := newOwnerRig(t, newStore(), ownerUsers(), ownerStates())
			w := rig.do(t, http.MethodGet, "/api/v1/collections/owned-by-alice", caller)
			assert.Equal(t, []string{"b1"}, decodeMaterialized(t, w))
			// What a read stores is what the ABS surface serves without
			// evaluating, so it must be the owner's answer too.
			assert.Equal(t, []string{"b1"}, rig.store.cols[0].MaterializedBookIDs)
		})
	}
}

// TestDynamicCollectionUnstartedMatchesBooksWithNoStateRow is the end-to-end
// side of the read_status:unstarted fix: books the owner never touched have no
// state row at all, and they are exactly the unstarted ones.
func TestDynamicCollectionUnstartedMatchesBooksWithNoStateRow(t *testing.T) {
	store := &colFakeStore{cols: []database.Collection{{
		ID: "c1", Name: "Untouched", Type: database.CollectionTypeDynamic,
		Query: "read_status:unstarted", CreatedByUserID: "alice",
	}}}
	rig := newOwnerRig(t, store, ownerUsers(), ownerStates())
	w := rig.do(t, http.MethodPost, "/api/v1/collections/c1/materialize", "bob")
	assert.Equal(t, []string{"b3"}, decodeMaterialized(t, w),
		"b1 is finished and b2 in progress for alice; only b3 has no row")
}

func TestDynamicCollectionFallsBackToFirstActiveAdmin(t *testing.T) {
	users := []database.User{
		// Earliest admin, but not active: must be skipped.
		{ID: "old-admin", Username: "old", Roles: []string{auth.SeedRoleAdmin}, Status: "inactive",
			CreatedAt: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)},
		{ID: "bob", Username: "bob", Roles: []string{"viewer"}, Status: "active",
			CreatedAt: time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)},
		{ID: "alice", Username: "alice", Roles: []string{auth.SeedRoleAdmin}, Status: "active",
			CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
	}
	for name, owner := range map[string]string{"owner row gone": "deleted-user", "no owner recorded": ""} {
		t.Run(name, func(t *testing.T) {
			// A query that reads nobody's state still falls back to the admin (D75).
			store := &colFakeStore{cols: []database.Collection{{
				ID: "c1", Name: "Orphan", Type: database.CollectionTypeDynamic,
				Query: "title:Alpha", CreatedByUserID: owner,
			}}}
			rig := newOwnerRig(t, store, users, ownerStates())
			w := rig.do(t, http.MethodPost, "/api/v1/collections/c1/materialize", "bob")
			assert.Equal(t, []string{"b1"}, decodeMaterialized(t, w))

			// A per-user query is never evaluated as the stand-in: the first ACTIVE
			// admin (alice, not the earlier but inactive old-admin) takes it over by
			// saving it, and only then does it evaluate, as alice (b1), never as bob (b2).
			store = &colFakeStore{cols: []database.Collection{{
				ID: "c1", Name: "Orphan", Type: database.CollectionTypeDynamic,
				Query: "read_status:finished", CreatedByUserID: owner, MaterializedBookIDs: []string{"b9"},
			}}}
			rig = newOwnerRig(t, store, users, ownerStates())
			w = rig.do(t, http.MethodPost, "/api/v1/collections/c1/materialize", "bob")
			assert.Equal(t, http.StatusConflict, w.Code, "body: %s", w.Body.String())

			for _, who := range []string{"old-admin", "bob"} {
				w = rig.send(t, http.MethodPut, "/api/v1/collections/c1", who, map[string]any{"query": "read_status:finished"})
				assert.Equal(t, http.StatusOK, w.Code, "%s re-sending the same query: %s", who, w.Body.String())
				assert.Equal(t, owner, store.cols[0].CreatedByUserID, "%s is not the stand-in, so nothing is adopted", who)
			}
			// The admin re-sending the query without the flag adopts nothing; the
			// collection stays blocked and the response says so.
			w = rig.send(t, http.MethodPut, "/api/v1/collections/c1", "alice", map[string]any{"query": "read_status:finished"})
			require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
			assert.Equal(t, owner, store.cols[0].CreatedByUserID, "no adopt_owner, no adoption")
			assert.Contains(t, w.Body.String(), "evaluation_blocked")
			// Adoption is the explicit act, and only the stand-in admin's.
			for _, who := range []string{"old-admin", "bob"} {
				w = rig.send(t, http.MethodPut, "/api/v1/collections/c1", who,
					map[string]any{"query": "read_status:finished", "adopt_owner": true})
				assert.Equal(t, http.StatusForbidden, w.Code, "%s: %s", who, w.Body.String())
				assert.Equal(t, owner, store.cols[0].CreatedByUserID)
			}
			w = rig.send(t, http.MethodPut, "/api/v1/collections/c1", "alice",
				map[string]any{"query": "read_status:finished", "adopt_owner": true})
			require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
			assert.Equal(t, "alice", store.cols[0].CreatedByUserID)
			w = rig.do(t, http.MethodPost, "/api/v1/collections/c1/materialize", "bob")
			assert.Equal(t, []string{"b1"}, decodeMaterialized(t, w), "bob's state (b2) must not leak in")
		})
	}
}

func TestDynamicCollectionWithNoEvaluatorIs503(t *testing.T) {
	// The owner is gone and nobody is an active admin.
	users := []database.User{
		{ID: "bob", Username: "bob", Roles: []string{"viewer"}, Status: "active"},
		{ID: "old-admin", Username: "old", Roles: []string{auth.SeedRoleAdmin}, Status: "revoked"},
	}
	store := &colFakeStore{cols: []database.Collection{{
		ID: "c1", Name: "Orphan", Type: database.CollectionTypeDynamic, Query: "read_status:finished",
		CreatedByUserID: "deleted-user", MaterializedBookIDs: []string{"b9"},
	}}}
	rig := newOwnerRig(t, store, users, ownerStates())

	w := rig.do(t, http.MethodPost, "/api/v1/collections/c1/materialize", "bob")
	assert.Equal(t, http.StatusServiceUnavailable, w.Code, "body: %s", w.Body.String())
	assert.Equal(t, 0, store.updates)

	w = rig.do(t, http.MethodGet, "/api/v1/collections/c1", "bob")
	assert.Equal(t, []string{"b9"}, decodeMaterialized(t, w),
		"a read serves the last stored members rather than failing")
}

// ── per-user fields in a query are the evaluator's to write ─────────────────

// A dynamic collection is evaluated as its owner and the answer is stored and
// served to everybody. Anyone with collections.manage could therefore edit the
// query to read the owner's private state back out of the members. The fix:
// only the evaluating user may write a per-user field into the query.
func TestCollectionPerUserQueryIsEvaluatorOnly(t *testing.T) {
	const forbidden = http.StatusForbidden
	aliceOwned := func() *colFakeStore {
		return &colFakeStore{cols: []database.Collection{{
			ID: "c1", Name: "Alice's", Type: database.CollectionTypeDynamic,
			Query: "title:Alpha", CreatedByUserID: "alice", MaterializedBookIDs: []string{"b1"},
		}}}
	}
	put := func(rig *ownerRig, who, query string) *httptest.ResponseRecorder {
		return rig.send(t, http.MethodPut, "/api/v1/collections/c1", who, map[string]any{"query": query})
	}
	requireRefused := func(t *testing.T, w *httptest.ResponseRecorder) {
		t.Helper()
		require.Equal(t, forbidden, w.Code, "body: %s", w.Body.String())
		var body struct {
			Code  string `json:"code"`
			Error string `json:"error"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body), w.Body.String())
		assert.Equal(t, handlers.ErrCodeCollectionPerUserQueryOwnerOnly, body.Code)
		assert.Contains(t, body.Error, "only the collection's owner can use per-user fields")
		for _, f := range []string{"read_status", "progress_pct", "last_played"} {
			assert.Contains(t, body.Error, f)
		}
	}

	t.Run("a non-owner cannot put a per-user field in the query", func(t *testing.T) {
		for _, q := range []string{
			"read_status:finished",
			"title:Alpha AND read_status:finished",
			"-read_status:finished",
			"NOT read_status:unstarted",
			"progress_pct:>5",
			"last_played:>2026-01-01",
			"last_played:>-30d",
			"title:Alpha OR (title:Bravo AND read_status:in_progress)",
			"read_status:(finished|in_progress)",
		} {
			store := aliceOwned()
			rig := newOwnerRig(t, store, ownerUsers(), ownerStates())
			requireRefused(t, put(rig, "bob", q))
			assert.Equal(t, "title:Alpha", store.cols[0].Query, "%q: the query must be unchanged", q)
			assert.Equal(t, []string{"b1"}, store.cols[0].MaterializedBookIDs, "%q: so must the members", q)
			assert.Zero(t, store.updates, "%q: nothing is written", q)
		}
	})

	t.Run("a non-owner may still edit a query with no per-user field", func(t *testing.T) {
		store := aliceOwned()
		rig := newOwnerRig(t, store, ownerUsers(), ownerStates())
		w := put(rig, "bob", "title:Bravo")
		require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
		assert.Equal(t, "title:Bravo", store.cols[0].Query)
		assert.Equal(t, []string{"b2"}, store.cols[0].MaterializedBookIDs)
	})

	t.Run("the owner may use a per-user field, and it is read as the owner", func(t *testing.T) {
		store := aliceOwned()
		rig := newOwnerRig(t, store, ownerUsers(), ownerStates())
		w := put(rig, "alice", "read_status:finished")
		require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
		assert.Equal(t, "read_status:finished", store.cols[0].Query)
		assert.Equal(t, []string{"b1"}, store.cols[0].MaterializedBookIDs)
	})

	t.Run("only the query is gated: a non-owner can rename, re-send or strip it", func(t *testing.T) {
		store := &colFakeStore{cols: []database.Collection{{
			ID: "c1", Name: "Alice's", Type: database.CollectionTypeDynamic,
			Query: "read_status:finished", CreatedByUserID: "alice", MaterializedBookIDs: []string{"b1"},
		}}}
		rig := newOwnerRig(t, store, ownerUsers(), ownerStates())

		w := rig.send(t, http.MethodPut, "/api/v1/collections/c1", "bob", map[string]any{"name": "Renamed"})
		require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
		assert.Equal(t, "Renamed", store.cols[0].Name)

		// The same text again changes nothing the evaluator's state is asked about.
		w = put(rig, "bob", "read_status:finished")
		require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())

		// Changing it to another per-user query is refused ...
		requireRefused(t, put(rig, "bob", "read_status:in_progress"))
		assert.Equal(t, "read_status:finished", store.cols[0].Query)

		// ... and removing the per-user field is not.
		w = put(rig, "bob", "title:Charlie")
		require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
		assert.Equal(t, "title:Charlie", store.cols[0].Query)
	})

	t.Run("a collection evaluated as the first admin: only that admin may set one", func(t *testing.T) {
		for name, owner := range map[string]string{"owner row gone": "deleted-user", "no owner recorded": ""} {
			t.Run(name, func(t *testing.T) {
				store := aliceOwned()
				store.cols[0].CreatedByUserID = owner
				rig := newOwnerRig(t, store, ownerUsers(), ownerStates()) // alice is the first admin
				requireRefused(t, put(rig, "bob", "read_status:finished"))
				assert.Equal(t, "title:Alpha", store.cols[0].Query)
				assert.Equal(t, []string{"b1"}, store.cols[0].MaterializedBookIDs)

				// The stand-in admin may write the query, but writing it adopts nothing:
				// the collection stays ownerless and unevaluated until adopt_owner.
				w := put(rig, "alice", "read_status:finished")
				require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
				assert.Equal(t, owner, store.cols[0].CreatedByUserID, "no adopt_owner, no adoption")
				assert.Equal(t, []string{"b1"}, store.cols[0].MaterializedBookIDs, "members untouched")

				w = rig.send(t, http.MethodPut, "/api/v1/collections/c1", "alice",
					map[string]any{"query": "read_status:in_progress", "adopt_owner": true})
				require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
				assert.Equal(t, "alice", store.cols[0].CreatedByUserID,
					"the admin who asks to adopt an ownerless per-user collection takes it over")
				assert.Equal(t, []string{"b2"}, store.cols[0].MaterializedBookIDs, "and it is evaluated as alice")
			})
		}
	})

	t.Run("nobody can set one when there is no evaluator at all", func(t *testing.T) {
		users := []database.User{{ID: "bob", Username: "bob", Roles: []string{"viewer"}, Status: "active"}}
		store := aliceOwned()
		store.cols[0].CreatedByUserID = "deleted-user"
		rig := newOwnerRig(t, store, users, ownerStates())
		requireRefused(t, put(rig, "bob", "read_status:finished"))
		assert.Equal(t, "title:Alpha", store.cols[0].Query)
	})

	t.Run("create: the creator is the owner and the evaluator", func(t *testing.T) {
		store := &colFakeStore{}
		rig := newOwnerRig(t, store, ownerUsers(), ownerStates())
		w := rig.send(t, http.MethodPost, "/api/v1/collections", "bob", map[string]any{
			"name": "Bob finished", "type": "dynamic", "query": "read_status:finished"})
		require.Equal(t, http.StatusCreated, w.Code, "body: %s", w.Body.String())
		require.Len(t, store.cols, 1)
		assert.Equal(t, "bob", store.cols[0].CreatedByUserID)
		assert.Equal(t, []string{"b2"}, store.cols[0].MaterializedBookIDs,
			"evaluated as bob (b2), never as the first admin alice (b1)")
	})

	t.Run("create: a creator who is not a user row never borrows the admin's state", func(t *testing.T) {
		store := &colFakeStore{}
		rig := newOwnerRig(t, store, ownerUsers(), ownerStates())
		w := rig.send(t, http.MethodPost, "/api/v1/collections", "ghost", map[string]any{
			"name": "Ghost finished", "type": "dynamic", "query": "read_status:finished"})
		requireRefused(t, w)
		assert.Empty(t, store.cols, "nothing is stored")

		// Without a per-user field the same caller may create one.
		w = rig.send(t, http.MethodPost, "/api/v1/collections", "ghost", map[string]any{
			"name": "Ghost title", "type": "dynamic", "query": "title:Alpha"})
		require.Equal(t, http.StatusCreated, w.Code, "body: %s", w.Body.String())
	})
}

// ── a stand-in admin never evaluates a per-user query ───────────────────────

// An ownerless collection falls back to the first active admin, but only for a
// query that does not read anyone's state. With a per-user field the fallback
// would publish the admin's listening state, so the evaluation is refused and
// the stored members stay as they were.
func TestCollectionFallbackNeverEvaluatesPerUserQuery(t *testing.T) {
	ownerless := func(query string, owner string) *colFakeStore {
		return &colFakeStore{cols: []database.Collection{{
			ID: "c1", Name: "Orphan", Type: database.CollectionTypeDynamic,
			Query: query, CreatedByUserID: owner, MaterializedBookIDs: []string{"b3"}, Version: 1,
		}}}
	}
	for name, owner := range map[string]string{"owner row gone": "deleted-user", "no owner": "", "_local": "_local"} {
		t.Run(name+": materialize is a 409 and keeps the stored members", func(t *testing.T) {
			store := ownerless("read_status:finished", owner)
			rig := newOwnerRig(t, store, ownerUsers(), ownerStates())
			w := rig.do(t, http.MethodPost, "/api/v1/collections/c1/materialize", "alice")
			require.Equal(t, http.StatusConflict, w.Code, "body: %s", w.Body.String())
			var body struct {
				Code  string `json:"code"`
				Error string `json:"error"`
			}
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body), w.Body.String())
			assert.Equal(t, handlers.ErrCodeCollectionOwnerMissingPerUserQuery, body.Code)
			assert.Contains(t, body.Error, "per-user fields")
			assert.Equal(t, []string{"b3"}, store.cols[0].MaterializedBookIDs,
				"not re-materialized from the stand-in admin's state (alice finished b1)")
			assert.Zero(t, store.updates, "nothing is written")
		})

		t.Run(name+": a read serves the stored members with a flag", func(t *testing.T) {
			store := ownerless("read_status:finished", owner)
			rig := newOwnerRig(t, store, ownerUsers(), ownerStates())
			w := rig.do(t, http.MethodGet, "/api/v1/collections/c1", "bob")
			require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
			var body map[string]any
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
			data, _ := body["data"].(map[string]any)
			require.NotNil(t, data, w.Body.String())
			assert.Equal(t, handlers.ErrCodeCollectionOwnerMissingPerUserQuery, data["evaluation_blocked"])
			assert.NotEmpty(t, data["evaluation_blocked_message"])
			assert.Equal(t, "c1", data["id"], "the collection fields are still flattened at the top level")
			assert.Equal(t, []any{"b3"}, data["materialized_book_ids"])
			assert.Zero(t, store.updates, "a read stores nothing")
		})

		t.Run(name+": without a per-user field it still falls back to the admin", func(t *testing.T) {
			store := ownerless("title:Alpha", owner)
			rig := newOwnerRig(t, store, ownerUsers(), ownerStates())
			w := rig.do(t, http.MethodPost, "/api/v1/collections/c1/materialize", "bob")
			require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
			assert.Equal(t, []string{"b1"}, store.cols[0].MaterializedBookIDs)

			w = rig.do(t, http.MethodGet, "/api/v1/collections/c1", "bob")
			require.Equal(t, http.StatusOK, w.Code)
			assert.NotContains(t, w.Body.String(), "evaluation_blocked")
		})
	}

	t.Run("a collection with a live owner evaluates a per-user query as before", func(t *testing.T) {
		store := ownerless("read_status:finished", "bob")
		rig := newOwnerRig(t, store, ownerUsers(), ownerStates())
		w := rig.do(t, http.MethodPost, "/api/v1/collections/c1/materialize", "alice")
		require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
		assert.Equal(t, []string{"b2"}, store.cols[0].MaterializedBookIDs, "as bob, who finished b2")
		w = rig.do(t, http.MethodGet, "/api/v1/collections/c1", "alice")
		assert.NotContains(t, w.Body.String(), "evaluation_blocked")
	})

	t.Run("the first admin saving the query with adopt_owner takes the collection over", func(t *testing.T) {
		store := ownerless("read_status:finished", "deleted-user")
		rig := newOwnerRig(t, store, ownerUsers(), ownerStates())
		// Not a non-admin: bob re-sending the same query neither adopts nor errors.
		w := rig.send(t, http.MethodPut, "/api/v1/collections/c1", "bob", map[string]any{"query": "read_status:finished"})
		require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
		assert.Equal(t, "deleted-user", store.cols[0].CreatedByUserID)

		w = rig.send(t, http.MethodPut, "/api/v1/collections/c1", "alice",
			map[string]any{"query": "read_status:finished", "adopt_owner": true})
		require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
		assert.Equal(t, "alice", store.cols[0].CreatedByUserID)
		assert.Equal(t, []string{"b1"}, store.cols[0].MaterializedBookIDs)

		w = rig.do(t, http.MethodPost, "/api/v1/collections/c1/materialize", "bob")
		require.Equal(t, http.StatusOK, w.Code, "now it has an owner and evaluates")
	})
}
