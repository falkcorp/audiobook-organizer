// file: internal/server/wire_operations_routes_test.go
// version: 1.0.0
// guid: e2b4944d-3a48-4ca4-adcf-5388b511dedc
// last-edited: 2026-08-22

package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// schedulerAdminRoutes is the table over the 6 task/maintenance-window routes
// TASK-155 moved from operations.Handler to handlers.SchedulerHandler. Paths,
// methods, and permission guards are unchanged from before the move (see
// internal/server/wire_operations_routes.go); what changed is which Go type
// answers each one.
var schedulerAdminRoutes = []struct {
	method  string
	path    string
	handler string // substring of the *SchedulerHandler method that must own this route
}{
	{http.MethodGet, "/api/v1/tasks", "ListTasks"},
	{http.MethodPost, "/api/v1/tasks/:name/run", "RunTask"},
	{http.MethodPut, "/api/v1/tasks/:name", "UpdateTaskConfig"},
	{http.MethodPost, "/api/v1/maintenance-window/run", "RunMaintenanceWindowNow"},
	{http.MethodGet, "/api/v1/maintenance-window/status", "GetMaintenanceWindowStatus"},
	{http.MethodPut, "/api/v1/maintenance-window/config", "UpdateMaintenanceWindowConfig"},
}

// TestSchedulerAdminRoutes_WiredToSchedulerHandler is the wiring-level guard
// TASK-155's brief asked for. The handler-level tests in
// internal/server/handlers/scheduler_admin_test.go call h.ListTasks etc.
// directly on a bare gin.New() and cannot see whether wireOperationsRoutes
// actually points these 6 paths at the new handler -- exactly the lesson
// wire_abs_routes_test.go documents for its own route table ("the only
// complete oracle is the flattened router: s.router.Routes()"). If schedulerH
// were dropped from the wireOperationsRoutes call, or one of these 6 routes
// were left on (or reverted to) operationsH, every handler-level test would
// stay green while production either 404s or answers from the wrong type.
//
// gin.Engine.Routes() reports, per route, the LAST handler in its chain
// (root.handlers.Last() in gin's iterate()) -- the permission middleware
// wrapping each route does not shadow this check.
func TestSchedulerAdminRoutes_WiredToSchedulerHandler(t *testing.T) {
	s, cleanup := setupTestServer(t)
	defer cleanup()

	routes := s.router.Routes()
	for _, want := range schedulerAdminRoutes {
		var got string
		var found bool
		for _, ri := range routes {
			if ri.Method == want.method && ri.Path == want.path {
				got = ri.Handler
				found = true
				break
			}
		}
		require.True(t, found, "%s %s must be registered", want.method, want.path)
		require.Contains(t, got, "SchedulerHandler",
			"%s %s must be handled by SchedulerHandler, got %q", want.method, want.path, got)
		require.Contains(t, got, want.handler,
			"%s %s must be handled by SchedulerHandler.%s, got %q", want.method, want.path, want.handler, got)
		// The route must not still resolve to the retired operations.Handler
		// method of the same name -- that package still declares (unused)
		// Scheduler plumbing kept only for New()'s constructor-signature
		// compatibility (see operations/handler.go), so a stale registration
		// left pointing at operationsH would compile and read identically to
		// a casual reviewer.
		require.NotContains(t, got, "operations.(*Handler)",
			"%s %s must not still be wired to the legacy operations.Handler, got %q", want.method, want.path, got)
	}
}

// TestSchedulerAdminRoutes_DispatchThroughRealRouter exercises each of the 6
// moved routes end to end through s.router.ServeHTTP -- the permission-guard
// chain, gin param binding, and the handler body -- rather than calling the
// handler method directly as the handler-level tests do. setupTestServer does
// not call Start(), so s.scheduler is nil (the lazy-provider closure resolves
// it at request time; see wire_handlers.go) and the 4 scheduler-backed routes
// answer 500 "scheduler not initialized". The 2 config-only routes
// (UpdateTaskConfig, UpdateMaintenanceWindowConfig) do not read the
// scheduler and answer 200. Both outcomes are asserted, together with a
// negative case per route where relevant, so a route silently falling
// through to a 404 (a dropped registration) or a 403 (a changed permission
// guard) is caught here rather than only "some handler ran."
func TestSchedulerAdminRoutes_DispatchThroughRealRouter(t *testing.T) {
	s, cleanup := setupTestServer(t)
	defer cleanup()

	cases := []struct {
		name       string
		method     string
		path       string
		body       string
		wantStatus int
		wantKey    string // top-level envelope key expected: "data" or "error"
	}{
		{"ListTasks_NoLiveScheduler", http.MethodGet, "/api/v1/tasks", "", http.StatusInternalServerError, "error"},
		{"RunTask_NoLiveScheduler", http.MethodPost, "/api/v1/tasks/library_scan/run", "", http.StatusInternalServerError, "error"},
		{"UpdateTaskConfig_UnknownTask", http.MethodPut, "/api/v1/tasks/does-not-exist", `{"enabled":true}`, http.StatusBadRequest, "error"},
		{"UpdateTaskConfig_KnownTask", http.MethodPut, "/api/v1/tasks/library_scan", `{"enabled":true}`, http.StatusOK, "data"},
		{"RunMaintenanceWindowNow_NoLiveScheduler", http.MethodPost, "/api/v1/maintenance-window/run", "", http.StatusInternalServerError, "error"},
		{"GetMaintenanceWindowStatus_NoLiveScheduler", http.MethodGet, "/api/v1/maintenance-window/status", "", http.StatusInternalServerError, "error"},
		{"UpdateMaintenanceWindowConfig_InvalidHour", http.MethodPut, "/api/v1/maintenance-window/config", `{"enabled":true,"window_start":99,"window_end":6}`, http.StatusBadRequest, "error"},
		{"UpdateMaintenanceWindowConfig_Valid", http.MethodPut, "/api/v1/maintenance-window/config", `{"enabled":true,"window_start":2,"window_end":6}`, http.StatusOK, "data"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			if tc.body != "" {
				req.Header.Set("Content-Type", "application/json")
			}
			w := httptest.NewRecorder()
			s.router.ServeHTTP(w, req)

			require.Equal(t, tc.wantStatus, w.Code, "unexpected status for %s %s: body=%s", tc.method, tc.path, w.Body.String())

			var envelope map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &envelope), "response must be valid JSON: %s", w.Body.String())
			_, ok := envelope[tc.wantKey]
			require.True(t, ok, "expected top-level %q key in response, got %s", tc.wantKey, w.Body.String())
		})
	}
}
