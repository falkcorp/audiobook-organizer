// file: internal/server/handlers/abs/collections_viewer_test.go
// version: 2.0.1
// guid: 6d3f9b28-4e71-4a05-b8c2-1f7a0e5d9c43
// last-edited: 2026-10-26

package abs_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	searchidx "github.com/falkcorp/audiobook-organizer/internal/search"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
	abshandler "github.com/falkcorp/audiobook-organizer/internal/server/handlers/abs"
	servermiddleware "github.com/falkcorp/audiobook-organizer/internal/server/middleware"
)

// viewerEvalStore answers the two reads the native handler's evaluator makes.
// states is keyed "user/book".
type viewerEvalStore struct {
	states map[string]*database.UserBookState
}

func (o *viewerEvalStore) GetBookByID(id string) (*database.Book, error) {
	return &database.Book{ID: id}, nil
}

func (o *viewerEvalStore) GetUserBookState(userID, bookID string) (*database.UserBookState, error) {
	return o.states[userID+"/"+bookID], nil
}

// withViewerMembers injects the native handler's per-viewer evaluation, the way
// the server wires it.
func withViewerMembers(native *handlers.CollectionHandler) harnessOpt {
	return func(o *abshandler.Options) {
		o.CollectionViewerMembers = func(ctx context.Context, cols []database.Collection, viewerID string) (abshandler.ViewerMembers, error) {
			viewer, err := native.ViewerMembers(ctx, cols, viewerID)
			unavailable := make(map[string]struct{}, len(viewer.Unavailable))
			for id := range viewer.Unavailable {
				unavailable[id] = struct{}{}
			}
			return abshandler.ViewerMembers{Sets: viewer.Sets, Unavailable: unavailable}, err
		}
	}
}

// viewerFixture is the shared setup: alice (the creator) finished the single-file
// book, bob finished the multi-file one, and a native handler over a fake store
// can create collections behind the real PermCollectionsManage gate.
type viewerFixture struct {
	seed   *oracleSeed
	store  *abscolFakeStore
	native *handlers.CollectionHandler
	create func(user string, body map[string]any) database.Collection
}

