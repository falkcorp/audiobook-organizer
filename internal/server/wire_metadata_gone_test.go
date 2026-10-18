// file: internal/server/wire_metadata_gone_test.go
// version: 1.0.1
// guid: 7e2c94d1-5b0a-4f38-9c6d-1a83f4e07b52
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
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

// retiredMetadataRoutes are the provider-throttle reset routes retired by
// 01-P81f. Both sit behind library.edit_metadata and name no replacement.
var retiredMetadataRoutes = []struct {
	method, pattern, path, body string
}{
	{http.MethodDelete, "/api/v1/metadata/providers/throttles", "/api/v1/metadata/providers/throttles",
		`{"error":"gone","message":"this endpoint has been removed and has no replacement"}`},
	{http.MethodDelete, "/api/v1/metadata/providers/throttles/:id", "/api/v1/metadata/providers/throttles/synthetic-provider",
		`{"error":"gone","message":"this endpoint has been removed and has no replacement"}`},
}

// TestRetiredMetadataRoutes_Answer410 drives each retired path through the real
// router as an admin: 410 with the shared gone() body.
func TestRetiredMetadataRoutes_Answer410(t *testing.T) {
	f := setupCredGuardServer(t)
	for _, r := range retiredMetadataRoutes {
		t.Run(r.method+" "+r.pattern, func(t *testing.T) {
			w := f.do(t, f.sessionToken, r.method, r.path, nil)
			assert.Equal(t, http.StatusGone, w.Code, w.Body.String())
			assert.Equal(t, r.body, w.Body.String())
		})
	}
}

// TestRetiredMetadataRoutes_AuthRunsFirst: the stubs sit on the protected
// group, so a caller with no credentials gets 401 and never learns the route
// is gone.
func TestRetiredMetadataRoutes_AuthRunsFirst(t *testing.T) {
	f := setupCredGuardServer(t)
	for _, r := range retiredMetadataRoutes {
		t.Run(r.method+" "+r.pattern, func(t *testing.T) {
			req := httptest.NewRequest(r.method, r.path, nil)
			w := httptest.NewRecorder()
			f.srv.router.ServeHTTP(w, req)
			assert.Equal(t, http.StatusUnauthorized, w.Code, w.Body.String())
			assert.NotContains(t, w.Body.String(), `"error":"gone"`)
		})
	}
}

// TestRetiredMetadataRoutes_PermissionGuardRunsFirst: both stubs keep the old
// library.edit_metadata guard, so a read-only viewer gets 403 and never learns
// the route is gone. The viewer's own 200 on the kept GET is the positive
// control that the viewer session is valid.
func TestRetiredMetadataRoutes_PermissionGuardRunsFirst(t *testing.T) {
	f := setupCredGuardServer(t)

	viewer, err := f.store.CreateUser("synthetic-viewer", "viewer@example.test", "bcrypt", "x", []string{auth.SeedRoleViewer}, "active")
	require.NoError(t, err)
	viewerSess, err := f.store.CreateSessionWithOrigin(viewer.ID, "127.0.0.1", "test", time.Hour, database.SessionOriginPassword)
	require.NoError(t, err)

	for _, r := range retiredMetadataRoutes {
		t.Run(r.method+" "+r.pattern, func(t *testing.T) {
			w := f.do(t, viewerSess.ID, r.method, r.path, nil)
			assert.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
			assert.NotContains(t, w.Body.String(), `"error":"gone"`)
		})
	}

	w := f.do(t, viewerSess.ID, http.MethodGet, "/api/v1/metadata/providers/throttles", nil)
	assert.Equal(t, http.StatusOK, w.Code, w.Body.String())
}

// TestRetiredMetadataRoutes_KeptNeighboursStillLive: the owner-kept (D73) read
// routes are not stubs. Against a seeded synthetic hold, GET throttles lists it,
// and the retired DELETE stub does not clear it; GET fields answers 200 with
// its own body.
func TestRetiredMetadataRoutes_KeptNeighboursStillLive(t *testing.T) {
	f := setupCredGuardServer(t)
	resetThrottles(t)
	throttleProvider(t, "hardcover", http.StatusUnauthorized, "bad token")
	require.True(t, metadata.DefaultThrottleRegistry().Throttled("hardcover"))

	w := f.do(t, f.sessionToken, http.MethodGet, "/api/v1/metadata/providers/throttles", nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.NotContains(t, w.Body.String(), `"error":"gone"`)
	var body struct {
		Data struct {
			Count     int `json:"count"`
			Throttles []struct {
				ProviderID string `json:"provider_id"`
			} `json:"throttles"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	require.Equal(t, 1, body.Data.Count, w.Body.String())
	assert.Equal(t, "hardcover", body.Data.Throttles[0].ProviderID)

	w = f.do(t, f.sessionToken, http.MethodDelete, "/api/v1/metadata/providers/throttles/hardcover", nil)
	assert.Equal(t, http.StatusGone, w.Code)
	assert.True(t, metadata.DefaultThrottleRegistry().Throttled("hardcover"), "the 410 stub must not clear the hold")

	w = f.do(t, f.sessionToken, http.MethodDelete, "/api/v1/metadata/providers/throttles", nil)
	assert.Equal(t, http.StatusGone, w.Code)
	assert.True(t, metadata.DefaultThrottleRegistry().Throttled("hardcover"), "the bare 410 stub must not clear the hold")

	w = f.do(t, f.sessionToken, http.MethodGet, "/api/v1/metadata/fields", nil)
	assert.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.NotContains(t, w.Body.String(), `"error":"gone"`)
}
