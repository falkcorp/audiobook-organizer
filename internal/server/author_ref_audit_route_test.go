// file: internal/server/author_ref_audit_route_test.go
// version: 1.0.0
// guid: 4e1d8c37-52b6-4a09-8d74-1f9b0a63c5e2
// last-edited: 2026-08-29

package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/gin-gonic/gin"
)

// TestAuthorRefAuditRouteIsWired asserts the ref-audit endpoint is REACHABLE at
// its real URL through the real router.
//
// The handler-level tests in internal/server/handlers/entities call
// AuditAuthorRefs directly, so every one of them stays green if the route
// registration in wire_entities_routes.go is deleted — the endpoint would be a
// 404 in production with a fully green suite. This test is the one that fails.
//
// It also covers a second failure mode registration can hit: /authors/ref-audit
// is a STATIC segment sharing a level with the /authors/:id wildcard routes, and
// a router that cannot reconcile the two would panic during NewServer or route
// the request to the :id handler instead.
func TestAuthorRefAuditRouteIsWired(t *testing.T) {
	gin.SetMode(gin.TestMode)

	store, err := database.NewPebbleStoreInMemory(filepath.Join(t.TempDir(), "pebble"))
	if err != nil {
		t.Fatalf("open pebble: %v", err)
	}
	origStore := database.GetGlobalStore()
	database.SetGlobalStore(store)
	t.Cleanup(func() {
		database.SetGlobalStore(origStore)
		store.Close()
	})

	live, err := store.CreateAuthor("Wired Live Author")
	if err != nil {
		t.Fatalf("create live author: %v", err)
	}
	gone, err := store.CreateAuthor("Wired Deleted Author")
	if err != nil {
		t.Fatalf("create doomed author: %v", err)
	}
	if err := store.DeleteAuthor(gone.ID); err != nil {
		t.Fatalf("delete author: %v", err)
	}

	srv := NewServer(store)

	url := "/api/v1/authors/ref-audit?ids=" +
		strconv.Itoa(live.ID) + "," + strconv.Itoa(gone.ID)
	req := httptest.NewRequest(http.MethodGet, url, nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code == http.StatusNotFound {
		t.Fatalf("GET %s is a 404 — the route is not registered", url)
	}
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Assert on the payload too, so routing the path to some OTHER authors
	// handler (the /authors/:id family shares this level) cannot pass.
	var resp struct {
		Data struct {
			Live []struct {
				ID   int    `json:"id"`
				Name string `json:"name"`
			} `json:"live"`
			Dangling []int          `json:"dangling"`
			Counts   map[string]int `json:"counts"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v (body %s)", err, w.Body.String())
	}
	if len(resp.Data.Live) != 1 || resp.Data.Live[0].ID != live.ID {
		t.Fatalf("expected the live author in the live bucket, got %+v", resp.Data.Live)
	}
	if len(resp.Data.Dangling) != 1 || resp.Data.Dangling[0] != gone.ID {
		t.Fatalf("expected the deleted author in the dangling bucket, got %+v", resp.Data.Dangling)
	}
	if resp.Data.Counts["live"] != 1 || resp.Data.Counts["dangling"] != 1 {
		t.Fatalf("unexpected counts: %+v", resp.Data.Counts)
	}
}
