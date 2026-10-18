// file: internal/server/handlers/abs/collections_owner_test.go
// version: 1.0.0
// guid: 6d3f9b28-4e71-4a05-b8c2-1f7a0e5d9c43
// last-edited: 2026-10-18

package abs_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	searchidx "github.com/falkcorp/audiobook-organizer/internal/search"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
	servermiddleware "github.com/falkcorp/audiobook-organizer/internal/server/middleware"
)

// ownerEvalStore answers the two reads the native handler's evaluator makes.
// states is keyed "user/book".
type ownerEvalStore struct {
	states map[string]*database.UserBookState
}

func (o *ownerEvalStore) GetBookByID(id string) (*database.Book, error) {
	return &database.Book{ID: id}, nil
}

func (o *ownerEvalStore) GetUserBookState(userID, bookID string) (*database.UserBookState, error) {
	return o.states[userID+"/"+bookID], nil
}

// ownerUsers is the native handler's owner lookup.
type ownerUsers struct{ users []database.User }

func (o *ownerUsers) GetUserByID(id string) (*database.User, error) {
	for i := range o.users {
		if o.users[i].ID == id {
			cp := o.users[i]
			return &cp, nil
		}
	}
	return nil, nil
}

func (o *ownerUsers) ListUsers() ([]database.User, error) { return o.users, nil }

