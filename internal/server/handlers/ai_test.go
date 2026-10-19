// file: internal/server/handlers/ai_test.go
// version: 1.4.1
// guid: 0e40aea8-a75e-4dc9-9521-11521efacaf8
// last-edited: 2026-10-18

package handlers_test

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/aiscan"
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

// A scan that has not finished refuses a second start with the store's own text,
// and nothing is enqueued.
func TestAIHandler_StartScan_AlreadyRunning_409(t *testing.T) {
	pipe := handlersmocks.NewMockAIPipeline(t)
	reg := handlersmocks.NewMockOperationsRegistry(t)
	pipe.EXPECT().CreateScan("realtime").
		Return(nil, fmt.Errorf("create scan: %w", &database.ScanAlreadyRunningError{ScanID: 4, Status: "scanning"})).Once()

	h := newAIHandlerWithRegistry(nil, pipe, reg)
	c, w := newAICtx(http.MethodPost, "/ai/scans", `{"mode":"realtime"}`, nil)
	h.StartScan(c)

	assert.Equal(t, http.StatusConflict, w.Code)
	assert.Contains(t, w.Body.String(), "already running (scan #4, scanning)")
}

// oneAuthorStore is the pipeline's author store for the concurrency test: just
// enough to let CreateScan count authors.
type oneAuthorStore struct{}

func (oneAuthorStore) GetAllAuthors() ([]database.Author, error) {
	return []database.Author{{ID: 1, Name: "Concurrent Fixture"}}, nil
}
func (oneAuthorStore) GetAuthorByID(int) (*database.Author, error)  { return nil, nil }
func (oneAuthorStore) GetAllAuthorBookCounts() (map[int]int, error) { return map[int]int{}, nil }
func (oneAuthorStore) GetBooksByAuthorIDWithRoleCore(int) ([]database.BookCore, error) {
	return nil, nil
}

// Two starts at once on the REAL scan store and pipeline: one scan is created
// (202) and the others are refused (409). A mocked pipeline would prove nothing
// here; the guarantee is the store's check-and-create being one step. Run under
// -race.
func TestAIHandler_StartScan_ConcurrentStartsCreateExactlyOneScan(t *testing.T) {
	scanStore, err := database.NewAIScanStore(t.TempDir() + "/aiscan.db")
	require.NoError(t, err)
	t.Cleanup(func() { _ = scanStore.Close() })
	pm := aiscan.NewPipelineManager(scanStore, oneAuthorStore{}, nil)
	reg := handlersmocks.NewMockOperationsRegistry(t)
	reg.EXPECT().EnqueueOp(mock.Anything, "ai.author-scan", mock.Anything).Return("op-1", nil).Maybe()

	h := newAIHandlerWithRegistry(scanStore, pm, reg)
	const starters = 8
	codes := make([]int, starters)
	var wg sync.WaitGroup
	gate := make(chan struct{})
	for i := range starters {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, w := newAICtx(http.MethodPost, "/ai/scans", `{"mode":"realtime"}`, nil)
			<-gate
			h.StartScan(c)
			codes[i] = w.Code
		}()
	}
	close(gate)
	wg.Wait()

	created, refused := 0, 0
	for _, code := range codes {
		switch code {
		case http.StatusAccepted:
			created++
		case http.StatusConflict:
			refused++
		default:
			t.Errorf("unexpected status %d", code)
		}
	}
	assert.Equal(t, 1, created)
	assert.Equal(t, starters-1, refused)
	scans, err := scanStore.ListScans()
	require.NoError(t, err)
	assert.Len(t, scans, 1)
}

