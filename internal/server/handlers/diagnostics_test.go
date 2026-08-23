// file: internal/server/handlers/diagnostics_test.go
// version: 1.4.0
// guid: 8ab4b825-05c3-4569-b450-0dca6b872771
// last-edited: 2026-08-23

package handlers_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	databasemocks "github.com/falkcorp/audiobook-organizer/internal/database/mocks"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
	handlersmocks "github.com/falkcorp/audiobook-organizer/internal/server/handlers/mocks"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
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

// TestDiagnosticsHandler_StartExport_ReturnsEnqueuedOpID pins the identity that
// was actually broken: the id in the response body must be the one EnqueueOp
// returned, because that is the id the client polls and later hands to
// DownloadExport.
//
// The previous version of this test asserted only the 202 and the word
// "generating". Both stayed true for six days while the handler returned a
// separately-minted legacy id that resolved at neither endpoint, so the export
// never reported completion and never downloaded. A test cannot catch an
// identity bug without asserting the identity.
func TestDiagnosticsHandler_StartExport_ReturnsEnqueuedOpID(t *testing.T) {
	store := databasemocks.NewMockStore(t)
	reg := handlersmocks.NewMockOperationsRegistry(t)

	// A value no other code path in this test could produce, so a pass cannot be
	// explained by an echoed request field or a zero value.
	const enqueuedID = "01JENQUEUED0000000000EXPORT"
	reg.EXPECT().EnqueueOp(mock.Anything, "diagnostics.export", mock.Anything).
		Return(enqueuedID, nil)

	h := handlers.NewDiagnosticsHandler(store, nil, nil, nil, reg)
	c, w := newDiagCtx(http.MethodPost, "/diagnostics/export", `{"category":"general"}`, nil)
	h.StartExport(c)

	assert.Equal(t, http.StatusAccepted, w.Code)

	var body struct {
		Data struct {
			OperationID string `json:"operation_id"`
			Status      string `json:"status"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, enqueuedID, body.Data.OperationID,
		"must return the enqueued op's id, not a separately-minted one")
	assert.Equal(t, "queued", body.Data.Status)
}

// TestDiagnosticsHandler_StartExport_EnqueueFailureIs500 pins that a failed
// enqueue is not reported as an accepted export.
func TestDiagnosticsHandler_StartExport_EnqueueFailureIs500(t *testing.T) {
	store := databasemocks.NewMockStore(t)
	reg := handlersmocks.NewMockOperationsRegistry(t)
	reg.EXPECT().EnqueueOp(mock.Anything, "diagnostics.export", mock.Anything).
		Return("", assert.AnError)

	h := handlers.NewDiagnosticsHandler(store, nil, nil, nil, reg)
	c, w := newDiagCtx(http.MethodPost, "/diagnostics/export", `{"category":"general"}`, nil)
	h.StartExport(c)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestDiagnosticsHandler_StartExport_InvalidCategory_400(t *testing.T) {
	store := databasemocks.NewMockStore(t)
	h := handlers.NewDiagnosticsHandler(store, nil, nil, nil, nil)
	c, w := newDiagCtx(http.MethodPost, "/diagnostics/export", `{"category":"bogus"}`, nil)
	h.StartExport(c)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// ── DownloadExport ────────────────────────────────────────────────────────

// GetOperationV2 returns (nil, nil) for a row that is not there, so absent and
// broken are distinguishable — and must stay distinguished. This test and the
// one below it were a single test asserting 404 for a store ERROR, which told a
// user whose database had just failed that their export never existed.
func TestDiagnosticsHandler_DownloadExport_AbsentRowIs404(t *testing.T) {
	store := databasemocks.NewMockStore(t)
	// Looked up as a v2 row: StartExport hands back an EnqueueOp id, so a legacy
	// lookup here would miss every export this endpoint is ever asked for.
	store.EXPECT().GetOperationV2("missing").Return(nil, nil)

	h := handlers.NewDiagnosticsHandler(store, nil, nil, nil, nil)
	c, w := newDiagCtx(http.MethodGet, "/diagnostics/export/missing/download", "",
		gin.Params{{Key: "operationId", Value: "missing"}})
	h.DownloadExport(c)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestDiagnosticsHandler_DownloadExport_StoreErrorIs500(t *testing.T) {
	store := databasemocks.NewMockStore(t)
	store.EXPECT().GetOperationV2("boom").Return(nil, assert.AnError)

	h := handlers.NewDiagnosticsHandler(store, nil, nil, nil, nil)
	c, w := newDiagCtx(http.MethodGet, "/diagnostics/export/boom/download", "",
		gin.Params{{Key: "operationId", Value: "boom"}})
	h.DownloadExport(c)

	assert.Equal(t, http.StatusInternalServerError, w.Code,
		"a store failure must not be reported as a missing export")
}

// TestDiagnosticsHandler_DownloadExport_TerminalFailureIsNot202 covers the case
// that made a killed export indistinguishable from a running one.
//
// interrupted_dropped is not hypothetical: diagnostics.export declares a 30m
// Timeout but no ProgressTimeout, so the watchdog's 5m default applies, and any
// export slower than that is cancelled and lands here.
func TestDiagnosticsHandler_DownloadExport_TerminalFailureIsNot202(t *testing.T) {
	for _, status := range []string{"failed", "canceled", "interrupted_dropped", "interrupted_quiesced"} {
		t.Run(status, func(t *testing.T) {
			store := databasemocks.NewMockStore(t)
			msg := "collect books: pebble: closed"
			store.EXPECT().GetOperationV2("op-x").Return(&database.OperationV2Row{
				ID: "op-x", Status: status,
				ProgressMessage: "Generating export data",
				ErrorMessage:    &msg,
			}, nil)

			h := handlers.NewDiagnosticsHandler(store, nil, nil, nil, nil)
			c, w := newDiagCtx(http.MethodGet, "/diagnostics/export/op-x/download", "",
				gin.Params{{Key: "operationId", Value: "op-x"}})
			h.DownloadExport(c)

			assert.NotEqual(t, http.StatusAccepted, w.Code,
				"a terminal op reported as 202 makes a polling client wait forever")
			assert.Equal(t, http.StatusInternalServerError, w.Code)
		})
	}
}

func TestDiagnosticsHandler_DownloadExport_NotCompleted_202(t *testing.T) {
	store := databasemocks.NewMockStore(t)
	store.EXPECT().GetOperationV2("op-1").
		Return(&database.OperationV2Row{ID: "op-1", Status: "running", ProgressMessage: "still going"}, nil)

	h := handlers.NewDiagnosticsHandler(store, nil, nil, nil, nil)
	c, w := newDiagCtx(http.MethodGet, "/diagnostics/export/op-1/download", "",
		gin.Params{{Key: "operationId", Value: "op-1"}})
	h.DownloadExport(c)

	// Not-completed branch preserves the 202 status code.
	assert.Equal(t, http.StatusAccepted, w.Code)
}

// TestDiagnosticsHandler_DownloadExport_Completed_ServesZipFromV2Result closes
// the loop on the export fix: the op's Run stores {"zip_path": ...} on its own
// v2 row via ReporterSetResult, and this endpoint has to read it back from
// there. Nothing else covered that hand-off — the two tests above stop at the
// error branches, which pass whichever row type is consulted.
func TestDiagnosticsHandler_DownloadExport_Completed_ServesZipFromV2Result(t *testing.T) {
	zipPath := filepath.Join(t.TempDir(), "diagnostics.zip")
	require.NoError(t, os.WriteFile(zipPath, []byte("PK\x03\x04not-a-real-zip"), 0o600))

	store := databasemocks.NewMockStore(t)
	result := `{"zip_path":"` + zipPath + `"}`
	store.EXPECT().GetOperationV2("op-done").
		Return(&database.OperationV2Row{ID: "op-done", Status: "completed", ResultData: &result}, nil)

	h := handlers.NewDiagnosticsHandler(store, nil, nil, nil, nil)
	c, w := newDiagCtx(http.MethodGet, "/diagnostics/export/op-done/download", "",
		gin.Params{{Key: "operationId", Value: "op-done"}})
	h.DownloadExport(c)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "PK\x03\x04not-a-real-zip", w.Body.String(),
		"must serve the file named by the v2 row's result payload")
}

// ── SubmitAI ──────────────────────────────────────────────────────────────

func TestDiagnosticsHandler_SubmitAI_NoAPIKey_400(t *testing.T) {
	orig := config.AppConfig
	defer func() { config.AppConfig = orig }()
	config.AppConfig.OpenAIAPIKey = ""

	store := databasemocks.NewMockStore(t)
	h := handlers.NewDiagnosticsHandler(store, nil, nil, nil, nil)
	c, w := newDiagCtx(http.MethodPost, "/diagnostics/submit-ai", `{}`, nil)
	h.SubmitAI(c)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// TestDiagnosticsHandler_SubmitAI_ReturnsEnqueuedOpID pins the same identity
// StartExport's test pins, for the same reason: the id in the body is the one
// the client polls at /diagnostics/ai-results/:operationId, so a handler that
// returns anything other than the enqueued op's id reports progress that never
// arrives. SubmitAI used to mint its own ULID and hand-drive a legacy row; the
// run is now the diagnostics.ai-analyze op and the id must be that op's.
func TestDiagnosticsHandler_SubmitAI_ReturnsEnqueuedOpID(t *testing.T) {
	orig := config.AppConfig
	defer func() { config.AppConfig = orig }()
	config.AppConfig.OpenAIAPIKey = "test-key"

	store := databasemocks.NewMockStore(t)
	reg := handlersmocks.NewMockOperationsRegistry(t)

	const enqueuedID = "01JENQUEUED00000000DIAGAI"
	reg.EXPECT().EnqueueOp(mock.Anything, handlers.DiagnosticsAIDefID,
		mock.MatchedBy(func(p any) bool {
			// The category and description the caller sent must reach the op —
			// they select the prompt and are the whole point of the request.
			params, ok := p.(handlers.DiagnosticsAIOpParams)
			return ok && params.Category == "metadata" && params.Description == "why so many dupes"
		})).Return(enqueuedID, nil)

	h := handlers.NewDiagnosticsHandler(store, nil, nil, nil, reg)
	c, w := newDiagCtx(http.MethodPost, "/diagnostics/submit-ai",
		`{"category":"metadata","description":"why so many dupes"}`, nil)
	h.SubmitAI(c)

	assert.Equal(t, http.StatusAccepted, w.Code)

	var body struct {
		Data struct {
			OperationID string `json:"operation_id"`
			Status      string `json:"status"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, enqueuedID, body.Data.OperationID,
		"must return the enqueued op's id, not a separately-minted one")
	assert.Equal(t, "queued", body.Data.Status)
}

// TestDiagnosticsHandler_SubmitAI_EmptyCategoryDefaultsToGeneral pins that the
// default is applied before the params are handed to the op, not left for the
// op to guess.
func TestDiagnosticsHandler_SubmitAI_EmptyCategoryDefaultsToGeneral(t *testing.T) {
	orig := config.AppConfig
	defer func() { config.AppConfig = orig }()
	config.AppConfig.OpenAIAPIKey = "test-key"

	store := databasemocks.NewMockStore(t)
	reg := handlersmocks.NewMockOperationsRegistry(t)
	reg.EXPECT().EnqueueOp(mock.Anything, handlers.DiagnosticsAIDefID,
		mock.MatchedBy(func(p any) bool {
			params, ok := p.(handlers.DiagnosticsAIOpParams)
			return ok && params.Category == "general"
		})).Return("op-1", nil)

	h := handlers.NewDiagnosticsHandler(store, nil, nil, nil, reg)
	c, w := newDiagCtx(http.MethodPost, "/diagnostics/submit-ai", `{}`, nil)
	h.SubmitAI(c)

	assert.Equal(t, http.StatusAccepted, w.Code)
}

// TestDiagnosticsHandler_SubmitAI_EnqueueFailureIs500 pins that a failed enqueue
// is not reported as an accepted submission. The old handler could not fail this
// way — it answered 202 and then lost the work in a goroutine.
func TestDiagnosticsHandler_SubmitAI_EnqueueFailureIs500(t *testing.T) {
	orig := config.AppConfig
	defer func() { config.AppConfig = orig }()
	config.AppConfig.OpenAIAPIKey = "test-key"

	store := databasemocks.NewMockStore(t)
	reg := handlersmocks.NewMockOperationsRegistry(t)
	reg.EXPECT().EnqueueOp(mock.Anything, handlers.DiagnosticsAIDefID, mock.Anything).
		Return("", assert.AnError)

	h := handlers.NewDiagnosticsHandler(store, nil, nil, nil, reg)
	c, w := newDiagCtx(http.MethodPost, "/diagnostics/submit-ai", `{"category":"general"}`, nil)
	h.SubmitAI(c)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

// ── GetAIResults ──────────────────────────────────────────────────────────

func TestDiagnosticsHandler_GetAIResults_NotFound(t *testing.T) {
	store := databasemocks.NewMockStore(t)
	store.EXPECT().GetOperationV2("missing").Return(nil, assert.AnError)

	h := handlers.NewDiagnosticsHandler(store, nil, nil, nil, nil)
	c, w := newDiagCtx(http.MethodGet, "/diagnostics/ai-results/missing", "",
		gin.Params{{Key: "operationId", Value: "missing"}})
	h.GetAIResults(c)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

// TestDiagnosticsHandler_GetAIResults_ReadsTheV2Row is the guard that fails if
// the lookup is pointed back at the legacy keyspace: the run writes its result
// to its own v2 row, so a GetOperationByID reader would find nothing.
func TestDiagnosticsHandler_GetAIResults_ReadsTheV2Row(t *testing.T) {
	store := databasemocks.NewMockStore(t)
	rd := `{"suggestions":[{"id":"s1","action":"merge_versions"}],"raw_responses":[{"custom_id":"c1"}]}`
	store.EXPECT().GetOperationV2("op-1").
		Return(&database.OperationV2Row{ID: "op-1", Status: "completed", ResultData: diagStrPtr(rd)}, nil)
	store.AssertNotCalled(t, "GetOperationByID", mock.Anything)

	h := handlers.NewDiagnosticsHandler(store, nil, nil, nil, nil)
	c, w := newDiagCtx(http.MethodGet, "/diagnostics/ai-results/op-1", "",
		gin.Params{{Key: "operationId", Value: "op-1"}})
	h.GetAIResults(c)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "s1")
	assert.Contains(t, w.Body.String(), "c1", "raw_responses must survive the round trip too")
}

// TestDiagnosticsHandler_GetAIResults_NotCompleted_200 reports a run still in
// flight, carrying the v2 row's progress message so the client has something to
// show. ProgressMessage is the v2 field; the legacy row called it Message.
func TestDiagnosticsHandler_GetAIResults_NotCompleted_200(t *testing.T) {
	store := databasemocks.NewMockStore(t)
	store.EXPECT().GetOperationV2("op-1").
		Return(&database.OperationV2Row{
			ID: "op-1", Status: "running", ProgressMessage: "Batch b-9: in_progress (poll 3/288)",
		}, nil)

	h := handlers.NewDiagnosticsHandler(store, nil, nil, nil, nil)
	c, w := newDiagCtx(http.MethodGet, "/diagnostics/ai-results/op-1", "",
		gin.Params{{Key: "operationId", Value: "op-1"}})
	h.GetAIResults(c)

	// GetAIResults uses RespondWithOK (200) for the not-completed branch,
	// unlike DownloadExport which uses 202.
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "running")
	assert.Contains(t, w.Body.String(), "poll 3/288",
		"the in-flight message must reach the client, not be dropped for an empty string")
}

// TestDiagnosticsHandler_GetAIResults_CompletedWithNoResult_IsEmptyNot500 pins
// that a completed run whose payload never landed answers with an empty
// suggestion list rather than failing.
func TestDiagnosticsHandler_GetAIResults_CompletedWithNoResult_IsEmptyNot500(t *testing.T) {
	store := databasemocks.NewMockStore(t)
	store.EXPECT().GetOperationV2("op-1").
		Return(&database.OperationV2Row{ID: "op-1", Status: "completed"}, nil)

	h := handlers.NewDiagnosticsHandler(store, nil, nil, nil, nil)
	c, w := newDiagCtx(http.MethodGet, "/diagnostics/ai-results/op-1", "",
		gin.Params{{Key: "operationId", Value: "op-1"}})
	h.GetAIResults(c)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), `"suggestions":[]`)
}

