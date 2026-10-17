// file: internal/server/wire_dedup_routes_gone_test.go
// version: 1.0.0
// guid: c6e41aa4-3cd4-4771-b556-56d39cb542d4
// last-edited: 2026-10-17

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

// retiredBookDuplicatesRoutes are the five /audiobooks/duplicates routes and
// two DEPRECATED aliases that only the old /dedup page's Version Groups and
// Duplicate Scan tabs called (03-PR12, owner decision D9). editOnly marks the
// routes guarded by library.edit_metadata rather than library.view.
var retiredBookDuplicatesRoutes = []struct {
	method, path string
	editOnly     bool
}{
	{http.MethodGet, "/api/v1/audiobooks/duplicates", false},
	{http.MethodGet, "/api/v1/audiobooks/duplicates/scan-results", false},
	{http.MethodPost, "/api/v1/audiobooks/duplicates/scan", true},
	{http.MethodPost, "/api/v1/audiobooks/duplicates/link", true},
	{http.MethodPost, "/api/v1/audiobooks/duplicates/merge", true},
	{http.MethodPost, "/api/v1/audiobooks/duplicates/reject", true},
	{http.MethodPost, "/api/v1/audiobooks/duplicates/dismiss", true},
}

const retiredBookDuplicatesBody = `{"error":"gone","message":"this endpoint has been removed; use GET /api/v1/dedup/candidates (the Review page, Duplicates lane) instead"}`

// TestRetiredBookDuplicatesRoutes_Answer410 drives each retired path through
// the real router as an admin: every one answers 410 with the shared gone()
// body naming the replacement, byte for byte.
func TestRetiredBookDuplicatesRoutes_Answer410(t *testing.T) {
	f := setupCredGuardServer(t)
	for _, r := range retiredBookDuplicatesRoutes {
		t.Run(r.method+" "+r.path, func(t *testing.T) {
			w := f.do(t, f.sessionToken, r.method, r.path, map[string]any{"book_ids": []string{"b1", "b2"}, "group_key": "b1+b2"})
			assert.Equal(t, http.StatusGone, w.Code, w.Body.String())
			assert.Equal(t, retiredBookDuplicatesBody, w.Body.String())
		})
	}
}

// TestRetiredBookDuplicatesRoutes_AuthRunsFirst: the stubs keep each route's
// old permission guard, so a caller with no credentials gets 401 and a
// read-only viewer gets 403 on the edit-guarded routes; neither learns the
// route is gone. The viewer reaching 410 on the view-guarded routes is the
// positive control that the viewer session itself is valid.
func TestRetiredBookDuplicatesRoutes_AuthRunsFirst(t *testing.T) {
	f := setupCredGuardServer(t)

	viewer, err := f.store.CreateUser("synthetic-viewer", "viewer@example.test", "bcrypt", "x", []string{auth.SeedRoleViewer}, "active")
	require.NoError(t, err)
	viewerSess, err := f.store.CreateSessionWithOrigin(viewer.ID, "127.0.0.1", "test", time.Hour, database.SessionOriginPassword)
	require.NoError(t, err)

	for _, r := range retiredBookDuplicatesRoutes {
		t.Run("unauthenticated "+r.method+" "+r.path, func(t *testing.T) {
			req := httptest.NewRequest(r.method, r.path, nil)
			w := httptest.NewRecorder()
			f.srv.router.ServeHTTP(w, req)
			assert.Equal(t, http.StatusUnauthorized, w.Code, w.Body.String())
			assert.NotContains(t, w.Body.String(), `"error":"gone"`)
		})
		t.Run("viewer "+r.method+" "+r.path, func(t *testing.T) {
			w := f.do(t, viewerSess.ID, r.method, r.path, nil)
			if r.editOnly {
				assert.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
				assert.NotContains(t, w.Body.String(), `"error":"gone"`)
				return
			}
			assert.Equal(t, http.StatusGone, w.Code, w.Body.String())
		})
	}
}

// TestRetiredBookDuplicatesRoutes_KeptSiblingsStillRoute: the routes beside
// the retired ones that something still calls (the Review page's Duplicates
// and Authors lanes, the Library page's version linking) still reach their
// handlers, so retiring /audiobooks/duplicates cannot take them with it.
func TestRetiredBookDuplicatesRoutes_KeptSiblingsStillRoute(t *testing.T) {
	f := setupCredGuardServer(t)

	for _, path := range []string{"/api/v1/dedup/candidates", "/api/v1/authors/duplicates"} {
		t.Run("GET "+path, func(t *testing.T) {
			w := f.do(t, f.sessionToken, http.MethodGet, path, nil)
			assert.Equal(t, http.StatusOK, w.Code, w.Body.String())
		})
	}

	// POST /audiobooks/link with a keep id that does not exist: its handler
	// answers 404 naming the book, which proves the route reached LinkBooks
	// (an unrouted path's 404 carries no such body) and writes nothing.
	t.Run("POST /api/v1/audiobooks/link", func(t *testing.T) {
		w := f.do(t, f.sessionToken, http.MethodPost, "/api/v1/audiobooks/link",
			map[string]any{"keep_id": "synthetic-missing-book", "merge_ids": []string{"synthetic-other-book"}})
		assert.Equal(t, http.StatusNotFound, w.Code, w.Body.String())
		assert.Contains(t, w.Body.String(), "synthetic-missing-book")
		assert.NotContains(t, w.Body.String(), `"error":"gone"`)
	})
}
