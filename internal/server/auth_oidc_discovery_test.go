// file: internal/server/auth_oidc_discovery_test.go
// version: 1.0.0
// guid: 5d81b06a-2fc4-43e7-9a58-0c37e4b91d26
// last-edited: 2026-08-01

package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

// The probe must not panic on gin route registration (/auth/openid alongside
// /auth/openid/*rest is a catch-all-next-to-static-path shape gin is picky about)
// and must redirect the client onward so its exchange call can be observed.
func TestOIDCDiscoveryProbe_RedirectsAndDoesNotPanic(t *testing.T) {
	t.Setenv(OIDCDiscoveryEnvVar, "1")
	server, cleanup := setupTestServer(t)
	defer cleanup()
	_ = os.Unsetenv(OIDCDiscoveryEnvVar)

	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, httptest.NewRequest(http.MethodGet,
		"/auth/openid?client_id=AudioBooth&response_type=code&redirect_uri=audiobooth://oauth&state=xyz", nil))
	require.Equal(t, http.StatusFound, w.Code)
	require.Contains(t, w.Header().Get("Location"), "audiobooth://oauth?code=")
	require.Contains(t, w.Header().Get("Location"), "state=xyz")

	// The exchange catch-all must answer without minting anything.
	w2 := httptest.NewRecorder()
	server.router.ServeHTTP(w2, httptest.NewRequest(http.MethodPost, "/auth/openid/callback", nil))
	require.Equal(t, http.StatusOK, w2.Code)
	require.Contains(t, w2.Body.String(), "discovery_probe")
	require.NotContains(t, w2.Body.String(), "token")
}
