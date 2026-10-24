// file: internal/server/repairs_op_access_wiring_test.go
// version: 1.0.0
// guid: 0c6b1f84-93d7-4e52-a8b0-d2f5e7a14c39
// last-edited: 2026-10-24

// The generic operations routes as the server wires them, driven through the
// real router: a Repairs operation's data (repairs.plan, repairs.apply) is
// readable only by a caller who holds the op's fixer's permissions, however it
// is reached (result, change journal, undo report, single-op read, log reads,
// timeline, event stream). Other operations are unchanged for a viewer.

package server

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
)

const (
	opScopedPlan    = "op-repairs-plan-scoped"    // fixer needing settings.manage
	opDefaultApply  = "op-repairs-apply-default"  // fixer needing library.edit_metadata
	opNoFixerApply  = "op-repairs-apply-nofixer"  // a repairs op that states no fixer
	opUnknownFixer  = "op-repairs-apply-unknown"  // a fixer this build does not register
	opPlainScan     = "op-plain-scan"             // not a repairs op
	scopedFixerID   = "reconcile.missing-files"   // settings.manage (repairs_permission_wiring_test.go)
	secondaryUserID = "user-whose-state-was-read" // appears in the repairs rows and logs
)

// repairsOpAccessFixture seeds the five operations above with a result, a log
// line and (for the repairs ones) a body naming secondaryUserID, and returns
// the server with a session for each role.
type repairsOpAccessFixture struct {
	srv                        *Server
	viewer, editor, admin      string
	defaultFixerID             string
	scopedPlan, defaultApply   string
	noFixerApply, unknownFixer string
	plainScan                  string
	allRepairsOps              []string
}

func newRepairsOpAccessFixture(t *testing.T) *repairsOpAccessFixture {
	t.Helper()
	srv, editor, admin := repairsPermServer(t, true, true)
	store, ok := srv.storeForWiring().(*database.PebbleStore)
	require.True(t, ok, "the test server runs on a PebbleStore")
	vu, err := store.CreateUser("viewer", "viewer@x.test", "bcrypt", "hash", []string{auth.SeedRoleViewer}, "active")
	require.NoError(t, err)
	vs, err := store.CreateSession(vu.ID, "192.0.2.10", "test", time.Hour)
	require.NoError(t, err)

	// A fixer whose only requirement is the lane's default permission.
	require.NotNil(t, srv.repairFixers)
	defaultFixer := ""
	for _, f := range srv.repairFixers.List() {
		if p := repairs.RequiredPermissions(f); len(p) == 1 && p[0] == repairs.DefaultPermission {
			defaultFixer = f.ID()
			break
		}
	}
	require.NotEmpty(t, defaultFixer, "no registered fixer uses the default permission")
	scoped, ok := srv.repairFixers.Get(scopedFixerID)
	require.True(t, ok)
	require.Contains(t, repairs.RequiredPermissions(scoped), auth.PermSettingsManage)

	now := time.Now().UTC()
	body := func(fixer string) string {
		return `{"fixer_id":"` + fixer + `","results":[{"row_id":"r1","owner_user_id":"` + secondaryUserID + `"}]}`
	}
	insert := func(id, def, params, result string) {
		res := result
		require.NoError(t, store.InsertOperationV2(database.OperationV2Row{
			ID: id, DefID: def, Plugin: "test", Status: "completed", Params: params,
			ResultData: &res, QueuedAt: now, CompletedAt: &now,
		}))
		require.NoError(t, store.AppendOpLogsV2([]database.OpLogV2Row{{
			OperationID: id, Level: "info", Message: "row ran for " + secondaryUserID, CreatedAt: now,
		}}))
	}
	insert(opScopedPlan, repairs.PlanOpID, `{"fixer_id":"`+scopedFixerID+`"}`, body(scopedFixerID))
	insert(opDefaultApply, repairs.ApplyOpID, `{"fixer_id":"`+defaultFixer+`"}`, body(defaultFixer))
	insert(opNoFixerApply, repairs.ApplyOpID, `{}`, `{"results":[{"owner_user_id":"`+secondaryUserID+`"}]}`)
	insert(opUnknownFixer, repairs.ApplyOpID, `{"fixer_id":"fixer.removed-since"}`, body("fixer.removed-since"))
	insert(opPlainScan, "library.scan", `{}`, `{"scanned":3}`)
	return &repairsOpAccessFixture{
		srv: srv, viewer: vs.ID, editor: editor, admin: admin, defaultFixerID: defaultFixer,
		scopedPlan: opScopedPlan, defaultApply: opDefaultApply, noFixerApply: opNoFixerApply,
		unknownFixer: opUnknownFixer, plainScan: opPlainScan,
		allRepairsOps: []string{opScopedPlan, opDefaultApply, opNoFixerApply, opUnknownFixer},
	}
}

