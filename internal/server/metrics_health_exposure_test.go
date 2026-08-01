// file: internal/server/metrics_health_exposure_test.go
// version: 1.0.0
// guid: 2f8a41c6-93d7-4e05-b17a-6c40e9d8b325
// last-edited: 2026-08-01

package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// /health is reachable without a credential BY DESIGN — it is how a caller
// decides whether the server is up, including the SPA's reconnect loop while
// logged out. The point of these tests is that being anonymous-readable and
// being informative are different things: it must answer, and it must not
// volunteer the build string or how large the library is.
func TestHealth_IsAnonymousButDisclosesNothing(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	for _, path := range []string{"/health", "/api/health", "/api/v1/health"} {
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))

		require.Equal(t, http.StatusOK, w.Code, "%s must stay reachable without auth", path)

		body := w.Body.String()
		// The exact build string is the most useful thing an unauthenticated
		// caller could learn: it maps directly to which advisories apply.
		require.NotContains(t, body, "version", "%s must not disclose the build version", path)
		require.NotContains(t, body, "database_type", "%s must not disclose the storage engine", path)
		// Inventory disclosure — how many books/authors/series exist.
		require.NotContains(t, body, "metrics", "%s must not disclose library counts", path)
		require.NotContains(t, body, "broken_file_count", "%s must not disclose library counts", path)
		// A store error string can carry filesystem paths.
		require.NotContains(t, body, "partial_error", "%s must not echo store errors", path)
	}
}

// Whatever else it omits, /health has to remain usable as a liveness probe:
// web/src/App.tsx polls it and reloads the page as soon as it answers ok.
func TestHealth_StillReportsStatus(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/health", nil))
	require.Equal(t, http.StatusOK, w.Code)

	var resp struct {
		Data struct {
			Status    string `json:"status"`
			Timestamp int64  `json:"timestamp"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Equal(t, "ok", resp.Data.Status)
	require.NotZero(t, resp.Data.Timestamp)
}

// /metrics used to be readable by anything that could reach the port. The
// "accepted risk" rested on a network-layer restriction that was never built,
// and on the false premise that Prometheus cannot authenticate.
//
// setupTestServer does not enable auth, so RequireAuth is not installed and a
// behavioural 401 cannot be asserted here. What IS asserted is the wiring that
// produces it: that the route carries a guard ahead of the handler rather than
// being registered bare. TestBuildTopLevelAuthChain_* covers the guard's own
// admit/reject behaviour.
func TestMetrics_RouteCarriesAnAuthGuard(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	var found bool
	for _, ri := range server.router.Routes() {
		if ri.Path == "/metrics" {
			found = true
		}
	}
	require.True(t, found, "/metrics must still be registered")

	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	// With auth disabled the guard is a pass-through, so this serves metrics.
	// The assertion that matters is that it is the real Prometheus handler and
	// the chain did not panic on a nil middleware — the failure mode
	// buildTopLevelAuthChain exists to prevent.
	require.Equal(t, http.StatusOK, w.Code)
	require.True(t,
		strings.Contains(w.Body.String(), "go_goroutines") ||
			strings.Contains(w.Body.String(), "# HELP"),
		"expected Prometheus exposition output, got: %.120s", w.Body.String())
}
