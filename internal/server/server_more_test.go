// file: internal/server/server_more_test.go
// version: 1.7.1
// guid: 18a6b0a3-7e78-4e0f-8b8e-0e4c1dbde6de
// last-edited: 2026-06-17

//go:build !windows

package server

import (
	"bytes"
	json "encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/require"
)

func copyFixtureToDir(t *testing.T, name, dir string) string {
	t.Helper()

	fixturePath := filepath.Join("..", "..", "testdata", "fixtures", name)
	if _, err := os.Stat(fixturePath); err != nil {
		t.Skipf("fixture missing: %v", err)
	}

	dstPath := filepath.Join(dir, name)
	src, err := os.Open(fixturePath)
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer src.Close()

	dst, err := os.Create(dstPath)
	if err != nil {
		t.Fatalf("create fixture copy: %v", err)
	}
	defer dst.Close()

	if _, err := io.Copy(dst, src); err != nil {
		t.Fatalf("copy fixture: %v", err)
	}

	return dstPath
}

func waitForOperationStatus(t *testing.T, id string, timeout time.Duration) *database.Operation {
	t.Helper()
	if id == "" {
		t.Fatal("waitForOperationStatus: empty op id (handler likely returned the v2 envelope this helper used to ignore)")
	}

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		// v2 path first — ops enqueued via opRegistry.EnqueueOp land in
		// operation_v2 (and never in the legacy ops table). Fall back
		// to the legacy ops table for endpoints that still use v1 storage.
		if row, err := database.GetGlobalStore().GetOperationV2(id); err == nil && row != nil {
			switch row.Status {
			case "completed", "failed", "canceled":
				return &database.Operation{ID: row.ID, Status: row.Status}
			}
		}
		if op, err := database.GetGlobalStore().GetOperationByID(id); err == nil && op != nil {
			switch op.Status {
			case "completed", "failed", "canceled":
				return op
			}
		}
		time.Sleep(50 * time.Millisecond)
	}

	t.Fatalf("timeout waiting for operation %s", id)
	return nil
}

func waitForQueueIdle(t *testing.T, _ *Server, _ time.Duration) {
	t.Helper()
	// v2 registry manages its own worker pool; this helper is a no-op now
	// that the v1 OperationQueue has been removed.
}

func TestUpdateConfigEndpoint(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	update := map[string]any{
		"root_dir":          "/tmp/library",
		"playlist_dir":      "/tmp/playlists",
		"openai_api_key":    "secret-key",
		"enable_ai_parsing": true,
		"concurrent_scans":  2,
		"language":          "en",
		"log_level":         "debug",
	}
	body, err := json.Marshal(update)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPut, "/api/v1/config", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	badBody, err := json.Marshal(map[string]any{"database_type": "pebble"})
	require.NoError(t, err)
	req = httptest.NewRequest(http.MethodPut, "/api/v1/config", bytes.NewReader(badBody))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code)
}

func TestImportMetadataEndpoint(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/metadata/import", strings.NewReader("{"))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code)

	badPayload := `{"data":{"books":"invalid"}}`
	req = httptest.NewRequest(http.MethodPost, "/api/v1/metadata/import", strings.NewReader(badPayload))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusPartialContent, w.Code)

	goodPayload := map[string]any{
		"data": map[string]any{
			"books": []map[string]any{
				{
					"title":     "Imported Book",
					"file_path": "/tmp/imported.m4b",
					"duration":  123,
				},
			},
		},
	}
	body, err := json.Marshal(goodPayload)
	require.NoError(t, err)
	req = httptest.NewRequest(http.MethodPost, "/api/v1/metadata/import", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
}

func TestSearchAndFetchMetadata(t *testing.T) {
	server, cleanup := setupTestServer(t)
	useOnlyOpenLibrary(t)
	defer cleanup()

	mux := http.NewServeMux()
	mux.HandleFunc("/search.json", func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"numFound": 1,
			"start":    0,
			"docs": []map[string]any{
				{
					"title":              "Test Book",
					"author_name":        []string{"Test Author"},
					"first_publish_year": 2020,
					"isbn":               []string{"1234567890"},
					"publisher":          []string{"Test Publisher"},
					"language":           []string{"eng"},
				},
			},
		}
		_ = json.MarshalWrite(w, resp)
	})
	openLibrary := httptest.NewServer(mux)
	defer openLibrary.Close()
	t.Setenv("OPENLIBRARY_BASE_URL", openLibrary.URL)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/metadata/search", nil)
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code)

	req = httptest.NewRequest(http.MethodGet, "/api/v1/metadata/search?title=Test", nil)
	w = httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	book, err := database.GetGlobalStore().CreateBook(&database.Book{
		Title:    "Test Book",
		FilePath: "/tmp/test-book.m4b",
	})
	require.NoError(t, err)

	req = httptest.NewRequest(http.MethodPost, "/api/v1/audiobooks/"+book.ID+"/fetch-metadata", nil)
	w = httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
}

