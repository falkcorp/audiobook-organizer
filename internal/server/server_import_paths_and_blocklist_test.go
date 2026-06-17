// file: internal/server/server_import_paths_and_blocklist_test.go
// version: 1.3.0
// guid: 2f4a6b8c-0d1e-2f3a-4b5c-6d7e8f9a0b1c
// last-edited: 2026-04-30

package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	dbmocks "github.com/falkcorp/audiobook-organizer/internal/database/mocks"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestListAuthorsAndSeries_ReturnsEmptyArrayWhenNil(t *testing.T) {
	store := dbmocks.NewMockStore(t)
	store.EXPECT().SetRootDir(mock.Anything).Return()
	// Authors endpoint: GetAllAuthors + GetAllAuthorBookCounts + GetAllAuthorAliases
	store.EXPECT().GetAllAuthors().Return(([]database.Author)(nil), nil).Maybe()
	store.EXPECT().GetAllAuthorBookCounts().Return(map[int]int{}, nil).Maybe()
	store.EXPECT().GetAllAuthorFileCounts().Return(map[int]int{}, nil).Maybe()
	store.EXPECT().GetAllAuthorAliases().Return([]database.AuthorAlias{}, nil).Maybe()
	// Series endpoint: GetAllSeries + GetAllSeriesBookCounts + GetAllSeriesFileCounts + GetAllAuthors (for author names)
	store.EXPECT().GetAllSeries().Return(([]database.Series)(nil), nil).Maybe()
	store.EXPECT().GetAllSeriesBookCounts().Return(map[int]int{}, nil).Maybe()
	store.EXPECT().GetAllSeriesFileCounts().Return(map[int]int{}, nil).Maybe()

	server, cleanup := setupTestServerWithStore(t, store)
	defer cleanup()

	// Authors
	req := httptest.NewRequest(http.MethodGet, "/api/v1/authors", nil)
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var authorsResp map[string]interface{}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &authorsResp))
	authorsResp = authorsResp["data"].(map[string]interface{})
	items, ok := authorsResp["items"].([]interface{})
	require.True(t, ok)
	assert.Len(t, items, 0)

	// Series
	req = httptest.NewRequest(http.MethodGet, "/api/v1/series", nil)
	w = httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var seriesResp map[string]interface{}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &seriesResp))
	seriesResp = seriesResp["data"].(map[string]interface{})
	items, ok = seriesResp["items"].([]interface{})
	require.True(t, ok)
	assert.Len(t, items, 0)
}

func TestImportPaths_ListNilAndRemoveInvalidID(t *testing.T) {
	// Phase 2: filesystemH captures the store at wireHandlers time, so we must
	// inject the mock store before constructing the server via setupTestServerWithStore.
	mockStore := dbmocks.NewMockStore(t)
	mockStore.EXPECT().SetRootDir(mock.Anything).Return()
	mockStore.EXPECT().GetAllImportPaths().Return(([]database.ImportPath)(nil), nil)
	mockStore.EXPECT().DeleteImportPath(mock.Anything).Return(nil).Maybe()
	// Suppress any store calls made during server construction / route registration.
	mockStore.EXPECT().GetDashboardStats().Return(nil, fmt.Errorf("no stats")).Maybe()
	mockStore.EXPECT().CountBooksByPathPrefix(mock.Anything).Return(0, nil).Maybe()

	server, cleanup := setupTestServerWithStore(t, mockStore)
	defer cleanup()

	// listImportPaths should return [] not null.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/import-paths", nil)
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var listResp struct {
		Data map[string]interface{} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &listResp))
	paths, ok := listResp.Data["importPaths"].([]interface{})
	require.True(t, ok)
	assert.Len(t, paths, 0)

	// removeImportPath invalid id
	req = httptest.NewRequest(http.MethodDelete, "/api/v1/import-paths/not-an-int", nil)
	w = httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code)
}

// TestAddImportPath_Returns201 verifies the happy-path: creating an import path
// returns 201 Created. The async folder scan runs via the v2 opRegistry worker
// pool and is not directly observable here; scan execution is covered by
// integration tests.
func TestAddImportPath_Returns201(t *testing.T) {
	origCfg := config.AppConfig
	t.Cleanup(func() { config.AppConfig = origCfg })
	config.AppConfig.AutoOrganize = false
	config.AppConfig.RootDir = ""

	importDir := t.TempDir()

	store := dbmocks.NewMockStore(t)
	store.EXPECT().SetRootDir(mock.Anything).Return()
	created := &database.ImportPath{ID: 123, Path: importDir, Name: "Test Import", Enabled: true}
	store.EXPECT().CreateImportPath(importDir, "Test Import").Return(created, nil)
	// opRegistry calls CreateOperation before enqueuing; return an error so we
	// skip the enqueue path and fall through to the plain 201 response.
	store.EXPECT().CreateOperation(mock.Anything, "scan", mock.Anything).
		Return(nil, fmt.Errorf("not needed")).Maybe()

	server, cleanup := setupTestServerWithStore(t, store)
	defer cleanup()

	body := bytes.NewBufferString(`{"path":"` + importDir + `","name":"Test Import"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/import-paths", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)

	require.Equal(t, http.StatusCreated, w.Code)
}

// TestAddImportPath_RejectsTraversal verifies that a request path containing
// traversal sequences is rejected with 400 before any store write or directory
// scan. Guards against go/path-injection at the AddImportPath entry point: the
// mock store sets no CreateImportPath expectation, so the test fails if the
// unvalidated path reaches the store.
func TestAddImportPath_RejectsTraversal(t *testing.T) {
	origCfg := config.AppConfig
	t.Cleanup(func() { config.AppConfig = origCfg })
	config.AppConfig.AutoOrganize = false
	config.AppConfig.RootDir = ""

	store := dbmocks.NewMockStore(t)
	store.EXPECT().SetRootDir(mock.Anything).Return()
	// No CreateImportPath / CreateOperation expectation: mockery fails the test
	// if the handler calls the store despite the traversal path.

	server, cleanup := setupTestServerWithStore(t, store)
	defer cleanup()

	body := bytes.NewBufferString(`{"path":"/mnt/books/../../etc","name":"Evil"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/import-paths", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
}

func TestBlockedHashes_CRUD(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	store := dbmocks.NewMockStore(t)
	origStore := server.store
	server.store = store
	t.Cleanup(func() { server.store = origStore })

	// addBlockedHash invalid hash length
	bad := bytes.NewBufferString(`{"hash":"abc","reason":"nope"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/blocked-hashes", bad)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code)

	// listBlockedHashes and removeBlockedHash
	hashes := []database.DoNotImport{{Hash: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", Reason: "test"}}
	store.EXPECT().GetAllBlockedHashes().Return(hashes, nil)
	store.EXPECT().RemoveBlockedHash(hashes[0].Hash).Return(nil)

	req = httptest.NewRequest(http.MethodGet, "/api/v1/blocked-hashes", nil)
	w = httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	req = httptest.NewRequest(http.MethodDelete, "/api/v1/blocked-hashes/"+hashes[0].Hash, nil)
	w = httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
}
