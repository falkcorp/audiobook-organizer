// file: internal/server/wire_lifecycle_gone_test.go
// version: 1.0.0
// guid: 2d9b6e84-1a3c-4f57-8e20-b7c5a41d9f63
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
	lifecycleGoneNoReplacementBody = `{"error":"gone","message":"this endpoint has been removed and has no replacement"}`
	lifecycleGoneSimilarBody       = `{"error":"gone","message":"this endpoint has been removed; use GET /api/v1/audiobooks?search=author:... (the library search takes the same author: and series: terms) instead"}`
	lifecycleGoneBulkPreviewBody   = `{"error":"gone","message":"this endpoint has been removed; use POST /api/v1/metadata/batch-apply-candidates or POST /api/v1/audiobooks/metadata/batch-apply-cached (each is a dry run unless the body sends dry_run:false) instead"}`
)

// retiredLifecycleRoutes are the server_lifecycle-owned orphan routes retired
// by 01-P81i, with the exact gone() body each one answers and the permission
// guard it keeps. The owner-kept diagnostics (fingerprint-failures,
// scan-composer-tags/:id, signals/coverage) are NOT in this table.
var retiredLifecycleRoutes = []struct {
	method, pattern, path, body string
	// writeGuard is true when the route's guard is library.edit_metadata (so a
	// viewer session is refused); false when it is library.view.
	writeGuard bool
}{
	{http.MethodGet, "/api/v1/audiobooks/:id/cover-history", "/api/v1/audiobooks/synthetic-book/cover-history", lifecycleGoneNoReplacementBody, false},
	{http.MethodPost, "/api/v1/audiobooks/:id/cover-history/restore", "/api/v1/audiobooks/synthetic-book/cover-history/restore", lifecycleGoneNoReplacementBody, true},
	{http.MethodGet, "/api/v1/audiobooks/:id/similar", "/api/v1/audiobooks/synthetic-book/similar", lifecycleGoneSimilarBody, false},
	{http.MethodPost, "/api/v1/metadata/bulk-apply-preview", "/api/v1/metadata/bulk-apply-preview", lifecycleGoneBulkPreviewBody, false},
	{http.MethodGet, "/api/v1/metadata/bulk-apply-preview/:id", "/api/v1/metadata/bulk-apply-preview/synthetic-op", lifecycleGoneNoReplacementBody, false},
	{http.MethodPost, "/api/v1/import/collision-preview", "/api/v1/import/collision-preview", lifecycleGoneNoReplacementBody, false},
}

// TestRetiredLifecycleRoutes_Answer410 drives each retired path through the
// real router as an admin: 410 with the shared gone() body.
func TestRetiredLifecycleRoutes_Answer410(t *testing.T) {
	f := setupCredGuardServer(t)
	for _, r := range retiredLifecycleRoutes {
		t.Run(r.method+" "+r.pattern, func(t *testing.T) {
			w := f.do(t, f.sessionToken, r.method, r.path, nil)
			assert.Equal(t, http.StatusGone, w.Code, w.Body.String())
			assert.Equal(t, r.body, w.Body.String())
		})
	}
}

// TestRetiredLifecycleRoutes_AuthRunsFirst: the stubs sit on the protected
// group, so a caller with no credentials gets 401 and never learns the route
// is gone.
func TestRetiredLifecycleRoutes_AuthRunsFirst(t *testing.T) {
	f := setupCredGuardServer(t)
	for _, r := range retiredLifecycleRoutes {
		t.Run(r.method+" "+r.pattern, func(t *testing.T) {
			req := httptest.NewRequest(r.method, r.path, nil)
			w := httptest.NewRecorder()
			f.srv.router.ServeHTTP(w, req)
			assert.Equal(t, http.StatusUnauthorized, w.Code, w.Body.String())
			assert.NotContains(t, w.Body.String(), `"error":"gone"`)
		})
	}
}

