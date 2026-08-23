// file: internal/server/handlers/ai_test.go
// version: 1.2.0
// guid: 0e40aea8-a75e-4dc9-9521-11521efacaf8
// last-edited: 2026-08-23

package handlers_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/cache"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	databasemocks "github.com/falkcorp/audiobook-organizer/internal/database/mocks"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
	handlersmocks "github.com/falkcorp/audiobook-organizer/internal/server/handlers/mocks"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// newAICtx builds a gin context with the given path params, query string, and
// optional JSON request body.
func newAICtx(method, path, body string, params gin.Params) (*gin.Context, *httptest.ResponseRecorder) {
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

// disableAI sets config so the OpenAI parser reports IsEnabled()==false,
// guaranteeing the parser-gated handlers short-circuit without any network
// call. Returns a restore func.
func disableAI(t *testing.T) func() {
	t.Helper()
	orig := config.AppConfig
	config.AppConfig.EnableAIParsing = false
	config.AppConfig.OpenAIAPIKey = ""
	return func() { config.AppConfig = orig }
}

func aiDedupCache() *cache.Cache[gin.H] {
	return cache.NewWithLimit[gin.H]("test-dedup", 0, 100)
}

// newAIHandler builds an AIHandler with the supplied dependencies; any nil arg
// is left as a nil interface/pointer.
func newAIHandler(
	store database.Store,
	scanStore handlers.AIScanStore,
	pipeline handlers.AIPipeline,
	updater handlers.AudiobookUpdater,
) *handlers.AIHandler {
	return handlers.NewAIHandler(
		store,
		scanStore,
		pipeline,
		updater,
		aiDedupCache(),
		nil, // registry; see newAIHandlerWithRegistry
		func(b *database.Book) any { return b },
	)
}

// newAIHandlerWithRegistry is newAIHandler plus an operations registry. StartScan
// needs one since the AI scan became a v2 operation: it creates the scan, then
// enqueues ai.author-scan to run it.
func newAIHandlerWithRegistry(
	scanStore handlers.AIScanStore,
	pipeline handlers.AIPipeline,
	registry handlers.OperationsRegistry,
) *handlers.AIHandler {
	return handlers.NewAIHandler(
		nil,
		scanStore,
		pipeline,
		nil,
		aiDedupCache(),
		registry,
		func(b *database.Book) any { return b },
	)
}

// ── ParseFilename ─────────────────────────────────────────────────────────

func TestAIHandler_ParseFilename_MissingBody_400(t *testing.T) {
	h := newAIHandler(nil, nil, nil, nil)
	c, w := newAICtx(http.MethodPost, "/ai/parse-filename", `{}`, nil)
	h.ParseFilename(c)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestAIHandler_ParseFilename_AIDisabled_400(t *testing.T) {
	defer disableAI(t)()
	h := newAIHandler(nil, nil, nil, nil)
	c, w := newAICtx(http.MethodPost, "/ai/parse-filename", `{"filename":"book.m4b"}`, nil)
	h.ParseFilename(c)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "AI parsing is not enabled")
}

// ── TestConnection ────────────────────────────────────────────────────────

func TestAIHandler_TestConnection_NoAPIKey_400(t *testing.T) {
	defer disableAI(t)() // also clears OpenAIAPIKey
	h := newAIHandler(nil, nil, nil, nil)
	c, w := newAICtx(http.MethodPost, "/ai/test-connection", `{}`, nil)
	h.TestConnection(c)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "API key not provided")
}

// ── TestMetadataSource ────────────────────────────────────────────────────

func TestAIHandler_TestMetadataSource_MissingSourceID_400(t *testing.T) {
	h := newAIHandler(nil, nil, nil, nil)
	c, w := newAICtx(http.MethodPost, "/metadata-sources/test", `{"api_key":"x"}`, nil)
	h.TestMetadataSource(c)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestAIHandler_TestMetadataSource_MissingAPIKey_400(t *testing.T) {
	h := newAIHandler(nil, nil, nil, nil)
	c, w := newAICtx(http.MethodPost, "/metadata-sources/test", `{"source_id":"google-books"}`, nil)
	h.TestMetadataSource(c)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "api_key is required")
}

func TestAIHandler_TestMetadataSource_UnknownSource_400(t *testing.T) {
	h := newAIHandler(nil, nil, nil, nil)
	c, w := newAICtx(http.MethodPost, "/metadata-sources/test", `{"source_id":"nope","api_key":"x"}`, nil)
	h.TestMetadataSource(c)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "unknown source")
}

