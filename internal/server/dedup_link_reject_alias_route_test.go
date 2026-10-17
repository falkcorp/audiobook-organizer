// file: internal/server/dedup_link_reject_alias_route_test.go
// version: 1.3.0
// guid: 4a7e2c19-6b3d-4f81-9e05-2c6b8a1d7f43
// last-edited: 2026-10-17

package server

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestRemovedDedupRoutes_Return404 pins the 01-P72 retirement: the deprecated
// merge/dismiss verb aliases and the dead operator-trigger routes are gone from
// the route table. The operations behind the triggers (embed, LSH build, purge,
// re-encode, series normalize, AI author review) stay registered as ops; only
// the HTTP entry points were removed. The /audiobooks/duplicates/merge and
// /dismiss aliases are NOT in this list: they answer 410 Gone for one release
// with the rest of /audiobooks/duplicates (03-PR12), asserted by
// wire_dedup_routes_gone_test.go.
func TestRemovedDedupRoutes_Return404(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	removed := []struct{ method, path string }{
		{http.MethodPost, "/api/v1/dedup/purge-acoustid-conflicts"},
		{http.MethodPost, "/api/v1/dedup/candidates/abc/merge"},
		{http.MethodPost, "/api/v1/dedup/candidates/abc/dismiss"},
		{http.MethodPost, "/api/v1/dedup/candidates/bulk-merge"},
		{http.MethodPost, "/api/v1/dedup/candidates/merge-cluster"},
		{http.MethodPost, "/api/v1/dedup/candidates/dismiss-cluster"},
		{http.MethodPost, "/api/v1/dedup/candidates/merge-series"},
		{http.MethodPost, "/api/v1/dedup/scan-book-signature"},
		{http.MethodPost, "/api/v1/dedup/refresh"},
		{http.MethodPost, "/api/v1/dedup/purge-legacy-fp"},
		{http.MethodPost, "/api/v1/dedup/embed-async"},
		{http.MethodPost, "/api/v1/dedup/lsh-index"},
		{http.MethodPost, "/api/v1/dedup/emb-reencode"},
		{http.MethodPost, "/api/v1/audiobooks/merge"},
		{http.MethodGet, "/api/v1/series/normalize/preview"},
		{http.MethodPost, "/api/v1/series/normalize"},
		{http.MethodPost, "/api/v1/authors/duplicates/ai-review"},
		{http.MethodPost, "/api/v1/authors/duplicates/ai-review/apply"},
	}
	for _, r := range removed {
		t.Run(r.method+" "+r.path, func(t *testing.T) {
			req := httptest.NewRequest(r.method, r.path, bytes.NewBufferString(`{}`))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			server.router.ServeHTTP(w, req)
			assert.Equal(t, http.StatusNotFound, w.Code, "%s %s must no longer be routed", r.method, r.path)
		})
	}
}

// TestKeptDedupRoutes_AreRegistered pins the other half of the 01-P72
// retirement: the link/reject replacements for the removed merge/dismiss verb
// aliases (and the routes deliberately kept for P81h) are in the route table
// and reach a handler, so removing the old verbs cannot silently take the new
// ones with it. Asserted twice: by gin's Routes() table, and by a request that
// must not 404.
func TestKeptDedupRoutes_AreRegistered(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	kept := []struct{ method, route, path string }{
		{http.MethodPost, "/api/v1/dedup/candidates/:id/link", "/api/v1/dedup/candidates/abc/link"},
		{http.MethodPost, "/api/v1/dedup/candidates/:id/reject", "/api/v1/dedup/candidates/abc/reject"},
		{http.MethodPost, "/api/v1/dedup/candidates/bulk-link", "/api/v1/dedup/candidates/bulk-link"},
		{http.MethodPost, "/api/v1/dedup/candidates/link-cluster", "/api/v1/dedup/candidates/link-cluster"},
		{http.MethodPost, "/api/v1/dedup/candidates/reject-cluster", "/api/v1/dedup/candidates/reject-cluster"},
		{http.MethodPost, "/api/v1/dedup/candidates/link-series", "/api/v1/dedup/candidates/link-series"},
		{http.MethodPost, "/api/v1/audiobooks/link", "/api/v1/audiobooks/link"},
		// Kept for P81h.
		{http.MethodPost, "/api/v1/dedup/purge-stale", "/api/v1/dedup/purge-stale"},
	}

	registered := map[string]bool{}
	for _, r := range server.router.Routes() {
		registered[r.Method+" "+r.Path] = true
	}

	for _, k := range kept {
		t.Run(k.method+" "+k.route, func(t *testing.T) {
			assert.True(t, registered[k.method+" "+k.route], "%s %s must stay registered", k.method, k.route)

			req := httptest.NewRequest(k.method, k.path, bytes.NewBufferString(`{}`))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			server.router.ServeHTTP(w, req)
			assert.NotEqual(t, http.StatusNotFound, w.Code, "%s %s must not 404", k.method, k.path)
		})
	}
}