// TestCollections_ABSServesTheOwnersMembersToAnotherUser: a static and a dynamic
// collection are created through the NATIVE handler (behind the real
// PermCollectionsManage gate) by alice, then listed on the ABS surface by a
// different user. The ABS list must show both with their names, serve the
// dynamic one's MATERIALIZED members, which are alice's (the owner's), not the
// reader's and not its stale book_ids, and must not filter by type.
func TestCollections_ABSServesTheOwnersMembersToAnotherUser(t *testing.T) {
	gin.SetMode(gin.TestMode)
	seed := seedOracleLibrary(t)
	store := &abscolFakeStore{}

	// Alice finished the single-file book; bob finished the multi-file one. A
	// dynamic "finished" collection owned by alice holds ONLY the first, so if
	// the reader's state ever decided membership, bob would see the second.
	evalStore := &ownerEvalStore{states: map[string]*database.UserBookState{
		"alice/" + seed.singleID: {UserID: "alice", BookID: seed.singleID, Status: database.UserBookStatusFinished},
		"bob/" + seed.multiID:    {UserID: "bob", BookID: seed.multiID, Status: database.UserBookStatusFinished},
	}}
	users := &ownerUsers{users: []database.User{
		{ID: "alice", Username: "alice", Roles: []string{auth.SeedRoleAdmin}, Status: "active",
			CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
		{ID: "bob", Username: "bob", Roles: []string{"viewer"}, Status: "active",
			CreatedAt: time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)},
	}}

	idx, err := searchidx.Open(filepath.Join(t.TempDir(), "bleve"))
	if err != nil {
		t.Fatalf("bleve open: %v", err)
	}
	t.Cleanup(func() { _ = idx.Close() })
	if err := idx.MarkRebuilt(); err != nil {
		t.Fatalf("MarkRebuilt: %v", err)
	}
	if err := idx.IndexBookBatch([]searchidx.BookDocument{
		{BookID: seed.singleID, Title: "Synthetic single"},
		{BookID: seed.multiID, Title: "Synthetic multi"},
	}); err != nil {
		t.Fatalf("index: %v", err)
	}

	// The gate needs a store with at least one user, or RequirePermission's
	// first-run bypass lets every caller through and the test measures nothing.
	authStore, err := database.NewPebbleStoreInMemory(filepath.Join(t.TempDir(), "auth.pebble"))
	if err != nil {
		t.Fatalf("auth store: %v", err)
	}
	t.Cleanup(func() { _ = authStore.Close() })
	if _, err := authStore.CreateUser("gatekeeper", "gate@example.test", "bcrypt", "hash", []string{"viewer"}, "active"); err != nil {
		t.Fatalf("seed user: %v", err)
	}

	native := handlers.NewCollectionHandler(store, store, users, evalStore, func() *searchidx.BleveIndex { return idx })
	r := gin.New()
	// The caller and its permissions come from headers, the way the auth
	// middleware binds them from a session.
	r.Use(func(c *gin.Context) {
		id := c.GetHeader("X-Test-User")
		u := &database.User{ID: id, Username: id}
		ctx := auth.WithUser(c.Request.Context(), u)
		if c.GetHeader("X-Test-Manage") == "1" {
			ctx = auth.WithPermissions(ctx, []auth.Permission{auth.PermLibraryView, auth.PermCollectionsManage})
		}
		c.Request = c.Request.WithContext(ctx)
		c.Set("auth_user", u)
		c.Next()
	})
	gate := servermiddleware.RequirePermission(authStore, auth.PermCollectionsManage)
	g := r.Group("/api/v1")
	g.POST("/collections", gate, native.CreateCollection)
	g.POST("/collections/:id/materialize", gate, native.MaterializeCollection)

	nativeDo := func(user string, manage bool, path string, body any) *httptest.ResponseRecorder {
		var buf []byte
		if body != nil {
			buf, _ = json.Marshal(body)
		}
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(buf))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Test-User", user)
		if manage {
			req.Header.Set("X-Test-Manage", "1")
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}

	// The gate is real: bob cannot create.
	if w := nativeDo("bob", false, "/api/v1/collections", map[string]any{"name": "Nope", "type": "static"}); w.Code != http.StatusForbidden {
		t.Fatalf("a caller without collections.manage created a collection: %d", w.Code)
	}

	if w := nativeDo("alice", true, "/api/v1/collections", map[string]any{
		"name": "Static Shelf", "type": "static", "book_ids": []string{seed.singleID},
	}); w.Code != http.StatusCreated {
		t.Fatalf("native static create = %d: %s", w.Code, w.Body.String())
	}
	w := nativeDo("alice", true, "/api/v1/collections", map[string]any{
		"name": "Alice Finished", "type": "dynamic", "query": "read_status:finished",
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("native dynamic create = %d: %s", w.Code, w.Body.String())
	}
	var created struct {
		Data database.Collection `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if created.Data.CreatedByUserID != "alice" {
		t.Fatalf("owner = %q, want alice", created.Data.CreatedByUserID)
	}
	if len(created.Data.MaterializedBookIDs) != 1 || created.Data.MaterializedBookIDs[0] != seed.singleID {
		t.Fatalf("materialized at create = %v, want alice's [%s]", created.Data.MaterializedBookIDs, seed.singleID)
	}

	// A stale book_ids on the dynamic row must be ignored by the ABS read.
	for i := range store.cols {
		if store.cols[i].Type == database.CollectionTypeDynamic {
			store.cols[i].BookIDs = []string{seed.multiID}
		}
	}

	// Bob (a different user, with the manage permission so his refresh is
	// allowed) re-materializes natively: the stored set must still be alice's.
	if w := nativeDo("bob", true, "/api/v1/collections/"+created.Data.ID+"/materialize", nil); w.Code != http.StatusOK {
		t.Fatalf("bob's materialize = %d: %s", w.Code, w.Body.String())
	}

	// ABS list, as a user who is neither the owner nor an admin.
	h := newHarness(t, "jwt", nil, withLibrary(seed), withUserData(fixtureUserData()), withCollections(store))
	h.seedUser(t, "bob", "bob", "", "pw-pw-pw-pw")
	tok := str(t, userObj(t, h.login(t, "bob", "pw-pw-pw-pw")), "accessToken")

	rows := abscolResults(t, h, tok)
	if store.gotType != "" {
		t.Fatalf("ABS list filtered on type %q; it must request both static and dynamic", store.gotType)
	}
	if len(rows) != 2 {
		t.Fatalf("ABS list has %d rows, want 2 (static and dynamic)", len(rows))
	}
	byName := map[string]map[string]any{}
	for _, row := range rows {
		name, _ := row["name"].(string)
		byName[name] = row
	}
	staticRow, dynRow := byName["Static Shelf"], byName["Alice Finished"]
	if staticRow == nil || dynRow == nil {
		t.Fatalf("ABS list names = %v, want both Static Shelf and Alice Finished", rows)
	}
	books, _ := dynRow["books"].([]any)
	if len(books) != 1 {
		t.Fatalf("dynamic collection served %d books, want exactly alice's one", len(books))
	}
	first, _ := books[0].(map[string]any)
	if first["id"] != absplSyncIDFor(t, seed, seed.singleID) {
		t.Fatalf("dynamic collection served %v, want alice's finished book: it must serve the owner's "+
			"materialized members, not the reader's, and not its book_ids", first["id"])
	}
	sbooks, _ := staticRow["books"].([]any)
	if len(sbooks) != 1 {
		t.Fatalf("static collection served %d books, want 1", len(sbooks))
	}
}