// ── ParseAudiobook ────────────────────────────────────────────────────────

func TestAIHandler_ParseAudiobook_NoStore_500(t *testing.T) {
	h := newAIHandler(nil, nil, nil, nil)
	c, w := newAICtx(http.MethodPost, "/audiobooks/abc/parse-with-ai", `{}`, gin.Params{{Key: "id", Value: "abc"}})
	h.ParseAudiobook(c)
	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestAIHandler_ParseAudiobook_BookNotFound_404(t *testing.T) {
	store := databasemocks.NewMockStore(t)
	store.EXPECT().GetBookByID("missing").Return(nil, errors.New("not found")).Once()
	h := newAIHandler(store, nil, nil, nil)
	c, w := newAICtx(http.MethodPost, "/audiobooks/missing/parse-with-ai", `{}`, gin.Params{{Key: "id", Value: "missing"}})
	h.ParseAudiobook(c)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestAIHandler_ParseAudiobook_AIDisabled_400(t *testing.T) {
	defer disableAI(t)()
	store := databasemocks.NewMockStore(t)
	store.EXPECT().GetBookByID("b1").Return(&database.Book{ID: "b1", Title: "X"}, nil).Once()
	h := newAIHandler(store, nil, nil, nil)
	c, w := newAICtx(http.MethodPost, "/audiobooks/b1/parse-with-ai", `{}`, gin.Params{{Key: "id", Value: "b1"}})
	h.ParseAudiobook(c)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "AI parsing is not enabled")
}

// ── StartScan ─────────────────────────────────────────────────────────────

func TestAIHandler_StartScan_NoPipeline_500(t *testing.T) {
	h := newAIHandler(nil, nil, nil, nil)
	c, w := newAICtx(http.MethodPost, "/ai/scans", `{}`, nil)
	h.StartScan(c)
	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestAIHandler_StartScan_NoRegistry_500(t *testing.T) {
	pipe := handlersmocks.NewMockAIPipeline(t)
	h := newAIHandlerWithRegistry(nil, pipe, nil)
	c, w := newAICtx(http.MethodPost, "/ai/scans", `{"mode":"realtime"}`, nil)
	h.StartScan(c)
	assert.Equal(t, http.StatusInternalServerError, w.Code)
	// The scan must not be created when there is nothing to run it — otherwise
	// an orphan scan row is left at "pending" forever. NewMockAIPipeline(t)
	// asserts expectations on cleanup, and CreateScan has none.
}

// TestAIHandler_StartScan_OK_202 pins the whole ordering: create the scan (so
// its id can be returned now), enqueue the op that runs it, then link the two.
func TestAIHandler_StartScan_OK_202(t *testing.T) {
	pipe := handlersmocks.NewMockAIPipeline(t)
	reg := handlersmocks.NewMockOperationsRegistry(t)

	pipe.EXPECT().CreateScan("realtime").Return(&database.Scan{ID: 7}, nil).Once()
	reg.EXPECT().EnqueueOp(mock.Anything, "ai.author-scan", mock.Anything).
		Return("op-abc", nil).Once()
	pipe.EXPECT().LinkOperation(7, "op-abc").Return(nil).Once()

	h := newAIHandlerWithRegistry(nil, pipe, reg)
	c, w := newAICtx(http.MethodPost, "/ai/scans", `{"mode":"realtime"}`, nil)
	h.StartScan(c)

	assert.Equal(t, http.StatusAccepted, w.Code)
	// Positive control: the response must carry BOTH ids. Asserting only the
	// status code would pass just as happily against a handler that enqueued
	// nothing, since 202 is also what the old v1 path returned.
	assert.Contains(t, w.Body.String(), `"operation_id":"op-abc"`)
	assert.Contains(t, w.Body.String(), `"id":7`)
}

// TestAIHandler_StartScan_LinkFails_Still202 fixes the deliberate asymmetry: the
// op is already queued and will run the scan, so a failed link degrades the
// cancel route but must not fail a request whose work is under way.
func TestAIHandler_StartScan_LinkFails_Still202(t *testing.T) {
	pipe := handlersmocks.NewMockAIPipeline(t)
	reg := handlersmocks.NewMockOperationsRegistry(t)

	pipe.EXPECT().CreateScan("batch").Return(&database.Scan{ID: 9}, nil).Once()
	reg.EXPECT().EnqueueOp(mock.Anything, "ai.author-scan", mock.Anything).
		Return("op-xyz", nil).Once()
	pipe.EXPECT().LinkOperation(9, "op-xyz").Return(errors.New("store down")).Once()

	h := newAIHandlerWithRegistry(nil, pipe, reg)
	c, w := newAICtx(http.MethodPost, "/ai/scans", `{"mode":"batch"}`, nil)
	h.StartScan(c)

	assert.Equal(t, http.StatusAccepted, w.Code)
	assert.Contains(t, w.Body.String(), `"operation_id":"op-xyz"`)
}

func TestAIHandler_StartScan_CreateScanError_500(t *testing.T) {
	pipe := handlersmocks.NewMockAIPipeline(t)
	reg := handlersmocks.NewMockOperationsRegistry(t)
	pipe.EXPECT().CreateScan("realtime").Return(nil, errors.New("boom")).Once()

	h := newAIHandlerWithRegistry(nil, pipe, reg)
	c, w := newAICtx(http.MethodPost, "/ai/scans", `{}`, nil) // bad body → defaults to realtime
	h.StartScan(c)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
	// reg has no EnqueueOp expectation: a scan that could not be created must
	// not enqueue an op that would then fail looking for it.
}

func TestAIHandler_StartScan_EnqueueError_500(t *testing.T) {
	pipe := handlersmocks.NewMockAIPipeline(t)
	reg := handlersmocks.NewMockOperationsRegistry(t)

	pipe.EXPECT().CreateScan("realtime").Return(&database.Scan{ID: 11}, nil).Once()
	reg.EXPECT().EnqueueOp(mock.Anything, "ai.author-scan", mock.Anything).
		Return("", errors.New("queue full")).Once()

	h := newAIHandlerWithRegistry(nil, pipe, reg)
	c, w := newAICtx(http.MethodPost, "/ai/scans", `{"mode":"realtime"}`, nil)
	h.StartScan(c)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
	// No LinkOperation expectation: there is no op id worth linking to.
}

// ── ListScans ─────────────────────────────────────────────────────────────

func TestAIHandler_ListScans_NoStore_EmptyOK(t *testing.T) {
	h := newAIHandler(nil, nil, nil, nil)
	c, w := newAICtx(http.MethodGet, "/ai/scans", "", nil)
	h.ListScans(c)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "scans")
}

func TestAIHandler_ListScans_OK(t *testing.T) {
	scanStore := handlersmocks.NewMockAIScanStore(t)
	scanStore.EXPECT().ListScans().Return([]database.Scan{{ID: 1}, {ID: 2}}, nil).Once()
	h := newAIHandler(nil, scanStore, nil, nil)
	c, w := newAICtx(http.MethodGet, "/ai/scans", "", nil)
	h.ListScans(c)
	assert.Equal(t, http.StatusOK, w.Code)
}

// ── GetScan ───────────────────────────────────────────────────────────────

func TestAIHandler_GetScan_InvalidID_400(t *testing.T) {
	scanStore := handlersmocks.NewMockAIScanStore(t)
	h := newAIHandler(nil, scanStore, nil, nil)
	c, w := newAICtx(http.MethodGet, "/ai/scans/abc", "", gin.Params{{Key: "id", Value: "abc"}})
	h.GetScan(c)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestAIHandler_GetScan_NotFound_404(t *testing.T) {
	scanStore := handlersmocks.NewMockAIScanStore(t)
	scanStore.EXPECT().GetScan(5).Return(nil, nil).Once()
	h := newAIHandler(nil, scanStore, nil, nil)
	c, w := newAICtx(http.MethodGet, "/ai/scans/5", "", gin.Params{{Key: "id", Value: "5"}})
	h.GetScan(c)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestAIHandler_GetScan_OK(t *testing.T) {
	scanStore := handlersmocks.NewMockAIScanStore(t)
	scanStore.EXPECT().GetScan(5).Return(&database.Scan{ID: 5}, nil).Once()
	scanStore.EXPECT().GetPhases(5).Return([]database.ScanPhase{}, nil).Once()
	h := newAIHandler(nil, scanStore, nil, nil)
	c, w := newAICtx(http.MethodGet, "/ai/scans/5", "", gin.Params{{Key: "id", Value: "5"}})
	h.GetScan(c)
	assert.Equal(t, http.StatusOK, w.Code)
}

// ── GetScanResults ────────────────────────────────────────────────────────

func TestAIHandler_GetScanResults_OK(t *testing.T) {
	scanStore := handlersmocks.NewMockAIScanStore(t)
	scanStore.EXPECT().GetScanResults(3).Return([]database.ScanResult{}, nil).Once()
	h := newAIHandler(nil, scanStore, nil, nil)
	c, w := newAICtx(http.MethodGet, "/ai/scans/3/results", "", gin.Params{{Key: "id", Value: "3"}})
	h.GetScanResults(c)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestAIHandler_GetScanResults_NoStore_404(t *testing.T) {
	h := newAIHandler(nil, nil, nil, nil)
	c, w := newAICtx(http.MethodGet, "/ai/scans/3/results", "", gin.Params{{Key: "id", Value: "3"}})
	h.GetScanResults(c)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

// ── ApplyScanResults ──────────────────────────────────────────────────────

func TestAIHandler_ApplyScanResults_OK(t *testing.T) {
	scanStore := handlersmocks.NewMockAIScanStore(t)
	scanStore.EXPECT().MarkResultApplied(9, 1).Return(nil).Once()
	scanStore.EXPECT().MarkResultApplied(9, 2).Return(errors.New("nope")).Once()
	h := newAIHandler(nil, scanStore, nil, nil)
	c, w := newAICtx(http.MethodPost, "/ai/scans/9/apply", `{"result_ids":[1,2]}`, gin.Params{{Key: "id", Value: "9"}})
	h.ApplyScanResults(c)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "applied")
}

func TestAIHandler_ApplyScanResults_InvalidID_400(t *testing.T) {
	scanStore := handlersmocks.NewMockAIScanStore(t)
	h := newAIHandler(nil, scanStore, nil, nil)
	c, w := newAICtx(http.MethodPost, "/ai/scans/x/apply", `{"result_ids":[1]}`, gin.Params{{Key: "id", Value: "x"}})
	h.ApplyScanResults(c)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// ── DeleteScan ────────────────────────────────────────────────────────────

func TestAIHandler_DeleteScan_OK(t *testing.T) {
	scanStore := handlersmocks.NewMockAIScanStore(t)
	scanStore.EXPECT().DeleteScan(4).Return(nil).Once()
	h := newAIHandler(nil, scanStore, nil, nil)
	c, w := newAICtx(http.MethodDelete, "/ai/scans/4", "", gin.Params{{Key: "id", Value: "4"}})
	h.DeleteScan(c)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "deleted")
}

func TestAIHandler_DeleteScan_Error_500(t *testing.T) {
	scanStore := handlersmocks.NewMockAIScanStore(t)
	scanStore.EXPECT().DeleteScan(4).Return(errors.New("boom")).Once()
	h := newAIHandler(nil, scanStore, nil, nil)
	c, w := newAICtx(http.MethodDelete, "/ai/scans/4", "", gin.Params{{Key: "id", Value: "4"}})
	h.DeleteScan(c)
	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

// ── CancelScan ────────────────────────────────────────────────────────────

func TestAIHandler_CancelScan_NoPipeline_500(t *testing.T) {
	h := newAIHandler(nil, nil, nil, nil)
	c, w := newAICtx(http.MethodPost, "/ai/scans/2/cancel", "", gin.Params{{Key: "id", Value: "2"}})
	h.CancelScan(c)
	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestAIHandler_CancelScan_NotFound_404(t *testing.T) {
	pipe := handlersmocks.NewMockAIPipeline(t)
	pipe.EXPECT().CancelScan(2).Return(errors.New("no such scan")).Once()
	h := newAIHandler(nil, nil, pipe, nil)
	c, w := newAICtx(http.MethodPost, "/ai/scans/2/cancel", "", gin.Params{{Key: "id", Value: "2"}})
	h.CancelScan(c)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestAIHandler_CancelScan_OK(t *testing.T) {
	pipe := handlersmocks.NewMockAIPipeline(t)
	pipe.EXPECT().CancelScan(2).Return(nil).Once()
	h := newAIHandler(nil, nil, pipe, nil)
	c, w := newAICtx(http.MethodPost, "/ai/scans/2/cancel", "", gin.Params{{Key: "id", Value: "2"}})
	h.CancelScan(c)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "canceled")
}

// ── CompareScans ──────────────────────────────────────────────────────────

func TestAIHandler_CompareScans_InvalidA_400(t *testing.T) {
	scanStore := handlersmocks.NewMockAIScanStore(t)
	h := newAIHandler(nil, scanStore, nil, nil)
	c, w := newAICtx(http.MethodGet, "/ai/scans/compare?a=x&b=2", "", nil)
	h.CompareScans(c)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestAIHandler_CompareScans_OK(t *testing.T) {
	scanStore := handlersmocks.NewMockAIScanStore(t)
	scanStore.EXPECT().GetScanResults(1).Return([]database.ScanResult{}, nil).Once()
	scanStore.EXPECT().GetScanResults(2).Return([]database.ScanResult{}, nil).Once()
	h := newAIHandler(nil, scanStore, nil, nil)
	c, w := newAICtx(http.MethodGet, "/ai/scans/compare?a=1&b=2", "", nil)
	h.CompareScans(c)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "new_in_b")
}

// ── ReviewDuplicateAuthors ────────────────────────────────────────────────

func TestAIHandler_ReviewDuplicateAuthors_AIDisabled_400(t *testing.T) {
	defer disableAI(t)()
	h := newAIHandler(nil, nil, nil, nil)
	c, w := newAICtx(http.MethodPost, "/authors/duplicates/ai-review", `{}`, nil)
	h.ReviewDuplicateAuthors(c)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "AI parsing is not enabled")
}

// ── ApplyAuthorReview ─────────────────────────────────────────────────────

func TestAIHandler_ApplyAuthorReview_NoSuggestions_400(t *testing.T) {
	h := newAIHandler(nil, nil, nil, nil)
	c, w := newAICtx(http.MethodPost, "/authors/duplicates/ai-review/apply", `{"suggestions":[]}`, nil)
	h.ApplyAuthorReview(c)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "no suggestions provided")
}

func TestAIHandler_ApplyAuthorReview_NoRegistry_500(t *testing.T) {
	h := newAIHandler(nil, nil, nil, nil) // registry is nil
	c, w := newAICtx(http.MethodPost, "/authors/duplicates/ai-review/apply",
		`{"suggestions":[{"group_index":0,"action":"merge","keep_id":1,"merge_ids":[2]}]}`, nil)
	h.ApplyAuthorReview(c)
	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

// ── ListAIJobs ────────────────────────────────────────────────────────────

func TestAIHandler_ListAIJobs_OK(t *testing.T) {
	// MockStore satisfies database.AIJobsStore (Store embeds it), so
	// UnwrapAIJobsStore resolves directly and ListAIJobs is invoked. Defaults:
	// limit 100, offset 0, empty filters.
	store := databasemocks.NewMockStore(t)
	store.EXPECT().ListAIJobs("", "", 100, 0).Return([]database.AIJob{{ID: "j1"}}, nil).Once()
	h := newAIHandler(store, nil, nil, nil)
	c, w := newAICtx(http.MethodGet, "/ai-jobs", "", nil)
	h.ListAIJobs(c)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "jobs")
}

func TestAIHandler_ListAIJobs_ClampsLimit(t *testing.T) {
	// limit>500 and negative offset are clamped to 100/0 respectively.
	store := databasemocks.NewMockStore(t)
	store.EXPECT().ListAIJobs("dedup_review", "pending", 100, 0).Return([]database.AIJob{}, nil).Once()
	h := newAIHandler(store, nil, nil, nil)
	c, w := newAICtx(http.MethodGet, "/ai-jobs?type=dedup_review&status=pending&limit=9999&offset=-5", "", nil)
	h.ListAIJobs(c)
	assert.Equal(t, http.StatusOK, w.Code)
}

// ── ReviewDuplicateAuthors: the same-mode in-flight guard ─────────────────
//
// The guard answers a request with the run already in flight instead of
// starting a second one. It matches v2 rows on DefID and on the mode decoded
// out of Params — neither of which the compiler checks against what EnqueueOp
// actually wrote. A guard that stops matching does not fail loudly: it just
// returns nil forever and every request starts another concurrent review. So
// each arm below pins one way it could go quietly wrong.

func enableAI(t *testing.T) func() {
	t.Helper()
	orig := config.AppConfig
	config.AppConfig.EnableAIParsing = true
	config.AppConfig.OpenAIAPIKey = "test-key"
	return func() { config.AppConfig = orig }
}

// activeReviewRow builds a row shaped like the one EnqueueOp writes. Params is
// a JSON literal rather than a marshalled AIReviewOpParams so this asserts the
// wire shape itself: if the json tag on Mode is ever renamed, the guard stops
// matching, and only a literal here notices.
func activeReviewRow(id, defID, paramsJSON string) database.OperationV2Row {
	return database.OperationV2Row{ID: id, DefID: defID, Status: "running", Params: paramsJSON}
}

// reviewRequest serves one review request against the given active rows.
// wantEnqueue says whether the guard is expected to let the request through:
// when false, the mock registry carries no EnqueueOp expectation, so a second
// run starting anyway fails the test on an unexpected call.
func reviewRequest(t *testing.T, rows []database.OperationV2Row, body string, wantEnqueue bool) *httptest.ResponseRecorder {
	t.Helper()
	store := databasemocks.NewMockStore(t)
	store.EXPECT().ListActiveOperationsV2().Return(rows, nil)
	reg := handlersmocks.NewMockOperationsRegistry(t)
	if wantEnqueue {
		reg.EXPECT().EnqueueOp(mock.Anything, handlers.AIAuthorReviewDefID, mock.Anything).
			Return("op-new", nil).Once()
	}
	h := handlers.NewAIHandler(store, nil, nil, nil, aiDedupCache(), reg,
		func(b *database.Book) any { return b })
	c, w := newAICtx(http.MethodPost, "/authors/duplicates/ai-review", body, nil)
	h.ReviewDuplicateAuthors(c)
	return w
}

func TestAIHandler_ReviewDuplicateAuthors_SameModeReturnsTheRunningOp(t *testing.T) {
	defer enableAI(t)()
	w := reviewRequest(t,
		[]database.OperationV2Row{activeReviewRow("op-live", handlers.AIAuthorReviewDefID, `{"mode":"full"}`)},
		`{"mode":"full"}`, false)

	assert.Equal(t, http.StatusAccepted, w.Code)
	assert.Contains(t, w.Body.String(), "op-live",
		"the caller must get the id of the run already in flight, not a new one")
}

// The guard is per-MODE, not per-def. A groups run must not block a full run —
// that is the whole reason this is not just a ConcurrencyKey.
func TestAIHandler_ReviewDuplicateAuthors_ADifferentModeDoesNotBlock(t *testing.T) {
	defer enableAI(t)()
	w := reviewRequest(t,
		[]database.OperationV2Row{activeReviewRow("op-groups", handlers.AIAuthorReviewDefID, `{"mode":"groups"}`)},
		`{"mode":"full"}`, true)

	assert.Equal(t, http.StatusAccepted, w.Code)
	assert.NotContains(t, w.Body.String(), "op-groups")
}

// An unrelated op that happens to be active must not be mistaken for a review.
func TestAIHandler_ReviewDuplicateAuthors_AnotherDefDoesNotBlock(t *testing.T) {
	defer enableAI(t)()
	w := reviewRequest(t,
		[]database.OperationV2Row{activeReviewRow("op-scan", "library.scan", `{"mode":"full"}`)},
		`{"mode":"full"}`, true)

	assert.Equal(t, http.StatusAccepted, w.Code)
	assert.NotContains(t, w.Body.String(), "op-scan")
}

// A row whose params do not decode is skipped, not treated as a match — the
// alternative is one malformed row wedging every future review request.
func TestAIHandler_ReviewDuplicateAuthors_UndecodableParamsAreSkipped(t *testing.T) {
	defer enableAI(t)()
	w := reviewRequest(t,
		[]database.OperationV2Row{activeReviewRow("op-bad", handlers.AIAuthorReviewDefID, `{{not json`)},
		`{"mode":"full"}`, true)

	assert.Equal(t, http.StatusAccepted, w.Code)
	assert.NotContains(t, w.Body.String(), "op-bad")
}

// A store that cannot answer must not block the user from starting a review.
// Worst case is two concurrent runs, which is what the v1 scan did too.
func TestAIHandler_ReviewDuplicateAuthors_LookupFailureDoesNotBlock(t *testing.T) {
	defer enableAI(t)()
	store := databasemocks.NewMockStore(t)
	store.EXPECT().ListActiveOperationsV2().Return(nil, errors.New("pebble closed"))
	reg := handlersmocks.NewMockOperationsRegistry(t)
	reg.EXPECT().EnqueueOp(mock.Anything, handlers.AIAuthorReviewDefID, mock.Anything).Return("op-new", nil)
	h := handlers.NewAIHandler(store, nil, nil, nil, aiDedupCache(), reg,
		func(b *database.Book) any { return b })

	c, w := newAICtx(http.MethodPost, "/authors/duplicates/ai-review", `{"mode":"full"}`, nil)
	h.ReviewDuplicateAuthors(c)

	assert.Equal(t, http.StatusAccepted, w.Code)
	assert.Contains(t, w.Body.String(), "op-new")
}
