// file: internal/server/wire_entities_gone_test.go
// version: 1.1.0
// guid: 6d2a9c41-7b58-4e03-91f6-a4c8e2b05d37
// last-edited: 2026-10-18

package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	entitiesGoneBody  = `{"error":"gone","message":"this endpoint has been removed and has no replacement"}`
	narratorsGoneBody = `{"error":"gone","message":"this endpoint has been removed; use GET/PUT /api/v1/audiobooks/:id (narrator field) instead"}`
)

// retiredEntityRoutes are the narrator-link and author/series tag routes
// retired by 01-P81e with the exact gone() body each one answers. The narrator
// routes name the book record as their replacement; the tag routes have none.
var retiredEntityRoutes = []struct {
	method, pattern, path, body string
}{
	{http.MethodGet, "/api/v1/audiobooks/:id/narrators", "/api/v1/audiobooks/synthetic-book/narrators", narratorsGoneBody},
	{http.MethodPut, "/api/v1/audiobooks/:id/narrators", "/api/v1/audiobooks/synthetic-book/narrators", narratorsGoneBody},
	{http.MethodGet, "/api/v1/authors/:id/tags", "/api/v1/authors/1/tags", entitiesGoneBody},
	{http.MethodPost, "/api/v1/authors/:id/tags", "/api/v1/authors/1/tags", entitiesGoneBody},
	{http.MethodGet, "/api/v1/series/:id/tags", "/api/v1/series/1/tags", entitiesGoneBody},
	{http.MethodPost, "/api/v1/series/:id/tags", "/api/v1/series/1/tags", entitiesGoneBody},
}

// TestRetiredEntityRoutes_Answer410 drives each retired path through the real
// router as an admin: 410 with the shared gone() body.
func TestRetiredEntityRoutes_Answer410(t *testing.T) {
	f := setupCredGuardServer(t)
	for _, r := range retiredEntityRoutes {
		t.Run(r.method+" "+r.pattern, func(t *testing.T) {
			w := f.do(t, f.sessionToken, r.method, r.path, nil)
			assert.Equal(t, http.StatusGone, w.Code, w.Body.String())
			assert.Equal(t, r.body, w.Body.String())
		})
	}
}

// TestRetiredEntityRoutes_AuthRunsFirst: the stubs sit on the protected group,
// so a caller with no credentials gets 401 and never learns the route is gone.
func TestRetiredEntityRoutes_AuthRunsFirst(t *testing.T) {
	f := setupCredGuardServer(t)
	for _, r := range retiredEntityRoutes {
		t.Run(r.method+" "+r.pattern, func(t *testing.T) {
			req := httptest.NewRequest(r.method, r.path, nil)
			w := httptest.NewRecorder()
			f.srv.router.ServeHTTP(w, req)
			assert.Equal(t, http.StatusUnauthorized, w.Code, w.Body.String())
			assert.NotContains(t, w.Body.String(), `"error":"gone"`)
		})
	}
}

// TestRetiredEntityRoutes_PermissionRunsBeforeGone: a viewer session is valid
// but lacks library.edit_metadata, so each write stub answers 403 from s.perm
// and never reaches gone(). The positive control shows the same viewer gets
// 410 on a GET stub, so the 403s are the permission guard and not a rejected
// session.
func TestRetiredEntityRoutes_PermissionRunsBeforeGone(t *testing.T) {
	f := setupCredGuardServer(t)

	viewer, err := f.store.CreateUser("synthetic-viewer", "viewer@example.test", "bcrypt", "x", []string{auth.SeedRoleViewer}, "active")
	require.NoError(t, err)
	viewerSess, err := f.store.CreateSessionWithOrigin(viewer.ID, "127.0.0.1", "test", time.Hour, database.SessionOriginPassword)
	require.NoError(t, err)

	for _, r := range retiredEntityRoutes {
		if r.method == http.MethodGet {
			continue
		}
		t.Run("viewer gets 403 on "+r.method+" "+r.pattern, func(t *testing.T) {
			w := f.do(t, viewerSess.ID, r.method, r.path, nil)
			assert.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
			assert.NotContains(t, w.Body.String(), `"error":"gone"`)
		})
	}

	t.Run("viewer reaches a GET stub", func(t *testing.T) {
		w := f.do(t, viewerSess.ID, http.MethodGet, "/api/v1/authors/1/tags", nil)
		assert.Equal(t, http.StatusGone, w.Code, w.Body.String())
	})
}

// TestRetiredEntityRoutes_KeptRoutesNotGone: the owner-kept diagnostics
// (GET /narrators/count, GET /work/stats) and the live neighbours are still
// registered and do not answer 410.
func TestRetiredEntityRoutes_KeptRoutesNotGone(t *testing.T) {
	f := setupCredGuardServer(t)
	kept := []struct{ method, pattern, path string }{
		{http.MethodGet, "/api/v1/narrators/count", "/api/v1/narrators/count"},
		{http.MethodGet, "/api/v1/work/stats", "/api/v1/work/stats"},
		{http.MethodGet, "/api/v1/authors/:id/aliases", "/api/v1/authors/1/aliases"},
		{http.MethodGet, "/api/v1/series/:id/books", "/api/v1/series/1/books"},
	}
	registered := map[string]bool{}
	for _, r := range f.srv.router.Routes() {
		registered[r.Method+" "+r.Path] = true
	}
	for _, k := range kept {
		t.Run(k.method+" "+k.pattern, func(t *testing.T) {
			require.True(t, registered[k.method+" "+k.pattern], "route not registered")
			w := f.do(t, f.sessionToken, k.method, k.path, nil)
			assert.NotEqual(t, http.StatusGone, w.Code, w.Body.String())
			assert.NotContains(t, w.Body.String(), `"error":"gone"`)
		})
	}
}
