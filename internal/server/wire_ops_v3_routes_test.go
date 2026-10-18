// file: internal/server/wire_ops_v3_routes_test.go
// version: 1.2.0
// guid: f0aca62d-8230-49ce-ab21-61cbd082ce99
// last-edited: 2026-10-18

package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database/mocks"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/scheduler"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers/opsv3"
)

// TestOpsV3Routes_WiredAndServed checks the oracle the handler tests cannot:
// that setupRoutes registers every /api/v3/ops route on the real router, with
// the opsv3 handler, and that the /api/* -> /api/v1/* compatibility redirect
// leaves them alone.
func TestOpsV3Routes_WiredAndServed(t *testing.T) {
	s, cleanup := setupTestServer(t)
	defer cleanup()

	want := []struct{ method, path string }{
		{http.MethodGet, "/api/v3/ops/defs"},
		{http.MethodGet, "/api/v3/ops/defs/:id"},
		{http.MethodPost, "/api/v3/ops/defs/:id/runs"},
		{http.MethodGet, "/api/v3/ops/runs"},
		{http.MethodPost, "/api/v3/ops/runs"},
		{http.MethodGet, "/api/v3/ops/runs/:id"},
		{http.MethodGet, "/api/v3/ops/runs/:id/rows"},
		{http.MethodPost, "/api/v3/ops/runs/:id/cancel"},
		{http.MethodPost, "/api/v3/ops/runs/:id/approve"},
		{http.MethodGet, "/api/v3/ops/counts"},
		{http.MethodGet, "/api/v3/ops/timeline"},
		{http.MethodGet, "/api/v3/ops/events"},
	}
	routes := s.router.Routes()
	for _, w := range want {
		var handler string
		for _, ri := range routes {
			if ri.Method == w.method && ri.Path == w.path {
				handler = ri.Handler
			}
		}
		require.NotEmpty(t, handler, "%s %s must be registered", w.method, w.path)
		require.Contains(t, handler, "opsv3", "%s %s handler = %q", w.method, w.path, handler)
	}

	for _, path := range []string{"/api/v3/ops/defs", "/api/v3/ops/counts?group_by=def", "/api/v3/ops/runs", "/api/v3/ops/timeline"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		s.router.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code, "%s: %s", path, rec.Body.String())
		require.True(t, json.Valid(rec.Body.Bytes()), path)
	}
}

// previewOnlyElsewhere are writing ops whose omitted mode writes nothing
// (write_op_modes.golden records them `preview`) but which have no params key
// /api/v3/ops can drive a preview with, so they declare no Preview and start
// in v3 only with an explicit mode "live":
var previewOnlyElsewhere = map[string]string{
	// No dry_run: its preview is a different op,
	// metadata.bulk-apply-preview (bulk_apply_preview.go), reached through
	// POST /metadata/bulk-apply-preview. A v3 "preview" of this op used to
	// apply metadata and write tags.
	"metadata.batch-apply-cached": "preview is the separate metadata.bulk-apply-preview op",
	// Persists candidate rows on every run; no mode key.
	"metadata.candidate-fetch": "no mode key; fetches and stores candidates on every run",
	// apply gates only the clone mode: rollback=true or repair=true write
	// with apply omitted (itunes_clone_into_library.go, `writes := modes == 1`),
	// so apply=false is not a preview.
	"itunes.clone-into-library": "apply gates only one of three write modes",
	// write := p.Apply && !dryRun: dry_run=false alone writes nothing, so a
	// dry_run preview would be honoured but its live run would silently
	// preview (maintenance.ExecutionPolicy.ExtraWriteKey).
	"maintenance.repoint-version-primary": "writes need apply=true and dry_run=false",
}

