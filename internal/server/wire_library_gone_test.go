// file: internal/server/wire_library_gone_test.go
// version: 1.0.0
// guid: 4a4124a6-9e47-439e-bf24-3e5da4f68289
// last-edited: 2026-10-18

package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// retiredLibraryRoutes are the activity-maintenance routes retired by
// 01-P81g, with the exact gone() body each one answers.
var retiredLibraryRoutes = []struct {
	method, path, body string
}{
	{http.MethodPost, "/api/v1/admin/recompact-digests",
		`{"error":"gone","message":"this endpoint has been removed; use POST /api/v1/operations/v2 with {\"def_id\":\"maintenance.recompact-activity-digests\"} instead"}`},
}

// TestRetiredLibraryRoutes_Answer410 drives each retired path through the real
// router as an admin: 410 with the shared gone() body.
func TestRetiredLibraryRoutes_Answer410(t *testing.T) {
	f := setupCredGuardServer(t)
	for _, r := range retiredLibraryRoutes {
		t.Run(r.method+" "+r.path, func(t *testing.T) {
			w := f.do(t, f.sessionToken, r.method, r.path, nil)
			assert.Equal(t, http.StatusGone, w.Code, w.Body.String())
			assert.Equal(t, r.body, w.Body.String())
		})
	}
}

// TestRetiredLibraryRoutes_AuthRunsFirst: the stubs sit on the protected
// group, so a caller with no credentials gets 401 and never learns the route
// is gone.
func TestRetiredLibraryRoutes_AuthRunsFirst(t *testing.T) {
	f := setupCredGuardServer(t)
	for _, r := range retiredLibraryRoutes {
		t.Run(r.method+" "+r.path, func(t *testing.T) {
			req := httptest.NewRequest(r.method, r.path, nil)
			w := httptest.NewRecorder()
			f.srv.router.ServeHTTP(w, req)
			assert.Equal(t, http.StatusUnauthorized, w.Code, w.Body.String())
			assert.NotContains(t, w.Body.String(), `"error":"gone"`)
		})
	}
}

// TestRetiredLibraryRoutes_AdminGuardRunsFirst: the stubs keep the admin-only
// group's guard. A viewer and a fingerprint-worker session are both refused
// with 403 and never learn the route is gone.
func TestRetiredLibraryRoutes_AdminGuardRunsFirst(t *testing.T) {
	f := setupCredGuardServer(t)
	for _, role := range []string{auth.SeedRoleViewer, auth.SeedRoleFPWorker} {
		u, err := f.store.CreateUser("synthetic-"+role, role+"@example.test", "bcrypt", "x", []string{role}, "active")
		require.NoError(t, err)
		sess, err := f.store.CreateSessionWithOrigin(u.ID, "127.0.0.1", "test", time.Hour, database.SessionOriginPassword)
		require.NoError(t, err)
		for _, r := range retiredLibraryRoutes {
			t.Run(role+" "+r.method+" "+r.path, func(t *testing.T) {
				w := f.do(t, sess.ID, r.method, r.path, nil)
				assert.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
				assert.NotContains(t, w.Body.String(), `"error":"gone"`)
			})
		}
	}
}

// TestRetiredLibraryRoutes_ReplacementWorks: the replacement the 410 names is
// a live route that really enqueues the digest recompaction op, and the kept
// neighbour POST /cache/invalidate still reaches its own handler.
func TestRetiredLibraryRoutes_ReplacementWorks(t *testing.T) {
	f := setupCredGuardServer(t)

	w := f.do(t, f.sessionToken, http.MethodPost, "/api/v1/operations/v2",
		map[string]any{"def_id": "maintenance.recompact-activity-digests"})
	require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())
	var body struct {
		OpID string `json:"op_id"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.NotEmpty(t, body.OpID)

	w = f.do(t, f.sessionToken, http.MethodPost, "/api/v1/cache/invalidate", nil)
	assert.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.NotContains(t, w.Body.String(), `"error":"gone"`)
}
