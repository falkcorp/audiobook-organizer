// file: internal/server/handlers/operations/handler_test.go
// version: 1.1.0
// guid: 36cf7fbb-8b23-4edb-ad4b-079ab2bd6cf1
// last-edited: 2026-07-07

// Unit tests for the operations-domain HTTP handlers. Each public method has at
// least one test; happy paths plus key branches (cancel not-found fallback,
// stale-op clear, task run unknown-name, maintenance-window running state) are
// covered. The store is exercised through the generated operationsmocks
// (which satisfy the narrow OperationsStore — a superset of the real store);
// the scheduler / registry / pipeline / scan-store deps use their generated
// mocks, and the three injected funcs (collectStale / preflightUndo / revert)
// are stubbed.

package operations_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/scheduler"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers/operations"
	operationsmocks "github.com/falkcorp/audiobook-organizer/internal/server/handlers/operations/mocks"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
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

func TestStartScan_Enqueues(t *testing.T) {
	h, _, reg, _, _, _ := newTestHandler(t)
	reg.EXPECT().EnqueueOp(mock.Anything, "library.scan", mock.Anything).Return("op-1", nil)

	w := run(http.MethodPost, "/operations/scan", "/operations/scan", []byte(`{}`), func(r *gin.Engine) {
		r.POST("/operations/scan", h.StartScan)
	})
	assert.Equal(t, http.StatusAccepted, w.Code)
}

