// file: internal/server/wire_audiobooks_gone_test.go
// version: 1.1.1
// guid: 3b6e1f08-92d4-4c75-a8e3-5d71c0f2b94a
// last-edited: 2026-10-30

package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// retiredAudiobookRoutes are the per-book routes retired by 01-P81d, with the
// exact gone() body each one answers.
var retiredAudiobookRoutes = []struct {
	method, pattern, path, body string
	// guarded marks stubs behind library.edit_metadata or library.delete, which
	// a read-only viewer must be refused (403) before the 410.
	guarded bool
}{
	{http.MethodGet, "/api/v1/audiobooks/:id/alternative-titles", "/api/v1/audiobooks/synthetic-book/alternative-titles",
		`{"error":"gone","message":"this endpoint has been removed and has no replacement"}`, false},
	{http.MethodPost, "/api/v1/audiobooks/:id/alternative-titles", "/api/v1/audiobooks/synthetic-book/alternative-titles",
		`{"error":"gone","message":"this endpoint has been removed and has no replacement"}`, true},
	{http.MethodDelete, "/api/v1/audiobooks/:id/alternative-titles", "/api/v1/audiobooks/synthetic-book/alternative-titles",
		`{"error":"gone","message":"this endpoint has been removed and has no replacement"}`, true},
	{http.MethodGet, "/api/v1/audiobooks/:id/path-history", "/api/v1/audiobooks/synthetic-book/path-history",
		`{"error":"gone","message":"this endpoint has been removed; use GET /api/v1/audiobooks/:id/changelog instead"}`, false},
	{http.MethodPost, "/api/v1/audiobooks/:id/rescan", "/api/v1/audiobooks/synthetic-book/rescan",
		`{"error":"gone","message":"this endpoint has been removed; use POST /api/v1/audiobooks/:id/reconcile-files instead"}`, true},
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

// TestRetiredAudiobookRoutes_PermissionGuardRunsFirst: the stubs keep each
// route's old permission guard. A read-only viewer is refused with 403 on the
// edit- and delete-guarded stubs and never learns the route is gone; the
// viewer reaching 410 on the view-guarded GETs is the positive control that
// the viewer session itself is valid.
func TestRetiredAudiobookRoutes_PermissionGuardRunsFirst(t *testing.T) {
	f := setupCredGuardServer(t)

	viewer, err := f.store.CreateUser("synthetic-viewer", "viewer@example.test", "bcrypt", "x", []string{auth.SeedRoleViewer}, "active")
	require.NoError(t, err)
	viewerSess, err := f.store.CreateSessionWithOrigin(viewer.ID, "127.0.0.1", "test", time.Hour, database.SessionOriginPassword, 0)
	require.NoError(t, err)

	for _, r := range retiredAudiobookRoutes {
		t.Run(r.method+" "+r.pattern, func(t *testing.T) {
			w := f.do(t, viewerSess.ID, r.method, r.path, nil)
			if r.guarded {
				assert.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
				assert.NotContains(t, w.Body.String(), `"error":"gone"`)
				return
			}
			assert.Equal(t, http.StatusGone, w.Code, w.Body.String())
		})
	}
}

// TestRetiredAudiobookRoutes_ReconcileFilesKept: the live twin of the retired
// /rescan alias is still registered and really runs: against a seeded
// synthetic book it answers 200 with the handler's own body, not the stub's.
func TestRetiredAudiobookRoutes_ReconcileFilesKept(t *testing.T) {
	f := setupCredGuardServer(t)
	registered := false
	for _, r := range f.srv.router.Routes() {
		if r.Method == http.MethodPost && r.Path == "/api/v1/audiobooks/:id/reconcile-files" {
			registered = true
		}
	}
	require.True(t, registered, "reconcile-files route not registered")

	book, err := f.store.CreateBook(&database.Book{Title: "Synthetic Reconcile Book", FilePath: "/nonexistent/synthetic/book.m4b"})
	require.NoError(t, err)

	w := f.do(t, f.sessionToken, http.MethodPost, "/api/v1/audiobooks/"+book.ID+"/reconcile-files", nil)
	assert.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.NotContains(t, w.Body.String(), `"error":"gone"`)
}
