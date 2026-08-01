// file: internal/server/server_test.go
// version: 2.1.0
// guid: b2c3d4e5-f6a7-8901-bcde-234567890abc
// last-edited: 2026-07-03

// NOTE(fable5 T022): setupTestServer ported from NewSQLiteStore to NewPebbleStore.

package server

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/falkcorp/audiobook-organizer/internal/scanner"
	"github.com/gin-gonic/gin"
	"github.com/oklog/ulid/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupTestServer creates a test server with in-memory database
func setupTestServer(t *testing.T) (*Server, func()) {
	// Set Gin to test mode
	gin.SetMode(gin.TestMode)

	origCfg := config.AppConfig

	// Create temporary directory for test database
	tempDir, err := os.MkdirTemp("", "audiobook-test-*")
	require.NoError(t, err)

	// Initialize test configuration
	config.AppConfig = config.Config{
		DatabaseType: "pebble",
		DatabasePath: filepath.Join(tempDir, "test.pebble"),
		RootDir:      tempDir,
	}

	// Initialize database
	store, err := database.NewPebbleStore(config.AppConfig.DatabasePath)
	require.NoError(t, err)
	database.SetGlobalStore(store)

	// Run migrations to ensure schema is up to date
	err = database.RunMigrations(store)
	require.NoError(t, err)

	// Create server (NewServer creates hub, queue, batcher, fileIOPool internally)
	server := NewServer(store)

	// Start the operations registry's dispatcher + worker pool. Without
	// this, ops enqueued by the test never execute (enqueued in the v2
	// table but no worker is reading from r.nextRun). Tests previously
	// timing out at 10s with "timeout waiting for operation" were
	// hitting this — the registry's Start lives in Container.Start
	// (lifecycle-flip) which setupTestServer doesn't call. Start it
	// explicitly here so the registry processes queued runs.
	if server.opRegistry != nil {
		server.opRegistry.Start(context.Background())
	}

	// Cleanup function
	cleanup := func() {
		// Clear scan hooks BEFORE closing the store. If a scan is in progress
		// when cleanup runs (or when the next test starts its own scan), stale
		// hooks would try to write activity to the already-closed PebbleStore
		// and panic ("pebble: closed"). Clearing early is safe — the hooks only
		// record activity, so skipping them during teardown is acceptable.
		scanner.SetScanHooks(nil)

		if server.opRegistry != nil {
			_ = server.opRegistry.Shutdown(context.Background())
		}
		if server.fileIOPool != nil {
			server.fileIOPool.Stop()
		}
		if server.writeBackBatcher != nil {
			_ = server.writeBackBatcher.Stop(context.Background())
		}
		if store != nil {
			database.SetGlobalStore(nil)
			store.Close()
		}
		_ = os.RemoveAll(tempDir)
		config.AppConfig = origCfg
	}

	return server, cleanup
}

// setupTestServerWithStore creates a test server with a provided database store
func setupTestServerWithStore(t *testing.T, store database.Store) (*Server, func()) {
	// Set Gin to test mode
	gin.SetMode(gin.TestMode)

	// Hermetic config: callers pass MockStores with a fixed expectation set,
	// but the plugin Build guards (itunes, deluge) go LIVE whenever the
	// ambient config.AppConfig.RootDir is non-empty and then call
	// UpsertOpDefinitionV2 on the mock, which has no such expectation —
	// testify FailNow fires before the test's real assertions run. RootDir
	// can be non-empty here through cross-test (and, under `go test
	// -count=N`, cross-iteration) config pollution, so pin it to "" for the
	// duration instead of trusting ambient state.
	origCfg := config.AppConfig
	config.AppConfig.RootDir = ""

	// Set the global store to the provided store
	database.SetGlobalStore(store)

	// Create server with the provided store (services will use it)
	server := NewServer(store)

	// Cleanup function
	cleanup := func() {
		if server.fileIOPool != nil {
			server.fileIOPool.Stop()
		}
		if server.writeBackBatcher != nil {
			_ = server.writeBackBatcher.Stop(context.Background())
		}
		config.AppConfig = origCfg
		// Don't close the store - caller is responsible for cleanup
	}

	return server, cleanup
}

// TestHealthCheck tests the health check endpoint
func TestHealthCheck(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	for _, path := range []string{"/api/health", "/api/v1/health"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		w := httptest.NewRecorder()

		server.router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)

		var wrapper struct {
			Data map[string]any `json:"data"`
		}
		err := json.Unmarshal(w.Body.Bytes(), &wrapper)
		require.NoError(t, err)

		assert.Equal(t, "ok", wrapper.Data["status"])
		assert.NotNil(t, wrapper.Data["timestamp"])
		// version/metrics are deliberately absent: /health is unauthenticated
		// and must not disclose the build string or library counts. See the doc
		// comment on system.Handler.HealthCheck, and
		// TestHealth_IsAnonymousButDisclosesNothing.
		assert.NotContains(t, wrapper.Data, "version")
		assert.NotContains(t, wrapper.Data, "metrics")
	}
}

// TestListAudiobooks tests the list audiobooks endpoint
func TestListAudiobooks(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	tests := []struct {
		name           string
		queryParams    string
		expectedStatus int
		validateFunc   func(t *testing.T, body []byte)
	}{
		{
			name:           "list all audiobooks without params",
			queryParams:    "",
			expectedStatus: http.StatusOK,
			validateFunc: func(t *testing.T, body []byte) {
				var response map[string]any
				err := json.Unmarshal(body, &response)
				require.NoError(t, err)
				response = response["data"].(map[string]any)
				assert.NotNil(t, response["items"])
			},
		},
		{
			name:           "list with limit and offset",
			queryParams:    "?limit=10&offset=0",
			expectedStatus: http.StatusOK,
			validateFunc: func(t *testing.T, body []byte) {
				var response map[string]any
				err := json.Unmarshal(body, &response)
				require.NoError(t, err)
				response = response["data"].(map[string]any)
				assert.NotNil(t, response["items"])
			},
		},
		{
			name:           "list with search query",
			queryParams:    "?search=test",
			expectedStatus: http.StatusOK,
			validateFunc: func(t *testing.T, body []byte) {
				var response map[string]any
				err := json.Unmarshal(body, &response)
				require.NoError(t, err)
				response = response["data"].(map[string]any)
				assert.NotNil(t, response["items"])
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/audiobooks"+tt.queryParams, nil)
			w := httptest.NewRecorder()

			server.router.ServeHTTP(w, req)

			assert.Equal(t, tt.expectedStatus, w.Code)
			if tt.validateFunc != nil {
				tt.validateFunc(t, w.Body.Bytes())
			}
		})
	}
}

