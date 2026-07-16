// file: internal/server/handlers_integration_test.go
// version: 1.8.1
// guid: 3f4a5b6c-7d8e-9f0a-1b2c-3d4e5f6a7b8c
// last-edited: 2026-07-16

package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"context"
	"fmt"

	"github.com/falkcorp/audiobook-organizer/internal/batch"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/fileops"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
	audiobookshandler "github.com/falkcorp/audiobook-organizer/internal/server/handlers/audiobooks"
	entities "github.com/falkcorp/audiobook-organizer/internal/server/handlers/entities"
	operations "github.com/falkcorp/audiobook-organizer/internal/server/handlers/operations"
	system "github.com/falkcorp/audiobook-organizer/internal/server/handlers/system"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
	"github.com/falkcorp/audiobook-organizer/internal/work"
	"github.com/gin-gonic/gin"
)

// newOperationsHandler constructs an operations.Handler from the test server's
// store + injected funcs. The operations domain handlers were extracted into
// the handlers/operations sub-package; the whitebox handler tests in
// handlers_unit_test.go still exercise the real store → JSON-envelope path
// through this constructor. The registry/scheduler/pipeline/scanStore deps and
// the three injected funcs are stubbed because the migrated store-only handlers
// under test (status/list/logs/result/changes) never reach them.
func newOperationsHandler(s *Server) *operations.Handler {
	return operations.New(
		s.Store(),
		nil, // registry
		nil, // scheduler
		nil, // pipeline (ScanCanceler)
		nil, // scanStore (AIScanLister)
		s.collectStaleOperations,
		func(id string) (*undo.UndoConflictReport, error) {
			return undo.PreflightUndoConflicts(s.Store(), id)
		},
		func(id string) error {
			return NewRevertService(s.Store()).RevertOperation(id)
		},
	)
}

// newEntitiesHandler constructs an entities.Handler from the test server's
// (real) services + caches. The entities domain handlers were extracted into
// the handlers/entities sub-package; these integration tests still exercise the
// real WorkService/AuthorSeriesService → MockStore → JSON-envelope path. The
// enrichBooks stub is trivial since none of these tests hit the enriching
// (author/series books) endpoints.
func newEntitiesHandler(s *Server) *entities.Handler {
	return entities.New(
		s.Store(),
		s.workService,
		s.authorSeriesService,
		s.opRegistry,
		s.authorsCache,
		s.seriesCache,
		s.dedupCache,
		func(b []database.Book) []any { return make([]any, len(b)) },
	)
}

// newSystemHandler constructs a system.Handler from the test server's store +
// injected deps. The system domain handlers were extracted into the
// handlers/system sub-package; the whitebox handler tests in
// handlers_unit_test.go still exercise the real store → JSON-envelope path
// through this constructor. systemService/configUpdateService/pluginRegistry/
// olService and the func deps are wired from the real Server fields; the hub is
// passed as a lazy provider closure (mirroring wireHandlers) so the
// handleEvents nil-guard test, which nils s.hub after construction, still
// resolves nil and 503s. filterReviewedAuthorGroups is the bound *Server method.
func newSystemHandler(s *Server) *system.Handler {
	var sysSvc system.SystemService
	if s.systemService != nil {
		sysSvc = s.systemService
	}
	var cfgUpd system.ConfigUpdateService
	if s.configUpdateService != nil {
		cfgUpd = s.configUpdateService
	}
	var plugins system.PluginHealthChecker
	if s.pluginRegistry != nil {
		plugins = s.pluginRegistry
	}
	var opLogs system.OperationLogsProvider
	if s.operationsHandler != nil {
		opLogs = s.operationsHandler
	}
	return system.New(
		func() system.SystemStore { return s.Store() },
		sysSvc,
		cfgUpd,
		plugins,
		// Lazy hub provider mirrors wireHandlers; resolve s.hub at request time so
		// TestHandleEventsUnavailable (which nils s.hub after construction) still
		// hits the 503 guard.
		func() system.EventStreamer {
			if s.hub == nil {
				return nil
			}
			return s.hub
		},
		opLogs,
		s.olService,
		getDiskStats,
		resetLibrarySizeCache,
		func() string { return appVersion },
		s.filterReviewedAuthorGroups,
	)
}

