// file: internal/server/handlers/repairs/handler_test.go
// version: 1.0.0
// guid: 8e3f6b21-7c94-4a0d-9b52-2d1a8e5c7f36
// last-edited: 2026-09-27

package repairs

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
)

func init() { gin.SetMode(gin.TestMode) }

type stubFixer struct{ id string }

func (f stubFixer) ID() string          { return f.id }
func (f stubFixer) Title() string       { return "Stub " + f.id }
func (f stubFixer) Description() string { return "stub fixer" }
func (f stubFixer) Plan(context.Context, json.RawMessage, opsregistry.Reporter) ([]repairs.Row, error) {
	return nil, nil
}
func (f stubFixer) Replan(_ context.Context, _ json.RawMessage, r repairs.Row, _ opsregistry.Reporter) (repairs.Row, error) {
	return r, nil
}
func (f stubFixer) Apply(context.Context, *repairs.Writer, repairs.Row) error { return nil }

type enqueued struct {
	defID  string
	params json.RawMessage
}

type fakeEnqueuer struct {
	mu    sync.Mutex
	calls []enqueued
	// fixedID, when set, is returned instead of a new id (registry dedupe).
	fixedID string
}

func (e *fakeEnqueuer) EnqueueOp(_ context.Context, defID string, params any, _ ...opsregistry.EnqueueOption) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	data, err := json.Marshal(params)
	if err != nil {
		return "", err
	}
	e.calls = append(e.calls, enqueued{defID: defID, params: data})
	if e.fixedID != "" {
		return e.fixedID, nil
	}
	return fmt.Sprintf("op-new-%d", len(e.calls)), nil
}

type fakeOps struct {
	mu       sync.Mutex
	rows     map[string]*database.OperationV2Row
	settings map[string]string
}

func (o *fakeOps) GetOperationV2(id string) (*database.OperationV2Row, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.rows[id], nil
}
func (o *fakeOps) ListActiveOperationsV2() ([]database.OperationV2Row, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	var out []database.OperationV2Row
	for _, r := range o.rows {
		if r.Status == "queued" || r.Status == "running" {
			out = append(out, *r)
		}
	}
	return out, nil
}
func (o *fakeOps) GetSetting(key string) (*database.Setting, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	v, ok := o.settings[key]
	if !ok {
		return nil, database.ErrSettingNotFound
	}
	return &database.Setting{Key: key, Value: v}, nil
}
func (o *fakeOps) SetSetting(key, value, _ string, _ bool) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.settings[key] = value
	return nil
}

func planRows(n int) []repairs.Row {
	rows := make([]repairs.Row, 0, n)
	for i := 0; i < n; i++ {
		r := repairs.Row{RowID: fmt.Sprintf("vg-%03d", i), BookIDs: []string{fmt.Sprintf("b%03d", i)},
			Title: fmt.Sprintf("Book %d", i), Reason: "two primaries", Risk: repairs.RiskLow, Fingerprint: "fp"}
		if i%4 == 3 {
			r.Skipped, r.SkipReason = repairs.SkipITunes, "hands-off"
		}
		rows = append(rows, r)
	}
	return rows
}

func storedPlan(t *testing.T, fixerID string, rows []repairs.Row) *string {
	t.Helper()
	res := repairs.PlanResult{FixerID: fixerID, PlannedAt: time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC),
		Total: len(rows), SkippedByKind: map[string]int{}, Rows: rows}
	for _, r := range rows {
		if r.Applicable() {
			res.Applicable++
		} else {
			res.SkippedByKind[r.Skipped]++
		}
	}
	data, err := json.Marshal(res)
	require.NoError(t, err)
	s := string(data)
	return &s
}

func setup(t *testing.T) (*gin.Engine, *fakeEnqueuer, *fakeOps) {
	t.Helper()
	reg := repairs.NewRegistry()
	require.NoError(t, reg.Register(stubFixer{id: "version-group-primary-repair"}))
	require.NoError(t, reg.Register(stubFixer{id: "other"}))
	enq := &fakeEnqueuer{}
	queued := time.Date(2026, 9, 27, 11, 0, 0, 0, time.UTC)
	ops := &fakeOps{rows: map[string]*database.OperationV2Row{
		"op-plan": {ID: "op-plan", DefID: repairs.PlanOpID, Status: "completed", QueuedAt: queued,
			Params: `{"fixer_id":"version-group-primary-repair"}`, ResultData: storedPlan(t, "version-group-primary-repair", planRows(10))},
		"op-old-plan": {ID: "op-old-plan", DefID: repairs.PlanOpID, Status: "completed", QueuedAt: queued.Add(-time.Hour),
			Params: `{"fixer_id":"version-group-primary-repair"}`, ResultData: storedPlan(t, "version-group-primary-repair", planRows(1))},
		"op-running": {ID: "op-running", DefID: repairs.PlanOpID, Status: "running", QueuedAt: queued.Add(-2 * time.Hour),
			Params: `{"fixer_id":"other"}`},
		"op-scan": {ID: "op-scan", DefID: "library.scan", Status: "completed", QueuedAt: queued},
	}, settings: map[string]string{
		"repairs_last_plan_op:version-group-primary-repair": "op-plan",
		"repairs_last_plan_op:other":                        "op-running",
	}}
	h := New(reg, enq, ops)
	r := gin.New()
	g := r.Group("/api/v1")
	g.GET("/repairs", h.ListFixers)
	g.POST("/repairs/:fixer/plan", h.StartPlan)
	g.GET("/repairs/:fixer/plan/:op_id/rows", h.ListPlanRows)
	g.POST("/repairs/:fixer/apply", h.StartApply)
	return r, enq, ops
}

