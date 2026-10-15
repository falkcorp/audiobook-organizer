// file: internal/server/gone_test.go
// version: 1.2.0
// guid: 8a3d6f12-c405-4e97-b1d8-72f9e0a5c346
// last-edited: 2026-10-14

package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

func TestGone_Helper(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name        string
		replacement string
		wantMessage string
	}{
		{
			name:        "names the replacement",
			replacement: "GET /api/v1/operations/timeline",
			wantMessage: "this endpoint has been removed; use GET /api/v1/operations/timeline instead",
		},
		{
			name:        "no replacement",
			replacement: "",
			wantMessage: "this endpoint has been removed and has no replacement",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := gin.New()
			r.GET("/x", gone(tc.replacement))
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/x", nil))

			assert.Equal(t, http.StatusGone, w.Code)
			var body map[string]string
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
			assert.Equal(t, "gone", body["error"])
			assert.NotEmpty(t, body["message"])
			assert.Equal(t, tc.wantMessage, body["message"])
			assert.Len(t, body, 2, "the shape is exactly {error, message}")
		})
	}
}

// TestGone_OperationsStubsKeepTheirBytes pins the wire format of the two UOS-14
// stubs that now go through gone(): the bytes a client saw before the helper
// existed must be the bytes it sees after.
func TestGone_OperationsStubsKeepTheirBytes(t *testing.T) {
	f := setupCredGuardServer(t)
	const want = `{"error":"gone","message":"this endpoint has been removed; use GET /api/v1/operations/timeline instead"}`
	for _, path := range []string{"/api/v1/operations/active", "/api/v1/operations/recent"} {
		t.Run(path, func(t *testing.T) {
			w := f.do(t, f.sessionToken, http.MethodGet, path, nil)
			assert.Equal(t, http.StatusGone, w.Code, w.Body.String())
			assert.JSONEq(t, want, w.Body.String())
			assert.Equal(t, want, w.Body.String(), "byte-for-byte, not just equivalent JSON")
		})
	}
}

// TestWipeRoute_IsA410StubBehindAdminOnly: the retired library wipe answers 410
// with a message to an admin, and a caller without the admin role still gets
// 403 from RequireAdmin before the stub is reached, so the guard holds until
// the stub itself is removed.
func TestWipeRoute_IsA410StubBehindAdminOnly(t *testing.T) {
	f := setupCredGuardServer(t)

	viewer, err := f.store.CreateUser("synthetic-viewer", "viewer@example.test", "bcrypt", "x", []string{auth.SeedRoleViewer}, "active")
	require.NoError(t, err)
	viewerSess, err := f.store.CreateSessionWithOrigin(viewer.ID, "127.0.0.1", "test", time.Hour, database.SessionOriginPassword)
	require.NoError(t, err)

	body := map[string]any{"targets": []string{"all"}, "confirm": "WIPE", "dry_run": false}

	t.Run("admin gets 410 with a message", func(t *testing.T) {
		w := f.do(t, f.sessionToken, http.MethodPost, "/api/v1/maintenance/wipe", body)
		require.Equal(t, http.StatusGone, w.Code, w.Body.String())
		var got map[string]string
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
		assert.Equal(t, "gone", got["error"])
		assert.NotEmpty(t, got["message"])
	})

	t.Run("non-admin gets 403 before the 410", func(t *testing.T) {
		w := f.do(t, viewerSess.ID, http.MethodPost, "/api/v1/maintenance/wipe", body)
		assert.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
		// It is RequireAdmin that refused, not some earlier layer.
		assert.Contains(t, w.Body.String(), "admin role required")
		assert.NotContains(t, w.Body.String(), `"error":"gone"`)
	})

	// Positive control: the viewer session is valid and authorized for
	// library.view, so a route that is not admin-only (the UOS-14 stub) answers
	// it with 410. Without this the 403 above could mean the viewer
	// session itself is rejected everywhere.
	t.Run("viewer session reaches a non-admin gone stub", func(t *testing.T) {
		w := f.do(t, viewerSess.ID, http.MethodGet, "/api/v1/operations/active", nil)
		assert.Equal(t, http.StatusGone, w.Code, w.Body.String())
	})
}

// TestRepairMissingFilesResultRoute_IsA410StubNamingBothSuccessors: the result
// getter for the retired maintenance.repair-missing-files job answers 410 to an
// allowed caller and names both replacement ops, and a caller without
// settings.manage still gets 403 first (04-P14e).
func TestRepairMissingFilesResultRoute_IsA410StubNamingBothSuccessors(t *testing.T) {
	f := setupCredGuardServer(t)

	viewer, err := f.store.CreateUser("synthetic-viewer", "viewer@example.test", "bcrypt", "x", []string{auth.SeedRoleViewer}, "active")
	require.NoError(t, err)
	viewerSess, err := f.store.CreateSessionWithOrigin(viewer.ID, "127.0.0.1", "test", time.Hour, database.SessionOriginPassword)
	require.NoError(t, err)

	const path = "/api/v1/maintenance/repair-missing-files/01SYNTHETICOPID0000000000"

	t.Run("admin gets 410 naming both successors", func(t *testing.T) {
		w := f.do(t, f.sessionToken, http.MethodGet, path, nil)
		require.Equal(t, http.StatusGone, w.Code, w.Body.String())
		var got map[string]string
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
		assert.Equal(t, "gone", got["error"])
		assert.Contains(t, got["message"], "maintenance.missing-file-repoint")
		assert.Contains(t, got["message"], "maintenance.recover-missing-files")
	})

	t.Run("a caller without settings.manage is refused before the 410", func(t *testing.T) {
		w := f.do(t, viewerSess.ID, http.MethodGet, path, nil)
		assert.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
		assert.NotContains(t, w.Body.String(), `"error":"gone"`)
	})
}
