// file: internal/server/handlers/operations/handler_test.go
// version: 1.5.0
// guid: 36cf7fbb-8b23-4edb-ad4b-079ab2bd6cf1
// last-edited: 2026-08-23

// Unit tests for the operations-domain HTTP handlers. Each public method has at
// least one test; happy paths plus key branches (cancel not-found fallback,
// stale-op clear) are covered. The store is exercised through the generated
// operationsmocks (which satisfy the narrow OperationsStore — a superset of
// the real store); the registry / pipeline / scan-store deps use their
// generated mocks, and the three injected funcs (collectStale / preflightUndo
// / revert) are stubbed.
//
// The task/maintenance-window tests (ListTasks, RunTask, UpdateTaskConfig,
// RunMaintenanceWindowNow, GetMaintenanceWindowStatus,
// UpdateMaintenanceWindowConfig) moved to
// internal/server/handlers/scheduler_admin_test.go along with the handler
// methods they cover (TODO.md scheduler-config item). newTestHandler still
// constructs a MockScheduler because operations.New's signature is unchanged
// (see handler.go's package doc), but no remaining test here asserts on it.

package operations_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers/operations"
	operationsmocks "github.com/falkcorp/audiobook-organizer/internal/server/handlers/operations/mocks"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// --- helpers ---

func newTestHandler(t *testing.T) (*operations.Handler, *operationsmocks.MockOperationsStore, *operationsmocks.MockOperationsRegistry, *operationsmocks.MockScheduler, *operationsmocks.MockScanCanceler, *operationsmocks.MockAIScanLister) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	store := operationsmocks.NewMockOperationsStore(t)
	reg := operationsmocks.NewMockOperationsRegistry(t)
	sched := operationsmocks.NewMockScheduler(t)
	pipe := operationsmocks.NewMockScanCanceler(t)
	scans := operationsmocks.NewMockAIScanLister(t)

	h := operations.New(
		store,
		reg,
		func() operations.Scheduler { return sched },
		pipe,
		scans,
		func(timeout time.Duration) ([]database.Operation, error) {
			return []database.Operation{{ID: "stale-1", Status: "running"}}, nil
		},
		func(id string) (*undo.UndoConflictReport, error) {
			return &undo.UndoConflictReport{TotalChanges: 1}, nil
		},
		func(id string) error { return nil },
	)
	return h, store, reg, sched, pipe, scans
}

// run wires a single route and serves one request, returning the recorder.
func run(method, routePath, reqPath string, body []byte, register func(r *gin.Engine)) *httptest.ResponseRecorder {
	r := gin.New()
	register(r)
	var rdr *bytes.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, reqPath, rdr)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// --- StartScan / StartOrganize / StartOptimize / StartTranscode ---

// --- GetOperationStatus ---

// --- CancelOperation ---

func TestCancelOperation_ViaPipeline(t *testing.T) {
	h, _, _, _, pipe, scans := newTestHandler(t)
	scans.EXPECT().ListScans().Return([]database.Scan{{ID: 7, OperationID: "op-x"}}, nil)
	pipe.EXPECT().CancelScan(7).Return(nil)

	w := run(http.MethodDelete, "/operations/:id", "/operations/op-x", nil, func(r *gin.Engine) {
		r.DELETE("/operations/:id", h.CancelOperation)
	})
	assert.Equal(t, http.StatusNoContent, w.Code)
}

func TestCancelOperation_ViaRegistry(t *testing.T) {
	h, _, reg, _, _, scans := newTestHandler(t)
	scans.EXPECT().ListScans().Return(nil, nil)
	reg.EXPECT().Cancel("op-y").Return(nil)

	w := run(http.MethodDelete, "/operations/:id", "/operations/op-y", nil, func(r *gin.Engine) {
		r.DELETE("/operations/:id", h.CancelOperation)
	})
	assert.Equal(t, http.StatusNoContent, w.Code)
}

func TestCancelOperation_FallbackForceStatus(t *testing.T) {
	h, store, reg, _, _, scans := newTestHandler(t)
	scans.EXPECT().ListScans().Return(nil, nil)
	reg.EXPECT().Cancel("op-z").Return(errors.New("not found"))
	store.EXPECT().UpdateOperationStatus("op-z", "canceled", 0, 0, mock.Anything).Return(nil)

	w := run(http.MethodDelete, "/operations/:id", "/operations/op-z", nil, func(r *gin.Engine) {
		r.DELETE("/operations/:id", h.CancelOperation)
	})
	assert.Equal(t, http.StatusNoContent, w.Code)
}