func newViewerFixture(t *testing.T) *viewerFixture {
	t.Helper()
	gin.SetMode(gin.TestMode)
	seed := seedOracleLibrary(t)
	store := &abscolFakeStore{}

	evalStore := &viewerEvalStore{states: map[string]*database.UserBookState{
		"alice/" + seed.singleID: {UserID: "alice", BookID: seed.singleID, Status: database.UserBookStatusFinished},
		"bob/" + seed.multiID:    {UserID: "bob", BookID: seed.multiID, Status: database.UserBookStatusFinished},
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

	native := handlers.NewCollectionHandler(store, store, evalStore, func() *searchidx.BleveIndex { return idx })
	r := gin.New()
	r.Use(func(c *gin.Context) {
		id := c.GetHeader("X-Test-User")
		u := &database.User{ID: id, Username: id}
		ctx := auth.WithUser(c.Request.Context(), u)
		ctx = auth.WithPermissions(ctx, []auth.Permission{auth.PermLibraryView, auth.PermCollectionsManage})
		c.Request = c.Request.WithContext(ctx)
		c.Set("auth_user", u)
		c.Next()
	})
	gate := servermiddleware.RequirePermission(authStore, auth.PermCollectionsManage)
	r.POST("/api/v1/collections", gate, native.CreateCollection)

	f := &viewerFixture{seed: seed, store: store, native: native}
	f.create = func(user string, body map[string]any) database.Collection {
		t.Helper()
		buf, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/collections", bytes.NewReader(buf))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Test-User", user)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusCreated {
			t.Fatalf("native create = %d: %s", w.Code, w.Body.String())
		}
		var created struct {
			Data database.Collection `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return created.Data
	}
	return f
}

// bookIDsOf is the library item ids a collection row on the ABS surface serves.
func bookIDsOf(row map[string]any) []string {
	books, _ := row["books"].([]any)
	out := make([]string, 0, len(books))
	for _, b := range books {
		m, _ := b.(map[string]any)
		id, _ := m["id"].(string)
		out = append(out, id)
	}
	return out
}

func rowNamed(t *testing.T, rows []map[string]any, name string) map[string]any {
	t.Helper()
	for _, row := range rows {
		if row["name"] == name {
			return row
		}
	}
	t.Fatalf("no collection named %q in %v", name, rows)
	return nil
}

func equalIDs(a, b []string) bool {
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

// TestCollections_ABSServesEachViewerTheirOwnMembers: alice creates a static and
// a per-user dynamic collection natively. On the ABS surface the dynamic one shows
// alice HER finished book and bob HIS, never the creator's to bob, while the
// static one is the same for both. This is the owner decision of 2026-10-25.
func TestCollections_ABSServesEachViewerTheirOwnMembers(t *testing.T) {
	f := newViewerFixture(t)
	f.create("alice", map[string]any{"name": "Static Shelf", "type": "static", "book_ids": []string{f.seed.singleID}})
	dyn := f.create("alice", map[string]any{"name": "Finished", "type": "dynamic", "query": "read_status:finished"})
	if dyn.CreatedByUserID != "alice" {
		t.Fatalf("creator = %q, want alice", dyn.CreatedByUserID)
	}
	for _, stored := range f.store.cols {
		if stored.Type == database.CollectionTypeDynamic && len(stored.MaterializedBookIDs) != 0 {
			t.Fatalf("the stored row carries %v; a per-user collection must store no member list", stored.MaterializedBookIDs)
		}
	}

	// A stale stored list (what the creator-evaluated behaviour left on a row) and
	// a stale book_ids must both be ignored by the ABS read.
	for i := range f.store.cols {
		if f.store.cols[i].Type == database.CollectionTypeDynamic {
			f.store.cols[i].MaterializedBookIDs = []string{f.seed.singleID}
			f.store.cols[i].BookIDs = []string{f.seed.singleID}
		}
	}

	h := newHarness(t, "jwt", nil, withLibrary(f.seed), withUserData(fixtureUserData()),
		withCollections(f.store), withViewerMembers(f.native))
	h.seedUser(t, "alice", "alice", "", "pw-pw-pw-pw")
	h.seedUser(t, "bob", "bob", "", "pw-pw-pw-pw")
	h.seedUser(t, "carol", "carol", "", "pw-pw-pw-pw")
	single := absplSyncIDFor(t, f.seed, f.seed.singleID)
	multi := absplSyncIDFor(t, f.seed, f.seed.multiID)

	want := map[string][]string{"alice": {single}, "bob": {multi}, "carol": {}}
	for user, members := range want {
		tok := str(t, userObj(t, h.login(t, user, "pw-pw-pw-pw")), "accessToken")
		rows := abscolResults(t, h, tok)
		if f.store.gotType != "" {
			t.Fatalf("ABS list filtered on type %q; it must request both static and dynamic", f.store.gotType)
		}
		if len(rows) != 2 {
			t.Fatalf("%s: ABS list has %d rows, want 2 (static and dynamic)", user, len(rows))
		}
		finished := rowNamed(t, rows, "Finished")
		if got := bookIDsOf(finished); !equalIDs(got, members) {
			t.Errorf("%s: dynamic collection served %v, want %v (the viewer's own finished books)", user, got, members)
		}
		if n, _ := finished["numBooks"].(float64); int(n) != len(members) {
			t.Errorf("%s: numBooks = %v, want %d: the count is the viewer's", user, finished["numBooks"], len(members))
		}
		if got := bookIDsOf(rowNamed(t, rows, "Static Shelf")); !equalIDs(got, []string{single}) {
			t.Errorf("%s: static collection served %v, want [%s]", user, got, single)
		}

		// The detail route is the same answer.
		code, body := h.doAny(t, request{
			method: http.MethodGet, path: "/api/collections/" + dyn.ID, headers: bearer(tok),
		})
		if code != http.StatusOK {
			t.Fatalf("%s: GET collection = %d", user, code)
		}
		detail, _ := body.(map[string]any)
		if got := bookIDsOf(detail); !equalIDs(got, members) {
			t.Errorf("%s: collection detail served %v, want %v", user, got, members)
		}
	}
}

// With no per-viewer evaluation injected, or a stored list present, a collection
// with a per-user rule shows NO books: it fails closed, never to the stored (the
// creator's) list. A dynamic collection with no per-user rule still serves its
// stored list.
func TestCollections_ABSFailsClosedForPerUserCollections(t *testing.T) {
	f := newViewerFixture(t)
	f.create("alice", map[string]any{"name": "Finished", "type": "dynamic", "query": "read_status:finished"})
	f.create("alice", map[string]any{"name": "Plain", "type": "dynamic", "query": "title:single"})
	for i := range f.store.cols {
		f.store.cols[i].MaterializedBookIDs = []string{f.seed.singleID}
	}

	// NO withViewerMembers: nothing can evaluate for the viewer.
	h := newHarness(t, "jwt", nil, withLibrary(f.seed), withUserData(fixtureUserData()), withCollections(f.store))
	h.seedUser(t, "bob", "bob", "", "pw-pw-pw-pw")
	tok := str(t, userObj(t, h.login(t, "bob", "pw-pw-pw-pw")), "accessToken")
	rows := abscolResults(t, h, tok)

	if got := bookIDsOf(rowNamed(t, rows, "Finished")); len(got) != 0 {
		t.Errorf("per-user collection with no evaluator served %v; it must serve nothing, never the stored creator list", got)
	}
	if got := bookIDsOf(rowNamed(t, rows, "Plain")); !equalIDs(got, []string{absplSyncIDFor(t, f.seed, f.seed.singleID)}) {
		t.Errorf("a collection without a per-user rule must keep serving its stored list, got %v", got)
	}
}
