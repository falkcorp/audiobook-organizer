// file: internal/server/handlers/ai_test.go
// version: 1.0.0
// guid: 0e40aea8-a75e-4dc9-9521-11521efacaf8
// last-edited: 2026-06-03

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
		nil, // registry; tests that need it pass via the typed mock below
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

func TestAIHandler_StartScan_OK_202(t *testing.T) {
	pipe := handlersmocks.NewMockAIPipeline(t)
	pipe.EXPECT().StartScan(mock.Anything, "realtime").Return(&database.Scan{ID: 7}, nil).Once()
	h := newAIHandler(nil, nil, pipe, nil)
	c, w := newAICtx(http.MethodPost, "/ai/scans", `{"mode":"realtime"}`, nil)
	h.StartScan(c)
	assert.Equal(t, http.StatusAccepted, w.Code)
}

func TestAIHandler_StartScan_PipelineError_500(t *testing.T) {
	pipe := handlersmocks.NewMockAIPipeline(t)
	pipe.EXPECT().StartScan(mock.Anything, "realtime").Return(nil, errors.New("boom")).Once()
	h := newAIHandler(nil, nil, pipe, nil)
	c, w := newAICtx(http.MethodPost, "/ai/scans", `{}`, nil) // bad body → defaults to realtime
	h.StartScan(c)
	assert.Equal(t, http.StatusInternalServerError, w.Code)
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
