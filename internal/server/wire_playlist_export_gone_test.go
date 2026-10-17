// file: internal/server/wire_playlist_export_gone_test.go
// version: 1.0.0
// guid: 7d2f9c41-5b8e-4a63-9e17-c40a1b6d83f5
// last-edited: 2026-10-17

package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const retiredPlaylistExportBody = `{"error":"gone","message":"this endpoint has been removed and has no replacement"}`

// TestRetiredPlaylistExportRoute_Answers410 drives the retired M3U export path
// through the real router as an admin: 410 with the shared gone() body.
func TestRetiredPlaylistExportRoute_Answers410(t *testing.T) {
	f := setupCredGuardServer(t)
	w := f.do(t, f.sessionToken, http.MethodGet, "/api/v1/playlists/synthetic-pl/export.m3u", nil)
	assert.Equal(t, http.StatusGone, w.Code, w.Body.String())
	assert.Equal(t, retiredPlaylistExportBody, w.Body.String())
}

// TestRetiredPlaylistExportRoute_AuthRunsFirst: the stub sits on the protected
// group, so a caller with no credentials gets 401 and never learns the route
// is gone.
func TestRetiredPlaylistExportRoute_AuthRunsFirst(t *testing.T) {
	f := setupCredGuardServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/playlists/synthetic-pl/export.m3u", nil)
	w := httptest.NewRecorder()
	f.srv.router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusUnauthorized, w.Code, w.Body.String())
	assert.NotContains(t, w.Body.String(), `"error":"gone"`)
}

// TestRetiredPlaylistExportRoute_CollectionRoutesKept: the four collection
// routes the owner keeps (D74) are still registered and none answers 410.
func TestRetiredPlaylistExportRoute_CollectionRoutesKept(t *testing.T) {
	f := setupCredGuardServer(t)

	registered := map[string]bool{}
	for _, r := range f.srv.router.Routes() {
		registered[r.Method+" "+r.Path] = true
	}
	kept := []struct{ method, pattern, path string }{
		{http.MethodGet, "/api/v1/collections/:id", "/api/v1/collections/synthetic-col"},
		{http.MethodPut, "/api/v1/collections/:id", "/api/v1/collections/synthetic-col"},
		{http.MethodDelete, "/api/v1/collections/:id", "/api/v1/collections/synthetic-col"},
		{http.MethodPost, "/api/v1/collections/:id/materialize", "/api/v1/collections/synthetic-col/materialize"},
	}
	for _, k := range kept {
		t.Run(k.method+" "+k.pattern, func(t *testing.T) {
			require.True(t, registered[k.method+" "+k.pattern], "route not registered")
			w := f.do(t, f.sessionToken, k.method, k.path, map[string]any{"name": "synthetic"})
			assert.NotEqual(t, http.StatusGone, w.Code, w.Body.String())
			assert.NotContains(t, w.Body.String(), `"error":"gone"`)
		})
	}
}
