// file: internal/server/wire_deluge_gone_test.go
// version: 1.0.0
// guid: 8d2f6b41-3a9c-4e75-b0d8-5c1e7a49f263
// last-edited: 2026-10-18

package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
)

const delugeDiscoverImportGoneBody = `{"error":"gone","message":"this endpoint has been removed; use POST /api/v1/import/file (imports one file by path, without a torrent link) instead"}`

const delugeDiscoverImportPath = "/api/v1/deluge/discover/import"

// TestRetiredDelugeDiscoverImport_Answer410 drives POST /deluge/discover/import
// through the real router as an admin: 410 with the shared gone() body whatever
// the request body, so a client that still posts a torrent selection is told
// where to go instead of getting a half-working import.
func TestRetiredDelugeDiscoverImport_Answer410(t *testing.T) {
	f := setupCredGuardServer(t)

	for name, body := range map[string]any{
		"no body": nil,
		"torrent selection": map[string]any{
			"torrent_hash": "0123456789abcdef0123456789abcdef01234567",
			"file_path":    "/nonexistent/synthetic/book.m4b",
		},
	} {
		t.Run(name, func(t *testing.T) {
			w := f.do(t, f.sessionToken, http.MethodPost, delugeDiscoverImportPath, body)
			assert.Equal(t, http.StatusGone, w.Code, w.Body.String())
			assert.Equal(t, delugeDiscoverImportGoneBody, w.Body.String())
		})
	}

	books, err := f.store.GetAllBooksCore(100, 0)
	require.NoError(t, err)
	assert.Empty(t, books, "the stub must not import anything")
}

// TestRetiredDelugeDiscoverImport_AuthRunsFirst: no credentials, no 410.
func TestRetiredDelugeDiscoverImport_AuthRunsFirst(t *testing.T) {
	f := setupCredGuardServer(t)
	w := httptest.NewRecorder()
	f.srv.router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, delugeDiscoverImportPath, nil))
	assert.Equal(t, http.StatusUnauthorized, w.Code, w.Body.String())
	assert.NotContains(t, w.Body.String(), `"error":"gone"`)
}

// TestRetiredDelugeDiscoverImport_PermissionRunsBeforeGone: the stub keeps
// integrations.manage. A viewer session and a custom role holding exactly
// library.delete are refused with 403 and never learn the route is gone; a
// custom role holding exactly integrations.manage reaches the 410.
func TestRetiredDelugeDiscoverImport_PermissionRunsBeforeGone(t *testing.T) {
	f := setupCredGuardServer(t)
	_, viewerTok := f.viewerGoneSession(t)
	onlyManage := f.exactPermGoneSession(t, "only-integrations", string(auth.PermIntegrationsManage))
	onlyDelete := f.exactPermGoneSession(t, "only-delete", auth.PermLibraryDelete)

	for name, tok := range map[string]string{"viewer": viewerTok, "library.delete only": onlyDelete} {
		t.Run(name, func(t *testing.T) {
			w := f.do(t, tok, http.MethodPost, delugeDiscoverImportPath, nil)
			assert.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
			assert.NotContains(t, w.Body.String(), `"error":"gone"`)
		})
	}

	w := f.do(t, onlyManage, http.MethodPost, delugeDiscoverImportPath, nil)
	assert.Equal(t, http.StatusGone, w.Code, w.Body.String())
	assert.Equal(t, delugeDiscoverImportGoneBody, w.Body.String())
}

// TestRetiredDelugeDiscoverImport_KeptNeighboursNotGone: the owner-kept Deluge
// read routes (D73) are still registered and run their handlers; neither
// answers 410. Deluge is not configured in the fixture, so the handlers answer
// their own not-configured error, which is the proof that they ran.
func TestRetiredDelugeDiscoverImport_KeptNeighboursNotGone(t *testing.T) {
	f := setupCredGuardServer(t)
	registered := map[string]bool{}
	for _, r := range f.srv.router.Routes() {
		registered[r.Method+" "+r.Path] = true
	}
	for _, k := range []struct{ method, path, handlerSays string }{
		{http.MethodGet, "/api/v1/deluge/labels", "deluge not configured"},
		{http.MethodGet, "/api/v1/deluge/discover", "deluge discovery is disabled"},
	} {
		t.Run(k.method+" "+k.path, func(t *testing.T) {
			require.True(t, registered[k.method+" "+k.path], "route not registered")
			w := f.do(t, f.sessionToken, k.method, k.path, nil)
			assert.NotEqual(t, http.StatusGone, w.Code, w.Body.String())
			assert.NotContains(t, w.Body.String(), `"error":"gone"`)
			assert.Contains(t, w.Body.String(), k.handlerSays, "the handler itself must have answered")
		})
	}
}
