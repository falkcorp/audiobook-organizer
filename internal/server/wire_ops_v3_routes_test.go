// file: internal/server/wire_ops_v3_routes_test.go
// version: 1.0.0
// guid: f0aca62d-8230-49ce-ab21-61cbd082ce99
// last-edited: 2026-10-17

package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestOpsV3Routes_WiredAndServed checks the oracle the handler tests cannot:
// that setupRoutes registers every /api/v3/ops route on the real router, with
// the opsv3 handler, and that the /api/* -> /api/v1/* compatibility redirect
// leaves them alone.
func TestOpsV3Routes_WiredAndServed(t *testing.T) {
	s, cleanup := setupTestServer(t)
	defer cleanup()

	want := []struct{ method, path string }{
		{http.MethodGet, "/api/v3/ops/defs"},
		{http.MethodGet, "/api/v3/ops/defs/:id"},
		{http.MethodPost, "/api/v3/ops/defs/:id/runs"},
		{http.MethodGet, "/api/v3/ops/runs"},
		{http.MethodPost, "/api/v3/ops/runs"},
		{http.MethodGet, "/api/v3/ops/runs/:id"},
		{http.MethodGet, "/api/v3/ops/runs/:id/rows"},
		{http.MethodPost, "/api/v3/ops/runs/:id/cancel"},
		{http.MethodPost, "/api/v3/ops/runs/:id/approve"},
		{http.MethodGet, "/api/v3/ops/counts"},
		{http.MethodGet, "/api/v3/ops/timeline"},
		{http.MethodGet, "/api/v3/ops/events"},
	}
	routes := s.router.Routes()
	for _, w := range want {
		var handler string
		for _, ri := range routes {
			if ri.Method == w.method && ri.Path == w.path {
				handler = ri.Handler
			}
		}
		require.NotEmpty(t, handler, "%s %s must be registered", w.method, w.path)
		require.Contains(t, handler, "opsv3", "%s %s handler = %q", w.method, w.path, handler)
	}

	for _, path := range []string{"/api/v3/ops/defs", "/api/v3/ops/counts?group_by=def", "/api/v3/ops/runs", "/api/v3/ops/timeline"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		s.router.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code, "%s: %s", path, rec.Body.String())
		require.True(t, json.Valid(rec.Body.Bytes()), path)
	}
}