// ── ApplySuggestions ──────────────────────────────────────────────────────

func TestDiagnosticsHandler_ApplySuggestions_MissingFields_400(t *testing.T) {
	store := databasemocks.NewMockStore(t)
	h := handlers.NewDiagnosticsHandler(store, nil, nil, nil, nil)
	// Missing required operation_id / approved_suggestion_ids.
	c, w := newDiagCtx(http.MethodPost, "/diagnostics/apply-suggestions", `{}`, nil)
	h.ApplySuggestions(c)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestDiagnosticsHandler_ApplySuggestions_OperationNotFound(t *testing.T) {
	store := databasemocks.NewMockStore(t)
	store.EXPECT().GetOperationV2("op-x").Return(nil, assert.AnError)

	h := handlers.NewDiagnosticsHandler(store, nil, nil, nil, nil)
	c, w := newDiagCtx(http.MethodPost, "/diagnostics/apply-suggestions",
		`{"operation_id":"op-x","approved_suggestion_ids":["s1"]}`, nil)
	h.ApplySuggestions(c)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestDiagnosticsHandler_ApplySuggestions_MergeVersions(t *testing.T) {
	store := databasemocks.NewMockStore(t)
	mergeSvc := handlersmocks.NewMockMergeService(t)

	rd := `{"suggestions":[{"id":"s1","action":"merge_versions","book_ids":["b1","b2"],"primary_id":"b1"}]}`
	store.EXPECT().GetOperationV2("op-1").
		Return(&database.OperationV2Row{ID: "op-1", Status: "completed", ResultData: diagStrPtr(rd)}, nil)
	mergeSvc.EXPECT().MergeBooks([]string{"b1", "b2"}, "b1").Return(nil, nil)

	h := handlers.NewDiagnosticsHandler(store, mergeSvc, nil, nil, nil)
	c, w := newDiagCtx(http.MethodPost, "/diagnostics/apply-suggestions",
		`{"operation_id":"op-1","approved_suggestion_ids":["s1"]}`, nil)
	h.ApplySuggestions(c)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), `"applied":1`)
}

// ── GetDBHealth ───────────────────────────────────────────────────────────

// TestDiagnosticsHandler_GetDBHealth_NilStore_500 covers the nil-store guard.
func TestDiagnosticsHandler_GetDBHealth_NilStore_500(t *testing.T) {
	h := handlers.NewDiagnosticsHandler(nil, nil, nil, nil, nil)
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

	h := handlers.NewDiagnosticsHandler(store, nil, nil, nil, nil)
	c, w := newDiagCtx(http.MethodGet, "/diagnostics/db-health", "", nil)
	h.GetDBHealth(c)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), `"total_entries":7`)
}