func getAs(srv *Server, token, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)
	return w
}

// The read routes that take an op id, for one op. Each is a 200 for a caller
// who may read the op and a 403 for one who may not.
func opReadPaths(id string) map[string]string {
	return map[string]string{
		"result":         "/api/v1/operations/" + id + "/result",
		"changes":        "/api/v1/operations/" + id + "/changes",
		"undo preflight": "/api/v1/operations/" + id + "/undo/preflight",
		"v2 op":          "/api/v1/operations/v2/" + id,
		"log download":   "/api/v1/operations/v2/" + id + "/logs/download",
	}
}

// TestRepairsOpData_ReadRoutesNeedTheFixersPermissions: by id, a viewer is
// refused every repairs op on every read route and nothing of the op leaks in
// the refusal; an editor reads the default-permission fixer's op but not the
// settings.manage fixer's; an admin reads every op whose fixer is known and
// stated; an op that states no fixer is refused to everyone (fail closed); and
// a plain op is a 200 for a viewer on every route.
// Not parallel: it swaps the process-wide config.
func TestRepairsOpData_ReadRoutesNeedTheFixersPermissions(t *testing.T) {
	f := newRepairsOpAccessFixture(t)
	for _, tc := range []struct {
		who, token string
		op         string
		allowed    bool
	}{
		{"viewer", f.viewer, f.scopedPlan, false},
		{"viewer", f.viewer, f.defaultApply, false},
		{"viewer", f.viewer, f.noFixerApply, false},
		{"viewer", f.viewer, f.unknownFixer, false},
		{"viewer", f.viewer, f.plainScan, true},
		{"editor", f.editor, f.defaultApply, true},
		{"editor", f.editor, f.scopedPlan, false},
		{"editor", f.editor, f.unknownFixer, false},
		{"admin", f.admin, f.scopedPlan, true},
		{"admin", f.admin, f.defaultApply, true},
		{"admin", f.admin, f.unknownFixer, true},
		{"admin", f.admin, f.noFixerApply, false},
		{"admin", f.admin, f.plainScan, true},
	} {
		for route, path := range opReadPaths(tc.op) {
			w := getAs(f.srv, tc.token, path)
			if tc.allowed {
				require.NotEqual(t, http.StatusForbidden, w.Code, "%s %s %s: %s", tc.who, tc.op, route, w.Body.String())
				continue
			}
			require.Equal(t, http.StatusForbidden, w.Code, "%s %s %s: %s", tc.who, tc.op, route, w.Body.String())
			require.NotContains(t, w.Body.String(), secondaryUserID, "%s %s %s", tc.who, tc.op, route)
		}
		if tc.allowed && tc.op != f.plainScan {
			w := getAs(f.srv, tc.token, "/api/v1/operations/"+tc.op+"/result")
			require.Equal(t, http.StatusOK, w.Code, "%s %s result: %s", tc.who, tc.op, w.Body.String())
			require.Contains(t, w.Body.String(), secondaryUserID, "the permitted caller reads the result")
		}
	}
	w := getAs(f.srv, f.viewer, "/api/v1/operations/"+f.plainScan+"/result")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), `"scanned":3`)
}

// TestRepairsOpData_TimelineLeavesOutWhatTheCallerMayNotRead: the timeline
// lists the repairs ops the caller may read and no others, and counts only
// those; a viewer sees the plain op alone, an admin sees every op whose fixer
// is stated.
// Not parallel: it swaps the process-wide config.
func TestRepairsOpData_TimelineLeavesOutWhatTheCallerMayNotRead(t *testing.T) {
	f := newRepairsOpAccessFixture(t)
	ids := func(token string) []string {
		w := getAs(f.srv, token, "/api/v1/operations/timeline?since=24h&limit=100")
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		var out []string
		for _, id := range []string{f.scopedPlan, f.defaultApply, f.noFixerApply, f.unknownFixer, f.plainScan} {
			if strings.Contains(w.Body.String(), `"`+id+`"`) {
				out = append(out, id)
			}
		}
		return out
	}
	require.ElementsMatch(t, []string{f.plainScan}, ids(f.viewer))
	require.ElementsMatch(t, []string{f.defaultApply, f.plainScan}, ids(f.editor))
	require.ElementsMatch(t, []string{f.scopedPlan, f.defaultApply, f.unknownFixer, f.plainScan}, ids(f.admin))

	w := getAs(f.srv, f.viewer, "/api/v1/operations/timeline?since=24h&limit=100")
	require.Contains(t, w.Body.String(), `"matched":1`, "the viewer's count covers only what the viewer may see")
}