// TestOpsV3_PreviewDeclarationsMatchTheOpsParams checks every registered op's
// Preview declaration against the op's code, not the ledger:
//
//   - a declaration names a bool field of the params type the op decodes
//     (PreviewSpec.Check, by reflection), and that type decodes the injected
//     key under DisallowUnknownFields, so a strict decoder accepts what v3
//     writes;
//   - /defs reports exactly the declared key for a writer, and no preview for
//     a writer that declares none (fail closed);
//   - every writing op is classified: one the ledger records `preview` either
//     declares a key or is listed in previewOnlyElsewhere with the reason,
//     and one recorded `no-mode` declares none.
func TestOpsV3_PreviewDeclarationsMatchTheOpsParams(t *testing.T) {
	srv, _, _ := bootRegisteredOpIDs(t)
	ledger := readWriteOpModes(t)
	defs := map[string]opsv3.DefInfo{}
	for _, d := range (&opsV3Defs{reg: srv.opRegistry}).ListDefs() {
		defs[d.ID] = d
	}
	require.NotEmpty(t, defs)
	declared := map[string]int{}
	usedElsewhere := map[string]bool{}
	for _, od := range srv.opRegistry.ActiveDefs() {
		d, ok := defs[od.ID]
		require.True(t, ok, "%s missing from the v3 catalog", od.ID)
		p := od.Preview
		if !p.IsZero() {
			require.NoError(t, p.Check(), "%s: Preview does not match its params type", od.ID)
			require.True(t, canWrite(od), "%s declares a preview but cannot write", od.ID)
			strictDecodes(t, od.ID, p)
			require.Equal(t, string(p.Key), d.PreviewParam, "%s: /defs preview_param", od.ID)
			require.NotContains(t, d.UnreadModeKeys, string(p.Key), od.ID)
			declared[string(p.Key)]++
		} else if d.Writer {
			require.Empty(t, d.PreviewParam, "%s declares no preview, so it must have none", od.ID)
		}
		if !canWrite(od) || isMaintenanceJobOp(od.ID) {
			continue
		}
		switch ledger[od.ID] {
		case "no-mode":
			require.True(t, p.IsZero(), "%s: the ledger says it always writes, yet it declares preview key %q; fix whichever is wrong", od.ID, p.Key)
		case "preview":
			if p.IsZero() {
				_, listed := previewOnlyElsewhere[od.ID]
				require.True(t, listed, "%s: the ledger says omitted mode writes nothing, but it declares no Preview. Declare the key its Run gates on (OperationDef.Preview), or list it in previewOnlyElsewhere with the reason", od.ID)
				usedElsewhere[od.ID] = true
			}
		}
	}
	for id := range previewOnlyElsewhere {
		if strings.HasPrefix(id, "maintenance.") && isMaintenanceJobOp(id) {
			od, ok := srv.opRegistry.Def(id)
			require.True(t, ok, id)
			require.True(t, od.Preview.IsZero(), "%s is listed as previewing elsewhere but declares %q", id, od.Preview.Key)
			continue
		}
		require.True(t, usedElsewhere[id], "previewOnlyElsewhere lists %s, which is not a registered ledger-preview op without a declaration; delete the entry", id)
	}
	for _, k := range []string{"dry_run", "apply", "live"} {
		require.Positive(t, declared[k], "no registered op declares %s; the check did not run", k)
	}
}

// strictDecodes: the declared params type accepts {"<key>": true} and
// {"<key>": false} under DisallowUnknownFields.
func strictDecodes(t *testing.T, id string, p opsregistry.PreviewSpec) {
	t.Helper()
	typ := reflect.TypeOf(p.Params)
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	for _, v := range []string{"true", "false"} {
		dec := json.NewDecoder(strings.NewReader(`{"` + string(p.Key) + `":` + v + `}`))
		dec.DisallowUnknownFields()
		require.NoError(t, dec.Decode(reflect.New(typ).Interface()), "%s: a strict decode of the injected %s fails", id, p.Key)
	}
}