func TestAIHandler_StartScan_EnqueueError_500(t *testing.T) {
	pipe := handlersmocks.NewMockAIPipeline(t)
	reg := handlersmocks.NewMockOperationsRegistry(t)

	pipe.EXPECT().CreateScan("realtime").Return(&database.Scan{ID: 11}, nil).Once()
	reg.EXPECT().EnqueueOp(mock.Anything, "ai.author-scan", mock.Anything).
		Return("", errors.New("queue full")).Once()
	// The scan row already exists and is not finished: left alone it would read as
	// a running scan and refuse every later start, so it is canceled.
	pipe.EXPECT().CancelScan(11).Return(nil).Once()

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

// mergeResult is a stored merge result with a keep author, as the pipeline writes it.
func mergeResult(id int, keep int, ids ...int) database.ScanResult {
	return database.ScanResult{ID: id, ScanID: 9, Agreement: "agreed", Suggestion: database.ScanSuggestion{
		Action: "merge", CanonicalName: "Fixture Name", AuthorIDs: ids, KeepID: keep, Source: "cross_validate",
	}}
}

// Apply validates and enqueues; it does not mark anything itself.
func TestAIHandler_ApplyScanResults_EnqueuesTheApplyOpLive(t *testing.T) {
	scanStore := handlersmocks.NewMockAIScanStore(t)
	pipe := handlersmocks.NewMockAIPipeline(t)
	reg := handlersmocks.NewMockOperationsRegistry(t)
	scanStore.EXPECT().GetScan(9).Return(&database.Scan{ID: 9, Status: "complete"}, nil).Once()
	pipe.EXPECT().EnsureKeepIDs(9).Return(nil).Once()
	scanStore.EXPECT().GetScanResults(9).Return([]database.ScanResult{
		mergeResult(1, 10, 10, 11), mergeResult(2, 20, 20, 21, 22),
	}, nil).Once()
	reg.EXPECT().EnqueueOp(mock.Anything, handlers.AIScanApplyDefID, mock.MatchedBy(func(p handlers.AIScanApplyOpParams) bool {
		// A real apply says dry_run=false itself: the op's own default is a preview.
		return p.ScanID == 9 && len(p.ResultIDs) == 2 && p.ResultIDs[0] == 1 && p.ResultIDs[1] == 2 &&
			p.DryRun != nil && !*p.DryRun
	})).Return("op-apply", nil).Once()

	h := handlers.NewAIHandler(nil, scanStore, pipe, nil, reg, func(b *database.Book) any { return b })
	c, w := newAICtx(http.MethodPost, "/ai/scans/9/apply", `{"result_ids":[1,2,2]}`, gin.Params{{Key: "id", Value: "9"}})
	h.ApplyScanResults(c)

	assert.Equal(t, http.StatusAccepted, w.Code)
	assert.Contains(t, w.Body.String(), `"id":"op-apply"`)
	assert.Contains(t, w.Body.String(), `"type":"ai.author-scan-apply"`)
}

// F5: applying from a superseded scan is refused with 409 and names the scan
// that replaced it, before anything is queued.
func TestAIHandler_ApplyScanResults_Superseded_409(t *testing.T) {
	scanStore := handlersmocks.NewMockAIScanStore(t)
	reg := handlersmocks.NewMockOperationsRegistry(t)
	scanStore.EXPECT().GetScan(9).Return(&database.Scan{ID: 9, Status: "superseded", SupersededBy: 12}, nil).Once()
	h := handlers.NewAIHandler(nil, scanStore, nil, nil, reg, func(b *database.Book) any { return b })
	c, w := newAICtx(http.MethodPost, "/ai/scans/9/apply", `{"result_ids":[1,2]}`, gin.Params{{Key: "id", Value: "9"}})
	h.ApplyScanResults(c)
	assert.Equal(t, http.StatusConflict, w.Code)
	assert.Contains(t, w.Body.String(), "superseded by scan 12")
}

func TestAIHandler_ApplyScanResults_ScanNotFinished_409(t *testing.T) {
	scanStore := handlersmocks.NewMockAIScanStore(t)
	reg := handlersmocks.NewMockOperationsRegistry(t)
	scanStore.EXPECT().GetScan(9).Return(&database.Scan{ID: 9, Status: "scanning"}, nil).Once()
	h := handlers.NewAIHandler(nil, scanStore, nil, nil, reg, func(b *database.Book) any { return b })
	c, w := newAICtx(http.MethodPost, "/ai/scans/9/apply", `{"result_ids":[1]}`, gin.Params{{Key: "id", Value: "9"}})
	h.ApplyScanResults(c)
	assert.Equal(t, http.StatusConflict, w.Code)
	assert.Contains(t, w.Body.String(), "scanning")
}

// Only merge rows can be applied: rename, alias, split, reclassify and skip are
// read-only here and are refused with 400, without queueing anything.
func TestAIHandler_ApplyScanResults_NonMergeRowIsRefused_400(t *testing.T) {
	for _, action := range []string{"rename", "alias", "split", "reclassify", "skip"} {
		t.Run(action, func(t *testing.T) {
			scanStore := handlersmocks.NewMockAIScanStore(t)
			reg := handlersmocks.NewMockOperationsRegistry(t)
			row := mergeResult(3, 10, 10, 11)
			row.Suggestion.Action = action
			scanStore.EXPECT().GetScan(9).Return(&database.Scan{ID: 9, Status: "complete"}, nil).Once()
			scanStore.EXPECT().GetScanResults(9).Return([]database.ScanResult{mergeResult(1, 10, 10, 11), row}, nil).Once()
			h := handlers.NewAIHandler(nil, scanStore, nil, nil, reg, func(b *database.Book) any { return b })
			c, w := newAICtx(http.MethodPost, "/ai/scans/9/apply", `{"result_ids":[1,3]}`, gin.Params{{Key: "id", Value: "9"}})
			h.ApplyScanResults(c)
			assert.Equal(t, http.StatusBadRequest, w.Code)
			assert.Contains(t, w.Body.String(), "result 3 is a ")
			assert.Contains(t, w.Body.String(), action)
			assert.Contains(t, w.Body.String(), "only merge suggestions can be applied here")
		})
	}
}

func TestAIHandler_ApplyScanResults_UnknownAppliedAndKeeplessRows(t *testing.T) {
	scanStore := handlersmocks.NewMockAIScanStore(t)
	reg := handlersmocks.NewMockOperationsRegistry(t)
	applied := mergeResult(1, 10, 10, 11)
	applied.Applied = true
	scanStore.EXPECT().GetScan(9).Return(&database.Scan{ID: 9, Status: "complete"}, nil).Times(3)
	scanStore.EXPECT().GetScanResults(9).Return([]database.ScanResult{applied, mergeResult(2, 0, 20, 21)}, nil).Times(3)
	h := handlers.NewAIHandler(nil, scanStore, nil, nil, reg, func(b *database.Book) any { return b })

	c, w := newAICtx(http.MethodPost, "/ai/scans/9/apply", `{"result_ids":[1]}`, gin.Params{{Key: "id", Value: "9"}})
	h.ApplyScanResults(c)
	assert.Equal(t, http.StatusConflict, w.Code)
	assert.Contains(t, w.Body.String(), "result 1 is already applied")

	c, w = newAICtx(http.MethodPost, "/ai/scans/9/apply", `{"result_ids":[77]}`, gin.Params{{Key: "id", Value: "9"}})
	h.ApplyScanResults(c)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "result 77 is not in scan 9")

	c, w = newAICtx(http.MethodPost, "/ai/scans/9/apply", `{"result_ids":[2]}`, gin.Params{{Key: "id", Value: "9"}})
	h.ApplyScanResults(c)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "no keep author")
}

