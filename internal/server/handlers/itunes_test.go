// file: internal/server/handlers/itunes_test.go
// version: 1.2.1
// guid: 9c2a4e71-6b53-4d18-8f0a-2e7c1b9d3a64
// last-edited: 2026-09-02

package handlers_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	itunesservice "github.com/falkcorp/audiobook-organizer/internal/itunes/service"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
	handlersmocks "github.com/falkcorp/audiobook-organizer/internal/server/handlers/mocks"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
		{ID: "b1", Title: "Book One", FilePath: "/x/b1.m4b", ITunesPersistentID: new("PID1")},
	}, nil)

	h := handlers.NewITunesHandler(enabledSvc(t), nil, nil, store)
	c, w := newITunesCtx(http.MethodGet, "/itunes/books", "", nil)
	h.ListBooks(c)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "PID1")
}

func TestITunesHandler_ListBooks_Search_BoundedOverfetch_PinsResultSet(t *testing.T) {
	// The search path over-fetches SearchBooks up to itunesSearchOverfetchWindow
	// (10000) before post-filtering for a non-empty iTunes PID, since SearchBooks
	// has no PID filter of its own. Seed more matching books than the window,
	// with more PID-tagged matches (125) than a single default page (50), and
	// assert BOTH pages of the response contain exactly the PID-tagged books
	// visible within the window — not just "no error". A naive small limit
	// regression would either error, undercount, or drop far-end PIDs; this
	// catches all three.
	store := handlersmocks.NewMockITunesStore(t)

	const window = 10000
	books := make([]database.Book, 0, window)
	var wantPIDs []string
	for i := range window {
		id := fmt.Sprintf("b%d", i)
		var pid *string
		if i%80 == 0 { // 10000/80 = 125 PID-tagged matches, spanning >1 page
			p := fmt.Sprintf("PID%04d", i)
			pid = &p
			wantPIDs = append(wantPIDs, p)
		}
		books = append(books, database.Book{ID: id, Title: "Match " + id, FilePath: "/x/" + id + ".m4b", ITunesPersistentID: pid})
	}
	if len(wantPIDs) != 125 {
		t.Fatalf("test setup produced %d PID-tagged books, want 125", len(wantPIDs))
	}

	// Assert the handler asks the store for exactly the bounded window, not 0
	// (unbounded). Each page request re-runs the search, so allow multiple calls.
	store.EXPECT().SearchBooks("match", window, 0).Return(books, nil).Times(2)

	type listResp struct {
		Data struct {
			Items []handlers.ITunesBookMapping `json:"items"`
			Count int                          `json:"count"`
		} `json:"data"`
	}

	h := handlers.NewITunesHandler(enabledSvc(t), nil, nil, store)

	// Page 1: default limit=50. count must report the full filtered total
	// (125), not the page length (50).
	c1, w1 := newITunesCtx(http.MethodGet, "/itunes/books?search=match", "", nil)
	h.ListBooks(c1)
	assert.Equal(t, http.StatusOK, w1.Code)
	var page1 listResp
	require.NoError(t, json.Unmarshal(w1.Body.Bytes(), &page1))
	assert.Equal(t, 125, page1.Data.Count, "count must be the full filtered total, not the page length")
	require.Len(t, page1.Data.Items, 50)
	for i, item := range page1.Data.Items {
		assert.Equal(t, wantPIDs[i], item.ITunesPersistentID)
	}

	// Page 2: offset=100 crosses into the tail of the filtered set (items
	// 100-124), proving the far-end PID-tagged matches within the window
	// survive both the bound and pagination.
	c2, w2 := newITunesCtx(http.MethodGet, "/itunes/books?search=match&offset=100", "", nil)
	h.ListBooks(c2)
	assert.Equal(t, http.StatusOK, w2.Code)
	var page2 listResp
	require.NoError(t, json.Unmarshal(w2.Body.Bytes(), &page2))
	assert.Equal(t, 125, page2.Data.Count)
	require.Len(t, page2.Data.Items, 25)
	for i, item := range page2.Data.Items {
		assert.Equal(t, wantPIDs[100+i], item.ITunesPersistentID)
	}
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
	store.EXPECT().GetOperationV2("op1").Return(&database.OperationV2Row{
		ID: "op1", Status: "running", ProgressCurrent: 5, ProgressTotal: 10, ProgressMessage: "working",
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
	store.EXPECT().GetOperationV2("missing").Return(nil, assert.AnError)

	imp := handlersmocks.NewMockITunesImporter(t)
	h := handlers.NewITunesHandler(enabledSvc(t), imp, nil, store)
	c, w := newITunesCtx(http.MethodGet, "/itunes/import-status/missing", "", gin.Params{{Key: "id", Value: "missing"}})
	h.ImportStatus(c)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

// ── ImportStatusBulk ─────────────────────────────────────────────────────────

func TestITunesHandler_ImportStatusBulk_Happy(t *testing.T) {
	store := handlersmocks.NewMockITunesStore(t)
	store.EXPECT().GetOperationV2("op1").Return(&database.OperationV2Row{
		ID: "op1", Status: "completed", ProgressCurrent: 10, ProgressTotal: 10,
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
