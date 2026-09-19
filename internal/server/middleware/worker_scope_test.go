// file: internal/server/middleware/worker_scope_test.go
// version: 1.0.0
// guid: 3491bedd-1c6e-45b4-afe0-973ac28648ba
// last-edited: 2026-09-19

package middleware

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// TestWorkerOnlyCredentials_ConfinedToWorkerAPI: a credential whose effective
// permissions are fingerprint.worker alone (an admin key scoped to it, or a
// user holding only the fp-worker seed role) reaches the worker API and
// nothing else, including the protected routes that check no permission
// (reading progress, playlists).
func TestWorkerOnlyCredentials_ConfinedToWorkerAPI(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	_, _, err = auth.SeedRoles(store)
	require.NoError(t, err)
	admin, err := store.CreateUser("admin", "admin@example.test", "bcrypt", "hash", []string{auth.SeedRoleAdmin}, "active")
	require.NoError(t, err)
	svc, err := store.CreateUser("fp-worker-svc", "svc@example.test", "bcrypt", "hash", []string{auth.SeedRoleFPWorker}, "active")
	require.NoError(t, err)
	mint := func(userID string, scopes ...string) string {
		raw, hash, err := database.GenerateAPIKeyToken()
		require.NoError(t, err)
		_, err = store.CreateAPIKey(&database.APIKey{UserID: userID, Name: "k", TokenHash: hash, Scopes: scopes, Status: "active"})
		require.NoError(t, err)
		return raw
	}
	sess, err := store.CreateSession(svc.ID, "192.0.2.10", "test", time.Hour)
	require.NoError(t, err)

	r := gin.New()
	ok := func(c *gin.Context) { c.Status(http.StatusOK) }
	api := r.Group("/api/v1", RequireAuth(store))
	api.POST("/playlists", ok) // permission-free protected route
	api.GET("/books/:id/position", ok)
	api.GET("/fingerprint/worker/hello", RequirePermission(store, auth.PermFingerprintWorker), ok)

	call := func(method, path, bearer string) int {
		req := httptest.NewRequest(method, path, nil)
		req.Header.Set("Authorization", "Bearer "+bearer)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w.Code
	}
	creds := map[string]string{
		"admin key scoped to fingerprint.worker": mint(admin.ID, auth.PermFingerprintWorker),
		"fp-worker role key":                     mint(svc.ID),
		"fp-worker role session":                 sess.ID,
	}
	for name, cred := range creds {
		require.Equal(t, http.StatusOK, call(http.MethodGet, "/api/v1/fingerprint/worker/hello", cred), name)
		require.Equal(t, http.StatusForbidden, call(http.MethodPost, "/api/v1/playlists", cred), name)
		require.Equal(t, http.StatusForbidden, call(http.MethodGet, "/api/v1/books/b1/position", cred), name)
	}
	// A normal credential is unaffected.
	full := mint(admin.ID)
	require.Equal(t, http.StatusOK, call(http.MethodPost, "/api/v1/playlists", full))
	require.Equal(t, http.StatusOK, call(http.MethodGet, "/api/v1/fingerprint/worker/hello", full))
}
