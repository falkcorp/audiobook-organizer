// file: internal/server/wire_audiobooks_gone_test.go
// version: 1.0.0
// guid: 3b6e1f08-92d4-4c75-a8e3-5d71c0f2b94a
// last-edited: 2026-10-18

package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// retiredAudiobookRoutes are the per-book routes retired by 01-P81d, with the
// exact gone() body each one answers.
var retiredAudiobookRoutes = []struct {
	method, pattern, path, body string
}{
	{http.MethodGet, "/api/v1/audiobooks/:id/alternative-titles", "/api/v1/audiobooks/synthetic-book/alternative-titles",
		`{"error":"gone","message":"this endpoint has been removed and has no replacement"}`},
	{http.MethodPost, "/api/v1/audiobooks/:id/alternative-titles", "/api/v1/audiobooks/synthetic-book/alternative-titles",
		`{"error":"gone","message":"this endpoint has been removed and has no replacement"}`},
	{http.MethodDelete, "/api/v1/audiobooks/:id/alternative-titles", "/api/v1/audiobooks/synthetic-book/alternative-titles",
		`{"error":"gone","message":"this endpoint has been removed and has no replacement"}`},
	{http.MethodGet, "/api/v1/audiobooks/:id/path-history", "/api/v1/audiobooks/synthetic-book/path-history",
		`{"error":"gone","message":"this endpoint has been removed; use GET /api/v1/audiobooks/:id/changelog instead"}`},
	{http.MethodPost, "/api/v1/audiobooks/:id/rescan", "/api/v1/audiobooks/synthetic-book/rescan",
		`{"error":"gone","message":"this endpoint has been removed; use POST /api/v1/audiobooks/:id/reconcile-files instead"}`},
}

// TestRetiredAudiobookRoutes_Answer410 drives each retired path through the
// real router as an admin: 410 with the shared gone() body.
func TestRetiredAudiobookRoutes_Answer410(t *testing.T) {
	f := setupCredGuardServer(t)
	for _, r := range retiredAudiobookRoutes {
		t.Run(r.method+" "+r.pattern, func(t *testing.T) {
			w := f.do(t, f.sessionToken, r.method, r.path, nil)
			assert.Equal(t, http.StatusGone, w.Code, w.Body.String())
			assert.Equal(t, r.body, w.Body.String())
		})
	}
}

// TestRetiredAudiobookRoutes_AuthRunsFirst: the stubs sit on the protected
// group, so a caller with no credentials gets 401 and never learns the route
// is gone.
func TestRetiredAudiobookRoutes_AuthRunsFirst(t *testing.T) {
	f := setupCredGuardServer(t)
	for _, r := range retiredAudiobookRoutes {
		t.Run(r.method+" "+r.pattern, func(t *testing.T) {
			req := httptest.NewRequest(r.method, r.path, nil)
			w := httptest.NewRecorder()
			f.srv.router.ServeHTTP(w, req)
			assert.Equal(t, http.StatusUnauthorized, w.Code, w.Body.String())
			assert.NotContains(t, w.Body.String(), `"error":"gone"`)
		})
	}
}

// TestRetiredAudiobookRoutes_ReconcileFilesKept: the live twin of the retired
// /rescan alias is still registered and does not answer 410.
func TestRetiredAudiobookRoutes_ReconcileFilesKept(t *testing.T) {
	f := setupCredGuardServer(t)
	registered := false
	for _, r := range f.srv.router.Routes() {
		if r.Method == http.MethodPost && r.Path == "/api/v1/audiobooks/:id/reconcile-files" {
			registered = true
		}
	}
	require.True(t, registered, "reconcile-files route not registered")
	w := f.do(t, f.sessionToken, http.MethodPost, "/api/v1/audiobooks/synthetic-book/reconcile-files", nil)
	assert.NotEqual(t, http.StatusGone, w.Code, w.Body.String())
	assert.NotContains(t, w.Body.String(), `"error":"gone"`)
}
