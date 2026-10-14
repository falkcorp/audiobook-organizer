// file: internal/server/flight_recorder_test.go
// version: 1.1.0
// guid: 2871ad47-cc1c-48ee-a8a8-51b2ac987666
// last-edited: 2026-10-14

package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	diagflight "github.com/falkcorp/audiobook-organizer/internal/diag/flight"
	"github.com/falkcorp/audiobook-organizer/internal/serviceregistry"
	"github.com/stretchr/testify/require"
)

// A stall reported by the search-index check asks the flight recorder for one
// snapshot; a pass without a stall asks for none.
func TestSearchIndexStallRequestsSnapshot(t *testing.T) {
	snap := newMockTraceSnapshotter(t)
	snap.EXPECT().Snapshot("search-index-stall").Return().Once()
	s := &Server{traceSnap: snap}

	s.searchIndexWatchdogTick(func() bool { return false })
	s.searchIndexWatchdogTick(func() bool { return true })
	// mockery's cleanup asserts exactly one call.
}

// The strike hook maps the registry's kinds onto reason strings that use '-'
// only (the recorder rejects anything else as "unknown").
func TestWatchdogStrikeHookReasons(t *testing.T) {
	snap := newMockTraceSnapshotter(t)
	snap.EXPECT().Snapshot("watchdog-stuck").Return().Once()
	snap.EXPECT().Snapshot("watchdog-never-reported").Return().Once()
	s := &Server{traceSnap: snap}

	s.requestWatchdogTrace("stuck")
	s.requestWatchdogTrace("never_reported")
}

// The production strike hook is installed by wireServerFromContainer, on the
// registry the container builds, not by anything a test sets up. This builds a
// server through NewServer (setupTestServer), swaps only the snapshot target
// for a mock, and fires the registry's strike hook the way its watchdog does:
// the mock must see the request. Deleting the SetWatchdogStrikeHook call in
// registry_wire.go makes this fail; the registry's own tests cannot see that,
// because they install their own hook.
func TestNewServerInstallsWatchdogStrikeHook(t *testing.T) {
	srv, cleanup := setupTestServer(t)
	defer cleanup()
	require.NotNil(t, srv.opRegistry, "the container must have built the ops registry")

	snap := newMockTraceSnapshotter(t)
	snap.EXPECT().Snapshot("watchdog-stuck").Return().Once()
	snap.EXPECT().Snapshot("watchdog-never-reported").Return().Once()
	srv.traceSnap = snap

	srv.opRegistry.NotifyWatchdogStrikeForTest("stuck")
	srv.opRegistry.NotifyWatchdogStrikeForTest("never_reported")
}

// A Start that fails after the recorder is up must not leave the runtime
// recorder tracing: only one may be active per process, so a recorder that can
// start afterwards proves the first one was stopped.
func TestStartErrorStopsFlightRecorder(t *testing.T) {
	t.Setenv(diagflight.EnvSwitch, "")
	dir := t.TempDir()
	rec := diagflight.New(diagflight.Config{Dir: filepath.Join(dir, "traces")})
	// An unbuilt container: Start fails at "container.Start", after the
	// recorder has been started.
	s := &Server{flight: rec, container: serviceregistry.NewContainer(), bgCtx: t.Context()}

	require.Error(t, s.Start(ServerConfig{}))

	next := diagflight.New(diagflight.Config{Dir: filepath.Join(dir, "next")})
	require.NoError(t, next.Start(), "the failed Start left its flight recorder running")
	next.Stop()
}

// A Server with no recorder (built without NewServer) must not panic on
// either watchdog path or at shutdown.
func TestFlightRecorderNilSafe(t *testing.T) {
	s := &Server{}
	s.searchIndexWatchdogTick(func() bool { return true })
	s.requestWatchdogTrace("stuck")
	s.flight.Stop()
}

// The traces routes are behind settings.manage: a viewer gets 403, an admin
// lists and downloads. Driven through the real router, since a route
// registered without s.perm(...) is what a handler-level test cannot see.
func TestDiagnosticsTracesRoutes_Permissions(t *testing.T) {
	srv, adminToken, viewerToken, _, cleanup := setupUserTagsAuthzTestServer(t)
	defer cleanup()

	dir := filepath.Join(filepath.Dir(config.AppConfig.DatabasePath), "traces")
	require.NoError(t, os.MkdirAll(dir, 0o750))
	const name = "20261014T010203Z-watchdog-stuck.trace"
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte("go 1.27 trace"), 0o600))

	get := func(path, token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		srv.router.ServeHTTP(w, req)
		return w
	}

	for _, path := range []string{"/api/v1/diagnostics/traces", "/api/v1/diagnostics/traces/" + name} {
		w := get(path, viewerToken)
		require.Equal(t, http.StatusForbidden, w.Code, "viewer on %s; body=%s", path, w.Body.String())
		w = get(path, "")
		require.Equal(t, http.StatusUnauthorized, w.Code, "anonymous on %s; body=%s", path, w.Body.String())
	}

	w := get("/api/v1/diagnostics/traces", adminToken)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var body struct {
		Data struct {
			Traces []struct {
				Name string `json:"name"`
				Size int64  `json:"size"`
			} `json:"traces"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	require.Len(t, body.Data.Traces, 1)
	require.Equal(t, name, body.Data.Traces[0].Name)

	w = get("/api/v1/diagnostics/traces/"+name, adminToken)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, "go 1.27 trace", w.Body.String())
	require.Equal(t, "application/octet-stream", w.Header().Get("Content-Type"))
}