func TestImportFileEndpoint(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	tempDir := t.TempDir()
	filePath := copyFixtureToDir(t, "test_sample.m4b", tempDir)
	config.AppConfig.SupportedExtensions = []string{".m4b"}
	// Allow the temp dir through the import path allow-list gate by registering
	// it on the store (parallel-safe; avoids global config mutation).
	if _, err := database.GetGlobalStore().CreateImportPath(tempDir, "test-allow"); err != nil {
		t.Fatalf("allow import path: %v", err)
	}

	payload := map[string]any{
		"file_path": filePath,
		"organize":  true,
	}
	body, err := json.Marshal(payload)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/import/file", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusCreated, w.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	if opID, ok := resp["operation_id"].(string); ok && opID != "" {
		waitForOperationStatus(t, opID, 5*time.Second)
	}
}

func TestAddImportPathAutoScan(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	importDir := t.TempDir()
	copyFixtureToDir(t, "test_sample.m4b", importDir)
	config.AppConfig.SupportedExtensions = []string{".m4b"}
	config.AppConfig.ConcurrentScans = 1

	payload := map[string]any{
		"path":    importDir,
		"name":    "Test Import",
		"enabled": true,
	}
	body, err := json.Marshal(payload)
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/import-paths", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusCreated, w.Code)

	var resp struct {
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	if opID, ok := resp.Data["scan_operation_id"].(string); ok && opID != "" {
		waitForOperationStatus(t, opID, 10*time.Second)
	}

	disabledPayload := map[string]any{
		"path":    filepath.Join(importDir, "secondary"),
		"name":    "Disabled Import",
		"enabled": false,
	}
	body, err = json.Marshal(disabledPayload)
	require.NoError(t, err)
	req = httptest.NewRequest(http.MethodPost, "/api/v1/import-paths", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusCreated, w.Code)
}

func TestAddImportPathFallbackScan(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	importDir := t.TempDir()
	copyFixtureToDir(t, "test_sample.m4b", importDir)
	config.AppConfig.SupportedExtensions = []string{".m4b"}
	config.AppConfig.AutoOrganize = true
	config.AppConfig.RootDir = ""

	payload := map[string]any{
		"path":    importDir,
		"name":    "Fallback Import",
		"enabled": true,
	}
	body, err := json.Marshal(payload)
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/import-paths", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusCreated, w.Code)
}

func TestServerStartGracefulShutdown(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	config.AppConfig.PurgeSoftDeletedAfterDays = 1
	config.AppConfig.PurgeSoftDeletedDeleteFiles = false

	_, err := database.GetGlobalStore().CreateBook(&database.Book{
		Title:    "Heartbeat Book",
		FilePath: "/tmp/heartbeat.m4b",
	})
	require.NoError(t, err)
	_, err = database.GetGlobalStore().CreateImportPath(t.TempDir(), "Heartbeat Import")
	require.NoError(t, err)

	done := make(chan error, 1)
	cfg := ServerConfig{
		Host:         "127.0.0.1",
		Port:         "0",
		ReadTimeout:  1 * time.Second,
		WriteTimeout: 1 * time.Second,
		IdleTimeout:  1 * time.Second,
	}
	go func() {
		done <- server.Start(cfg)
	}()

	time.Sleep(6 * time.Second)
	_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("server Start returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for server shutdown")
	}
}

func TestWorkEndpointErrors(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/works", strings.NewReader(`{"title":""}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code)

	req = httptest.NewRequest(http.MethodPut, "/api/v1/works/missing", strings.NewReader(`{"title":""}`))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code)

	req = httptest.NewRequest(http.MethodGet, "/api/v1/works/missing", nil)
	w = httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusNotFound, w.Code)

	req = httptest.NewRequest(http.MethodDelete, "/api/v1/works/missing", nil)
	w = httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusNotFound, w.Code)
}

func TestGetOperationLogsEndpoint(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	opID := "op-logs"
	_, err := database.GetGlobalStore().CreateOperation(opID, "scan", nil)
	require.NoError(t, err)
	detail := "details"
	require.NoError(t, database.GetGlobalStore().AddOperationLog(opID, "info", "message", &detail))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/operations/"+opID+"/logs?tail=1", nil)
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
}

