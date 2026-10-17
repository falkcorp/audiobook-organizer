// file: internal/server/wire_ops_v3_routes_test.go
// version: 1.1.0
// guid: f0aca62d-8230-49ce-ab21-61cbd082ce99
// last-edited: 2026-10-17

package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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

// TestOpsV3Defs_PreviewFollowsTheWriteModeLedger: every adapted v2 op the
// ledger records `no-mode` is a writer without a preview, every `preview` one
// has one, and a v2 op that cannot write is not a writer. The real catalog,
// not a fake.
func TestOpsV3Defs_PreviewFollowsTheWriteModeLedger(t *testing.T) {
	srv, _, _ := bootRegisteredOpIDs(t)
	ledger := readWriteOpModes(t)
	defs := (&opsV3Defs{reg: srv.opRegistry}).ListDefs()
	require.NotEmpty(t, defs)
	noMode, preview := 0, 0
	for _, d := range defs {
		od, ok := srv.opRegistry.Def(d.ID)
		require.True(t, ok, d.ID)
		switch class, listed := ledger[d.ID]; {
		case listed && class == "no-mode":
			noMode++
			require.True(t, d.Writer && !d.HasPreview, "%s is no-mode: writer without preview, got writer=%v preview=%v", d.ID, d.Writer, d.HasPreview)
		case listed && class == "preview":
			preview++
			require.True(t, d.Writer && d.HasPreview, "%s is recorded preview", d.ID)
		case !canWrite(od):
			require.False(t, d.Writer, "%s declares no write capability", d.ID)
		}
	}
	require.Positive(t, noMode, "no registered no-mode op was checked")
	require.Positive(t, preview, "no registered preview op was checked")
}

// TestOpsV3Routes_NoPreviewOpRefusesPreview drives the real router: a
// writing op without a preview shows has_preview=false and a preview (or no
// mode) start is a 409, so it is never queued live under a preview label.
func TestOpsV3Routes_NoPreviewOpRefusesPreview(t *testing.T) {
	s, cleanup := setupTestServer(t)
	defer cleanup()
	req := httptest.NewRequest(http.MethodGet, "/api/v3/ops/defs", nil)
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var list struct {
		Items []struct {
			ID         string `json:"id"`
			HasPreview *bool  `json:"has_preview"`
		} `json:"items"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))
	target := ""
	for _, d := range list.Items {
		if d.HasPreview != nil && !*d.HasPreview {
			target = d.ID
			break
		}
	}
	require.NotEmpty(t, target, "no writing op without a preview is registered on the test server")
	for _, body := range []string{`{"mode":"preview"}`, `{}`} {
		req := httptest.NewRequest(http.MethodPost, "/api/v3/ops/defs/"+target+"/runs", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		s.router.ServeHTTP(rec, req)
		require.Equal(t, http.StatusConflict, rec.Code, "%s %s: %s", target, body, rec.Body.String())
	}
}