// TestOpsV3Routes_NoPreviewOpRefusesPreview drives the real router: a
// writing op without a preview shows has_preview=false and a preview (or no
// mode) start is a 409, so it is never queued live under a preview label.
func TestOpsV3Routes_NoPreviewOpRefusesPreview(t *testing.T) {
	s, cleanup := setupTestServer(t)
	defer cleanup()
	req := httptest.NewRequest(http.MethodGet, "/api/v3/ops/defs", nil)
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var list struct {
		Items []struct {
			ID         string `json:"id"`
			HasPreview *bool  `json:"has_preview"`
		} `json:"items"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))
	target := ""
	for _, d := range list.Items {
		if d.HasPreview != nil && !*d.HasPreview {
			target = d.ID
			break
		}
	}
	require.NotEmpty(t, target, "no writing op without a preview is registered on the test server")
	for _, body := range []string{`{"mode":"preview"}`, `{}`} {
		req := httptest.NewRequest(http.MethodPost, "/api/v3/ops/defs/"+target+"/runs", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		s.router.ServeHTTP(rec, req)
		require.Equal(t, http.StatusConflict, rec.Code, "%s %s: %s", target, body, rec.Body.String())
	}
}

// TestOpsV3Routes_DefsShowTheSchedulerStartAssigns: setupRoutes runs before
// Start() assigns s.scheduler, so the v3 routes must resolve it per request.
// The scheduler is assigned after the routes exist, exactly as Start does,
// and /defs must then show each schedule and its next fire.
func TestOpsV3Routes_DefsShowTheSchedulerStartAssigns(t *testing.T) {
	s, cleanup := setupTestServer(t)
	defer cleanup()
	require.Nil(t, s.scheduler, "the test server must not have a scheduler before the routes are built")

	yes := func() bool { return true }
	ts := scheduler.NewTaskScheduler(scheduler.SchedulerDeps{
		Store:               func() scheduler.SchedulerStore { return nil },
		OpRegistry:          s.opRegistry,
		HasDedupPlugin:      yes,
		HasMetadataFetchSvc: yes,
		HasActivitySvc:      yes,
		HasBatchPoller:      yes,
	})
	require.NoError(t, ts.RegisterSchedules(s.opRegistry.Catalog()))
	s.scheduler = ts
	t.Cleanup(func() { s.scheduler = nil })

	want := map[string]scheduler.ScheduleStatus{}
	withNext := 0
	for _, st := range ts.ScheduleStatuses() {
		want[st.DefID] = st
		if st.NextFire != nil {
			withNext++
		}
	}
	require.NotEmpty(t, want, "the catalog registered no schedules")
	require.Positive(t, withNext, "no schedule has a next fire to show")

	req := httptest.NewRequest(http.MethodGet, "/api/v3/ops/defs", nil)
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var list struct {
		Items []struct {
			ID       string `json:"id"`
			Schedule struct {
				Kind     string     `json:"kind"`
				NextFire *time.Time `json:"next_fire"`
			} `json:"schedule"`
		} `json:"items"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))
	shown, shownNext := 0, 0
	for _, d := range list.Items {
		st, ok := want[d.ID]
		if !ok {
			require.Equal(t, "manual", d.Schedule.Kind, d.ID)
			continue
		}
		shown++
		require.Equal(t, st.Kind, d.Schedule.Kind, "%s schedule kind", d.ID)
		require.Equal(t, st.NextFire != nil, d.Schedule.NextFire != nil, "%s next_fire", d.ID)
		if d.Schedule.NextFire != nil {
			shownNext++
		}
	}
	require.Equal(t, len(want), shown, "every scheduled def is listed with its schedule")
	require.Equal(t, withNext, shownNext)
}

// TestOpsV3Routes_EventStreamsEndOnShutdownAndStartLazily: building the
// server starts no event loop (it starts with the first stream), and
// closeOpsV3Streams, which Shutdown calls before the graceful HTTP shutdown,
// ends an open stream at once instead of after the 30s timeout.
func TestOpsV3Routes_EventStreamsEndOnShutdownAndStartLazily(t *testing.T) {
	s, cleanup := setupTestServer(t)
	defer cleanup()
	require.NotContains(t, s.bgWG.Running(), "ops-v3-events", "the broker loop must not run before anyone streams")

	srv := httptest.NewServer(s.router)
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/v3/ops/events", nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Eventually(t, func() bool { return slices.Contains(s.bgWG.Running(), "ops-v3-events") }, 5*time.Second, 10*time.Millisecond)

	ended := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		close(ended)
	}()
	s.closeOpsV3Streams()
	s.closeOpsV3Streams() // idempotent
	select {
	case <-ended:
	case <-time.After(5 * time.Second):
		t.Fatal("the event stream outlived closeOpsV3Streams")
	}
}