func TestStartScan_NilRegistry(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := operations.New(operationsmocks.NewMockOperationsStore(t), nil, nil, nil, nil, nil, nil, nil)
	w := run(http.MethodPost, "/operations/scan", "/operations/scan", []byte(`{}`), func(r *gin.Engine) {
		r.POST("/operations/scan", h.StartScan)
	})
	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestStartOrganize_Enqueues(t *testing.T) {
	h, _, reg, _, _, _ := newTestHandler(t)
	reg.EXPECT().EnqueueOp(mock.Anything, "library.organize", mock.Anything).Return("op-2", nil)

	w := run(http.MethodPost, "/operations/organize", "/operations/organize", []byte(`{}`), func(r *gin.Engine) {
		r.POST("/operations/organize", h.StartOrganize)
	})
	assert.Equal(t, http.StatusAccepted, w.Code)
}

func TestStartOptimize_Enqueues(t *testing.T) {
	h, _, reg, _, _, _ := newTestHandler(t)
	reg.EXPECT().EnqueueOp(mock.Anything, "library.optimize", mock.Anything).Return("op-3", nil)

	w := run(http.MethodPost, "/operations/optimize", "/operations/optimize", nil, func(r *gin.Engine) {
		r.POST("/operations/optimize", h.StartOptimize)
	})
	assert.Equal(t, http.StatusAccepted, w.Code)
}

func TestStartTranscode_RequiresBookID(t *testing.T) {
	h, _, _, _, _, _ := newTestHandler(t)
	w := run(http.MethodPost, "/operations/transcode", "/operations/transcode", []byte(`{}`), func(r *gin.Engine) {
		r.POST("/operations/transcode", h.StartTranscode)
	})
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestStartTranscode_Enqueues(t *testing.T) {
	h, _, reg, _, _, _ := newTestHandler(t)
	reg.EXPECT().EnqueueOp(mock.Anything, "library.transcode", mock.Anything).Return("op-4", nil)

	w := run(http.MethodPost, "/operations/transcode", "/operations/transcode", []byte(`{"book_id":"b1"}`), func(r *gin.Engine) {
		r.POST("/operations/transcode", h.StartTranscode)
	})
	assert.Equal(t, http.StatusAccepted, w.Code)
}

// --- GetOperationStatus ---

func TestGetOperationStatus_LegacyFound(t *testing.T) {
	h, store, _, _, _, _ := newTestHandler(t)
	store.EXPECT().GetOperationV2("op-1").Return(nil, errors.New("not found"))
	store.EXPECT().GetOperationByID("op-1").Return(&database.Operation{ID: "op-1", Status: "completed"}, nil)

	w := run(http.MethodGet, "/operations/:id/status", "/operations/op-1/status", nil, func(r *gin.Engine) {
		r.GET("/operations/:id/status", h.GetOperationStatus)
	})
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestGetOperationStatus_V2Found(t *testing.T) {
	h, store, _, _, _, _ := newTestHandler(t)
	now := time.Now()
	store.EXPECT().GetOperationV2("v2-1").Return(&database.OperationV2Row{ID: "v2-1", DefID: "dedup.scan", Status: "completed", QueuedAt: now}, nil)

	w := run(http.MethodGet, "/operations/:id/status", "/operations/v2-1/status", nil, func(r *gin.Engine) {
		r.GET("/operations/:id/status", h.GetOperationStatus)
	})
	assert.Equal(t, http.StatusOK, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "v2-1", resp["data"].(map[string]any)["id"])
}

func TestGetOperationStatus_NotFound(t *testing.T) {
	h, store, _, _, _, _ := newTestHandler(t)
	store.EXPECT().GetOperationV2("nope").Return(nil, errors.New("nf"))
	store.EXPECT().GetOperationByID("nope").Return(nil, errors.New("nf"))

	w := run(http.MethodGet, "/operations/:id/status", "/operations/nope/status", nil, func(r *gin.Engine) {
		r.GET("/operations/:id/status", h.GetOperationStatus)
	})
	assert.Equal(t, http.StatusNotFound, w.Code)
}

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
	store.EXPECT().GetAllBooksCore(10000, 0).Return([]database.BookCore{}, nil)
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
	store.EXPECT().GetAllBooksCore(100000, 0).Return([]database.BookCore{}, nil)
	w := run(http.MethodGet, "/operations/audit-files", "/operations/audit-files", nil, func(r *gin.Engine) {
		r.GET("/operations/audit-files", h.AuditFileConsistency)
	})
	assert.Equal(t, http.StatusOK, w.Code)
}

// --- ListOperations ---

func TestListOperations_Success(t *testing.T) {
	h, store, _, _, _, _ := newTestHandler(t)
	store.EXPECT().ListOperations(mock.AnythingOfType("int"), mock.AnythingOfType("int")).
		Return([]database.Operation{{ID: "o1"}, {ID: "o2"}}, 2, nil)
	w := run(http.MethodGet, "/operations", "/operations?limit=10&offset=0", nil, func(r *gin.Engine) {
		r.GET("/operations", h.ListOperations)
	})
	assert.Equal(t, http.StatusOK, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, float64(2), resp["data"].(map[string]any)["total"])
}

func TestListOperations_StoreError(t *testing.T) {
	h, store, _, _, _, _ := newTestHandler(t)
	store.EXPECT().ListOperations(mock.AnythingOfType("int"), mock.AnythingOfType("int")).Return(nil, 0, errors.New("db"))
	w := run(http.MethodGet, "/operations", "/operations", nil, func(r *gin.Engine) {
		r.GET("/operations", h.ListOperations)
	})
	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

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

func TestGetOperationLogs_V1Fallback(t *testing.T) {
	h, store, _, _, _, _ := newTestHandler(t)
	store.EXPECT().GetOpLogsV2("op-1", 1000).Return(nil, nil)
	store.EXPECT().GetOperationLogs("op-1").Return([]database.OperationLog{
		{ID: 1, Level: "info", Message: "a"},
		{ID: 2, Level: "info", Message: "b"},
	}, nil)
	w := run(http.MethodGet, "/operations/:id/logs", "/operations/op-1/logs", nil, func(r *gin.Engine) {
		r.GET("/operations/:id/logs", h.GetOperationLogs)
	})
	assert.Equal(t, http.StatusOK, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, float64(2), resp["data"].(map[string]any)["count"])
}

func TestGetOperationLogs_V2(t *testing.T) {
	h, store, _, _, _, _ := newTestHandler(t)
	store.EXPECT().GetOpLogsV2("op-2", 1000).Return([]database.OpLogV2Row{
		{Level: "info", Message: "x"},
	}, nil)
	w := run(http.MethodGet, "/operations/:id/logs", "/operations/op-2/logs", nil, func(r *gin.Engine) {
		r.GET("/operations/:id/logs", h.GetOperationLogs)
	})
	assert.Equal(t, http.StatusOK, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, float64(1), resp["data"].(map[string]any)["count"])
}

// --- GetOperationResult ---

func TestGetOperationResult_WithData(t *testing.T) {
	h, store, _, _, _, _ := newTestHandler(t)
	rd := `{"files":10}`
	store.EXPECT().GetOperationByID("op-1").Return(&database.Operation{ID: "op-1", ResultData: &rd}, nil)
	w := run(http.MethodGet, "/operations/:id/result", "/operations/op-1/result", nil, func(r *gin.Engine) {
		r.GET("/operations/:id/result", h.GetOperationResult)
	})
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestGetOperationResult_NotFound(t *testing.T) {
	h, store, _, _, _, _ := newTestHandler(t)
	store.EXPECT().GetOperationByID("nope").Return(nil, nil)
	w := run(http.MethodGet, "/operations/:id/result", "/operations/nope/result", nil, func(r *gin.Engine) {
		r.GET("/operations/:id/result", h.GetOperationResult)
	})
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

// --- ListTasks ---

func TestListTasks_Success(t *testing.T) {
	h, _, _, sched, _, _ := newTestHandler(t)
	sched.EXPECT().ListTasks().Return([]scheduler.TaskInfo{{Name: "dedup_refresh"}})
	w := run(http.MethodGet, "/tasks", "/tasks", nil, func(r *gin.Engine) {
		r.GET("/tasks", h.ListTasks)
	})
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestListTasks_NilScheduler(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := operations.New(operationsmocks.NewMockOperationsStore(t), nil, nil, nil, nil, nil, nil, nil)
	w := run(http.MethodGet, "/tasks", "/tasks", nil, func(r *gin.Engine) {
		r.GET("/tasks", h.ListTasks)
	})
	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

// --- RunTask ---

func TestRunTask_UnknownName(t *testing.T) {
	h, _, _, sched, _, _ := newTestHandler(t)
	sched.EXPECT().RunTaskManual("bogus").Return(nil, errors.New("unknown task"))
	w := run(http.MethodPost, "/tasks/:name/run", "/tasks/bogus/run", nil, func(r *gin.Engine) {
		r.POST("/tasks/:name/run", h.RunTask)
	})
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestRunTask_Success(t *testing.T) {
	h, _, _, sched, _, _ := newTestHandler(t)
	sched.EXPECT().RunTaskManual("dedup_refresh").Return(&database.Operation{ID: "op-1"}, nil)
	w := run(http.MethodPost, "/tasks/:name/run", "/tasks/dedup_refresh/run", nil, func(r *gin.Engine) {
		r.POST("/tasks/:name/run", h.RunTask)
	})
	assert.Equal(t, http.StatusAccepted, w.Code)
}

// --- UpdateTaskConfig ---

func TestUpdateTaskConfig_KnownTask(t *testing.T) {
	h, store, _, _, _, _ := newTestHandler(t)
	// config.SaveConfigToDatabase persists settings; it reads/writes via the
	// SettingsStore subset of our store. Allow any setting writes.
	store.EXPECT().SetSetting(mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil).Maybe()
	store.EXPECT().GetSetting(mock.Anything).Return(nil, nil).Maybe()
	enabled := true
	body, _ := json.Marshal(map[string]any{"enabled": enabled})
	w := run(http.MethodPut, "/tasks/:name", "/tasks/dedup_refresh", body, func(r *gin.Engine) {
		r.PUT("/tasks/:name", h.UpdateTaskConfig)
	})
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestUpdateTaskConfig_UnknownTask(t *testing.T) {
	h, _, _, _, _, _ := newTestHandler(t)
	body, _ := json.Marshal(map[string]any{"enabled": true})
	w := run(http.MethodPut, "/tasks/:name", "/tasks/not_a_task", body, func(r *gin.Engine) {
		r.PUT("/tasks/:name", h.UpdateTaskConfig)
	})
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// --- RunMaintenanceWindowNow ---

func TestRunMaintenanceWindowNow_Triggers(t *testing.T) {
	h, _, _, sched, _, _ := newTestHandler(t)
	sched.EXPECT().RunMaintenanceWindow(mock.Anything).Return(nil)
	w := run(http.MethodPost, "/maintenance-window/run", "/maintenance-window/run", nil, func(r *gin.Engine) {
		r.POST("/maintenance-window/run", h.RunMaintenanceWindowNow)
	})
	assert.Equal(t, http.StatusAccepted, w.Code)
}

// --- GetMaintenanceWindowStatus ---

func TestGetMaintenanceWindowStatus_RunningState(t *testing.T) {
	h, _, _, sched, _, _ := newTestHandler(t)
	sched.EXPECT().GetLastMaintenanceRunDate().Return("2026-06-01")
	sched.EXPECT().IsMaintenanceRunning().Return(true)
	w := run(http.MethodGet, "/maintenance-window/status", "/maintenance-window/status", nil, func(r *gin.Engine) {
		r.GET("/maintenance-window/status", h.GetMaintenanceWindowStatus)
	})
	assert.Equal(t, http.StatusOK, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	data := resp["data"].(map[string]any)
	assert.Equal(t, true, data["currently_running"])
	assert.NotEmpty(t, data["next_run_estimate"])
}

// --- UpdateMaintenanceWindowConfig ---

func TestUpdateMaintenanceWindowConfig_Valid(t *testing.T) {
	h, store, _, _, _, _ := newTestHandler(t)
	store.EXPECT().SetSetting(mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil).Maybe()
	store.EXPECT().GetSetting(mock.Anything).Return(nil, nil).Maybe()
	body, _ := json.Marshal(map[string]any{"enabled": true, "window_start": 3, "window_end": 5})
	w := run(http.MethodPut, "/maintenance-window/config", "/maintenance-window/config", body, func(r *gin.Engine) {
		r.PUT("/maintenance-window/config", h.UpdateMaintenanceWindowConfig)
	})
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestUpdateMaintenanceWindowConfig_InvalidHour(t *testing.T) {
	h, _, _, _, _, _ := newTestHandler(t)
	body, _ := json.Marshal(map[string]any{"enabled": true, "window_start": 24, "window_end": 5})
	w := run(http.MethodPut, "/maintenance-window/config", "/maintenance-window/config", body, func(r *gin.Engine) {
		r.PUT("/maintenance-window/config", h.UpdateMaintenanceWindowConfig)
	})
	assert.Equal(t, http.StatusBadRequest, w.Code)
}
