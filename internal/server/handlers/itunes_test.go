// file: internal/server/handlers/itunes_test.go
// version: 1.1.0
// guid: 9c2a4e71-6b53-4d18-8f0a-2e7c1b9d3a64
// last-edited: 2026-06-16

package handlers_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	itunesservice "github.com/falkcorp/audiobook-organizer/internal/itunes/service"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
	handlersmocks "github.com/falkcorp/audiobook-organizer/internal/server/handlers/mocks"
	"github.com/stretchr/testify/assert"
)

// newITunesCtx builds a gin context with the given path params, query string,
// and optional JSON request body.
func newITunesCtx(method, path, body string, params gin.Params) (*gin.Context, *httptest.ResponseRecorder) {
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

// enabledSvc returns a mock ITunesService whose Enabled() reports true.
func enabledSvc(t *testing.T) *handlersmocks.MockITunesService {
	svc := handlersmocks.NewMockITunesService(t)
	svc.EXPECT().Enabled().Return(true).Maybe()
	return svc
}

func itunesStrptr(s string) *string { return &s }

// ── itunesEnabledOrError (503 disabled path) ──────────────────────────────

func TestITunesHandler_Import_ServiceDisabled_503(t *testing.T) {
	svc := handlersmocks.NewMockITunesService(t)
	svc.EXPECT().Enabled().Return(false).Once()

	h := handlers.NewITunesHandler(svc, nil, nil, handlersmocks.NewMockITunesStore(t))
	c, w := newITunesCtx(http.MethodPost, "/itunes/import", `{}`, nil)
	h.Import(c)

	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
}

func TestITunesHandler_Sync_ServiceNil_503(t *testing.T) {
	// A nil ITunesService (iTunes not configured) must also yield 503, not panic.
	h := handlers.NewITunesHandler(nil, nil, nil, handlersmocks.NewMockITunesStore(t))
	c, w := newITunesCtx(http.MethodPost, "/itunes/sync", `{}`, nil)
	h.Sync(c)

	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
}

// ── Validate ──────────────────────────────────────────────────────────────

func TestITunesHandler_Validate_BadJSON(t *testing.T) {
	h := handlers.NewITunesHandler(enabledSvc(t), nil, nil, handlersmocks.NewMockITunesStore(t))
	c, w := newITunesCtx(http.MethodPost, "/itunes/validate", `{not json`, nil)
	h.Validate(c)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestITunesHandler_Validate_MissingLibraryPath(t *testing.T) {
	// library_path is required by the binding tag.
	h := handlers.NewITunesHandler(enabledSvc(t), nil, nil, handlersmocks.NewMockITunesStore(t))
	c, w := newITunesCtx(http.MethodPost, "/itunes/validate", `{}`, nil)
	h.Validate(c)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestITunesHandler_Validate_LibraryNotFound(t *testing.T) {
	// A non-existent path drives itunesservice.Validate to ErrLibraryNotFound,
	// which the handler maps to 400.
	h := handlers.NewITunesHandler(enabledSvc(t), nil, nil, handlersmocks.NewMockITunesStore(t))
	c, w := newITunesCtx(http.MethodPost, "/itunes/validate",
		`{"library_path":"/nonexistent/path/to/iTunes.xml"}`, nil)
	h.Validate(c)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// ── TestMapping ─────────────────────────────────────────────────────────────

func TestITunesHandler_TestMapping_BadJSON(t *testing.T) {
	h := handlers.NewITunesHandler(enabledSvc(t), nil, nil, handlersmocks.NewMockITunesStore(t))
	c, w := newITunesCtx(http.MethodPost, "/itunes/test-mapping", `{bad`, nil)
	h.TestMapping(c)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestITunesHandler_TestMapping_MissingFields(t *testing.T) {
	// from/to/library_path are required; missing → 400 from binding.
	h := handlers.NewITunesHandler(enabledSvc(t), nil, nil, handlersmocks.NewMockITunesStore(t))
	c, w := newITunesCtx(http.MethodPost, "/itunes/test-mapping", `{"library_path":"/x"}`, nil)
	h.TestMapping(c)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// ── Import ──────────────────────────────────────────────────────────────────

func TestITunesHandler_Import_NilStore_500(t *testing.T) {
	h := handlers.NewITunesHandler(enabledSvc(t), nil, nil, nil)
	c, w := newITunesCtx(http.MethodPost, "/itunes/import", `{}`, nil)
	h.Import(c)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestITunesHandler_Import_NilRegistry_500(t *testing.T) {
	// store non-nil, registry nil → "operation registry not initialized".
	store := handlersmocks.NewMockITunesStore(t)
	h := handlers.NewITunesHandler(enabledSvc(t), nil, nil, store)
	c, w := newITunesCtx(http.MethodPost, "/itunes/import", `{}`, nil)
	h.Import(c)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestITunesHandler_Import_LibraryFileNotFound_400(t *testing.T) {
	store := handlersmocks.NewMockITunesStore(t)
	reg := handlersmocks.NewMockOperationsRegistry(t)
	h := handlers.NewITunesHandler(enabledSvc(t), nil, reg, store)
	c, w := newITunesCtx(http.MethodPost, "/itunes/import",
		`{"library_path":"/nonexistent/iTunes.xml","import_mode":"import"}`, nil)
	h.Import(c)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// ── WriteBack ────────────────────────────────────────────────────────────────

func TestITunesHandler_WriteBack_NilStore_500(t *testing.T) {
	h := handlers.NewITunesHandler(enabledSvc(t), nil, nil, nil)
	c, w := newITunesCtx(http.MethodPost, "/itunes/write-back", `{}`, nil)
	h.WriteBack(c)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestITunesHandler_WriteBack_NotEnabled_400(t *testing.T) {
	orig := config.AppConfig
	defer func() { config.AppConfig = orig }()
	config.AppConfig.ITunes.WriteBackEnabled = false

	store := handlersmocks.NewMockITunesStore(t)
	h := handlers.NewITunesHandler(enabledSvc(t), nil, nil, store)
	c, w := newITunesCtx(http.MethodPost, "/itunes/write-back", `{}`, nil)
	h.WriteBack(c)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// ── WriteBackAll ────────────────────────────────────────────────────────────

func TestITunesHandler_WriteBackAll_NotEnabled_400(t *testing.T) {
	orig := config.AppConfig
	defer func() { config.AppConfig = orig }()
	config.AppConfig.ITunes.WriteBackEnabled = false

	store := handlersmocks.NewMockITunesStore(t)
	h := handlers.NewITunesHandler(enabledSvc(t), nil, nil, store)
	c, w := newITunesCtx(http.MethodPost, "/itunes/write-back-all", `{}`, nil)
	h.WriteBackAll(c)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestITunesHandler_WriteBackAll_NoITLPath_400(t *testing.T) {
	orig := config.AppConfig
	defer func() { config.AppConfig = orig }()
	config.AppConfig.ITunes.WriteBackEnabled = true
	config.AppConfig.ITunes.LibraryWritePath = ""

	store := handlersmocks.NewMockITunesStore(t)
	h := handlers.NewITunesHandler(enabledSvc(t), nil, nil, store)
	c, w := newITunesCtx(http.MethodPost, "/itunes/write-back-all", `{}`, nil)
	h.WriteBackAll(c)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// ── WriteBackPreview ─────────────────────────────────────────────────────────

func TestITunesHandler_WriteBackPreview_NilStore_500(t *testing.T) {
	h := handlers.NewITunesHandler(enabledSvc(t), nil, nil, nil)
	c, w := newITunesCtx(http.MethodPost, "/itunes/write-back/preview", `{}`, nil)
	h.WriteBackPreview(c)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestITunesHandler_WriteBackPreview_NoLibraryPath_400(t *testing.T) {
	orig := config.AppConfig
	defer func() { config.AppConfig = orig }()
	config.AppConfig.ITunes.LibraryReadPath = ""

	store := handlersmocks.NewMockITunesStore(t)
	h := handlers.NewITunesHandler(enabledSvc(t), nil, nil, store)
	c, w := newITunesCtx(http.MethodPost, "/itunes/write-back/preview", `{}`, nil)
	h.WriteBackPreview(c)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// ── ListBooks ────────────────────────────────────────────────────────────────

func TestITunesHandler_ListBooks_Happy(t *testing.T) {
	store := handlersmocks.NewMockITunesStore(t)
	store.EXPECT().ListBooksByITunesPID(0, 0).Return([]database.Book{
		{ID: "b1", Title: "Book One", FilePath: "/x/b1.m4b", ITunesPersistentID: itunesStrptr("PID1")},
	}, nil)

	h := handlers.NewITunesHandler(enabledSvc(t), nil, nil, store)
	c, w := newITunesCtx(http.MethodGet, "/itunes/books", "", nil)
	h.ListBooks(c)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "PID1")
}

func TestITunesHandler_ListBooks_NilStore_500(t *testing.T) {
	h := handlers.NewITunesHandler(enabledSvc(t), nil, nil, nil)
	c, w := newITunesCtx(http.MethodGet, "/itunes/books", "", nil)
	h.ListBooks(c)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

// ── ImportStatus ─────────────────────────────────────────────────────────────

func TestITunesHandler_ImportStatus_Happy(t *testing.T) {
	store := handlersmocks.NewMockITunesStore(t)
	store.EXPECT().GetOperationByID("op1").Return(&database.Operation{
		ID: "op1", Status: "running", Progress: 5, Total: 10, Message: "working",
	}, nil)

	imp := handlersmocks.NewMockITunesImporter(t)
	imp.EXPECT().GetStatus("op1").Return(&itunesservice.ImportStatusSnapshot{
		Total: 10, Processed: 5, Imported: 4, Skipped: 1,
	})

	h := handlers.NewITunesHandler(enabledSvc(t), imp, nil, store)
	c, w := newITunesCtx(http.MethodGet, "/itunes/import-status/op1", "", gin.Params{{Key: "id", Value: "op1"}})
	h.ImportStatus(c)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), `"progress":50`)
}

func TestITunesHandler_ImportStatus_NotFound(t *testing.T) {
	store := handlersmocks.NewMockITunesStore(t)
	store.EXPECT().GetOperationByID("missing").Return(nil, assert.AnError)

	imp := handlersmocks.NewMockITunesImporter(t)
	h := handlers.NewITunesHandler(enabledSvc(t), imp, nil, store)
	c, w := newITunesCtx(http.MethodGet, "/itunes/import-status/missing", "", gin.Params{{Key: "id", Value: "missing"}})
	h.ImportStatus(c)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

// ── ImportStatusBulk ─────────────────────────────────────────────────────────

func TestITunesHandler_ImportStatusBulk_Happy(t *testing.T) {
	store := handlersmocks.NewMockITunesStore(t)
	store.EXPECT().GetOperationByID("op1").Return(&database.Operation{
		ID: "op1", Status: "done", Progress: 10, Total: 10,
	}, nil)

	imp := handlersmocks.NewMockITunesImporter(t)
	imp.EXPECT().GetStatusBulk([]string{"op1"}).Return(map[string]*itunesservice.ImportStatusSnapshot{
		"op1": {Total: 10, Processed: 10, Imported: 10},
	})

	h := handlers.NewITunesHandler(enabledSvc(t), imp, nil, store)
	c, w := newITunesCtx(http.MethodPost, "/itunes/import-status/bulk", `{"ids":["op1"]}`, nil)
	h.ImportStatusBulk(c)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "statuses")
}

func TestITunesHandler_ImportStatusBulk_MissingIDs_400(t *testing.T) {
	store := handlersmocks.NewMockITunesStore(t)
	imp := handlersmocks.NewMockITunesImporter(t)
	h := handlers.NewITunesHandler(enabledSvc(t), imp, nil, store)
	c, w := newITunesCtx(http.MethodPost, "/itunes/import-status/bulk", `{}`, nil)
	h.ImportStatusBulk(c)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// ── LibraryStatus ────────────────────────────────────────────────────────────

func TestITunesHandler_LibraryStatus_MissingPath_400(t *testing.T) {
	h := handlers.NewITunesHandler(enabledSvc(t), nil, nil, handlersmocks.NewMockITunesStore(t))
	c, w := newITunesCtx(http.MethodGet, "/itunes/library-status", "", nil)
	h.LibraryStatus(c)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestITunesHandler_LibraryStatus_NoFingerprint_OK(t *testing.T) {
	store := handlersmocks.NewMockITunesStore(t)
	store.EXPECT().GetLibraryFingerprint("/lib/iTunes.xml").Return(nil, nil)

	h := handlers.NewITunesHandler(enabledSvc(t), nil, nil, store)
	c, w := newITunesCtx(http.MethodGet, "/itunes/library-status?path=/lib/iTunes.xml", "", nil)
	h.LibraryStatus(c)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "last_synced")
}

// ── Sync ─────────────────────────────────────────────────────────────────────

func TestITunesHandler_Sync_NilRegistry_500(t *testing.T) {
	store := handlersmocks.NewMockITunesStore(t)
	imp := handlersmocks.NewMockITunesImporter(t)
	// store non-nil, registry nil → "operation registry not initialized".
	h := handlers.NewITunesHandler(enabledSvc(t), imp, nil, store)
	c, w := newITunesCtx(http.MethodPost, "/itunes/sync", `{}`, nil)
	h.Sync(c)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestITunesHandler_Sync_NoLibraryPath_400(t *testing.T) {
	orig := config.AppConfig
	defer func() { config.AppConfig = orig }()
	config.AppConfig.ITunes.LibraryReadPath = ""

	store := handlersmocks.NewMockITunesStore(t)
	reg := handlersmocks.NewMockOperationsRegistry(t)
	imp := handlersmocks.NewMockITunesImporter(t)
	// No request path, no configured read path, importer discovers nothing.
	imp.EXPECT().DiscoverLibraryPath().Return("")

	h := handlers.NewITunesHandler(enabledSvc(t), imp, reg, store)
	c, w := newITunesCtx(http.MethodPost, "/itunes/sync", `{}`, nil)
	h.Sync(c)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// ── LibraryStats ─────────────────────────────────────────────────────────────

func TestITunesHandler_LibraryStats_NoITLPath_400(t *testing.T) {
	orig := config.AppConfig
	defer func() { config.AppConfig = orig }()
	config.AppConfig.ITunes.LibraryWritePath = ""

	h := handlers.NewITunesHandler(enabledSvc(t), nil, nil, handlersmocks.NewMockITunesStore(t))
	c, w := newITunesCtx(http.MethodGet, "/itunes/library-stats", "", nil)
	h.LibraryStats(c)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestITunesHandler_LibraryStats_Disabled_503(t *testing.T) {
	svc := handlersmocks.NewMockITunesService(t)
	svc.EXPECT().Enabled().Return(false).Once()

	h := handlers.NewITunesHandler(svc, nil, nil, handlersmocks.NewMockITunesStore(t))
	c, w := newITunesCtx(http.MethodGet, "/itunes/library-stats", "", nil)
	h.LibraryStats(c)

	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
}
