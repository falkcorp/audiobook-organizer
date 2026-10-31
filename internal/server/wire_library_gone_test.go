// file: internal/server/wire_library_gone_test.go
// version: 1.1.2
// guid: 4a4124a6-9e47-439e-bf24-3e5da4f68289
// last-edited: 2026-10-30

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

// retiredLibraryRoutes are the cache and activity-maintenance routes retired by
// 01-P81g, with the exact gone() body each one answers.
var retiredLibraryRoutes = []struct {
	method, path, body string
}{
	{http.MethodPost, "/api/v1/admin/recompact-digests",
		`{"error":"gone","message":"this endpoint has been removed; use POST /api/v1/operations/v2 with {\"def_id\":\"maintenance.recompact-activity-digests\"} instead"}`},
	{http.MethodPost, "/api/v1/cache/invalidate",
		`{"error":"gone","message":"this endpoint has been removed and has no replacement"}`},
	{http.MethodPost, "/api/v1/activity/clamp-summaries",
		`{"error":"gone","message":"this endpoint has been removed and has no replacement"}`},
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
// group (clamp-summaries) or its admin-only subgroup (the other two), so a caller with no credentials gets 401 and never learns the route
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

// TestRetiredLibraryRoutes_GuardRunsFirst: the stubs keep their old guards:
// RequireAdmin for recompact-digests and cache/invalidate, settings.manage for
// clamp-summaries. A viewer and a fingerprint-worker session hold neither, so
// both are refused with 403 and never learn the route is gone. The admin
// session reaching 410 in TestRetiredLibraryRoutes_Answer410 is the positive
// control that the guards let an allowed caller through to the stub.
func TestRetiredLibraryRoutes_GuardRunsFirst(t *testing.T) {
	f := setupCredGuardServer(t)
	for _, role := range []string{auth.SeedRoleViewer, auth.SeedRoleEditor, auth.SeedRoleFPWorker} {
		u, err := f.store.CreateUser("synthetic-"+role, role+"@example.test", "bcrypt", "x", []string{role}, "active")
		require.NoError(t, err)
		sess, err := f.store.CreateSessionWithOrigin(u.ID, "127.0.0.1", "test", time.Hour, database.SessionOriginPassword, 0)
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

// TestRetiredLibraryRoutes_ReplacementAndKeptNeighbours: the replacement the
// recompact-digests 410 names is a live route that really enqueues the op, and
// the routes the owner kept (D73) next to the retired ones still answer from
// their own handlers, not the stub. The introspection route is driven with a
// real registered cache name so it exercises the handler, not a 400 path.
func TestRetiredLibraryRoutes_ReplacementAndKeptNeighbours(t *testing.T) {
	f := setupCredGuardServer(t)

	w := f.do(t, f.sessionToken, http.MethodPost, "/api/v1/operations/v2",
		map[string]any{"def_id": "maintenance.recompact-activity-digests"})
	require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())
	var body struct {
		OpID string `json:"op_id"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.NotEmpty(t, body.OpID)

	name := f.srv.listCache.Name()
	require.NotEmpty(t, name)
	w = f.do(t, f.sessionToken, http.MethodGet, "/api/v1/cache/stats/keys?cache="+name, nil)
	assert.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.NotContains(t, w.Body.String(), `"error":"gone"`)
	assert.Contains(t, w.Body.String(), `"cache":"`+name+`"`)

	w = f.do(t, f.sessionToken, http.MethodPost, "/api/v1/activity/compact",
		map[string]any{"older_than_days": 3650})
	assert.Equal(t, http.StatusAccepted, w.Code, w.Body.String())
	assert.NotContains(t, w.Body.String(), `"error":"gone"`)
}
