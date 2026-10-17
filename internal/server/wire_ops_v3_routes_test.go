// file: internal/server/wire_ops_v3_routes_test.go
// version: 1.1.2
// guid: f0aca62d-8230-49ce-ab21-61cbd082ce99
// last-edited: 2026-10-17

package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/scheduler"
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

// TestOpsV3Defs_PreviewFollowsTheWriteModeLedger: every adapted v2 op the
// ledger records `no-mode` is a writer without a preview, every `preview` one
// has one, and a v2 op that cannot write is not a writer. The real catalog,
// not a fake.
func TestOpsV3Defs_PreviewFollowsTheWriteModeLedger(t *testing.T) {
	srv, _, _ := bootRegisteredOpIDs(t)
	ledger := readWriteOpModes(t)
	defs := (&opsV3Defs{reg: srv.opRegistry}).ListDefs()
	require.NotEmpty(t, defs)
	noMode, preview := 0, 0
	for _, d := range defs {
		od, ok := srv.opRegistry.Def(d.ID)
		require.True(t, ok, d.ID)
		switch class, listed := ledger[d.ID]; {
		case listed && class == "no-mode":
			noMode++
			require.True(t, d.Writer && !d.HasPreview, "%s is no-mode: writer without preview, got writer=%v preview=%v", d.ID, d.Writer, d.HasPreview)
		case listed && class == "preview":
			preview++
			require.True(t, d.Writer && d.HasPreview, "%s is recorded preview", d.ID)
		case !canWrite(od):
			require.False(t, d.Writer, "%s declares no write capability", d.ID)
		}
	}
	require.Positive(t, noMode, "no registered no-mode op was checked")
	require.Positive(t, preview, "no registered preview op was checked")
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