// TestOpsV3Authz_FirstRunCheckReadOncePerRequest: Holds runs once per
// permission (a definition's set plus the start floor); the first-run check
// behind it is one CountUsers per request, not one per permission.
func TestOpsV3Authz_FirstRunCheckReadOncePerRequest(t *testing.T) {
	orig := config.AppConfig
	t.Cleanup(func() { config.AppConfig = orig })
	config.AppConfig.EnableAuth = true
	store := mocks.NewMockStore(t)
	store.EXPECT().CountUsers().Return(3, nil).Times(2)
	s := &Server{store: store}
	authz := s.opsV3Authz()
	for range 2 { // two requests
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
		for _, p := range []auth.Permission{auth.PermScanTrigger, auth.PermLibraryEditMetadata, auth.PermSettingsManage} {
			require.False(t, authz.Holds(c, p), "a request with no permissions holds none once users exist")
		}
	}
}

// TestOpsV3Routes_NeedCredentialsWhenAuthIsOn: with auth on and users present,
// every kind of /api/v3/ops route (a read, a start, an approve and the SSE
// feed) answers 401 to a request with no credentials, through the real
// middleware chain.
// Not parallel: it swaps the process-wide config.
func TestOpsV3Routes_NeedCredentialsWhenAuthIsOn(t *testing.T) {
	srv, _, _ := repairsPermServer(t, true, true)
	for _, r := range []struct{ method, path string }{
		{http.MethodGet, "/api/v3/ops/defs"},
		{http.MethodGet, "/api/v3/ops/runs"},
		{http.MethodPost, "/api/v3/ops/defs/library.scan/runs"},
		{http.MethodPost, "/api/v3/ops/runs"},
		{http.MethodPost, "/api/v3/ops/runs/r1/cancel"},
		{http.MethodPost, "/api/v3/ops/runs/r1/approve"},
		{http.MethodGet, "/api/v3/ops/events"},
	} {
		req := httptest.NewRequest(r.method, r.path, strings.NewReader(`{}`))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		srv.router.ServeHTTP(w, req)
		require.Equal(t, http.StatusUnauthorized, w.Code, "%s %s: %s", r.method, r.path, w.Body.String())
	}
}

// TestOpsV3Routes_ForwardedHeaderCannotApprove: the unsigned
// Cf-Access-Authenticated-User-Email header, naming a real admin, with no
// verified Access JWT, session or token, is not an identity: approve is
// refused (401/403) by the real server chain, never reaching the handler's
// 501.
// Not parallel: it swaps the process-wide config.
func TestOpsV3Routes_ForwardedHeaderCannotApprove(t *testing.T) {
	srv, _, _ := repairsPermServer(t, true, true)
	req := httptest.NewRequest(http.MethodPost, "/api/v3/ops/runs/r1/approve", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cf-Access-Authenticated-User-Email", "admin@x.test")
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)
	require.Contains(t, []int{http.StatusUnauthorized, http.StatusForbidden}, w.Code, w.Body.String())
}

// TestOpsV3Routes_ViewerCannotStartARun: the seeded viewer role (no
// scan.trigger) is refused at both start routes by the real permission
// middleware; an editor (scan.trigger) passes the floor.
// Not parallel: it swaps the process-wide config.
func TestOpsV3Routes_ViewerCannotStartARun(t *testing.T) {
	srv, editor, _ := repairsPermServer(t, true, true)
	store := srv.storeForWiring()
	viewerUser, err := store.CreateUser("viewer", "viewer@x.test", "bcrypt", "hash", []string{auth.SeedRoleViewer}, "active")
	require.NoError(t, err)
	vs, err := store.CreateSession(viewerUser.ID, "192.0.2.10", "test", time.Hour)
	require.NoError(t, err)
	post := func(token, path, body string) *httptest.ResponseRecorder {
		return postRepairs(srv, token, path, body)
	}
	for _, r := range []struct{ path, body string }{
		{"/api/v3/ops/defs/library.scan/runs", `{"mode":"preview"}`},
		{"/api/v3/ops/runs", `{"def":"library.scan","mode":"preview"}`},
	} {
		w := post(vs.ID, r.path, r.body)
		require.Equal(t, http.StatusForbidden, w.Code, "viewer %s: %s", r.path, w.Body.String())
		require.Contains(t, w.Body.String(), "scan.trigger")
	}
	// The editor passes the floor and the permission check and reaches the
	// mode check: library.scan has no preview, so a preview is a 409 and
	// nothing is queued.
	w := post(editor, "/api/v3/ops/defs/library.scan/runs", `{"mode":"preview"}`)
	require.Equal(t, http.StatusConflict, w.Code, "an editor holds scan.trigger: %s", w.Body.String())
}
