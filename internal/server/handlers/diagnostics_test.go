// file: internal/server/handlers/diagnostics_test.go
// version: 1.1.0
// guid: 8ab4b825-05c3-4569-b450-0dca6b872771
// last-edited: 2026-07-07

package handlers_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	databasemocks "github.com/falkcorp/audiobook-organizer/internal/database/mocks"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
	handlersmocks "github.com/falkcorp/audiobook-organizer/internal/server/handlers/mocks"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// newDiagCtx builds a gin context with the given path params and an optional
// JSON request body.
func newDiagCtx(method, path, body string, params gin.Params) (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	var req *http.Request
	if body != "" {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	c, _ := gin.CreateTestContext(w)
	c.Request = req
	c.Params = params
	return c, w
}

func diagStrPtr(s string) *string { return &s }

// ── StartExport ───────────────────────────────────────────────────────────

func TestDiagnosticsHandler_StartExport_Enqueues202(t *testing.T) {
	store := databasemocks.NewMockStore(t)
	reg := handlersmocks.NewMockOperationsRegistry(t)

	store.EXPECT().CreateOperation(mock.Anything, "diagnostics_export", mock.Anything).
		Return(&database.Operation{}, nil)
	reg.EXPECT().EnqueueOp(mock.Anything, "diagnostics.export", mock.Anything).
		Return("op-1", nil)

	h := handlers.NewDiagnosticsHandler(store, nil, nil, nil, nil, reg, nil)
	c, w := newDiagCtx(http.MethodPost, "/diagnostics/export", `{"category":"general"}`, nil)
	h.StartExport(c)

	assert.Equal(t, http.StatusAccepted, w.Code)
	assert.Contains(t, w.Body.String(), "generating")
}

func TestDiagnosticsHandler_StartExport_InvalidCategory_400(t *testing.T) {
	store := databasemocks.NewMockStore(t)
	h := handlers.NewDiagnosticsHandler(store, nil, nil, nil, nil, nil, nil)
	c, w := newDiagCtx(http.MethodPost, "/diagnostics/export", `{"category":"bogus"}`, nil)
	h.StartExport(c)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// ── DownloadExport ────────────────────────────────────────────────────────

func TestDiagnosticsHandler_DownloadExport_NotFound(t *testing.T) {
	store := databasemocks.NewMockStore(t)
	store.EXPECT().GetOperationByID("missing").Return(nil, assert.AnError)

	h := handlers.NewDiagnosticsHandler(store, nil, nil, nil, nil, nil, nil)
	c, w := newDiagCtx(http.MethodGet, "/diagnostics/export/missing/download", "",
		gin.Params{{Key: "operationId", Value: "missing"}})
	h.DownloadExport(c)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestDiagnosticsHandler_DownloadExport_NotCompleted_202(t *testing.T) {
	store := databasemocks.NewMockStore(t)
	store.EXPECT().GetOperationByID("op-1").
		Return(&database.Operation{ID: "op-1", Status: "running", Message: "still going"}, nil)

	h := handlers.NewDiagnosticsHandler(store, nil, nil, nil, nil, nil, nil)
	c, w := newDiagCtx(http.MethodGet, "/diagnostics/export/op-1/download", "",
		gin.Params{{Key: "operationId", Value: "op-1"}})
	h.DownloadExport(c)

	// Not-completed branch preserves the 202 status code.
	assert.Equal(t, http.StatusAccepted, w.Code)
}

// ── SubmitAI ──────────────────────────────────────────────────────────────

func TestDiagnosticsHandler_SubmitAI_NoAPIKey_400(t *testing.T) {
	orig := config.AppConfig
	defer func() { config.AppConfig = orig }()
	config.AppConfig.OpenAIAPIKey = ""

	store := databasemocks.NewMockStore(t)
	h := handlers.NewDiagnosticsHandler(store, nil, nil, nil, nil, nil, nil)
	c, w := newDiagCtx(http.MethodPost, "/diagnostics/submit-ai", `{}`, nil)
	h.SubmitAI(c)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// TestDiagnosticsHandler_SubmitAI_NilParserFallback verifies the synchronous
// 202 response plus the async "no AI parser available" fallback path. The
// handler does its real work in a goroutine, so we synchronize on the mock
// store's terminal UpdateOperationStatus("completed", ...) call rather than
// sleeping.
func TestDiagnosticsHandler_SubmitAI_NilParserFallback(t *testing.T) {
	orig := config.AppConfig
	defer func() { config.AppConfig = orig }()
	config.AppConfig.OpenAIAPIKey = "test-key"

	store := databasemocks.NewMockStore(t)
	diagSvc := handlersmocks.NewMockDiagnosticsService(t)

	done := make(chan struct{})

	store.EXPECT().CreateOperation(mock.Anything, "diagnostics_ai", mock.Anything).
		Return(&database.Operation{}, nil)
	// Intermediate progress updates (running 10/50/70) — match loosely.
	store.EXPECT().UpdateOperationStatus(mock.Anything, "running", mock.Anything, mock.Anything, mock.Anything).
		Return(nil).Maybe()
	store.EXPECT().UpdateOperationResultData(mock.Anything, mock.Anything).Return(nil).Maybe()
	// Terminal fallback status — close the done channel so the test can join.
	store.EXPECT().UpdateOperationStatus(mock.Anything, "completed", 100, 100, "Batch data prepared (no AI parser available)").
		Run(func(_ string, _ string, _ int, _ int, _ string) { close(done) }).
		Return(nil)

	diagSvc.EXPECT().CollectAllBooks().Return([]database.BookCore{{ID: "b1", Title: "X"}}, nil)

	// batchParser is nil → the no-parser fallback path.
	h := handlers.NewDiagnosticsHandler(store, diagSvc, nil, nil, nil, nil, nil)
	c, w := newDiagCtx(http.MethodPost, "/diagnostics/submit-ai", `{"category":"general"}`, nil)
	h.SubmitAI(c)

	assert.Equal(t, http.StatusAccepted, w.Code)
	assert.Contains(t, w.Body.String(), "submitted")

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("async submit-ai fallback did not complete in time")
	}
}

// ── GetAIResults ──────────────────────────────────────────────────────────

func TestDiagnosticsHandler_GetAIResults_NotFound(t *testing.T) {
	store := databasemocks.NewMockStore(t)
	store.EXPECT().GetOperationByID("missing").Return(nil, assert.AnError)

	h := handlers.NewDiagnosticsHandler(store, nil, nil, nil, nil, nil, nil)
	c, w := newDiagCtx(http.MethodGet, "/diagnostics/ai-results/missing", "",
		gin.Params{{Key: "operationId", Value: "missing"}})
	h.GetAIResults(c)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestDiagnosticsHandler_GetAIResults_NotCompleted_200(t *testing.T) {
	store := databasemocks.NewMockStore(t)
	store.EXPECT().GetOperationByID("op-1").
		Return(&database.Operation{ID: "op-1", Status: "running"}, nil)

	h := handlers.NewDiagnosticsHandler(store, nil, nil, nil, nil, nil, nil)
	c, w := newDiagCtx(http.MethodGet, "/diagnostics/ai-results/op-1", "",
		gin.Params{{Key: "operationId", Value: "op-1"}})
	h.GetAIResults(c)

	// GetAIResults uses RespondWithOK (200) for the not-completed branch,
	// unlike DownloadExport which uses 202.
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestDiagnosticsHandler_GetAIResults_Completed_WithSuggestions(t *testing.T) {
	store := databasemocks.NewMockStore(t)
	rd := `{"suggestions":[{"id":"s1","action":"merge_versions"}]}`
	store.EXPECT().GetOperationByID("op-1").
		Return(&database.Operation{ID: "op-1", Status: "completed", ResultData: diagStrPtr(rd)}, nil)

	h := handlers.NewDiagnosticsHandler(store, nil, nil, nil, nil, nil, nil)
	c, w := newDiagCtx(http.MethodGet, "/diagnostics/ai-results/op-1", "",
		gin.Params{{Key: "operationId", Value: "op-1"}})
	h.GetAIResults(c)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "s1")
}

// ── ApplySuggestions ──────────────────────────────────────────────────────

func TestDiagnosticsHandler_ApplySuggestions_MissingFields_400(t *testing.T) {
	store := databasemocks.NewMockStore(t)
	h := handlers.NewDiagnosticsHandler(store, nil, nil, nil, nil, nil, nil)
	// Missing required operation_id / approved_suggestion_ids.
	c, w := newDiagCtx(http.MethodPost, "/diagnostics/apply-suggestions", `{}`, nil)
	h.ApplySuggestions(c)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestDiagnosticsHandler_ApplySuggestions_OperationNotFound(t *testing.T) {
	store := databasemocks.NewMockStore(t)
	store.EXPECT().GetOperationByID("op-x").Return(nil, assert.AnError)

	h := handlers.NewDiagnosticsHandler(store, nil, nil, nil, nil, nil, nil)
	c, w := newDiagCtx(http.MethodPost, "/diagnostics/apply-suggestions",
		`{"operation_id":"op-x","approved_suggestion_ids":["s1"]}`, nil)
	h.ApplySuggestions(c)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestDiagnosticsHandler_ApplySuggestions_MergeVersions(t *testing.T) {
	store := databasemocks.NewMockStore(t)
	mergeSvc := handlersmocks.NewMockMergeService(t)

	rd := `{"suggestions":[{"id":"s1","action":"merge_versions","book_ids":["b1","b2"],"primary_id":"b1"}]}`
	store.EXPECT().GetOperationByID("op-1").
		Return(&database.Operation{ID: "op-1", Status: "completed", ResultData: diagStrPtr(rd)}, nil)
	mergeSvc.EXPECT().MergeBooks([]string{"b1", "b2"}, "b1").Return(nil, nil)

	h := handlers.NewDiagnosticsHandler(store, nil, mergeSvc, nil, nil, nil, nil)
	c, w := newDiagCtx(http.MethodPost, "/diagnostics/apply-suggestions",
		`{"operation_id":"op-1","approved_suggestion_ids":["s1"]}`, nil)
	h.ApplySuggestions(c)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), `"applied":1`)
}

// ── GetDBHealth ───────────────────────────────────────────────────────────

// TestDiagnosticsHandler_GetDBHealth_NilStore_500 covers the nil-store guard.
func TestDiagnosticsHandler_GetDBHealth_NilStore_500(t *testing.T) {
	h := handlers.NewDiagnosticsHandler(nil, nil, nil, nil, nil, nil, nil)
	c, w := newDiagCtx(http.MethodGet, "/diagnostics/db-health", "", nil)
	h.GetDBHealth(c)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

// TestDiagnosticsHandler_GetDBHealth_MockStore exercises GetDBHealth with a mock
// store. NOTE (test limitation): a mock store satisfies neither the
// *database.SQLiteStore nor *database.PebbleStore concrete type-switch case, so
// the sqlite/pebble sub-objects stay nil. The embedding/ai-scan stores are
// concrete *database structs (not interfaces) and are passed nil here, so those
// sub-objects also stay zero-valued. This test therefore asserts the
// metadata-cache path (CountPrefix) and a clean 200 rather than deep DB stats.
func TestDiagnosticsHandler_GetDBHealth_MockStore(t *testing.T) {
	orig := config.AppConfig
	defer func() { config.AppConfig = orig }()
	config.AppConfig.MetadataFetchCacheTTLDays = 0 // skip the ScanPrefix expiry scan

	store := databasemocks.NewMockStore(t)
	store.EXPECT().CountPrefix("metadata_fetch_cache:").Return(int64(7), nil)

	h := handlers.NewDiagnosticsHandler(store, nil, nil, nil, nil, nil, nil)
	c, w := newDiagCtx(http.MethodGet, "/diagnostics/db-health", "", nil)
	h.GetDBHealth(c)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), `"total_entries":7`)
}
