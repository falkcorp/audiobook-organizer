// file: internal/server/removed_itunes_routes_test.go
// version: 1.1.0
// guid: 3f8c2a6e-91d4-4b57-a0e3-7c5d1b9f4e28
// last-edited: 2026-10-08

package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// removedITunesRoutes are the endpoints that wrote the iTunes library (or
// queued, previewed or reported on such writes), deleted when iTunes became
// an import-only source on 2026-10-07, plus the incremental sync, deleted on
// 2026-10-08 when import became the only iTunes action.
var removedITunesRoutes = []struct{ method, path string }{
	{http.MethodPost, "/api/v1/itunes/rebuild"},
	{http.MethodPost, "/api/v1/itunes/rebuild-full"},
	{http.MethodPost, "/api/v1/itunes/export-partial"},
	{http.MethodPost, "/api/v1/itunes/relocate"},
	{http.MethodPost, "/api/v1/itunes/adopt-base"},
	{http.MethodPost, "/api/v1/itunes/cleanup-merged"},
	{http.MethodPost, "/api/v1/itunes/write-back"},
	{http.MethodPost, "/api/v1/itunes/write-back-all"},
	{http.MethodPost, "/api/v1/itunes/write-back/preview"},
	{http.MethodGet, "/api/v1/itunes/writeback/status"},
	{http.MethodPost, "/api/v1/itunes/writeback/held/release"},
	{http.MethodPost, "/api/v1/itunes/writeback/requeue"},
	{http.MethodPost, "/api/v1/itunes/writeback/requeue-remove"},
	{http.MethodPost, "/api/v1/itunes/library/upload"},
	{http.MethodPost, "/api/v1/itunes/library/restore"},
	{http.MethodGet, "/api/v1/itunes/library/backups"},
	{http.MethodPost, "/api/v1/operations/itunes-path-reconcile"},
	{http.MethodPost, "/api/v1/operations/itunes-path-repair"},
	// The incremental sync, removed 2026-10-08: import is the one iTunes
	// action and re-running it links rather than duplicates.
	{http.MethodPost, "/api/v1/itunes/sync"},
}

// TestRemovedITunesWriteRoutes_Are404 asserts each removed route is gone from
// the router: no registered route matches it (so no handler, guard or SPA
// page can answer), and a request gets a 404.
func TestRemovedITunesWriteRoutes_Are404(t *testing.T) {
	srv, cleanup := setupTestServer(t)
	defer cleanup()

	registered := map[string]bool{}
	for _, r := range srv.router.Routes() {
		registered[r.Method+" "+r.Path] = true
	}

	for _, rt := range removedITunesRoutes {
		t.Run(rt.method+" "+rt.path, func(t *testing.T) {
			assert.False(t, registered[rt.method+" "+rt.path], "route is still registered")

			w := httptest.NewRecorder()
			req := httptest.NewRequest(rt.method, rt.path, strings.NewReader(`{}`))
			req.Header.Set("Content-Type", "application/json")
			srv.router.ServeHTTP(w, req)
			assert.Equal(t, http.StatusNotFound, w.Code, w.Body.String())
		})
	}
}

// The import side stays: a removed write route must not have taken a read or
// import route with it.
func TestRemovedITunesWriteRoutes_ImportRoutesStay(t *testing.T) {
	srv, cleanup := setupTestServer(t)
	defer cleanup()

	registered := map[string]bool{}
	for _, r := range srv.router.Routes() {
		registered[r.Method+" "+r.Path] = true
	}
	for _, key := range []string{
		"POST /api/v1/itunes/validate",
		"POST /api/v1/itunes/import",
		"GET /api/v1/itunes/library/download",
		"GET /api/v1/itunes/pid-integrity",
		"POST /api/v1/itunes/pid-repair",
	} {
		assert.True(t, registered[key], "%s must stay registered", key)
	}
}