// TestRetiredLifecycleRoutes_PermissionRunsBeforeGone: a viewer session holds
// library.view but not library.edit_metadata, so the restore stub answers 403
// from s.perm and never reaches gone(); the positive control shows the same
// viewer gets 410 on a view-guarded stub, so the 403 is the permission guard
// and not a rejected session. An fp-worker session holds only
// PermFingerprintWorker, so s.perm must refuse it on every stub; without that
// loop, dropping s.perm from a stub would fail no test.
func TestRetiredLifecycleRoutes_PermissionRunsBeforeGone(t *testing.T) {
	f := setupCredGuardServer(t)

	viewer, err := f.store.CreateUser("synthetic-viewer", "viewer@example.test", "bcrypt", "x", []string{auth.SeedRoleViewer}, "active")
	require.NoError(t, err)
	viewerSess, err := f.store.CreateSessionWithOrigin(viewer.ID, "127.0.0.1", "test", time.Hour, database.SessionOriginPassword)
	require.NoError(t, err)
	fpWorker, err := f.store.CreateUser("synthetic-fp-worker", "fpworker@example.test", "bcrypt", "x", []string{auth.SeedRoleFPWorker}, "active")
	require.NoError(t, err)
	fpSess, err := f.store.CreateSessionWithOrigin(fpWorker.ID, "127.0.0.1", "test", time.Hour, database.SessionOriginPassword)
	require.NoError(t, err)

	for _, r := range retiredLifecycleRoutes {
		if r.writeGuard {
			t.Run("viewer gets 403 on "+r.method+" "+r.pattern, func(t *testing.T) {
				w := f.do(t, viewerSess.ID, r.method, r.path, nil)
				assert.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
				assert.NotContains(t, w.Body.String(), `"error":"gone"`)
			})
		} else {
			t.Run("viewer gets 410 on "+r.method+" "+r.pattern, func(t *testing.T) {
				w := f.do(t, viewerSess.ID, r.method, r.path, nil)
				assert.Equal(t, http.StatusGone, w.Code, w.Body.String())
			})
		}
		t.Run("fp-worker gets 403 on "+r.method+" "+r.pattern, func(t *testing.T) {
			w := f.do(t, fpSess.ID, r.method, r.path, nil)
			assert.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
			assert.NotContains(t, w.Body.String(), `"error":"gone"`)
		})
	}
}

// TestRetiredLifecycleRoutes_KeptRoutesNotGone: the owner-kept diagnostics
// (D73) and the live neighbours of the retired routes are still registered and
// really run: each answers with its own handler's body, never the stub's. The
// book routes use a seeded synthetic book so the handler has something to find.
func TestRetiredLifecycleRoutes_KeptRoutesNotGone(t *testing.T) {
	f := setupCredGuardServer(t)
	book, err := f.store.CreateBook(&database.Book{Title: "Synthetic Lifecycle Book", FilePath: "/nonexistent/synthetic/lifecycle.m4b"})
	require.NoError(t, err)

	kept := []struct {
		method, pattern, path string
		want                  int
		body                  any
	}{
		{http.MethodGet, "/api/v1/diagnostics/fingerprint-failures", "/api/v1/diagnostics/fingerprint-failures", http.StatusOK, nil},
		{http.MethodGet, "/api/v1/signals/coverage", "/api/v1/signals/coverage", http.StatusOK, nil},
		// No such operation: the handler's own 404 (a JSON error), not gin's
		// unrouted 404 and not 410.
		{http.MethodGet, "/api/v1/maintenance/scan-composer-tags/:id", "/api/v1/maintenance/scan-composer-tags/synthetic-op", http.StatusNotFound, nil},
		{http.MethodGet, "/api/v1/audiobooks/:id", "/api/v1/audiobooks/" + book.ID, http.StatusOK, nil},
		{http.MethodGet, "/api/v1/audiobooks/:id/cover-text", "/api/v1/audiobooks/" + book.ID + "/cover-text", http.StatusOK, nil},
		// An empty body is the handler's own 400.
		{http.MethodPost, "/api/v1/metadata/batch-apply-candidates", "/api/v1/metadata/batch-apply-candidates", http.StatusBadRequest, map[string]any{}},
	}
	registered := map[string]bool{}
	for _, r := range f.srv.router.Routes() {
		registered[r.Method+" "+r.Path] = true
	}
	for _, k := range kept {
		t.Run(k.method+" "+k.pattern, func(t *testing.T) {
			require.True(t, registered[k.method+" "+k.pattern], "route not registered")
			w := f.do(t, f.sessionToken, k.method, k.path, k.body)
			assert.Equal(t, k.want, w.Code, w.Body.String())
			assert.NotContains(t, w.Body.String(), `"error":"gone"`)
			assert.NotEqual(t, "404 page not found", w.Body.String())
		})
	}
}