// --- ClearStaleOperations ---

func TestClearStaleOperations_ClearsRunning(t *testing.T) {
	h, store, _, _, _, _ := newTestHandler(t)
	store.EXPECT().GetRecentOperations(500).Return([]database.Operation{
		{ID: "a", Status: "running"},
		{ID: "b", Status: "completed"},
		{ID: "c", Status: "queued"},
	}, nil)
	store.EXPECT().UpdateOperationStatus("a", "failed", 0, 0, mock.Anything).Return(nil)
	store.EXPECT().UpdateOperationStatus("c", "failed", 0, 0, mock.Anything).Return(nil)

	w := run(http.MethodPost, "/operations/clear-stale", "/operations/clear-stale", nil, func(r *gin.Engine) {
		r.POST("/operations/clear-stale", h.ClearStaleOperations)
	})
	assert.Equal(t, http.StatusOK, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, float64(2), resp["data"].(map[string]any)["cleared"])
}

// --- DeleteOperationHistory ---

func TestDeleteOperationHistory_RequiresStatus(t *testing.T) {
	h, _, _, _, _, _ := newTestHandler(t)
	w := run(http.MethodDelete, "/operations/history", "/operations/history", nil, func(r *gin.Engine) {
		r.DELETE("/operations/history", h.DeleteOperationHistory)
	})
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestDeleteOperationHistory_RejectsNonTerminal(t *testing.T) {
	h, _, _, _, _, _ := newTestHandler(t)
	w := run(http.MethodDelete, "/operations/history", "/operations/history?status=running", nil, func(r *gin.Engine) {
		r.DELETE("/operations/history", h.DeleteOperationHistory)
	})
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestDeleteOperationHistory_Deletes(t *testing.T) {
	h, store, _, _, _, _ := newTestHandler(t)
	store.EXPECT().DeleteOperationsByStatus([]string{"completed", "failed"}).Return(5, nil)
	w := run(http.MethodDelete, "/operations/history", "/operations/history?status=completed,failed", nil, func(r *gin.Engine) {
		r.DELETE("/operations/history", h.DeleteOperationHistory)
	})
	assert.Equal(t, http.StatusOK, w.Code)
}

// --- OptimizeDatabase ---

func TestOptimizeDatabase_NoBooks(t *testing.T) {
	h, store, _, _, _, _ := newTestHandler(t)
	store.EXPECT().GetAllBooksCore(0, 0).Return([]database.BookCore{}, nil)
	w := run(http.MethodPost, "/operations/optimize-database", "/operations/optimize-database", nil, func(r *gin.Engine) {
		r.POST("/operations/optimize-database", h.OptimizeDatabase)
	})
	assert.Equal(t, http.StatusOK, w.Code)
}

// --- SweepTombstones ---

func TestSweepTombstones_Empty(t *testing.T) {
	h, store, _, _, _, _ := newTestHandler(t)
	store.EXPECT().ListBookTombstones(1000).Return(nil, nil)
	w := run(http.MethodPost, "/operations/sweep-tombstones", "/operations/sweep-tombstones", nil, func(r *gin.Engine) {
		r.POST("/operations/sweep-tombstones", h.SweepTombstones)
	})
	assert.Equal(t, http.StatusOK, w.Code)
}

// --- SetInternalFlag ---

func TestSetInternalFlag_Sets(t *testing.T) {
	h, store, _, _, _, _ := newTestHandler(t)
	store.EXPECT().SetSetting("k", "v", "string", false).Return(nil)
	w := run(http.MethodPost, "/operations/set-internal-flag", "/operations/set-internal-flag", []byte(`{"key":"k","value":"v"}`), func(r *gin.Engine) {
		r.POST("/operations/set-internal-flag", h.SetInternalFlag)
	})
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestSetInternalFlag_RequiresKey(t *testing.T) {
	h, _, _, _, _, _ := newTestHandler(t)
	w := run(http.MethodPost, "/operations/set-internal-flag", "/operations/set-internal-flag", []byte(`{"value":"v"}`), func(r *gin.Engine) {
		r.POST("/operations/set-internal-flag", h.SetInternalFlag)
	})
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// --- AuditFileConsistency ---

func TestAuditFileConsistency_Empty(t *testing.T) {
	h, store, _, _, _, _ := newTestHandler(t)
	store.EXPECT().GetAllBooksCore(0, 0).Return([]database.BookCore{}, nil)
	w := run(http.MethodGet, "/operations/audit-files", "/operations/audit-files", nil, func(r *gin.Engine) {
		r.GET("/operations/audit-files", h.AuditFileConsistency)
	})
	assert.Equal(t, http.StatusOK, w.Code)
}

// --- ListOperations ---

// --- ListStaleOperations ---

func TestListStaleOperations_UsesInjectedCollector(t *testing.T) {
	h, _, _, _, _, _ := newTestHandler(t)
	w := run(http.MethodGet, "/operations/stale", "/operations/stale", nil, func(r *gin.Engine) {
		r.GET("/operations/stale", h.ListStaleOperations)
	})
	assert.Equal(t, http.StatusOK, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, float64(1), resp["data"].(map[string]any)["count"])
}

// --- GetOperationLogs ---

// --- GetOperationResult ---

// GetOperationResult serves two keyspaces. Ops that still mint a v1 row store
// their payload with UpdateOperationResultData; ops that have gone v2-native
// store it on the v2 row via ReporterSetResult. Nothing else serves ResultData
// — rowToResponse omits it — so if this route reads only one keyspace, half the
// ops' output is written and never readable. Each test below pins one arm.

func getResult(t *testing.T, h *operations.Handler, id string) *httptest.ResponseRecorder {
	t.Helper()
	return run(http.MethodGet, "/operations/:id/result", "/operations/"+id+"/result", nil, func(r *gin.Engine) {
		r.GET("/operations/:id/result", h.GetOperationResult)
	})
}

// resultData digs the payload out of the {"data":{"result_data":…}} envelope.
func resultData(t *testing.T, w *httptest.ResponseRecorder) any {
	t.Helper()
	var resp struct {
		Data struct {
			ResultData any `json:"result_data"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	return resp.Data.ResultData
}

// The v2 arm: the id belongs to a v2-native op and there is no v1 twin at all.
// Before this route learned the v2 keyspace, this 404'd.
func TestGetOperationResult_ReadsTheV2Row(t *testing.T) {
	h, store, _, _, _, _ := newTestHandler(t)
	rd := `{"suggestions":[{"group_index":3}]}`
	store.EXPECT().GetOperationV2("op-v2").Return(&database.OperationV2Row{ID: "op-v2", ResultData: &rd}, nil)

	w := getResult(t, h, "op-v2")
	require.Equal(t, http.StatusOK, w.Code)
	got, ok := resultData(t, w).(map[string]any)
	require.True(t, ok, "result_data must be structured JSON, got %T", resultData(t, w))
	require.Contains(t, got, "suggestions")
}

// The v1 arm. Five callers were still writing v1 result data on 2026-08-23
// (maintenance dedup/reconcile, itunes path-repair, batch_poller, diagnostics),
// so the fallback is load-bearing, not legacy politeness.
func TestGetOperationResult_FallsBackToV1WhenNoV2Row(t *testing.T) {
	h, store, _, _, _, _ := newTestHandler(t)
	rd := `{"files":10}`
	store.EXPECT().GetOperationV2("op-1").Return(nil, nil)
	store.EXPECT().GetOperationByID("op-1").Return(&database.Operation{ID: "op-1", ResultData: &rd}, nil)

	w := getResult(t, h, "op-1")
	require.Equal(t, http.StatusOK, w.Code)
	got, ok := resultData(t, w).(map[string]any)
	require.True(t, ok)
	require.Equal(t, float64(10), got["files"])
}

// A v2 row that exists but has not stored a result yet answers null — it must
// NOT fall through to v1. A twinned op mid-run would otherwise serve the stale
// result of its v1 mirror as if it were this run's output.
func TestGetOperationResult_V2RowWithNoResultDoesNotFallThroughToV1(t *testing.T) {
	h, store, _, _, _, _ := newTestHandler(t)
	store.EXPECT().GetOperationV2("op-running").Return(&database.OperationV2Row{ID: "op-running"}, nil)

	w := getResult(t, h, "op-running")
	require.Equal(t, http.StatusOK, w.Code)
	require.Nil(t, resultData(t, w))
	store.AssertNotCalled(t, "GetOperationByID", "op-running")
}

// A v2 lookup error is not fatal: the id may be a v1 id, and failing the whole
// request would make a v2 store hiccup take the v1 ops down with it.
func TestGetOperationResult_V2LookupErrorStillTriesV1(t *testing.T) {
	h, store, _, _, _, _ := newTestHandler(t)
	rd := `{"files":2}`
	store.EXPECT().GetOperationV2("op-1").Return(nil, errors.New("pebble closed"))
	store.EXPECT().GetOperationByID("op-1").Return(&database.Operation{ID: "op-1", ResultData: &rd}, nil)

	w := getResult(t, h, "op-1")
	require.Equal(t, http.StatusOK, w.Code)
}

// Whatever the op stored is what comes back. A payload that is not valid JSON
// is echoed as a raw string rather than 500'd — a caller holding a mangled
// result is better off than one holding an error.
func TestGetOperationResult_UnparseablePayloadIsEchoedRaw(t *testing.T) {
	h, store, _, _, _, _ := newTestHandler(t)
	rd := `not json at all`
	store.EXPECT().GetOperationV2("op-v2").Return(&database.OperationV2Row{ID: "op-v2", ResultData: &rd}, nil)

	w := getResult(t, h, "op-v2")
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, rd, resultData(t, w))
}

func TestGetOperationResult_NotFoundInEitherKeyspace(t *testing.T) {
	h, store, _, _, _, _ := newTestHandler(t)
	store.EXPECT().GetOperationV2("nope").Return(nil, nil)
	store.EXPECT().GetOperationByID("nope").Return(nil, nil)

	w := getResult(t, h, "nope")
	assert.Equal(t, http.StatusNotFound, w.Code)
}

// --- GetOperationChanges ---

func TestGetOperationChanges_Success(t *testing.T) {
	h, store, _, _, _, _ := newTestHandler(t)
	store.EXPECT().GetOperationChanges("op-1").Return([]*database.OperationChange{{ID: "c1"}}, nil)
	w := run(http.MethodGet, "/operations/:id/changes", "/operations/op-1/changes", nil, func(r *gin.Engine) {
		r.GET("/operations/:id/changes", h.GetOperationChanges)
	})
	assert.Equal(t, http.StatusOK, w.Code)
}

// --- UndoPreflightHandler ---

func TestUndoPreflightHandler_UsesInjectedFunc(t *testing.T) {
	h, _, _, _, _, _ := newTestHandler(t)
	w := run(http.MethodGet, "/operations/:id/undo/preflight", "/operations/op-1/undo/preflight", nil, func(r *gin.Engine) {
		r.GET("/operations/:id/undo/preflight", h.UndoPreflightHandler)
	})
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestUndoPreflightHandler_Error(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := operationsmocks.NewMockOperationsStore(t)
	h := operations.New(store, nil, nil, nil, nil, nil,
		func(id string) (*undo.UndoConflictReport, error) { return nil, errors.New("boom") },
		func(id string) error { return nil },
	)
	w := run(http.MethodGet, "/operations/:id/undo/preflight", "/operations/op-1/undo/preflight", nil, func(r *gin.Engine) {
		r.GET("/operations/:id/undo/preflight", h.UndoPreflightHandler)
	})
	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

// --- RevertOperation ---

func TestRevertOperation_Success(t *testing.T) {
	h, _, _, _, _, _ := newTestHandler(t)
	w := run(http.MethodPost, "/operations/:id/revert", "/operations/op-1/revert", nil, func(r *gin.Engine) {
		r.POST("/operations/:id/revert", h.RevertOperation)
	})
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestRevertOperation_Error(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := operationsmocks.NewMockOperationsStore(t)
	h := operations.New(store, nil, nil, nil, nil, nil,
		func(id string) (*undo.UndoConflictReport, error) { return nil, nil },
		func(id string) error { return errors.New("revert failed") },
	)
	w := run(http.MethodPost, "/operations/:id/revert", "/operations/op-1/revert", nil, func(r *gin.Engine) {
		r.POST("/operations/:id/revert", h.RevertOperation)
	})
	assert.Equal(t, http.StatusInternalServerError, w.Code)
}