func do(r *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func data[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()
	var env struct {
		Data T `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &env), w.Body.String())
	return env.Data
}

func TestListFixers_WithLastPlanSummary(t *testing.T) {
	r, _, _ := setup(t)
	w := do(r, http.MethodGet, "/api/v1/repairs", "")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	got := data[struct {
		Fixers []FixerInfo `json:"fixers"`
	}](t, w)
	require.Len(t, got.Fixers, 2)
	require.Equal(t, "other", got.Fixers[0].ID)
	require.Equal(t, "op-running", got.Fixers[0].LastPlan.OperationID)
	require.Nil(t, got.Fixers[0].LastPlan.Total)
	vg := got.Fixers[1]
	require.Equal(t, "version-group-primary-repair", vg.ID)
	require.Equal(t, "op-plan", vg.LastPlan.OperationID, "the newest plan")
	require.Equal(t, 10, *vg.LastPlan.Total)
	require.Equal(t, 8, *vg.LastPlan.Applicable)
	require.Equal(t, map[string]int{repairs.SkipITunes: 2}, vg.LastPlan.SkippedByKind)
	require.Nil(t, vg.LastApply)
}

func TestEnqueue_RecordsLastRunAndReportsDedupe(t *testing.T) {
	r, enq, ops := setup(t)
	w := do(r, http.MethodPost, "/api/v1/repairs/version-group-primary-repair/apply",
		`{"plan_op_id":"op-plan","row_ids":["vg-000"],"dry_run":false}`)
	require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())
	require.False(t, data[StartedResponse](t, w).Deduped)
	require.Equal(t, "op-new-1", ops.settings["repairs_last_apply_op:version-group-primary-repair"])
	require.Len(t, enq.calls, 1)

	// The registry answers an identical request with the run already queued.
	ops.rows["op-new-1"] = &database.OperationV2Row{ID: "op-new-1", DefID: repairs.ApplyOpID, Status: "running",
		Params: `{"fixer_id":"version-group-primary-repair"}`}
	enq.fixedID = "op-new-1"
	w = do(r, http.MethodPost, "/api/v1/repairs/version-group-primary-repair/apply",
		`{"plan_op_id":"op-plan","row_ids":["vg-000"],"dry_run":false}`)
	require.Equal(t, http.StatusAccepted, w.Code)
	got := data[StartedResponse](t, w)
	require.True(t, got.Deduped)
	require.Equal(t, "running", got.Status)

	w = do(r, http.MethodGet, "/api/v1/repairs", "")
	fixers := data[struct {
		Fixers []FixerInfo `json:"fixers"`
	}](t, w).Fixers
	require.Equal(t, "op-new-1", fixers[1].LastApply.OperationID)
	require.Equal(t, "running", fixers[1].LastApply.Status)
}

func TestStartPlan(t *testing.T) {
	r, enq, _ := setup(t)
	w := do(r, http.MethodPost, "/api/v1/repairs/version-group-primary-repair/plan", `{"group_ids":["vg-1"]}`)
	require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())
	got := data[StartedResponse](t, w)
	require.Equal(t, "op-new-1", got.OperationID)
	require.Equal(t, repairs.PlanOpID, got.DefID)
	require.Len(t, enq.calls, 1)
	require.Equal(t, repairs.PlanOpID, enq.calls[0].defID)
	require.JSONEq(t, `{"fixer_id":"version-group-primary-repair","params":{"group_ids":["vg-1"]}}`, string(enq.calls[0].params))

	w = do(r, http.MethodPost, "/api/v1/repairs/version-group-primary-repair/plan", "")
	require.Equal(t, http.StatusAccepted, w.Code, "no body plans the whole library")

	require.Equal(t, http.StatusNotFound, do(r, http.MethodPost, "/api/v1/repairs/nope/plan", "").Code)
	require.Equal(t, http.StatusBadRequest, do(r, http.MethodPost, "/api/v1/repairs/other/plan", `[1,2]`).Code)
	require.Len(t, enq.calls, 2)
}

func TestListPlanRows_Pages(t *testing.T) {
	r, _, _ := setup(t)
	w := do(r, http.MethodGet, "/api/v1/repairs/version-group-primary-repair/plan/op-plan/rows?offset=2&limit=3", "")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	page := data[repairs.RowsPage](t, w)
	require.Equal(t, 10, page.Total)
	require.Equal(t, 8, page.Applicable)
	require.Equal(t, 2, page.Offset)
	require.Equal(t, 3, page.Limit)
	require.Len(t, page.Rows, 3)
	require.Equal(t, "vg-002", page.Rows[0].RowID)
	require.Equal(t, repairs.SkipITunes, page.Rows[1].Skipped)

	w = do(r, http.MethodGet, "/api/v1/repairs/version-group-primary-repair/plan/op-plan/rows?filter=skipped", "")
	page = data[repairs.RowsPage](t, w)
	require.Equal(t, 2, page.Total)
	require.Equal(t, DefaultLimit, page.Limit)

	w = do(r, http.MethodGet, "/api/v1/repairs/version-group-primary-repair/plan/op-plan/rows?limit=100000", "")
	require.Equal(t, MaxLimit, data[repairs.RowsPage](t, w).Limit, "limit is clamped")

	w = do(r, http.MethodGet, "/api/v1/repairs/version-group-primary-repair/plan/op-plan/rows?offset=50", "")
	page = data[repairs.RowsPage](t, w)
	require.NotNil(t, page.Rows)
	require.Empty(t, page.Rows)
}

func TestListPlanRows_Errors(t *testing.T) {
	r, _, _ := setup(t)
	base := "/api/v1/repairs/"
	cases := map[string]int{
		base + "nope/plan/op-plan/rows":                                   http.StatusNotFound,
		base + "version-group-primary-repair/plan/op-missing/rows":        http.StatusNotFound,
		base + "other/plan/op-running/rows":                               http.StatusConflict,
		base + "other/plan/op-plan/rows":                                  http.StatusBadRequest,
		base + "version-group-primary-repair/plan/op-scan/rows":           http.StatusBadRequest,
		base + "version-group-primary-repair/plan/op-plan/rows?limit=0":   http.StatusBadRequest,
		base + "version-group-primary-repair/plan/op-plan/rows?offset=-1": http.StatusBadRequest,
		base + "version-group-primary-repair/plan/op-plan/rows?filter=x":  http.StatusBadRequest,
	}
	for path, want := range cases {
		require.Equal(t, want, do(r, http.MethodGet, path, "").Code, path)
	}
}

func TestStartApply(t *testing.T) {
	r, enq, _ := setup(t)
	path := "/api/v1/repairs/version-group-primary-repair/apply"
	w := do(r, http.MethodPost, path, `{"plan_op_id":"op-plan","row_ids":["vg-000","vg-001"],"dry_run":false}`)
	require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())
	require.Equal(t, repairs.ApplyOpID, data[StartedResponse](t, w).DefID)
	require.Len(t, enq.calls, 1)
	require.JSONEq(t, `{"fixer_id":"version-group-primary-repair","plan_op_id":"op-plan","row_ids":["vg-000","vg-001"],"dry_run":false}`,
		string(enq.calls[0].params))

	// Omitted dry_run is a preview.
	w = do(r, http.MethodPost, path, `{"plan_op_id":"op-plan","row_ids":["vg-000"]}`)
	require.Equal(t, http.StatusAccepted, w.Code)
	require.JSONEq(t, `{"fixer_id":"version-group-primary-repair","plan_op_id":"op-plan","row_ids":["vg-000"],"dry_run":true}`,
		string(enq.calls[1].params))

	for body, want := range map[string]int{
		`{"plan_op_id":"op-plan","row_ids":[]}`:       http.StatusBadRequest,
		`{"row_ids":["vg-000"]}`:                      http.StatusBadRequest,
		`{"plan_op_id":"op-missing","row_ids":["x"]}`: http.StatusNotFound,
		`{"plan_op_id":"op-scan","row_ids":["x"]}`:    http.StatusBadRequest,
		`not json`: http.StatusBadRequest,
	} {
		require.Equal(t, want, do(r, http.MethodPost, path, body).Code, body)
	}
	require.Equal(t, http.StatusConflict,
		do(r, http.MethodPost, "/api/v1/repairs/other/apply", `{"plan_op_id":"op-running","row_ids":["x"]}`).Code)
	require.Equal(t, http.StatusBadRequest,
		do(r, http.MethodPost, "/api/v1/repairs/other/apply", `{"plan_op_id":"op-plan","row_ids":["x"]}`).Code,
		"a plan of another fixer")
	require.Len(t, enq.calls, 2, "no refused request enqueued anything")
}

func TestPlanAndApply_NoRegistryIs503(t *testing.T) {
	reg := repairs.NewRegistry()
	require.NoError(t, reg.Register(stubFixer{id: "x"}))
	h := New(reg, nil, &fakeOps{rows: map[string]*database.OperationV2Row{}, settings: map[string]string{}})
	r := gin.New()
	r.POST("/repairs/:fixer/plan", h.StartPlan)
	r.POST("/repairs/:fixer/apply", h.StartApply)
	require.Equal(t, http.StatusServiceUnavailable, do(r, http.MethodPost, "/repairs/x/plan", "").Code)
	require.Equal(t, http.StatusServiceUnavailable, do(r, http.MethodPost, "/repairs/x/apply", `{"plan_op_id":"p","row_ids":["a"]}`).Code)
}
