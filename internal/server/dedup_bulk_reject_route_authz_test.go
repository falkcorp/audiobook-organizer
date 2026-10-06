// file: internal/server/dedup_bulk_reject_route_authz_test.go
// version: 1.1.0
// guid: 8d2c5f17-4a90-4e3b-9b6d-0e7f1a3c5b29
// last-edited: 2026-10-06

// The filter-scoped bulk reject and its revert are registered behind
// library.edit_metadata, like bulk-link: a view-only session gets 403 and an
// admin session reaches the handler. bulk-count writes nothing and is open to
// library.view. Driven through the real router, since a
// route registered without s.perm(...) is exactly what a handler-level test
// cannot see.

package server

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDedupBulkRejectRoutes_Permissions(t *testing.T) {
	server, adminToken, viewerToken, _, cleanup := setupUserTagsAuthzTestServer(t)
	defer cleanup()

	routes := []struct {
		path string
		body string
	}{
		{"/api/v1/dedup/candidates/bulk-reject", `{"expected_total": 0}`},
		{"/api/v1/dedup/candidates/bulk-reject/revert", `{"candidate_ids": []}`},
	}
	for _, r := range routes {
		t.Run(r.path, func(t *testing.T) {
			send := func(token string) *httptest.ResponseRecorder {
				req := httptest.NewRequest(http.MethodPost, r.path, bytes.NewReader([]byte(r.body)))
				req.Header.Set("Authorization", "Bearer "+token)
				req.Header.Set("Content-Type", "application/json")
				w := httptest.NewRecorder()
				server.router.ServeHTTP(w, req)
				return w
			}
			w := send(viewerToken)
			require.Equal(t, http.StatusForbidden, w.Code, "viewer must be forbidden; body=%s", w.Body.String())
			w = send(adminToken)
			require.NotEqual(t, http.StatusForbidden, w.Code, "admin must reach the handler; body=%s", w.Body.String())
			require.NotEqual(t, http.StatusUnauthorized, w.Code, "admin must authenticate; body=%s", w.Body.String())
			require.NotEqual(t, http.StatusNotFound, w.Code, "route must be registered; body=%s", w.Body.String())
		})
	}
}

func TestDedupBulkCountRoute_OpenToViewers(t *testing.T) {
	server, _, viewerToken, _, cleanup := setupUserTagsAuthzTestServer(t)
	defer cleanup()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/dedup/candidates/bulk-count", bytes.NewReader([]byte(`{}`)))
	req.Header.Set("Authorization", "Bearer "+viewerToken)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	require.NotEqual(t, http.StatusForbidden, w.Code, "bulk-count writes nothing; viewers may count; body=%s", w.Body.String())
	require.NotEqual(t, http.StatusNotFound, w.Code, "route must be registered; body=%s", w.Body.String())
}
