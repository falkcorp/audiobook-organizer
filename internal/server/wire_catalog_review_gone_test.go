// file: internal/server/wire_catalog_review_gone_test.go
// version: 1.0.0
// guid: 2f249ef1-e42a-4424-9b7a-b8d1c8e263c2
// last-edited: 2026-10-18

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

const (
	catalogGoneBody = `{"error":"gone","message":"this endpoint has been removed and has no replacement"}`
	replayGoneBody  = `{"error":"gone","message":"this endpoint has been removed; use POST /api/v1/review/bulk with {\"action\":\"approve\",\"ids\":[...]} (list the approved items with GET /api/v1/review/items?status=approved) once review_apply_enabled is on instead"}`
)

// retiredCatalogReviewRoutes are the routes retired by 01-P81j with the exact
// gone() body each answers. guard is the permission the original route required.
// The other D73 rows of that brief (op-defs/:id, ai/capabilities,
// ai/endpoints/status, tools/:name/status) were kept by the owner and are in
// the kept-routes test below.
var retiredCatalogReviewRoutes = []struct {
	method, pattern, path, body string
	guard                       string
}{
	{http.MethodGet, "/api/v1/catalog/entries", "/api/v1/catalog/entries", catalogGoneBody, auth.PermLibraryView},
	{http.MethodGet, "/api/v1/catalog/entries/:id", "/api/v1/catalog/entries/synthetic-entry", catalogGoneBody, auth.PermLibraryView},
	{http.MethodPost, "/api/v1/review/replay-approved", "/api/v1/review/replay-approved", replayGoneBody, auth.PermLibraryEditMetadata},
}

// TestRetiredCatalogReviewRoutes_Answer410 drives each retired path through the
// real router as an admin: 410 with the shared gone() body.
func TestRetiredCatalogReviewRoutes_Answer410(t *testing.T) {
	f := setupCredGuardServer(t)
	for _, r := range retiredCatalogReviewRoutes {
		t.Run(r.method+" "+r.pattern, func(t *testing.T) {
			w := f.do(t, f.sessionToken, r.method, r.path, nil)
			assert.Equal(t, http.StatusGone, w.Code, w.Body.String())
			assert.Equal(t, r.body, w.Body.String())
		})
	}
}

// TestRetiredCatalogReviewRoutes_AuthRunsFirst: the stubs sit on the protected
// group, so a caller with no credentials gets 401 and never learns the route
// is gone.
func TestRetiredCatalogReviewRoutes_AuthRunsFirst(t *testing.T) {
	f := setupCredGuardServer(t)
	for _, r := range retiredCatalogReviewRoutes {
		t.Run(r.method+" "+r.pattern, func(t *testing.T) {
			req := httptest.NewRequest(r.method, r.path, nil)
			w := httptest.NewRecorder()
			f.srv.router.ServeHTTP(w, req)
			assert.Equal(t, http.StatusUnauthorized, w.Code, w.Body.String())
			assert.NotContains(t, w.Body.String(), `"error":"gone"`)
		})
	}
}

// TestRetiredCatalogReviewRoutes_PermissionRunsBeforeGone proves each stub's
// s.perm guard still runs first, with a session that is valid (so the 403 is
// not a rejected session) and holds a permission that is not the one under test.
//
//   - The edit-guarded stub (replay-approved): a SeedRoleViewer session holds
//     library.view but not library.edit_metadata, so it must get 403.
//   - The view-guarded stubs (catalog GETs): a custom role holding exactly one
//     permission that is neither library.view nor fingerprint.worker. A
//     fingerprint-worker session would not do: its 403 comes from the worker-only
//     middleware (abortWorkerOnlyOutsideWorkerAPI), not from s.perm, so it would
//     pass with the guard deleted.
//
// The positive control shows a viewer reaches the view-guarded GET stubs, so the
// 403s above are the permission guard and not a rejected session.
func TestRetiredCatalogReviewRoutes_PermissionRunsBeforeGone(t *testing.T) {
	f := setupCredGuardServer(t)

	session := func(name string, roles []string) string {
		u, err := f.store.CreateUser(name, name+"@example.test", "bcrypt", "x", roles, "active")
		require.NoError(t, err)
		s, err := f.store.CreateSessionWithOrigin(u.ID, "127.0.0.1", "test", time.Hour, database.SessionOriginPassword)
		require.NoError(t, err)
		return s.ID
	}
	viewer := session("synthetic-viewer", []string{auth.SeedRoleViewer})

	// One permission, deliberately neither library.view nor fingerprint.worker.
	role, err := f.store.CreateRole(&database.Role{
		Name:        "synthetic-playlists-only",
		Permissions: []string{auth.PermPlaylistsCreate},
	})
	require.NoError(t, err)
	playlistsOnly := session("synthetic-playlists-only", []string{role.ID})

	for _, r := range retiredCatalogReviewRoutes {
		bearer := viewer
		if r.guard == auth.PermLibraryView {
			bearer = playlistsOnly
		}
		t.Run("403 on "+r.method+" "+r.pattern, func(t *testing.T) {
			w := f.do(t, bearer, r.method, r.path, nil)
			assert.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
			assert.NotContains(t, w.Body.String(), `"error":"gone"`)
		})
	}

	for _, r := range retiredCatalogReviewRoutes {
		if r.guard != auth.PermLibraryView {
			continue
		}
		t.Run("viewer reaches "+r.method+" "+r.pattern, func(t *testing.T) {
			w := f.do(t, viewer, r.method, r.path, nil)
			assert.Equal(t, http.StatusGone, w.Code, w.Body.String())
			assert.Equal(t, r.body, w.Body.String())
		})
	}
}

// TestRetiredCatalogReviewRoutes_KeptRoutesNotGone: the owner-kept diagnostics
// (D73) and the live review neighbours are still registered and answer for real,
// not 410. The controls use synthetic seeded entities so a pass proves the route
// ran, not just that it was registered.
func TestRetiredCatalogReviewRoutes_KeptRoutesNotGone(t *testing.T) {
	f := setupCredGuardServer(t)
	held, err := f.store.UpsertReviewItem(database.ReviewItem{
		Kind: "regroup.multidisc", DedupKey: "synthetic-hold", FolderRef: "/f/synthetic-hold",
		Summary: "synthetic-hold-summary", Payload: `{}`,
	})
	require.NoError(t, err)
	require.NotNil(t, held)

	kept := []struct {
		method, pattern, path string
		wantCode              int
		wantBody              string
	}{
		{http.MethodGet, "/api/v1/op-defs/:id", "/api/v1/op-defs/catalog.coverage-report", http.StatusOK, "catalog.coverage-report"},
		{http.MethodGet, "/api/v1/ai/capabilities", "/api/v1/ai/capabilities", http.StatusOK, ""},
		{http.MethodGet, "/api/v1/ai/endpoints/status", "/api/v1/ai/endpoints/status", http.StatusOK, ""},
		{http.MethodGet, "/api/v1/tools/:name/status", "/api/v1/tools/synthetic-tool/status", http.StatusOK, ""},
		{http.MethodGet, "/api/v1/review/items", "/api/v1/review/items", http.StatusOK, "synthetic-hold-summary"},
		{http.MethodPost, "/api/v1/review/bulk", "/api/v1/review/bulk", http.StatusBadRequest, ""},
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
			assert.Equal(t, k.wantCode, w.Code, w.Body.String())
			if k.wantBody != "" {
				assert.Contains(t, w.Body.String(), k.wantBody)
			}
		})
	}
}