// newAudiobooksHandler constructs an audiobookshandler.Handler from the test
// server's store + injected deps. The audiobooks domain handlers (main library
// list / CRUD) were extracted into the handlers/audiobooks sub-package; the
// whitebox handler tests in handlers_unit_test.go + handlers_integration_test.go
// still exercise the real store → JSON-envelope path through this constructor.
// The service/cache deps are wired from the real Server fields; the store is a
// lazy provider closure (mirroring wireHandlers).
func newAudiobooksHandler(s *Server) *audiobookshandler.Handler {
	var abSvc audiobookshandler.AudiobookService
	if s.audiobookService != nil {
		abSvc = s.audiobookService
	}
	var abUpdater audiobookshandler.AudiobookUpdater
	if s.audiobookUpdateService != nil {
		abUpdater = s.audiobookUpdateService
	}
	var abMetaState audiobookshandler.MetadataStateService
	if s.metadataStateService != nil {
		abMetaState = s.metadataStateService
	}
	var abMetaFetch audiobookshandler.MetadataFetchService
	if s.metadataFetchService != nil {
		abMetaFetch = s.metadataFetchService
	}
	var abBatch audiobookshandler.BatchService
	if s.batchService != nil {
		abBatch = s.batchService
	}
	var abChangelog audiobookshandler.ChangelogService
	if s.changelogService != nil {
		abChangelog = s.changelogService
	}
	return audiobookshandler.New(
		s.Store(),
		abSvc,
		abUpdater,
		func() audiobookshandler.WriteBackEnqueuer {
			if s.writeBackBatcher == nil {
				return nil
			}
			return s.writeBackBatcher
		},
		abMetaState,
		abMetaFetch,
		abBatch,
		abChangelog,
		s.listCache,
		s.facetsCache,
		s.authorsCache,
		s.seriesCache,
		s.buildAudiobookListResponse,
		s.buildFacetsResponse,
		s.isProtectedPath,
		func(b *database.Book) any { return s.enrichBookForResponseSingle(b) },
		func(id string) (any, error) { return s.metadataStateService.LoadMetadataState(id) },
		func() audiobookshandler.ExternalIDStore {
			eid := asExternalIDStore(s.Store())
			if eid == nil {
				return nil
			}
			return eid
		},
		s.publishEvent,
	)
}

// setupHandlerTestServer creates a test server with mock database
func setupHandlerTestServer(t *testing.T) *Server {
	// Create a test database instance
	authorID := 1
	mockDB := &database.MockStore{
		GetAllWorksFunc: func() ([]database.Work, error) {
			return []database.Work{
				{ID: "1", Title: "Work 1"},
				{ID: "2", Title: "Work 2"},
			}, nil
		},
		GetAllAuthorsFunc: func() ([]database.Author, error) {
			return []database.Author{
				{ID: 1, Name: "Author 1"},
				{ID: 2, Name: "Author 2"},
			}, nil
		},
		GetAllSeriesFunc: func() ([]database.Series, error) {
			return []database.Series{
				{ID: 1, Name: "Series 1", AuthorID: &authorID},
			}, nil
		},
	}

	// Temporarily set the global store for this test
	oldStore := database.GetGlobalStore()
	database.SetGlobalStore(mockDB)
	t.Cleanup(func() {
		database.SetGlobalStore(oldStore)
	})

	srv := NewServer(mockDB)
	// INTERNAL-SERVER-PKG-STALL: NewServer starts a 4-worker FileIOPool. Without
	// this cleanup every test that builds a server leaks those workers for the
	// rest of the binary's life — a timed-out run's dump showed 352 parked
	// FileIOPool.worker goroutines (≈88 leaked servers), which is pure race-
	// detector and scheduler overhead piled onto an already-slow package.
	t.Cleanup(srv.fileIOPool.Stop)
	return srv
}