func TestGetAudiobookAndDashboard(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	tempDir := t.TempDir()
	filePath := copyFixtureToDir(t, "test_sample.m4b", tempDir)

	author, err := database.GetGlobalStore().CreateAuthor("Author Name")
	require.NoError(t, err)
	series, err := database.GetGlobalStore().CreateSeries("Series Name", &author.ID)
	require.NoError(t, err)

	size := int64(50 * 1024 * 1024)
	book, err := database.GetGlobalStore().CreateBook(&database.Book{
		Title:    "Dashboard Book",
		FilePath: filePath,
		FileSize: &size,
		AuthorID: &author.ID,
		SeriesID: &series.ID,
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/audiobooks/"+book.ID, nil)
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	size2 := int64(600 * 1024 * 1024)
	_, err = database.GetGlobalStore().CreateBook(&database.Book{
		Title:    "Dashboard Book 2",
		FilePath: filepath.Join(tempDir, "book2.mp3"),
		FileSize: &size2,
	})
	require.NoError(t, err)

	req = httptest.NewRequest(http.MethodGet, "/api/v1/dashboard", nil)
	w = httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
}

func TestExportMetadataEndpoint(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	_, err := database.GetGlobalStore().CreateBook(&database.Book{
		Title:    "Exported Book",
		FilePath: "/tmp/export.m4b",
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/metadata/export", nil)
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
}

func TestParseAudiobookWithAIEndpoint(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	config.AppConfig.EnableAIParsing = false
	config.AppConfig.OpenAIAPIKey = ""

	book, err := database.GetGlobalStore().CreateBook(&database.Book{
		Title:    "AI Book",
		FilePath: "/tmp/ai.m4b",
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/audiobooks/"+book.ID+"/parse-with-ai", nil)
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code)
}

func TestParseAudiobookAIPayloadSplitsAuthors(t *testing.T) {
	// Verify that the payload shape built by parseAudiobookWithAI (author_name with "&")
	// is properly split into multiple book_authors by the update service.
	server, cleanup := setupTestServer(t)
	defer cleanup()

	book, err := database.GetGlobalStore().CreateBook(&database.Book{
		Title:    "Multi Author Book",
		FilePath: "/tmp/multi.m4b",
	})
	require.NoError(t, err)

	// This is the same payload shape that parseAudiobookWithAI now builds
	payload := map[string]any{
		"title":       "Parsed Title",
		"author_name": "Author A & Author B",
		"narrator":    "Narrator X & Narrator Y",
		"series_name": "Test Series",
	}
	body, err := json.Marshal(payload)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPut, "/api/v1/audiobooks/"+book.ID, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	// Verify book_authors junction table has 2 entries
	bookAuthors, err := database.GetGlobalStore().GetBookAuthors(book.ID)
	require.NoError(t, err)
	require.Len(t, bookAuthors, 2, "expected 2 book_authors after splitting on &")

	// Verify first author
	author1, err := database.GetGlobalStore().GetAuthorByID(bookAuthors[0].AuthorID)
	require.NoError(t, err)
	require.Equal(t, "Author A", author1.Name)
	require.Equal(t, "author", bookAuthors[0].Role)

	// Verify second author
	author2, err := database.GetGlobalStore().GetAuthorByID(bookAuthors[1].AuthorID)
	require.NoError(t, err)
	require.Equal(t, "Author B", author2.Name)
	require.Equal(t, "co-author", bookAuthors[1].Role)

	// Verify book_narrators junction table has 2 entries
	bookNarrators, err := database.GetGlobalStore().GetBookNarrators(book.ID)
	require.NoError(t, err)
	require.Len(t, bookNarrators, 2, "expected 2 book_narrators after splitting on &")

	narr1, err := database.GetGlobalStore().GetNarratorByID(bookNarrators[0].NarratorID)
	require.NoError(t, err)
	require.Equal(t, "Narrator X", narr1.Name)

	narr2, err := database.GetGlobalStore().GetNarratorByID(bookNarrators[1].NarratorID)
	require.NoError(t, err)
	require.Equal(t, "Narrator Y", narr2.Name)
}

func TestUpdateDeleteBatchAudiobook(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	tempDir := t.TempDir()
	filePath := filepath.Join(tempDir, "book.m4b")
	require.NoError(t, os.WriteFile(filePath, []byte("audio"), 0o644))
	hash := "hash-1"
	book, err := database.GetGlobalStore().CreateBook(&database.Book{
		Title:    "Old Title",
		FilePath: filePath,
		FileHash: &hash,
	})
	require.NoError(t, err)

	updatePayload := map[string]any{
		"title":                  "New Title",
		"author_name":            "Author Name",
		"series_name":            "Series Name",
		"narrator":               "Narrator Name",
		"publisher":              "Publisher",
		"language":               "en",
		"audiobook_release_year": 2020,
		"isbn13":                 "1234567890123",
	}
	body, err := json.Marshal(updatePayload)
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPut, "/api/v1/audiobooks/"+book.ID, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	batchPayload := map[string]any{
		"ids": []string{book.ID},
		"updates": map[string]any{
			"title":           "Batch Title",
			"series_sequence": 1,
		},
	}
	body, err = json.Marshal(batchPayload)
	require.NoError(t, err)
	req = httptest.NewRequest(http.MethodPost, "/api/v1/audiobooks/batch", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	req = httptest.NewRequest(http.MethodDelete, "/api/v1/audiobooks/"+book.ID+"?soft_delete=true&block_hash=true", nil)
	w = httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	filePath2 := filepath.Join(tempDir, "book2.m4b")
	require.NoError(t, os.WriteFile(filePath2, []byte("audio"), 0o644))
	hash2 := "hash-2"
	book2, err := database.GetGlobalStore().CreateBook(&database.Book{
		Title:    "Hard Delete",
		FilePath: filePath2,
		FileHash: &hash2,
	})
	require.NoError(t, err)

	req = httptest.NewRequest(http.MethodDelete, "/api/v1/audiobooks/"+book2.ID+"?block_hash=true", nil)
	w = httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
}

func TestStartScanOperation(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	rootDir := t.TempDir()
	importDir := t.TempDir()
	config.AppConfig.RootDir = rootDir
	config.AppConfig.SupportedExtensions = []string{".m4b"}
	config.AppConfig.ConcurrentScans = 1
	config.AppConfig.AutoOrganize = false

	copyFixtureToDir(t, "test_sample.m4b", rootDir)
	copyFixtureToDir(t, "test_sample.m4b", importDir)

	_, err := database.GetGlobalStore().CreateImportPath(importDir, "Import")
	require.NoError(t, err)

	payload := map[string]any{
		"force_update": true,
	}
	body, err := json.Marshal(payload)
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/operations/scan", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusAccepted, w.Code)

	// startScan returns {"op_id": "...", "id": "..."} (no `data` envelope).
	// Read the flat field; the old `Data database.Operation` shape was a
	// vestige of an earlier handler version and produced an empty ID,
	// which is why this test was on the SERVER-THIN-8 baseline list.
	var resp struct {
		OpID string `json:"op_id"`
		ID   string `json:"id"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	opID := resp.OpID
	if opID == "" {
		opID = resp.ID
	}
	waitForOperationStatus(t, opID, 10*time.Second)
}

func TestStartOrganizeOperation(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	rootDir := t.TempDir()
	sourceDir := t.TempDir()
	config.AppConfig.RootDir = rootDir
	config.AppConfig.OrganizationStrategy = "copy"
	config.AppConfig.FolderNamingPattern = "{title}"
	config.AppConfig.FileNamingPattern = "{title}"
	config.AppConfig.SupportedExtensions = []string{".m4b"}
	config.AppConfig.ConcurrentScans = 1

	filePath := copyFixtureToDir(t, "test_sample.m4b", sourceDir)
	_, err := database.GetGlobalStore().CreateBook(&database.Book{
		Title:    "Organize Me",
		FilePath: filePath,
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/operations/organize", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusAccepted, w.Code)

	var resp struct {
		OpID string `json:"op_id"`
		ID   string `json:"id"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	opID := resp.OpID
	if opID == "" {
		opID = resp.ID
	}
	waitForOperationStatus(t, opID, 10*time.Second)
	waitForQueueIdle(t, server, 10*time.Second)
}

func TestListActiveOperations(t *testing.T) {
	// UOS-14: /operations/active is removed (410 Gone); use /operations/timeline.
	server, cleanup := setupTestServer(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/operations/active", nil)
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusGone, w.Code)
}

func TestRunAutoPurgeSoftDeleted(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	config.AppConfig.PurgeSoftDeletedAfterDays = 1
	config.AppConfig.PurgeSoftDeletedDeleteFiles = true

	tempDir := t.TempDir()
	filePath := filepath.Join(tempDir, "old.m4b")
	require.NoError(t, os.WriteFile(filePath, []byte("audio"), 0o644))
	marked := true
	deletedAt := time.Now().AddDate(0, 0, -2)
	book, err := database.GetGlobalStore().CreateBook(&database.Book{
		Title:               "Old Book",
		FilePath:            filePath,
		MarkedForDeletion:   &marked,
		MarkedForDeletionAt: &deletedAt,
	})
	require.NoError(t, err)

	server.runAutoPurgeSoftDeleted("")

	if got, err := database.GetGlobalStore().GetBookByID(book.ID); err == nil && got != nil {
		t.Fatal("expected book to be purged")
	}
	if _, err := os.Stat(filePath); !os.IsNotExist(err) {
		t.Fatalf("expected file to be removed, got %v", err)
	}
}
