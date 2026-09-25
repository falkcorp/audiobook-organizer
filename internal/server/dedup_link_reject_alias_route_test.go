// file: internal/server/dedup_link_reject_alias_route_test.go
// version: 1.0.0
// guid: 4a7e2c19-6b3d-4f81-9e05-2c6b8a1d7f43
// last-edited: 2026-09-25

package server

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestDeprecatedMergeDismissAliases_StillRoute is the router-level guard for
// the naming-audit class "merge vs link verbs"
// (docs/audits/2026-09-25-interface-naming-consistency.md classes 2/3). It
// proves the OLD "merge"/"dismiss" paths are still registered as deprecated
// aliases for the same handlers as the NEW "link"/"reject" paths, by driving
// both through the real gin router (server.router.ServeHTTP) — not by calling
// the handler function directly, which would prove nothing about whether the
// alias route registration actually exists.
//
// Each pair is sent the same request body and must return the SAME status
// code. The bodies below are deliberately minimal/invalid (missing a
// required field, or a nonsense id) so the assertion only needs the shared
// early-validation status, not full merge-service wiring — the point is
// "does this path reach the same handler", not "does the merge succeed".
func TestDeprecatedMergeDismissAliases_StillRoute(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	type pair struct {
		name       string
		method     string
		oldPath    string
		newPath    string
		body       string
		wantStatus int // asserted on the OLD path; the NEW path must match it
	}

	pairs := []pair{
		{
			name:       "audiobooks/duplicates merge->link",
			method:     http.MethodPost,
			oldPath:    "/api/v1/audiobooks/duplicates/merge",
			newPath:    "/api/v1/audiobooks/duplicates/link",
			body:       `{}`, // missing required book_ids
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "audiobooks/duplicates dismiss->reject",
			method:     http.MethodPost,
			oldPath:    "/api/v1/audiobooks/duplicates/dismiss",
			newPath:    "/api/v1/audiobooks/duplicates/reject",
			body:       `{}`, // missing required group_key
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "audiobooks merge->link",
			method:     http.MethodPost,
			oldPath:    "/api/v1/audiobooks/merge",
			newPath:    "/api/v1/audiobooks/link",
			body:       `{}`, // missing required keep_id
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "dedup candidate merge->link",
			method:     http.MethodPost,
			oldPath:    "/api/v1/dedup/candidates/abc/merge",
			newPath:    "/api/v1/dedup/candidates/abc/link",
			body:       ``,
			wantStatus: http.StatusBadRequest, // "abc" is not a valid candidate id
		},
		{
			name:       "dedup candidate dismiss->reject",
			method:     http.MethodPost,
			oldPath:    "/api/v1/dedup/candidates/abc/dismiss",
			newPath:    "/api/v1/dedup/candidates/abc/reject",
			body:       ``,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "dedup candidates bulk-merge->bulk-link",
			method:     http.MethodPost,
			oldPath:    "/api/v1/dedup/candidates/bulk-merge",
			newPath:    "/api/v1/dedup/candidates/bulk-link",
			body:       `{}`,
			wantStatus: http.StatusServiceUnavailable, // no embedding store wired in setupTestServer
		},
		{
			name:    "dedup candidates merge-cluster->link-cluster",
			method:  http.MethodPost,
			oldPath: "/api/v1/dedup/candidates/merge-cluster",
			newPath: "/api/v1/dedup/candidates/link-cluster",
			body:    `{}`,
			// setupTestServer wires no dedup engine, and MergeDedupCluster
			// checks h.dedupEngine before parsing book_ids.
			wantStatus: http.StatusServiceUnavailable,
		},
		{
			name:       "dedup candidates dismiss-cluster->reject-cluster",
			method:     http.MethodPost,
			oldPath:    "/api/v1/dedup/candidates/dismiss-cluster",
			newPath:    "/api/v1/dedup/candidates/reject-cluster",
			body:       `{}`, // missing required book_ids
			wantStatus: http.StatusBadRequest,
		},
		{
			name:    "dedup candidates merge-series->link-series",
			method:  http.MethodPost,
			oldPath: "/api/v1/dedup/candidates/merge-series",
			newPath: "/api/v1/dedup/candidates/link-series",
			body:    `{}`,
			// setupTestServer wires no dedup engine, and
			// LinkDedupCandidateSeries checks h.dedupEngine before parsing
			// series_id.
			wantStatus: http.StatusServiceUnavailable,
		},
	}

	for _, p := range pairs {
		t.Run(p.name, func(t *testing.T) {
			oldReq := httptest.NewRequest(p.method, p.oldPath, bytes.NewBufferString(p.body))
			oldReq.Header.Set("Content-Type", "application/json")
			oldW := httptest.NewRecorder()
			server.router.ServeHTTP(oldW, oldReq)

			assert.NotEqual(t, http.StatusNotFound, oldW.Code, "deprecated alias path %s must still be registered", p.oldPath)
			assert.Equal(t, p.wantStatus, oldW.Code, "deprecated alias path %s status", p.oldPath)

			newReq := httptest.NewRequest(p.method, p.newPath, bytes.NewBufferString(p.body))
			newReq.Header.Set("Content-Type", "application/json")
			newW := httptest.NewRecorder()
			server.router.ServeHTTP(newW, newReq)

			assert.NotEqual(t, http.StatusNotFound, newW.Code, "new path %s must be registered", p.newPath)
			assert.Equal(t, oldW.Code, newW.Code, "old alias %s and new path %s must reach the same handler (same status for the same body)", p.oldPath, p.newPath)
		})
	}
}