// TestListWorks_Success tests the listWorks handler with successful response
func TestListWorks_Success(t *testing.T) {
	server := setupHandlerTestServer(t)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/works", nil)
	c, _ := gin.CreateTestContext(w)
	c.Request = req

	h := newEntitiesHandler(server)
	h.ListWorks(c)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	var envelope struct {
		Data work.WorkListResponse `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	resp := envelope.Data
	if resp.Count != 2 {
		t.Errorf("expected count 2, got %d", resp.Count)
	}
	if len(resp.Items) != 2 {
		t.Errorf("expected 2 items, got %d", len(resp.Items))
	}
}

// TestCreateWork_Success tests the createWork handler with valid input
func TestCreateWork_Success(t *testing.T) {
	server := setupHandlerTestServer(t)

	work := database.Work{Title: "New Work"}
	body, _ := json.Marshal(work)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/works", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	c, _ := gin.CreateTestContext(w)
	c.Request = req

	// Mock the CreateWork function
	if store, ok := database.GetGlobalStore().(*database.MockStore); ok {
		store.CreateWorkFunc = func(w *database.Work) (*database.Work, error) {
			w.ID = "new-123"
			return w, nil
		}
	}

	h := newEntitiesHandler(server)
	h.CreateWork(c)

	if w.Code != http.StatusCreated {
		t.Errorf("expected status 201, got %d", w.Code)
	}
}

// TestCreateWork_InvalidJSON tests the createWork handler with invalid JSON
func TestCreateWork_InvalidJSON(t *testing.T) {
	server := setupHandlerTestServer(t)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/works", bytes.NewReader([]byte("{invalid")))
	req.Header.Set("Content-Type", "application/json")
	c, _ := gin.CreateTestContext(w)
	c.Request = req

	h := newEntitiesHandler(server)
	h.CreateWork(c)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", w.Code)
	}
}

// TestGetWork_Success tests the getWork handler with existing work
func TestGetWork_Success(t *testing.T) {
	server := setupHandlerTestServer(t)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/works/1", nil)
	c, _ := gin.CreateTestContext(w)
	c.Request = req
	c.Params = append(c.Params, gin.Param{Key: "id", Value: "1"})

	// Mock the GetWorkByID function
	if store, ok := database.GetGlobalStore().(*database.MockStore); ok {
		store.GetWorkByIDFunc = func(id string) (*database.Work, error) {
			return &database.Work{ID: id, Title: "Work 1"}, nil
		}
	}

	h := newEntitiesHandler(server)
	h.GetWork(c)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}
}

// TestGetWork_NotFound tests the getWork handler with non-existent work
func TestGetWork_NotFound(t *testing.T) {
	server := setupHandlerTestServer(t)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/works/nonexistent", nil)
	c, _ := gin.CreateTestContext(w)
	c.Request = req
	c.Params = append(c.Params, gin.Param{Key: "id", Value: "nonexistent"})

	// Mock the GetWorkByID function to return nil
	if store, ok := database.GetGlobalStore().(*database.MockStore); ok {
		store.GetWorkByIDFunc = func(id string) (*database.Work, error) {
			return nil, nil
		}
	}

	h := newEntitiesHandler(server)
	h.GetWork(c)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected status 404, got %d", w.Code)
	}
}

// TestListAuthors_Success tests the listAuthors handler
func TestListAuthors_Success(t *testing.T) {
	server := setupHandlerTestServer(t)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/authors", nil)
	c, _ := gin.CreateTestContext(w)
	c.Request = req

	h := newEntitiesHandler(server)
	h.ListAuthors(c)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	var envelope struct {
		Data AuthorListResponse `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	resp := envelope.Data
	if resp.Count != 2 {
		t.Errorf("expected count 2, got %d", resp.Count)
	}
}

// TestListSeries_Success tests the listSeries handler
func TestListSeries_Success(t *testing.T) {
	server := setupHandlerTestServer(t)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/series", nil)
	c, _ := gin.CreateTestContext(w)
	c.Request = req

	h := newEntitiesHandler(server)
	h.ListSeries(c)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	var envelope struct {
		Data SeriesListResponse `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	resp := envelope.Data
	if resp.Count != 1 {
		t.Errorf("expected count 1, got %d", resp.Count)
	}
}

// errBrowser is a minimal FilesystemBrowser that returns an error for any path.
type errBrowser struct{ err error }

func (b *errBrowser) BrowseDirectory(_ context.Context, _ string) (*fileops.BrowseResult, error) {
	return nil, b.err
}
func (b *errBrowser) CreateExclusion(_ context.Context, _ string) error { return nil }
func (b *errBrowser) RemoveExclusion(_ context.Context, _ string) error { return nil }

// TestBrowseFilesystem_NoPath tests the BrowseFilesystem handler without path.
func TestBrowseFilesystem_NoPath(t *testing.T) {
	browser := &errBrowser{err: fmt.Errorf("path is required")}
	h := handlers.NewFilesystemHandler(nil, browser, nil, nil, nil, nil, "", false)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/browse?path=", nil)
	c, _ := gin.CreateTestContext(w)
	c.Request = req

	h.BrowseFilesystem(c)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", w.Code)
	}
}

// TestBatchUpdateAudiobooks_Empty tests batch update with no IDs
func TestBatchUpdateAudiobooks_Empty(t *testing.T) {
	server := setupHandlerTestServer(t)

	req := &batch.BatchUpdateRequest{
		IDs:     []string{},
		Updates: map[string]interface{}{},
	}
	body, _ := json.Marshal(req)

	w := httptest.NewRecorder()
	httpReq := httptest.NewRequest("POST", "/api/audiobooks/batch", bytes.NewReader(body))
	httpReq.Header.Set("Content-Type", "application/json")
	c, _ := gin.CreateTestContext(w)
	c.Request = httpReq

	newAudiobooksHandler(server).BatchUpdateAudiobooks(c)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	var resp batch.BatchUpdateResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	if resp.Total != 0 {
		t.Errorf("expected total 0, got %d", resp.Total)
	}
}

// TestDeleteWork_Success tests the deleteWork handler
func TestDeleteWork_Success(t *testing.T) {
	server := setupHandlerTestServer(t)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("DELETE", "/api/works/1", nil)
	c, _ := gin.CreateTestContext(w)
	c.Request = req
	c.Params = append(c.Params, gin.Param{Key: "id", Value: "1"})

	// Mock GetWorkByID (so WorkService.DeleteWork passes existence check) and DeleteWork.
	if store, ok := database.GetGlobalStore().(*database.MockStore); ok {
		store.GetWorkByIDFunc = func(id string) (*database.Work, error) {
			return &database.Work{ID: id, Title: "Test Work"}, nil
		}
		store.DeleteWorkFunc = func(id string) error {
			return nil
		}
	}

	h := newEntitiesHandler(server)
	h.DeleteWork(c)

	// c.Status() may return 200 if no response body is written, which is OK
	// The handler sets status correctly via c.Status()
	if w.Code != http.StatusNoContent && w.Code != http.StatusOK {
		t.Errorf("expected status 204 or 200, got %d", w.Code)
	}
}

// TestUpdateWork_InvalidTitle tests updateWork with empty title
func TestUpdateWork_InvalidTitle(t *testing.T) {
	server := setupHandlerTestServer(t)

	work := database.Work{Title: ""}
	body, _ := json.Marshal(work)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("PUT", "/api/works/1", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	c, _ := gin.CreateTestContext(w)
	c.Request = req
	c.Params = append(c.Params, gin.Param{Key: "id", Value: "1"})

	h := newEntitiesHandler(server)
	h.UpdateWork(c)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", w.Code)
	}
}