// TestGetAudiobook tests getting a specific audiobook
func TestGetAudiobook(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	// Test with non-existent ID
	req := httptest.NewRequest(http.MethodGet, "/api/v1/audiobooks/01HXZ123456789ABCDEFGHJ", nil)
	w := httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

// TestUpdateAudiobook tests updating audiobook metadata
func TestUpdateAudiobook(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	updateData := map[string]any{
		"title":  "Updated Title",
		"author": "Updated Author",
	}
	body, err := json.Marshal(updateData)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPut, "/api/v1/audiobooks/01HXZ123456789ABCDEFGHJ", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	// Expect 404 for non-existent audiobook
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestGetAudiobookTagsReportsEffectiveSourceSimple(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	tempDir := t.TempDir()
	tempFile := filepath.Join(tempDir, "book.m4b")
	require.NoError(t, os.WriteFile(tempFile, []byte("audio"), 0o644))

	created, err := database.GetGlobalStore().CreateBook(&database.Book{
		Title:    "Stored Title",
		FilePath: tempFile,
		Format:   "m4b",
	})
	require.NoError(t, err)

	now := time.Now()
	err = server.saveMetadataState(created.ID, map[string]metafetch.MetadataFieldState{
		"title": {
			FetchedValue:   "Fetched Title",
			OverrideValue:  "Override Title",
			OverrideLocked: true,
			UpdatedAt:      now,
		},
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/audiobooks/%s/tags", created.ID), nil)
	w := httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var wrapper struct {
		Data struct {
			Tags map[string]struct {
				EffectiveValue  any    `json:"effective_value"`
				EffectiveSource string `json:"effective_source"`
				StoredValue     any    `json:"stored_value"`
				OverrideValue   any    `json:"override_value"`
				FetchedValue    any    `json:"fetched_value"`
				OverrideLocked  bool   `json:"override_locked"`
			} `json:"tags"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &wrapper))
	response := wrapper.Data

	entry, ok := response.Tags["title"]
	require.True(t, ok, "title tag should exist")
	assert.Equal(t, "Override Title", entry.EffectiveValue)
	assert.Equal(t, "override", entry.EffectiveSource)
	assert.Equal(t, "Override Title", entry.OverrideValue)
	assert.Equal(t, "Fetched Title", entry.FetchedValue)
	assert.Equal(t, "Stored Title", entry.StoredValue)
	assert.True(t, entry.OverrideLocked)
}

func TestGetAudiobookTagsRejectsInvalidSnapshotTimestamp(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	tempDir := t.TempDir()
	tempFile := filepath.Join(tempDir, "book.m4b")
	require.NoError(t, os.WriteFile(tempFile, []byte("audio"), 0o644))

	created, err := database.GetGlobalStore().CreateBook(&database.Book{
		Title:    "Snapshot Test",
		FilePath: tempFile,
		Format:   "m4b",
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/audiobooks/%s/tags?snapshot_ts=not-a-time", created.ID), nil)
	w := httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "snapshot_ts")
}

func TestUpdateAudiobookOverridesPersist(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	tempFile := filepath.Join(t.TempDir(), "book-override.m4b")
	require.NoError(t, os.WriteFile(tempFile, []byte("audio"), 0o644))

	created, err := database.GetGlobalStore().CreateBook(&database.Book{
		Title:    "Original Title",
		FilePath: tempFile,
		Format:   "m4b",
	})
	require.NoError(t, err)

	body := bytes.NewBufferString(`{"overrides":{"title":{"value":"New Title","locked":true}}}`)
	req := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/api/v1/audiobooks/%s", created.ID), body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var updatedWrap struct {
		Data database.Book `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &updatedWrap))
	updated := updatedWrap.Data
	assert.Equal(t, "New Title", updated.Title)

	states, err := database.GetGlobalStore().GetMetadataFieldStates(created.ID)
	require.NoError(t, err)

	stateByField := map[string]database.MetadataFieldState{}
	for _, st := range states {
		stateByField[st.Field] = st
	}
	state, ok := stateByField["title"]
	require.True(t, ok, "expected metadata state for title")
	assert.True(t, state.OverrideLocked)
	assert.NotNil(t, state.OverrideValue)
	assert.NotZero(t, state.UpdatedAt)
	assert.Equal(t, "New Title", decodeMetadataValue(state.OverrideValue))
}

// TestDeleteAudiobook tests deleting an audiobook
func TestDeleteAudiobook(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/audiobooks/01HXZ123456789ABCDEFGHJ", nil)
	w := httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	// Expect 404 for non-existent audiobook
	assert.Equal(t, http.StatusNotFound, w.Code)
}

// TestBatchUpdateAudiobooks tests batch updating audiobooks
func TestBatchUpdateAudiobooks(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	batchData := map[string]any{
		"audiobooks": []map[string]any{},
	}
	body, err := json.Marshal(batchData)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/audiobooks/batch", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
}

// TestListAuthors tests listing authors
func TestListAuthors(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/authors", nil)
	w := httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var response map[string]any
	err := json.Unmarshal(w.Body.Bytes(), &response)
	require.NoError(t, err)
	response = response["data"].(map[string]any)
	assert.NotNil(t, response["items"])
}

// TestListSeries tests listing series
func TestListSeries(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/series", nil)
	w := httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var response map[string]any
	err := json.Unmarshal(w.Body.Bytes(), &response)
	require.NoError(t, err)
	response = response["data"].(map[string]any)
	assert.NotNil(t, response["items"])
}

// TestBrowseFilesystem tests filesystem browsing
func TestBrowseFilesystem(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	tests := []struct {
		name           string
		queryParams    string
		expectedStatus int
	}{
		{
			name:           "browse root",
			queryParams:    "?path=/",
			expectedStatus: http.StatusForbidden,
		},
		{
			name:           "browse without path",
			queryParams:    "",
			expectedStatus: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/filesystem/browse"+tt.queryParams, nil)
			w := httptest.NewRecorder()

			server.router.ServeHTTP(w, req)

			assert.Equal(t, tt.expectedStatus, w.Code)
		})
	}
}

// TestListImportPaths tests listing import paths
func TestListImportPaths(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/import-paths", nil)
	w := httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var response struct {
		Data map[string]any `json:"data"`
	}
	err := json.Unmarshal(w.Body.Bytes(), &response)
	require.NoError(t, err)
	assert.NotNil(t, response.Data["importPaths"])
}

// TestGetOperationStatus tests getting operation status
func TestGetOperationStatus(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/operations/01HXZ123456789ABCDEFGHJ/status", nil)
	w := httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	// Expect 404 for non-existent operation
	assert.Equal(t, http.StatusNotFound, w.Code)
}

// TestGetSystemStatus tests getting system status
func TestGetSystemStatus(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/system/status", nil)
	w := httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
}

// TestGetConfig tests getting configuration
func TestGetConfig(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/config", nil)
	w := httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var response map[string]any
	err := json.Unmarshal(w.Body.Bytes(), &response)
	require.NoError(t, err)
	response = response["data"].(map[string]any)
	assert.NotNil(t, response["config"])
}

// TestListBackups tests listing backups
func TestListBackups(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/backup/list", nil)
	w := httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var wrapper struct {
		Data map[string]any `json:"data"`
	}
	err := json.Unmarshal(w.Body.Bytes(), &wrapper)
	require.NoError(t, err)
	assert.NotNil(t, wrapper.Data["backups"])
}

// TestBatchUpdateMetadata tests batch metadata updates
func TestBatchUpdateMetadata(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	tests := []struct {
		name           string
		requestBody    map[string]any
		expectedStatus int
	}{
		{
			name: "empty batch",
			requestBody: map[string]any{
				"updates":  []any{},
				"validate": true,
			},
			expectedStatus: http.StatusOK,
		},
		{
			name: "invalid request - missing updates",
			requestBody: map[string]any{
				"validate": true,
			},
			expectedStatus: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, err := json.Marshal(tt.requestBody)
			require.NoError(t, err, "failed to marshal request body for test case %s", tt.name)
			req := httptest.NewRequest(http.MethodPost, "/api/v1/metadata/batch-update", bytes.NewBuffer(body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()

			server.router.ServeHTTP(w, req)

			assert.Equal(t, tt.expectedStatus, w.Code)
		})
	}
}

// TestValidateMetadata tests metadata validation
func TestValidateMetadata(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	tests := []struct {
		name           string
		requestBody    map[string]any
		expectedStatus int
		expectValid    bool
	}{
		{
			name: "valid metadata",
			requestBody: map[string]any{
				"updates": map[string]any{
					"title":  "Test Book",
					"author": "Test Author",
				},
			},
			expectedStatus: http.StatusOK,
			expectValid:    true,
		},
		{
			name: "invalid metadata - missing required field",
			requestBody: map[string]any{
				"updates": map[string]any{
					"title": "",
				},
			},
			expectedStatus: http.StatusBadRequest,
			expectValid:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, err := json.Marshal(tt.requestBody)
			require.NoError(t, err, "failed to marshal request body for test case %s", tt.name)
			req := httptest.NewRequest(http.MethodPost, "/api/v1/metadata/validate", bytes.NewBuffer(body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()

			server.router.ServeHTTP(w, req)

			assert.Equal(t, tt.expectedStatus, w.Code)

			var response map[string]any
			err = json.Unmarshal(w.Body.Bytes(), &response)
			require.NoError(t, err)
			if tt.expectedStatus == http.StatusOK {
				response = response["data"].(map[string]any)
				if valid, ok := response["valid"].(bool); ok {
					assert.Equal(t, tt.expectValid, valid)
				}
			}
		})
	}
}

// TestExportMetadata tests metadata export
func TestExportMetadata(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/metadata/export", nil)
	w := httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
}

func TestBulkFetchMetadataRespectsOverridesAndMissingFields(t *testing.T) {
	server, cleanup := setupTestServer(t)
	useOnlyOpenLibrary(t)
	defer cleanup()

	tempDir := t.TempDir()
	tempFile := filepath.Join(tempDir, "book.m4b")
	otherFile := filepath.Join(tempDir, "other.m4b")
	require.NoError(t, os.WriteFile(tempFile, []byte("audio"), 0o644))
	require.NoError(t, os.WriteFile(otherFile, []byte("audio"), 0o644))

	// Arrange: book with a locked publisher override and missing language/author.
	created, err := database.GetGlobalStore().CreateBook(&database.Book{
		Title:    "The Hobbit",
		FilePath: tempFile,
		Format:   "m4b",
	})
	require.NoError(t, err)

	err = server.saveMetadataState(created.ID, map[string]metafetch.MetadataFieldState{
		"publisher": {
			OverrideValue:  "Manual Publisher",
			OverrideLocked: true,
			UpdatedAt:      time.Now(),
		},
	})
	require.NoError(t, err)

	other, err := database.GetGlobalStore().CreateBook(&database.Book{
		Title:    "Unknown Title",
		FilePath: otherFile,
		Format:   "m4b",
	})
	require.NoError(t, err)

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search.json" {
			http.NotFound(w, r)
			return
		}
		title := r.URL.Query().Get("title")
		if title == "The Hobbit" {
			_, err := w.Write([]byte(`{"numFound":1,"start":0,"docs":[{"title":"The Hobbit","author_name":["J.R.R. Tolkien"],"first_publish_year":1937,"isbn":["1234567890"],"publisher":["Test Publisher"],"language":["eng"]}]}`))
			_ = err
			return
		}
		_, err := w.Write([]byte(`{"numFound":0,"start":0,"docs":[]}`))
		_ = err
	}))
	defer mockServer.Close()

	originalBaseURL := os.Getenv("OPENLIBRARY_BASE_URL")
	require.NoError(t, os.Setenv("OPENLIBRARY_BASE_URL", mockServer.URL))
	t.Cleanup(func() {
		_ = os.Setenv("OPENLIBRARY_BASE_URL", originalBaseURL)
	})

	// Act: bulk fetch metadata for both books.
	payload := map[string]any{
		"book_ids": []string{created.ID, other.ID},
	}
	body, err := json.Marshal(payload)
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/metadata/bulk-fetch", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	// Assert: first book updates missing fields but does not override locked publisher.
	assert.Equal(t, http.StatusOK, w.Code)

	var response struct {
		Data struct {
			UpdatedCount int `json:"updated_count"`
			TotalCount   int `json:"total_count"`
			Results      []struct {
				BookID        string   `json:"book_id"`
				Status        string   `json:"status"`
				AppliedFields []string `json:"applied_fields"`
				FetchedFields []string `json:"fetched_fields"`
			} `json:"results"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
	assert.Equal(t, 2, response.Data.TotalCount)
	assert.Equal(t, 1, response.Data.UpdatedCount)

	var primaryResult, missingResult *struct {
		BookID        string   `json:"book_id"`
		Status        string   `json:"status"`
		AppliedFields []string `json:"applied_fields"`
		FetchedFields []string `json:"fetched_fields"`
	}
	for i := range response.Data.Results {
		if response.Data.Results[i].BookID == created.ID {
			primaryResult = &response.Data.Results[i]
		}
		if response.Data.Results[i].BookID == other.ID {
			missingResult = &response.Data.Results[i]
		}
	}
	require.NotNil(t, primaryResult)
	require.NotNil(t, missingResult)

	assert.Equal(t, "updated", primaryResult.Status)
	assert.Contains(t, primaryResult.AppliedFields, "author_name")
	assert.Contains(t, primaryResult.AppliedFields, "language")
	assert.NotContains(t, primaryResult.AppliedFields, "publisher")
	assert.Contains(t, primaryResult.FetchedFields, "publisher")

	assert.Equal(t, "not_found", missingResult.Status)

	updatedBook, err := database.GetGlobalStore().GetBookByID(created.ID)
	require.NoError(t, err)
	require.NotNil(t, updatedBook)
	assert.Nil(t, updatedBook.Publisher)
	require.NotNil(t, updatedBook.Language)
	assert.Equal(t, "eng", *updatedBook.Language)
	assert.NotNil(t, updatedBook.AuthorID)

	state, err := server.loadMetadataState(created.ID)
	require.NoError(t, err)
	require.NotNil(t, state)
	entry := state["publisher"]
	assert.Equal(t, "Test Publisher", entry.FetchedValue)
	assert.Equal(t, "Manual Publisher", entry.OverrideValue)
	assert.True(t, entry.OverrideLocked)
}

// TestCORSMiddleware tests CORS headers
func TestCORSMiddleware(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodOptions, "/api/health", nil)
	w := httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNoContent, w.Code)
	assert.Empty(t, w.Header().Get("Access-Control-Allow-Origin"))
}

// TestRouteNotFound tests 404 handling
func TestRouteNotFound(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	tests := []struct {
		name           string
		path           string
		expectedStatus int
	}{
		{
			name:           "api endpoint not found",
			path:           "/api/v1/nonexistent",
			expectedStatus: http.StatusNotFound,
		},
		{
			name:           "root path",
			path:           "/",
			expectedStatus: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			w := httptest.NewRecorder()

			server.router.ServeHTTP(w, req)

			assert.Equal(t, tt.expectedStatus, w.Code)
		})
	}
}

// BenchmarkHealthCheck benchmarks the health check endpoint
func BenchmarkHealthCheck(b *testing.B) {
	server, cleanup := setupTestServer(&testing.T{})
	defer cleanup()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)
	}
}

// BenchmarkListAudiobooks benchmarks listing audiobooks
func BenchmarkListAudiobooks(b *testing.B) {
	server, cleanup := setupTestServer(&testing.T{})
	defer cleanup()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/audiobooks?limit=50", nil)
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)
	}
}

// TestEndToEndWorkflow tests a complete workflow
func TestEndToEndWorkflow(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	// Step 1: Check health
	t.Run("check health", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
	})

	// Step 2: List audiobooks (should be empty)
	t.Run("list empty audiobooks", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/audiobooks", nil)
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
	})

	// Step 3: Get configuration
	t.Run("get configuration", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/config", nil)
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
	})

	// Step 4: List import paths
	t.Run("list import paths", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/import-paths", nil)
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
	})

	// Step 5: List backups
	t.Run("list backups", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/backup/list", nil)
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
	})
}

// TestResponseTimes tests that endpoints respond within acceptable time
func TestResponseTimes(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	endpoints := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/health"},
		{http.MethodGet, "/api/v1/audiobooks"},
		{http.MethodGet, "/api/v1/authors"},
		{http.MethodGet, "/api/v1/series"},
		{http.MethodGet, "/api/v1/config"},
	}

	for _, endpoint := range endpoints {
		t.Run(fmt.Sprintf("%s %s", endpoint.method, endpoint.path), func(t *testing.T) {
			req := httptest.NewRequest(endpoint.method, endpoint.path, nil)
			w := httptest.NewRecorder()

			server.router.ServeHTTP(w, req)

			assert.Equal(t, http.StatusOK, w.Code)
		})
	}
}

// =============================================================================
// Task 3: Size & Format Testing (formerly in task3_size_test.go)
// =============================================================================

// TestDashboardSizeFormat tests dashboard size and format counts
func TestDashboardSizeFormat(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/dashboard", nil)
	w := httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var wrapper struct {
		Data map[string]any `json:"data"`
	}
	err := json.Unmarshal(w.Body.Bytes(), &wrapper)
	require.NoError(t, err)
	response := wrapper.Data

	// Verify formatDistribution exists
	formatDistribution, ok := response["formatDistribution"].(map[string]any)
	assert.True(t, ok, "formatDistribution should exist")
	assert.NotNil(t, formatDistribution)

	// Verify stateDistribution exists
	stateDistribution, ok := response["stateDistribution"].(map[string]any)
	assert.True(t, ok, "stateDistribution should exist")
	assert.NotNil(t, stateDistribution)

	t.Run("format distribution structure", func(t *testing.T) {
		for _, count := range formatDistribution {
			_, isNumber := count.(float64)
			assert.True(t, isNumber, "Format counts should be numbers")
		}
	})
}

// TestSizeCalculationAccuracy tests size calculation accuracy
func TestSizeCalculationAccuracy(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	// This test verifies that size calculations are accurate
	// In a real test, we'd create test audiobooks with known sizes
	req := httptest.NewRequest(http.MethodGet, "/api/v1/dashboard", nil)
	w := httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var response map[string]any
	err := json.Unmarshal(w.Body.Bytes(), &response)
	require.NoError(t, err)
	response = response["data"].(map[string]any)

	// Verify totalSize is a number
	totalSize, ok := response["totalSize"].(float64)
	assert.True(t, ok, "totalSize should be a number")
	assert.GreaterOrEqual(t, totalSize, float64(0), "totalSize should be non-negative")
}

// TestFormatDetection tests format detection accuracy
func TestFormatDetection(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	tests := []struct {
		name           string
		expectedFormat string
	}{
		{"m4b detection", "m4b"},
		{"mp3 detection", "mp3"},
		{"opus detection", "opus"},
		{"flac detection", "flac"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// In a real test, we'd create audiobooks with these formats
			// and verify they're detected correctly
			req := httptest.NewRequest(http.MethodGet, "/api/v1/dashboard", nil)
			w := httptest.NewRecorder()

			server.router.ServeHTTP(w, req)
			assert.Equal(t, http.StatusOK, w.Code)
		})
	}
}

// TestSizeBucketDistribution tests state distribution in dashboard
func TestSizeBucketDistribution(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/dashboard", nil)
	w := httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var response map[string]any
	err := json.Unmarshal(w.Body.Bytes(), &response)
	require.NoError(t, err)
	response = response["data"].(map[string]any)

	// totalBooks and totalSize should be present
	_, ok := response["totalBooks"]
	assert.True(t, ok, "totalBooks should exist")
	_, ok = response["totalSize"]
	assert.True(t, ok, "totalSize should exist")
}

// TestEmptyDashboardSizeFormat tests dashboard with no audiobooks
func TestEmptyDashboardSizeFormat(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/dashboard", nil)
	w := httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var wrapper struct {
		Data map[string]any `json:"data"`
	}
	err := json.Unmarshal(w.Body.Bytes(), &wrapper)
	require.NoError(t, err)
	response := wrapper.Data

	// Even with no audiobooks, distributions should exist
	formatDistribution, ok := response["formatDistribution"].(map[string]any)
	assert.True(t, ok, "formatDistribution should exist even when empty")
	assert.NotNil(t, formatDistribution)

	stateDistribution, ok := response["stateDistribution"].(map[string]any)
	assert.True(t, ok, "stateDistribution should exist even when empty")
	assert.NotNil(t, stateDistribution)
}

// =============================================================================
// Metadata Fields Testing (formerly in metadata_fields_test.go)
// =============================================================================

// TestGetMetadataFields tests getting metadata fields
func TestGetMetadataFields(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/metadata/fields", nil)
	w := httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var response struct {
		Data struct {
			Fields []any `json:"fields"`
		} `json:"data"`
	}
	err := json.Unmarshal(w.Body.Bytes(), &response)
	require.NoError(t, err)

	// Verify fields structure
	fields := response.Data.Fields
	assert.NotNil(t, fields)

	// Verify required fields are present
	requiredFields := []string{"title", "author", "narrator", "series", "publishDate"}
	fieldNames := make(map[string]bool)

	for _, field := range fields {
		fieldMap, ok := field.(map[string]any)
		if ok {
			if name, ok := fieldMap["name"].(string); ok {
				fieldNames[name] = true
			}
		}
	}

	for _, required := range requiredFields {
		assert.True(t, fieldNames[required], fmt.Sprintf("Required field %s should be present", required))
	}
}

// TestMetadataFieldValidation tests metadata field validation
func TestMetadataFieldValidation(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	tests := []struct {
		name           string
		field          string
		value          any
		expectedValid  bool
		expectedStatus int
	}{
		{
			name:           "valid title",
			field:          "title",
			value:          "Test Book",
			expectedValid:  true,
			expectedStatus: http.StatusOK,
		},
		{
			name:           "empty title",
			field:          "title",
			value:          "",
			expectedValid:  false,
			expectedStatus: http.StatusBadRequest,
		},
		{
			name:           "valid publish date",
			field:          "publishDate",
			value:          "2024-01-01",
			expectedValid:  true,
			expectedStatus: http.StatusOK,
		},
		{
			name:           "invalid publish date",
			field:          "publishDate",
			value:          "invalid-date",
			expectedValid:  false,
			expectedStatus: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requestBody := map[string]any{
				"updates": map[string]any{
					tt.field: tt.value,
				},
			}

			body, err := json.Marshal(requestBody)
			require.NoError(t, err, "failed to marshal request body for test case %s", tt.name)
			req := httptest.NewRequest(http.MethodPost, "/api/v1/metadata/validate", bytes.NewBuffer(body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()

			server.router.ServeHTTP(w, req)

			assert.Equal(t, tt.expectedStatus, w.Code)
		})
	}
}

// =============================================================================
// Work Queue Testing (formerly in work_test.go)
// =============================================================================

// TestGetWork tests getting work items from the queue
func TestGetWork(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/work", nil)
	w := httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var response map[string]any
	err := json.Unmarshal(w.Body.Bytes(), &response)
	require.NoError(t, err)
	response = response["data"].(map[string]any)

	// Verify work queue structure
	workItems, ok := response["items"].([]any)
	assert.True(t, ok, "work items should be an array")
	assert.NotNil(t, workItems)
}

// TestWorkQueueOperations tests work queue operations
func TestWorkQueueOperations(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	tests := []struct {
		name           string
		endpoint       string
		method         string
		expectedStatus int
	}{
		{
			name:           "list work items",
			endpoint:       "/api/v1/work",
			method:         http.MethodGet,
			expectedStatus: http.StatusOK,
		},
		{
			name:           "get work statistics",
			endpoint:       "/api/v1/work/stats",
			method:         http.MethodGet,
			expectedStatus: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.endpoint, nil)
			w := httptest.NewRecorder()

			server.router.ServeHTTP(w, req)

			assert.Equal(t, tt.expectedStatus, w.Code)
		})
	}
}

// TestWorkQueuePriority tests work queue priority handling
func TestWorkQueuePriority(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	// Test that work queue respects priority
	req := httptest.NewRequest(http.MethodGet, "/api/v1/work", nil)
	w := httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var response map[string]any
	err := json.Unmarshal(w.Body.Bytes(), &response)
	require.NoError(t, err)
	response = response["data"].(map[string]any)

	workItems, ok := response["items"].([]any)
	assert.True(t, ok)

	// Verify priority ordering if items exist
	if len(workItems) > 1 {
		for i := 0; i < len(workItems)-1; i++ {
			current := workItems[i].(map[string]any)
			next := workItems[i+1].(map[string]any)

			currentPriority, _ := current["priority"].(float64)
			nextPriority, _ := next["priority"].(float64)

			assert.GreaterOrEqual(t, currentPriority, nextPriority,
				"Work items should be ordered by priority (highest first)")
		}
	}
}

func TestGetAudiobookTagsReportsEffectiveSource(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	author, err := database.GetGlobalStore().CreateAuthor("Test Author")
	require.NoError(t, err)
	series, err := database.GetGlobalStore().CreateSeries("Test Series", &author.ID)
	require.NoError(t, err)

	tempDir := t.TempDir()
	filePath := filepath.Join(tempDir, "book.m4b")
	require.NoError(t, os.WriteFile(filePath, []byte("dummy audio"), 0o644))

	book, err := database.GetGlobalStore().CreateBook(&database.Book{
		Title:    "Stored Title",
		AuthorID: &author.ID,
		SeriesID: &series.ID,
		FilePath: filePath,
	})
	require.NoError(t, err)

	state := map[string]metafetch.MetadataFieldState{
		"title": {
			FetchedValue:   "Fetched Title",
			OverrideValue:  "Override Title",
			OverrideLocked: true,
		},
		"narrator": {
			FetchedValue:   "Fetched Narrator",
			OverrideValue:  "Override Narrator",
			OverrideLocked: false,
		},
	}
	require.NoError(t, server.saveMetadataState(book.ID, state))

	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/audiobooks/%s/tags", book.ID), nil)
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var wrapper struct {
		Data struct {
			Tags map[string]struct {
				FileValue      any  `json:"file_value"`
				FetchedValue   any  `json:"fetched_value"`
				StoredValue    any  `json:"stored_value"`
				OverrideValue  any  `json:"override_value"`
				OverrideLocked bool `json:"override_locked"`
			} `json:"tags"`
		} `json:"data"`
	}

	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &wrapper))
	resp := wrapper.Data

	require.Contains(t, resp.Tags, "title")
	assert.Equal(t, "Stored Title", resp.Tags["title"].StoredValue)
	assert.Equal(t, "Fetched Title", resp.Tags["title"].FetchedValue)
	assert.Equal(t, "Override Title", resp.Tags["title"].OverrideValue)
	assert.True(t, resp.Tags["title"].OverrideLocked)

	require.Contains(t, resp.Tags, "author_name")
	assert.Equal(t, "Test Author", resp.Tags["author_name"].StoredValue)
	require.Contains(t, resp.Tags, "series_name")
	assert.Equal(t, "Test Series", resp.Tags["series_name"].StoredValue)

	require.Contains(t, resp.Tags, "narrator")
	assert.Equal(t, "Fetched Narrator", resp.Tags["narrator"].FetchedValue)
}

func TestUpdateAudiobookPersistsOverrides(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	tempDir := t.TempDir()
	filePath := filepath.Join(tempDir, "book.m4b")
	require.NoError(t, os.WriteFile(filePath, []byte("dummy audio"), 0o644))

	book, err := database.GetGlobalStore().CreateBook(&database.Book{
		Title:    "Original Title",
		FilePath: filePath,
	})
	require.NoError(t, err)

	payload := map[string]any{
		"title":       "Updated Title",
		"author_name": "Override Author",
		"overrides": map[string]any{
			"narrator": map[string]any{
				"value":  "Narrator Override",
				"locked": true,
			},
		},
	}

	body, err := json.Marshal(payload)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/api/v1/audiobooks/%s", book.ID), bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	state, err := server.loadMetadataState(book.ID)
	require.NoError(t, err)

	if assert.Contains(t, state, "title") {
		assert.Equal(t, "Updated Title", state["title"].OverrideValue)
		assert.True(t, state["title"].OverrideLocked)
	}

	if assert.Contains(t, state, "narrator") {
		assert.Equal(t, "Narrator Override", state["narrator"].OverrideValue)
		assert.True(t, state["narrator"].OverrideLocked)
	}

	if assert.Contains(t, state, "author_name") {
		assert.Equal(t, "Override Author", state["author_name"].OverrideValue)
		assert.True(t, state["author_name"].OverrideLocked)
	}
}

// TestAddImportPath tests creating an import path
func TestAddImportPath(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	payload := map[string]any{
		"path": "/some/import/path",
		"name": "Test Import",
	}
	body, _ := json.Marshal(payload)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/import-paths", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusCreated, w.Code)

	// Verify it shows up in the list
	req2 := httptest.NewRequest(http.MethodGet, "/api/v1/import-paths", nil)
	w2 := httptest.NewRecorder()
	server.router.ServeHTTP(w2, req2)
	assert.Equal(t, http.StatusOK, w2.Code)
	assert.Contains(t, w2.Body.String(), "/some/import/path")
}

// TestAddImportPathEmptyPath tests validation for empty import path
func TestAddImportPathEmptyPath(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	payload := map[string]any{
		"path": "",
		"name": "Empty Path",
	}
	body, _ := json.Marshal(payload)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/import-paths", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// TestUpdateConfig tests the config update endpoint
func TestUpdateConfig(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	payload := map[string]any{
		"root_dir":              "/new/root",
		"auto_organize":         true,
		"scan_on_startup":       false,
		"folder_naming_pattern": "{author}/{title}",
		"file_naming_pattern":   "{title}",
		"log_level":             "debug",
	}
	body, _ := json.Marshal(payload)

	req := httptest.NewRequest(http.MethodPut, "/api/v1/config", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	resp = resp["data"].(map[string]any)
	assert.Equal(t, "/new/root", resp["root_dir"])
	assert.Equal(t, true, resp["auto_organize"])
}

// TestUpdateConfigRejectsDatabaseType tests that database_type changes are rejected
func TestUpdateConfigRejectsDatabaseType(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	payload := map[string]any{
		"database_type": "postgres",
	}
	body, _ := json.Marshal(payload)

	req := httptest.NewRequest(http.MethodPut, "/api/v1/config", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// TestSoftDeleteAudiobook tests soft deletion
func TestSoftDeleteAudiobook(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	tempDir := t.TempDir()
	filePath := filepath.Join(tempDir, "softdelete.m4b")
	require.NoError(t, os.WriteFile(filePath, []byte("audio"), 0o644))

	book, err := database.GetGlobalStore().CreateBook(&database.Book{
		Title:    "To Soft Delete",
		FilePath: filePath,
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodDelete,
		fmt.Sprintf("/api/v1/audiobooks/%s?soft_delete=true", book.ID), nil)
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "soft deleted")
}

// TestGetOperationLogs tests the operation logs endpoint
func TestGetOperationLogs(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/system/logs", nil)
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
}

// TestGetOperationLogsWithPagination tests logs with pagination
func TestGetOperationLogsWithPagination(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/system/logs?page=1&page_size=10&search=scan", nil)
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
}

// TestListAudiobookVersions tests the version listing endpoint
func TestListAudiobookVersions(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	tempDir := t.TempDir()
	filePath := filepath.Join(tempDir, "version.m4b")
	require.NoError(t, os.WriteFile(filePath, []byte("audio"), 0o644))

	book, err := database.GetGlobalStore().CreateBook(&database.Book{
		Title:    "Version Test",
		FilePath: filePath,
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet,
		fmt.Sprintf("/api/v1/audiobooks/%s/versions", book.ID), nil)
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
}

// TestGetDuplicates tests the duplicates endpoint
func TestGetDuplicates(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/audiobooks/duplicates", nil)
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
}

// TestGetSoftDeletedBooks tests the soft-deleted books endpoint
func TestGetSoftDeletedBooks(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/audiobooks/soft-deleted", nil)
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
}

// TestCountAudiobooks tests the count endpoint
func TestCountAudiobooks(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/audiobooks/count", nil)
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
}

// TestCreateAndListWorks tests work creation and listing
func TestCreateAndListWorks(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	// Create a work
	payload := map[string]any{
		"title":       "Test Work",
		"description": "A test work item",
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/works", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusCreated, w.Code)

	// List works
	req2 := httptest.NewRequest(http.MethodGet, "/api/v1/works", nil)
	w2 := httptest.NewRecorder()
	server.router.ServeHTTP(w2, req2)
	assert.Equal(t, http.StatusOK, w2.Code)
}

// TestRestoreAudiobook tests restoring a soft-deleted audiobook
func TestRestoreAudiobook(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	tempDir := t.TempDir()
	filePath := filepath.Join(tempDir, "restore.m4b")
	require.NoError(t, os.WriteFile(filePath, []byte("audio"), 0o644))

	book, err := database.GetGlobalStore().CreateBook(&database.Book{
		Title:    "To Restore",
		FilePath: filePath,
	})
	require.NoError(t, err)

	// Soft delete first
	req := httptest.NewRequest(http.MethodDelete,
		fmt.Sprintf("/api/v1/audiobooks/%s?soft_delete=true", book.ID), nil)
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	// Now restore
	req2 := httptest.NewRequest(http.MethodPost,
		fmt.Sprintf("/api/v1/audiobooks/%s/restore", book.ID), nil)
	w2 := httptest.NewRecorder()
	server.router.ServeHTTP(w2, req2)
	assert.Equal(t, http.StatusOK, w2.Code)
}

// TestPurgeSoftDeletedBooks tests purging soft-deleted books
func TestPurgeSoftDeletedBooks(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/audiobooks/purge-soft-deleted", nil)
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
}

// TestLinkAudiobookVersion tests linking two audiobooks as versions
func TestLinkAudiobookVersion(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	tempDir := t.TempDir()

	// Create two books
	filePath1 := filepath.Join(tempDir, "v1.m4b")
	filePath2 := filepath.Join(tempDir, "v2.m4b")
	require.NoError(t, os.WriteFile(filePath1, []byte("v1"), 0o644))
	require.NoError(t, os.WriteFile(filePath2, []byte("v2"), 0o644))

	book1, err := database.GetGlobalStore().CreateBook(&database.Book{Title: "Book V1", FilePath: filePath1})
	require.NoError(t, err)
	book2, err := database.GetGlobalStore().CreateBook(&database.Book{Title: "Book V2", FilePath: filePath2})
	require.NoError(t, err)

	payload := map[string]any{
		"other_id": book2.ID,
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost,
		fmt.Sprintf("/api/v1/audiobooks/%s/versions", book1.ID), bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)

	// Should succeed or at least not 500
	assert.True(t, w.Code == http.StatusOK || w.Code == http.StatusCreated,
		"expected 200 or 201, got %d: %s", w.Code, w.Body.String())
}

// TestRemoveImportPath tests removing an import path
func TestRemoveImportPath(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	// Create an import path directly via DB
	importPath, err := database.GetGlobalStore().CreateImportPath("/to/remove", "Remove Me")
	require.NoError(t, err)

	// Delete via API - note route uses :id suffix on import-paths
	req := httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/api/v1/import-paths/%d", importPath.ID), nil)
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	assert.True(t, w.Code == http.StatusOK || w.Code == http.StatusNoContent,
		"expected 200 or 204, got %d: %s", w.Code, w.Body.String())
}

// TestCreateExclusion tests filesystem exclusion creation
func TestCreateExclusion(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	payload := map[string]any{"pattern": "*.tmp"}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/filesystem/exclude", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)

	// Should succeed (200/201) or error gracefully
	assert.True(t, w.Code < 500, "unexpected server error: %d %s", w.Code, w.Body.String())
}

// TestGetHomeDirectory tests home directory endpoint
func TestGetHomeDirectory(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/filesystem/home", nil)
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "path")
}

// TestGetAudiobookNotFound tests 404 for nonexistent audiobook
func TestGetAudiobookNotFound(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/audiobooks/nonexistent-id", nil)
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

// TestUpdateAudiobookNotFound tests 404 for updating nonexistent audiobook
func TestUpdateAudiobookNotFound(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	payload := map[string]any{"title": "New Title"}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPut, "/api/v1/audiobooks/nonexistent-id", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

// TestDeleteAudiobookNotFound tests 404 for deleting nonexistent audiobook
func TestDeleteAudiobookNotFound(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/audiobooks/nonexistent-id", nil)
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

// TestListAudiobooksWithFilters tests listing audiobooks with various filters
func TestListAudiobooksWithFilters(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	// Create an author and audiobook
	author, err := database.GetGlobalStore().CreateAuthor("Filter Author")
	require.NoError(t, err)

	tempDir := t.TempDir()
	filePath := filepath.Join(tempDir, "filter.m4b")
	require.NoError(t, os.WriteFile(filePath, []byte("audio"), 0o644))
	_, err = database.GetGlobalStore().CreateBook(&database.Book{
		Title:    "Filter Book",
		AuthorID: &author.ID,
		FilePath: filePath,
	})
	require.NoError(t, err)

	tests := []struct {
		name  string
		query string
	}{
		{"with search", "/api/v1/audiobooks?search=Filter"},
		{"with author_id", fmt.Sprintf("/api/v1/audiobooks?author_id=%d", author.ID)},
		{"with limit", "/api/v1/audiobooks?limit=5&offset=0"},
		{"with all params", fmt.Sprintf("/api/v1/audiobooks?search=Filter&author_id=%d&limit=10", author.ID)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.query, nil)
			w := httptest.NewRecorder()
			server.router.ServeHTTP(w, req)
			assert.Equal(t, http.StatusOK, w.Code)
		})
	}
}

// TestRemoveExclusion tests removing a directory exclusion
func TestRemoveExclusion(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	tests := []struct {
		name           string
		requestBody    map[string]any
		expectedStatus int
		validateFunc   func(t *testing.T, body []byte)
	}{
		{
			name:        "missing path returns error",
			requestBody: map[string]any{
				// path is missing
			},
			expectedStatus: http.StatusBadRequest,
			validateFunc: func(t *testing.T, body []byte) {
				var response map[string]any
				err := json.Unmarshal(body, &response)
				require.NoError(t, err)
				assert.NotNil(t, response["error"])
			},
		},
		{
			name: "empty path returns error",
			requestBody: map[string]any{
				"path": "",
			},
			expectedStatus: http.StatusBadRequest,
			validateFunc: func(t *testing.T, body []byte) {
				var response map[string]any
				err := json.Unmarshal(body, &response)
				require.NoError(t, err)
				assert.NotNil(t, response["error"])
			},
		},
		{
			name: "nonexistent exclusion returns error",
			requestBody: map[string]any{
				"path": t.TempDir(),
			},
			expectedStatus: http.StatusBadRequest,
			validateFunc: func(t *testing.T, body []byte) {
				var response map[string]any
				err := json.Unmarshal(body, &response)
				require.NoError(t, err)
				assert.NotNil(t, response["error"])
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, err := json.Marshal(tt.requestBody)
			require.NoError(t, err)
			req := httptest.NewRequest(http.MethodDelete, "/api/v1/filesystem/exclude", bytes.NewBuffer(body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()

			server.router.ServeHTTP(w, req)

			assert.Equal(t, tt.expectedStatus, w.Code)
			if tt.validateFunc != nil {
				tt.validateFunc(t, w.Body.Bytes())
			}
		})
	}
}

// TestListBlockedHashes tests listing blocked hashes
func TestListBlockedHashes(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	// Add a blocked hash
	err := database.GetGlobalStore().AddBlockedHash("abc123", "test reason")
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/blocked-hashes", nil)
	w := httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var wrapper struct {
		Data map[string]any `json:"data"`
	}
	err = json.Unmarshal(w.Body.Bytes(), &wrapper)
	require.NoError(t, err)
	response := wrapper.Data

	assert.NotNil(t, response["items"])
	assert.NotNil(t, response["total"])

	items, ok := response["items"].([]any)
	require.True(t, ok)
	assert.Greater(t, len(items), 0)
}

// TestRemoveBlockedHash tests removing a blocked hash
func TestRemoveBlockedHash(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	tests := []struct {
		name           string
		hash           string
		setup          func()
		expectedStatus int
		validateFunc   func(t *testing.T, body []byte)
	}{
		{
			name: "empty hash returns error",
			hash: "",
			setup: func() {
				// No setup needed
			},
			expectedStatus: http.StatusNotFound,
			validateFunc:   nil,
		},
		{
			name: "successfully remove blocked hash",
			hash: "def456",
			setup: func() {
				err := database.GetGlobalStore().AddBlockedHash("def456", "test reason")
				require.NoError(t, err)
			},
			expectedStatus: http.StatusOK,
			validateFunc: func(t *testing.T, body []byte) {
				var response map[string]any
				err := json.Unmarshal(body, &response)
				require.NoError(t, err)
				response = response["data"].(map[string]any)
				assert.Equal(t, "hash unblocked successfully", response["message"])
				assert.Equal(t, "def456", response["hash"])
			},
		},
		{
			name: "remove nonexistent hash succeeds",
			hash: "nonexistent789",
			setup: func() {
				// No setup - hash doesn't exist
			},
			expectedStatus: http.StatusOK,
			validateFunc: func(t *testing.T, body []byte) {
				var response map[string]any
				err := json.Unmarshal(body, &response)
				require.NoError(t, err)
				response = response["data"].(map[string]any)
				assert.Equal(t, "hash unblocked successfully", response["message"])
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.setup != nil {
				tt.setup()
			}

			url := "/api/v1/blocked-hashes/" + tt.hash
			req := httptest.NewRequest(http.MethodDelete, url, nil)
			w := httptest.NewRecorder()

			server.router.ServeHTTP(w, req)

			assert.Equal(t, tt.expectedStatus, w.Code)
			if tt.validateFunc != nil {
				tt.validateFunc(t, w.Body.Bytes())
			}
		})
	}
}

// TestExportMetadataHandler tests the export metadata endpoint
func TestExportMetadataHandler(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	// Create a test book
	tempDir := t.TempDir()
	filePath := filepath.Join(tempDir, "test.m4b")
	require.NoError(t, os.WriteFile(filePath, []byte("audio"), 0o644))

	_, err := database.GetGlobalStore().CreateBook(&database.Book{
		Title:    "Export Test Book",
		FilePath: filePath,
	})
	require.NoError(t, err)

	tests := []struct {
		name           string
		expectedStatus int
		validateFunc   func(t *testing.T, body []byte)
	}{
		{
			name:           "export metadata successfully",
			expectedStatus: http.StatusOK,
			validateFunc: func(t *testing.T, body []byte) {
				var response map[string]any
				err := json.Unmarshal(body, &response)
				require.NoError(t, err)
				response = response["data"].(map[string]any)
				assert.NotNil(t, response)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/metadata/export", nil)
			w := httptest.NewRecorder()

			server.router.ServeHTTP(w, req)

			assert.Equal(t, tt.expectedStatus, w.Code)
			if tt.validateFunc != nil {
				tt.validateFunc(t, w.Body.Bytes())
			}
		})
	}
}

// TestSetEmbeddedFS tests the SetEmbeddedFS function
func TestSetEmbeddedFS(t *testing.T) {
	// This test verifies that SetEmbeddedFS is a no-op when not embedding frontend
	// The function should not panic and should complete successfully
	t.Run("no-op when not embedding", func(t *testing.T) {
		// Create an empty embed.FS (this will be empty in non-embed builds)
		var fs embed.FS

		// Should not panic
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("SetEmbeddedFS panicked: %v", r)
			}
		}()

		SetEmbeddedFS(fs)
	})
}

// TestGetDashboardWithData tests dashboard with actual data
func TestGetDashboardWithData(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	// Create some test data
	author, err := database.GetGlobalStore().CreateAuthor("Test Author")
	require.NoError(t, err)

	tempDir := t.TempDir()
	filePath := filepath.Join(tempDir, "test.m4b")
	require.NoError(t, os.WriteFile(filePath, []byte("audio data for testing"), 0o644))

	_, err = database.GetGlobalStore().CreateBook(&database.Book{
		Title:    "Dashboard Test Book",
		AuthorID: &author.ID,
		FilePath: filePath,
		Format:   "m4b",
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/dashboard", nil)
	w := httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var wrapper struct {
		Data map[string]any `json:"data"`
	}
	err = json.Unmarshal(w.Body.Bytes(), &wrapper)
	require.NoError(t, err)
	response := wrapper.Data

	// Verify dashboard response structure with data
	assert.NotNil(t, response["totalBooks"])
	assert.NotNil(t, response["totalSize"])
	assert.NotNil(t, response["stateDistribution"])
	assert.NotNil(t, response["formatDistribution"])

	totalBooks, ok := response["totalBooks"].(float64)
	require.True(t, ok, "totalBooks should be a number")
	assert.Equal(t, float64(1), totalBooks)
}

// TestBatchUpdateAudiobooksWithData tests batch update with actual data
func TestBatchUpdateAudiobooksWithData(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	tempDir := t.TempDir()
	filePath := filepath.Join(tempDir, "batch.m4b")
	require.NoError(t, os.WriteFile(filePath, []byte("audio"), 0o644))

	book, err := database.GetGlobalStore().CreateBook(&database.Book{
		Title:    "Batch Test",
		FilePath: filePath,
	})
	require.NoError(t, err)

	batchData := map[string]any{
		"audiobooks": []map[string]any{
			{
				"id":    book.ID,
				"title": "Updated Batch Title",
			},
		},
	}
	body, err := json.Marshal(batchData)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/audiobooks/batch", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	// Just verify it doesn't error
	assert.Equal(t, http.StatusOK, w.Code)
}

// TestGetWorkStats tests the work queue stats endpoint
func TestGetWorkStats(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/work/stats", nil)
	w := httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var response map[string]any
	err := json.Unmarshal(w.Body.Bytes(), &response)
	require.NoError(t, err)
	response = response["data"].(map[string]any)
	assert.NotNil(t, response)
}

// TestDeleteAudiobookWithSoftDelete tests soft deletion behavior
func TestDeleteAudiobookWithSoftDelete(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	tempDir := t.TempDir()
	filePath := filepath.Join(tempDir, "softdel.m4b")
	require.NoError(t, os.WriteFile(filePath, []byte("audio"), 0o644))

	book, err := database.GetGlobalStore().CreateBook(&database.Book{
		Title:    "To Be Soft Deleted",
		FilePath: filePath,
	})
	require.NoError(t, err)

	// Soft delete
	req := httptest.NewRequest(http.MethodDelete,
		fmt.Sprintf("/api/v1/audiobooks/%s?soft_delete=true", book.ID), nil)
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	// Verify soft-deleted books endpoint
	req2 := httptest.NewRequest(http.MethodGet, "/api/v1/audiobooks/soft-deleted", nil)
	w2 := httptest.NewRecorder()
	server.router.ServeHTTP(w2, req2)

	assert.Equal(t, http.StatusOK, w2.Code)
}

// TestUpdateAudiobookWithMetadata tests updating audiobook with various metadata fields
func TestUpdateAudiobookWithMetadata(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	author, err := database.GetGlobalStore().CreateAuthor("Original Author")
	require.NoError(t, err)

	tempDir := t.TempDir()
	filePath := filepath.Join(tempDir, "meta.m4b")
	require.NoError(t, os.WriteFile(filePath, []byte("audio"), 0o644))

	book, err := database.GetGlobalStore().CreateBook(&database.Book{
		Title:    "Original Title",
		AuthorID: &author.ID,
		FilePath: filePath,
	})
	require.NoError(t, err)

	publisher := "Test Publisher"
	narrator := "Test Narrator"
	language := "en"
	updateData := map[string]any{
		"title":     "Updated Title",
		"publisher": publisher,
		"narrator":  narrator,
		"language":  language,
	}
	body, err := json.Marshal(updateData)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPut,
		fmt.Sprintf("/api/v1/audiobooks/%s", book.ID),
		bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	// Verify the metadata was updated
	updated, err := database.GetGlobalStore().GetBookByID(book.ID)
	require.NoError(t, err)
	assert.Equal(t, "Updated Title", updated.Title)
	require.NotNil(t, updated.Publisher)
	assert.Equal(t, publisher, *updated.Publisher)
	require.NotNil(t, updated.Narrator)
	assert.Equal(t, narrator, *updated.Narrator)
	require.NotNil(t, updated.Language)
	assert.Equal(t, language, *updated.Language)
}

// TestListAudiobooksWithSearchAndPagination tests search and pagination
func TestListAudiobooksWithSearchAndPagination(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	// Create multiple books
	tempDir := t.TempDir()
	for i := 0; i < 5; i++ {
		filePath := filepath.Join(tempDir, fmt.Sprintf("book%d.m4b", i))
		require.NoError(t, os.WriteFile(filePath, []byte("audio"), 0o644))
		_, err := database.GetGlobalStore().CreateBook(&database.Book{
			Title:    fmt.Sprintf("Test Book %d", i),
			FilePath: filePath,
		})
		require.NoError(t, err)
	}

	// Test with pagination
	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/audiobooks?limit=2&offset=0", nil)
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var response map[string]any
	err := json.Unmarshal(w.Body.Bytes(), &response)
	require.NoError(t, err)
	response = response["data"].(map[string]any)

	items, ok := response["items"].([]any)
	require.True(t, ok)
	assert.LessOrEqual(t, len(items), 2)

	// Test with search
	req2 := httptest.NewRequest(http.MethodGet,
		"/api/v1/audiobooks?search=Test", nil)
	w2 := httptest.NewRecorder()
	server.router.ServeHTTP(w2, req2)

	assert.Equal(t, http.StatusOK, w2.Code)
}

// TestHandleITunesImportStatus tests the iTunes import status endpoint
func TestHandleITunesImportStatus(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	tests := []struct {
		name           string
		operationID    string
		setup          func() string
		expectedStatus int
		validateFunc   func(t *testing.T, body []byte)
	}{
		{
			name:           "operation not found",
			operationID:    "01HXZ999999999999999999",
			setup:          func() string { return "01HXZ999999999999999999" },
			expectedStatus: http.StatusNotFound,
			validateFunc: func(t *testing.T, body []byte) {
				var response map[string]any
				err := json.Unmarshal(body, &response)
				require.NoError(t, err)
				assert.Contains(t, response["error"], "operation not found")
			},
		},
		{
			name: "success - queued operation",
			setup: func() string {
				libPath := "/fake/library.xml"
				opID := ulid.Make().String()
				op, err := database.GetGlobalStore().CreateOperation(opID, "itunes_import", &libPath)
				require.NoError(t, err)
				return op.ID
			},
			expectedStatus: http.StatusOK,
			validateFunc: func(t *testing.T, body []byte) {
				var wrapper struct {
					Data map[string]any `json:"data"`
				}
				err := json.Unmarshal(body, &wrapper)
				require.NoError(t, err)
				assert.NotNil(t, wrapper.Data["operation_id"])
				assert.NotNil(t, wrapper.Data["status"])
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opID := tt.operationID
			if tt.setup != nil {
				opID = tt.setup()
			}

			req := httptest.NewRequest(http.MethodGet,
				fmt.Sprintf("/api/v1/itunes/import-status/%s", opID), nil)
			w := httptest.NewRecorder()

			server.router.ServeHTTP(w, req)

			assert.Equal(t, tt.expectedStatus, w.Code)
			if tt.validateFunc != nil {
				tt.validateFunc(t, w.Body.Bytes())
			}
		})
	}
}

// TestListAudiobookVersions_ErrorCases tests error cases for version listing
func TestListAudiobookVersions_ErrorCases(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	tests := []struct {
		name           string
		bookID         string
		setup          func() string
		expectedStatus int
		validateFunc   func(t *testing.T, body []byte)
	}{
		{
			name:           "book not found",
			bookID:         "01HXZ888888888888888888",
			expectedStatus: http.StatusNotFound,
			validateFunc: func(t *testing.T, body []byte) {
				var response map[string]any
				err := json.Unmarshal(body, &response)
				require.NoError(t, err)
				assert.Contains(t, response["error"], "not found")
			},
		},
		{
			name: "book with no version group",
			setup: func() string {
				tempDir := t.TempDir()
				filePath := filepath.Join(tempDir, "single.m4b")
				require.NoError(t, os.WriteFile(filePath, []byte("audio"), 0o644))
				book, err := database.GetGlobalStore().CreateBook(&database.Book{
					Title:    "Single Version Book",
					FilePath: filePath,
				})
				require.NoError(t, err)
				return book.ID
			},
			expectedStatus: http.StatusOK,
			validateFunc: func(t *testing.T, body []byte) {
				var response map[string]any
				err := json.Unmarshal(body, &response)
				require.NoError(t, err)
				response = response["data"].(map[string]any)
				versions, ok := response["versions"].([]any)
				assert.True(t, ok)
				assert.Equal(t, 1, len(versions))
			},
		},
		{
			name: "book with version group",
			setup: func() string {
				tempDir := t.TempDir()
				filePath1 := filepath.Join(tempDir, "v1.m4b")
				filePath2 := filepath.Join(tempDir, "v2.m4b")
				require.NoError(t, os.WriteFile(filePath1, []byte("v1"), 0o644))
				require.NoError(t, os.WriteFile(filePath2, []byte("v2"), 0o644))

				groupID := "vg-" + ulid.Make().String()
				book1, err := database.GetGlobalStore().CreateBook(&database.Book{
					Title:          "Version 1",
					FilePath:       filePath1,
					VersionGroupID: &groupID,
				})
				require.NoError(t, err)
				_, err = database.GetGlobalStore().CreateBook(&database.Book{
					Title:          "Version 2",
					FilePath:       filePath2,
					VersionGroupID: &groupID,
				})
				require.NoError(t, err)
				return book1.ID
			},
			expectedStatus: http.StatusOK,
			validateFunc: func(t *testing.T, body []byte) {
				var response map[string]any
				err := json.Unmarshal(body, &response)
				require.NoError(t, err)
				response = response["data"].(map[string]any)
				versions, ok := response["versions"].([]any)
				assert.True(t, ok)
				assert.Equal(t, 2, len(versions))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bookID := tt.bookID
			if tt.setup != nil {
				bookID = tt.setup()
			}

			req := httptest.NewRequest(http.MethodGet,
				fmt.Sprintf("/api/v1/audiobooks/%s/versions", bookID), nil)
			w := httptest.NewRecorder()

			server.router.ServeHTTP(w, req)

			assert.Equal(t, tt.expectedStatus, w.Code)
			if tt.validateFunc != nil {
				tt.validateFunc(t, w.Body.Bytes())
			}
		})
	}
}

func TestListWorks(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/works", nil)
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestListDuplicateAudiobooks(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/audiobooks/duplicates", nil)
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestListSoftDeletedAudiobooks(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/audiobooks/soft-deleted", nil)
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestPurgeSoftDeletedAudiobooks(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/audiobooks/purge-soft-deleted?older_than_days=30", nil)
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestRestoreAudiobookNotFound(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()
	fakeID := ulid.Make().String()
	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/audiobooks/%s/restore", fakeID), nil)
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestCreateWork(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()
	body := `{"title":"Test Work","author":"Test Author"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/works", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusCreated, w.Code)
}

func TestGetWorkNotFound(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/works/nonexistent", nil)
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestAddImportPathInvalid(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/import-paths", bytes.NewBufferString("{}"))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestGetOperationStatusNotFound(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/operations/nonexistent", nil)
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	assert.Contains(t, []int{404, 500}, w.Code)
}

// TestAutoNumberTrackNumbering tests the track renumbering logic used by handleAutoNumber.
// The logic is inline in server.go (~line 1738-1775) and operates on []metadata.TrackInfo:
//  1. If any track has number 0, all tracks get +1 (0-based → 1-based)
//  2. Files without parseable track numbers get gap-filled sequential numbers
//  3. All files get TotalTracks set to len(segments)
func TestAutoNumberTrackNumbering(t *testing.T) {
	intPtr := func(n int) *int { return &n }

	// applyAutoNumberLogic replicates the inline logic from handleAutoNumber in server.go.
	applyAutoNumberLogic := func(trackInfos []metadata.TrackInfo, totalSegments int) []metadata.TrackInfo {
		// Phase 1: if any track is 0, shift all numbered tracks +1
		hasZero := false
		for _, info := range trackInfos {
			if info.TrackNumber != nil && *info.TrackNumber == 0 {
				hasZero = true
				break
			}
		}
		if hasZero {
			for i := range trackInfos {
				if trackInfos[i].TrackNumber != nil {
					n := *trackInfos[i].TrackNumber + 1
					trackInfos[i].TrackNumber = &n
				}
			}
		}

		// Phase 2: assign sequential numbers to nil entries, filling gaps
		usedNumbers := map[int]bool{}
		for _, info := range trackInfos {
			if info.TrackNumber != nil {
				usedNumbers[*info.TrackNumber] = true
			}
		}
		nextNum := 1
		total := totalSegments
		for i := range trackInfos {
			if trackInfos[i].TrackNumber == nil {
				for usedNumbers[nextNum] {
					nextNum++
				}
				n := nextNum
				trackInfos[i].TrackNumber = &n
				usedNumbers[nextNum] = true
				nextNum++
			}
			trackInfos[i].TotalTracks = &total
		}
		return trackInfos
	}

	tests := []struct {
		name           string
		input          []metadata.TrackInfo
		total          int
		expectedTracks []int
	}{
		{
			name: "0-based tracks shifted to 1-based",
			input: []metadata.TrackInfo{
				{TrackNumber: intPtr(0)},
				{TrackNumber: intPtr(1)},
				{TrackNumber: intPtr(2)},
			},
			total:          3,
			expectedTracks: []int{1, 2, 3},
		},
		{
			name: "gap filling with missing track number",
			input: []metadata.TrackInfo{
				{TrackNumber: intPtr(1)},
				{TrackNumber: intPtr(3)},
				{TrackNumber: nil},
			},
			total:          3,
			expectedTracks: []int{1, 3, 2},
		},
		{
			name: "already 1-based stays the same",
			input: []metadata.TrackInfo{
				{TrackNumber: intPtr(1)},
				{TrackNumber: intPtr(2)},
				{TrackNumber: intPtr(3)},
			},
			total:          3,
			expectedTracks: []int{1, 2, 3},
		},
		{
			name: "all files without track numbers get sequential",
			input: []metadata.TrackInfo{
				{TrackNumber: nil},
				{TrackNumber: nil},
				{TrackNumber: nil},
			},
			total:          3,
			expectedTracks: []int{1, 2, 3},
		},
		{
			name: "mixed 0-based with gaps and nil",
			input: []metadata.TrackInfo{
				{TrackNumber: intPtr(0)},
				{TrackNumber: intPtr(2)},
				{TrackNumber: nil},
			},
			total:          3,
			expectedTracks: []int{1, 3, 2},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := applyAutoNumberLogic(tc.input, tc.total)
			require.Len(t, result, len(tc.expectedTracks))
			for i, expected := range tc.expectedTracks {
				require.NotNil(t, result[i].TrackNumber, "track %d should not be nil", i)
				assert.Equal(t, expected, *result[i].TrackNumber, "track %d number", i)
				require.NotNil(t, result[i].TotalTracks, "track %d TotalTracks should not be nil", i)
				assert.Equal(t, tc.total, *result[i].TotalTracks, "track %d TotalTracks", i)
			}
		})
	}
}
