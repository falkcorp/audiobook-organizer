// file: internal/server/removed_rename_routes_test.go
// version: 1.0.0
// guid: 7c1e9a42-5d3b-4f86-8a0d-2b6e4c9f1a73
// last-edited: 2026-10-10

package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// removedRenameRoutes are the per-book rename endpoints deleted with their
// handlers and the organizer's RenameService (owner decision D13): organize
// and write-back already cover renames. They are deleted, not stubbed, so the
// expected status is 404, not 410.
var removedRenameRoutes = []struct{ method, path string }{
	{http.MethodPost, "/api/v1/audiobooks/book-1/rename/preview"},
	{http.MethodPost, "/api/v1/audiobooks/book-1/rename/apply"},
}

// TestRemovedRenameRoutes_Are404 asserts neither rename route is registered
// and that a request to each gets a 404.
func TestRemovedRenameRoutes_Are404(t *testing.T) {
	srv, cleanup := setupTestServer(t)
	defer cleanup()

	registered := map[string]bool{}
	for _, r := range srv.router.Routes() {
		registered[r.Method+" "+r.Path] = true
	}
	for _, key := range []string{
		"POST /api/v1/audiobooks/:id/rename/preview",
		"POST /api/v1/audiobooks/:id/rename/apply",
	} {
		assert.False(t, registered[key], "%s is still registered", key)
	}

	for _, rt := range removedRenameRoutes {
		t.Run(rt.method+" "+rt.path, func(t *testing.T) {
			w := httptest.NewRecorder()
			req := httptest.NewRequest(rt.method, rt.path, strings.NewReader(`{}`))
			req.Header.Set("Content-Type", "application/json")
			srv.router.ServeHTTP(w, req)
			assert.Equal(t, http.StatusNotFound, w.Code, w.Body.String())
		})
	}
}

// The organize routes that sat next to the rename ones stay registered.
func TestRemovedRenameRoutes_OrganizeRoutesStay(t *testing.T) {
	srv, cleanup := setupTestServer(t)
	defer cleanup()

	registered := map[string]bool{}
	for _, r := range srv.router.Routes() {
		registered[r.Method+" "+r.Path] = true
	}
	for _, key := range []string{
		"GET /api/v1/audiobooks/:id/preview-organize",
		"POST /api/v1/audiobooks/:id/organize",
	} {
		assert.True(t, registered[key], "%s must stay registered", key)
	}
}