// syncRecorder is a ResponseWriter a test can read while the handler writes.
type syncRecorder struct {
	mu   sync.Mutex
	hdr  http.Header
	buf  bytes.Buffer
	code int
}

func (r *syncRecorder) Header() http.Header { return r.hdr }
func (r *syncRecorder) WriteHeader(c int)   { r.mu.Lock(); r.code = c; r.mu.Unlock() }
func (r *syncRecorder) Flush()              {}
func (r *syncRecorder) Write(b []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.buf.Write(b)
}
func (r *syncRecorder) body() string { r.mu.Lock(); defer r.mu.Unlock(); return r.buf.String() }

// eventsSeenBy opens /operations/events as token, publishes op.log, op.created
// and op.terminal for every seeded op, and returns the stream once the
// plain op's last event has arrived (it is published last).
func eventsSeenBy(t *testing.T, f *repairsOpAccessFixture, token string) string {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/api/v1/operations/events", nil).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := &syncRecorder{hdr: http.Header{}}
	done := make(chan struct{})
	go func() { defer close(done); f.srv.router.ServeHTTP(rec, req) }()
	require.Eventually(t, func() bool { return strings.Contains(rec.body(), ": heartbeat") },
		5*time.Second, 5*time.Millisecond, "the stream never opened: %s", rec.body())

	ops := append(append([]string{}, f.allRepairsOps...), f.plainScan)
	for _, id := range ops {
		def, msg := repairs.ApplyOpID, "row ran for "+secondaryUserID
		if id == f.plainScan {
			def, msg = "library.scan", "scanning"
		}
		require.NoError(t, f.srv.opHub.Publish(ctx, "op.created", map[string]any{"op_id": id, "def_id": def}))
		require.NoError(t, f.srv.opHub.Publish(ctx, "op.log", map[string]any{"op_id": id, "message": msg}))
		require.NoError(t, f.srv.opHub.Publish(ctx, "op.updated", map[string]any{"op_id": id, "progress_current": 1}))
	}
	// A marker event naming no op passes for everyone: it proves the stream
	// had drained everything published before it.
	require.NoError(t, f.srv.opHub.Publish(ctx, "op.marker", map[string]any{"note": "end"}))
	require.Eventually(t, func() bool { return strings.Contains(rec.body(), "op.marker") },
		5*time.Second, 5*time.Millisecond, "the marker never arrived: %s", rec.body())
	cancel()
	<-done
	return rec.body()
}

// TestRepairsOpData_EventStreamLeavesOutWhatTheCallerMayNotRead: a viewer's
// stream carries the plain op's events and none of any repairs op's (not the
// op id, not the log line naming a user); an admin's carries every op whose
// fixer is stated.
// Not parallel: it swaps the process-wide config.
func TestRepairsOpData_EventStreamLeavesOutWhatTheCallerMayNotRead(t *testing.T) {
	f := newRepairsOpAccessFixture(t)
	viewer := eventsSeenBy(t, f, f.viewer)
	require.Contains(t, viewer, f.plainScan)
	for _, id := range f.allRepairsOps {
		require.NotContains(t, viewer, id, "the viewer's stream names a repairs op")
	}
	require.NotContains(t, viewer, secondaryUserID)

	admin := eventsSeenBy(t, f, f.admin)
	for _, id := range []string{f.scopedPlan, f.defaultApply, f.unknownFixer, f.plainScan} {
		require.Contains(t, admin, id)
	}
	require.NotContains(t, admin, f.noFixerApply, "an op with no fixer is withheld from everyone")
}

// TestWireHandlers_RepairsOpAccessIsInstalled: both generic handlers were
// given the check. Without it they refuse every repairs op (fail closed), so
// an admin reading a stated-fixer op is the oracle that the wiring is there.
// Not parallel: it swaps the process-wide config.
func TestWireHandlers_RepairsOpAccessIsInstalled(t *testing.T) {
	f := newRepairsOpAccessFixture(t)
	for _, path := range []string{
		"/api/v1/operations/" + f.scopedPlan + "/result", // operations.Handler
		"/api/v1/operations/v2/" + f.scopedPlan,          // OperationsV2Handler
	} {
		w := getAs(f.srv, f.admin, path)
		require.Equal(t, http.StatusOK, w.Code, "%s: %s", path, w.Body.String())
	}
}