func TestAIHandler_ApplyScanResults_EmptySelectionIsRefused_400(t *testing.T) {
	reg := handlersmocks.NewMockOperationsRegistry(t)
	h := handlers.NewAIHandler(nil, handlersmocks.NewMockAIScanStore(t), nil, nil, reg, func(b *database.Book) any { return b })
	c, w := newAICtx(http.MethodPost, "/ai/scans/9/apply", `{"result_ids":[]}`, gin.Params{{Key: "id", Value: "9"}})
	h.ApplyScanResults(c)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestAIHandler_ApplyScanResults_InvalidID_400(t *testing.T) {
	scanStore := handlersmocks.NewMockAIScanStore(t)
	reg := handlersmocks.NewMockOperationsRegistry(t)
	h := handlers.NewAIHandler(nil, scanStore, nil, nil, reg, func(b *database.Book) any { return b })
	c, w := newAICtx(http.MethodPost, "/ai/scans/x/apply", `{"result_ids":[1]}`, gin.Params{{Key: "id", Value: "x"}})
	h.ApplyScanResults(c)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// GetScanResults fills in keep authors lazily, names every author a row names, and
// links a disagreement's two rows to each other.
func TestAIHandler_GetScanResults_NamesAuthorsAndPairsDisagreements(t *testing.T) {
	scanStore := handlersmocks.NewMockAIScanStore(t)
	pipe := handlersmocks.NewMockAIPipeline(t)
	store := databasemocks.NewMockStore(t)
	pipe.EXPECT().EnsureKeepIDs(3).Return(nil).Once()

	groups := database.ScanResult{ID: 5, ScanID: 3, Agreement: "disagreed", Suggestion: database.ScanSuggestion{
		Action: "merge", AuthorIDs: []int{10, 11}, KeepID: 10, Source: "groups_scan"}}
	full := database.ScanResult{ID: 6, ScanID: 3, Agreement: "disagreed", Suggestion: database.ScanSuggestion{
		Action: "alias", AuthorIDs: []int{10, 11}, Source: "full_scan"}}
	lone := database.ScanResult{ID: 7, ScanID: 3, Agreement: "disagreed", Suggestion: database.ScanSuggestion{
		Action: "merge", AuthorIDs: []int{30}, KeepID: 30, Source: "groups_scan"}}
	scanStore.EXPECT().GetScanResults(3).Return([]database.ScanResult{groups, full, lone}, nil).Once()
	store.EXPECT().GetAuthorByID(10).Return(&database.Author{ID: 10, Name: "Kept One"}, nil).Once()
	store.EXPECT().GetAuthorByID(11).Return(&database.Author{ID: 11, Name: "Variant One"}, nil).Once()
	// GetAuthorByID follows the tombstone: 30 was merged away and answers as 10.
	store.EXPECT().GetAuthorByID(30).Return(&database.Author{ID: 10, Name: "Kept One"}, nil).Once()

	h := handlers.NewAIHandler(store, scanStore, pipe, nil, nil, func(b *database.Book) any { return b })
	c, w := newAICtx(http.MethodGet, "/ai/scans/3/results", "", gin.Params{{Key: "id", Value: "3"}})
	h.GetScanResults(c)

	assert.Equal(t, http.StatusOK, w.Code)
	body := w.Body.String()
	assert.Contains(t, body, `"author_names":{"10":"Kept One","11":"Variant One"}`)
	assert.Contains(t, body, `"keep_id":10`)
	assert.Contains(t, body, `"paired_with":6`)
	assert.Contains(t, body, `"paired_with":5`)
	assert.Equal(t, 2, strings.Count(body, `"paired_with"`), "a disagreed row with no counterpart is not paired")
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

// ── ListAIJobs ────────────────────────────────────────────────────────────

func TestAIHandler_ListAIJobs_OK(t *testing.T) {
	// MockStore satisfies database.AIJobsStore (Store embeds it), so
	// database.GetAIJobs resolves directly and ListAIJobs is invoked. Defaults:
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
