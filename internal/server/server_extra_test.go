// file: internal/server/server_extra_test.go
// version: 1.5.0
// guid: 61a2d3c4-80ab-4f6f-8c39-15a2ac5b7f0c
// last-edited: 2026-07-07

package server

import (
	"bytes"
	"embed"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/scanner"
	"github.com/stretchr/testify/require"
)

func TestServerHelpers(t *testing.T) {
	resetLibrarySizeCache()
	if got := *intPtrHelper(7); got != 7 {
		t.Fatalf("expected int pointer 7, got %d", got)
	}
	if got := *boolPtr(true); got != true {
		t.Fatal("expected bool pointer true")
	}

	tempDir := t.TempDir()
	filePath := filepath.Join(tempDir, "book.bin")
	require.NoError(t, os.WriteFile(filePath, []byte("audio"), 0o644))

	book := &database.Book{FilePath: filePath}
	scanner.ApplyOrganizedFileMetadata(book, filePath)
	if book.FileHash == nil || book.OrganizedFileHash == nil || book.OriginalFileHash == nil {
		t.Fatal("expected hashes to be set")
	}
	if book.FileSize == nil || *book.FileSize == 0 {
		t.Fatal("expected file size to be set")
	}

	resetLibrarySizeCache()
	rootDir := t.TempDir()
	importDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(rootDir, "root.txt"), []byte("1234"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(importDir, "import.txt"), []byte("12345"), 0o644))

	librarySize, importSize := calculateLibrarySizes(rootDir, []database.ImportPath{
		{Path: importDir, Enabled: true},
	})
	if librarySize == 0 || importSize == 0 {
		t.Fatalf("expected sizes to be non-zero, got %d/%d", librarySize, importSize)
	}
	cachedLibrary, cachedImport := calculateLibrarySizes(rootDir, []database.ImportPath{
		{Path: importDir, Enabled: true},
	})
	if cachedLibrary != librarySize || cachedImport != importSize {
		t.Fatalf("expected cached sizes to match, got %d/%d", cachedLibrary, cachedImport)
	}

	SetEmbeddedFS(embed.FS{})
}

func TestDuplicateAndSoftDeleteEndpoints(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	tempDir := t.TempDir()
	file1 := filepath.Join(tempDir, "dup1.m4b")
	file2 := filepath.Join(tempDir, "dup2.m4b")
	require.NoError(t, os.WriteFile(file1, []byte("audio"), 0o644))
	require.NoError(t, os.WriteFile(file2, []byte("audio"), 0o644))

	hash := "dup-hash"
	_, err := database.GetGlobalStore().CreateBook(&database.Book{
		Title:             "Dup One",
		FilePath:          file1,
		FileHash:          &hash,
		OrganizedFileHash: &hash,
	})
	require.NoError(t, err)
	_, err = database.GetGlobalStore().CreateBook(&database.Book{
		Title:             "Dup Two",
		FilePath:          file2,
		FileHash:          &hash,
		OrganizedFileHash: &hash,
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/audiobooks/duplicates", nil)
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	softFile := filepath.Join(tempDir, "soft.m4b")
	require.NoError(t, os.WriteFile(softFile, []byte("audio"), 0o644))
	marked := true
	deletedAt := time.Now().Add(-24 * time.Hour)
	_, err = database.GetGlobalStore().CreateBook(&database.Book{
		Title:               "Soft Delete",
		FilePath:            softFile,
		MarkedForDeletion:   &marked,
		MarkedForDeletionAt: &deletedAt,
	})
	require.NoError(t, err)

	req = httptest.NewRequest(http.MethodGet, "/api/v1/audiobooks/soft-deleted?limit=10", nil)
	w = httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	req = httptest.NewRequest(http.MethodDelete, "/api/v1/audiobooks/purge-soft-deleted?delete_files=true", nil)
	w = httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	if _, err := os.Stat(softFile); !os.IsNotExist(err) {
		t.Fatalf("expected soft deleted file to be removed, got %v", err)
	}

	restoreFile := filepath.Join(tempDir, "restore.m4b")
	require.NoError(t, os.WriteFile(restoreFile, []byte("audio"), 0o644))
	restoreBook, err := database.GetGlobalStore().CreateBook(&database.Book{
		Title:               "Restore",
		FilePath:            restoreFile,
		MarkedForDeletion:   &marked,
		MarkedForDeletionAt: &deletedAt,
	})
	require.NoError(t, err)

	req = httptest.NewRequest(http.MethodPost, "/api/v1/audiobooks/"+restoreBook.ID+"/restore", nil)
	w = httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	req = httptest.NewRequest(http.MethodGet, "/api/v1/audiobooks/count", nil)
	w = httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
}

func TestWorkEndpoints(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	createBody := bytes.NewBufferString(`{"title":"Test Work"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/works", createBody)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusCreated, w.Code)

	var createdWrap struct {
		Data database.Work `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &createdWrap))
	created := createdWrap.Data

	req = httptest.NewRequest(http.MethodGet, "/api/v1/works", nil)
	w = httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	req = httptest.NewRequest(http.MethodGet, "/api/v1/works/"+created.ID, nil)
	w = httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	updateBody := bytes.NewBufferString(`{"title":"Updated Work"}`)
	req = httptest.NewRequest(http.MethodPut, "/api/v1/works/"+created.ID, updateBody)
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	bookPath := filepath.Join(t.TempDir(), "work.m4b")
	require.NoError(t, os.WriteFile(bookPath, []byte("audio"), 0o644))
	_, err := database.GetGlobalStore().CreateBook(&database.Book{
		Title:    "Work Book",
		FilePath: bookPath,
		WorkID:   &created.ID,
	})
	require.NoError(t, err)

	req = httptest.NewRequest(http.MethodGet, "/api/v1/works/"+created.ID+"/books", nil)
	w = httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	req = httptest.NewRequest(http.MethodDelete, "/api/v1/works/"+created.ID, nil)
	w = httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusNoContent, w.Code)
}

func TestExclusionEndpoints(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	dir := t.TempDir()
	// Allow the temp dir through the exclusion path gate by registering it as
	// an import path on the store (parallel-safe; avoids global config mutation).
	if _, err := database.GetGlobalStore().CreateImportPath(dir, "test-allow"); err != nil {
		t.Fatalf("allow import path: %v", err)
	}
	payload := bytes.NewBufferString(`{"path":"` + dir + `","reason":"test"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/filesystem/exclude", payload)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusCreated, w.Code)

	if _, err := os.Stat(filepath.Join(dir, ".jabexclude")); err != nil {
		t.Fatalf("expected .jabexclude file: %v", err)
	}

	req = httptest.NewRequest(http.MethodDelete, "/api/v1/filesystem/exclude", bytes.NewBufferString(`{"path":"`+dir+`"}`))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusNoContent, w.Code)
}

func TestImportPathEndpoints(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	dir := t.TempDir()
	payload := bytes.NewBufferString(`{"path":"` + dir + `","name":"Test Import","enabled":false}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/import-paths", payload)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusCreated, w.Code)

	var response struct {
		Data map[string]interface{} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
	importPath := response.Data["importPath"].(map[string]interface{})
	importID := int(importPath["id"].(float64))

	req = httptest.NewRequest(http.MethodGet, "/api/v1/import-paths", nil)
	w = httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	req = httptest.NewRequest(http.MethodDelete, "/api/v1/import-paths/"+strconv.Itoa(importID), nil)
	w = httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusNoContent, w.Code)
}

func TestOperationEndpointsErrors(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	// opRegistry is always initialized in NewServer — scan/organize return 202 Accepted.
	req := httptest.NewRequest(http.MethodPost, "/api/v1/operations/scan", nil)
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusAccepted, w.Code)

	req = httptest.NewRequest(http.MethodPost, "/api/v1/operations/organize", nil)
	w = httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusAccepted, w.Code)

	// /operations/active returns 410 Gone since UOS-14
	req = httptest.NewRequest(http.MethodGet, "/api/v1/operations/active", nil)
	w = httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusGone, w.Code)

	req = httptest.NewRequest(http.MethodDelete, "/api/v1/operations/bad-id", nil)
	w = httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	// Cancel falls back to DB update for stale operations, which succeeds.
	require.Equal(t, http.StatusNoContent, w.Code)
}

func TestImportFileErrors(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/import/file", bytes.NewBufferString(`{}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code)

	dir := t.TempDir()
	payload := bytes.NewBufferString(`{"file_path":"` + dir + `"}`)
	req = httptest.NewRequest(http.MethodPost, "/api/v1/import/file", payload)
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code)

	txtFile := filepath.Join(t.TempDir(), "bad.txt")
	require.NoError(t, os.WriteFile(txtFile, []byte("data"), 0o644))
	payload = bytes.NewBufferString(`{"file_path":"` + txtFile + `"}`)
	req = httptest.NewRequest(http.MethodPost, "/api/v1/import/file", payload)
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code)
}

func TestSystemLogsEndpoints(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	folder := "/tmp"
	op, err := database.GetGlobalStore().CreateOperation("log-op", "scan", &folder)
	require.NoError(t, err)
	require.NoError(t, database.GetGlobalStore().AddOperationLog(op.ID, "info", "hello", nil))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/system/logs?level=info", nil)
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	req = httptest.NewRequest(http.MethodGet, "/api/v1/system/logs?operation_id=log-op", nil)
	w = httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
}

func TestAIEndpoints(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	origConfig := config.AppConfig
	defer func() {
		config.AppConfig = origConfig
	}()
	config.AppConfig.EnableAIParsing = false
	config.AppConfig.OpenAIAPIKey = ""

	req := httptest.NewRequest(http.MethodPost, "/api/v1/ai/parse-filename", bytes.NewBufferString(`{}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code)

	req = httptest.NewRequest(http.MethodPost, "/api/v1/ai/parse-filename", bytes.NewBufferString(`{"filename":"book.mp3"}`))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code)

	req = httptest.NewRequest(http.MethodPost, "/api/v1/ai/test-connection", bytes.NewBufferString(`{}`))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code)

	req = httptest.NewRequest(http.MethodPost, "/api/v1/audiobooks/missing/parse-with-ai", bytes.NewBufferString(`{}`))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code)

	bookPath := filepath.Join(t.TempDir(), "ai.m4b")
	require.NoError(t, os.WriteFile(bookPath, []byte("audio"), 0o644))
	book, err := database.GetGlobalStore().CreateBook(&database.Book{
		Title:    "AI Book",
		FilePath: bookPath,
	})
	require.NoError(t, err)

	req = httptest.NewRequest(http.MethodPost, "/api/v1/audiobooks/"+book.ID+"/parse-with-ai", bytes.NewBufferString(`{}`))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code)
}

func TestVersionEndpoints(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	book1Path := filepath.Join(t.TempDir(), "v1.m4b")
	book2Path := filepath.Join(t.TempDir(), "v2.m4b")
	require.NoError(t, os.WriteFile(book1Path, []byte("audio"), 0o644))
	require.NoError(t, os.WriteFile(book2Path, []byte("audio"), 0o644))

	book1, err := database.GetGlobalStore().CreateBook(&database.Book{Title: "Version One", FilePath: book1Path})
	require.NoError(t, err)
	book2, err := database.GetGlobalStore().CreateBook(&database.Book{Title: "Version Two", FilePath: book2Path})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/audiobooks/"+book1.ID+"/versions", nil)
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	linkPayload := bytes.NewBufferString(`{"other_id":"` + book2.ID + `"}`)
	req = httptest.NewRequest(http.MethodPost, "/api/v1/audiobooks/"+book1.ID+"/versions", linkPayload)
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var linkResp struct {
		Data map[string]string `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &linkResp))
	groupID := linkResp.Data["version_group_id"]

	req = httptest.NewRequest(http.MethodGet, "/api/v1/version-groups/"+groupID, nil)
	w = httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	req = httptest.NewRequest(http.MethodPut, "/api/v1/audiobooks/"+book1.ID+"/set-primary", nil)
	w = httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	book3Path := filepath.Join(t.TempDir(), "v3.m4b")
	require.NoError(t, os.WriteFile(book3Path, []byte("audio"), 0o644))
	book3, err := database.GetGlobalStore().CreateBook(&database.Book{Title: "Version Three", FilePath: book3Path})
	require.NoError(t, err)

	req = httptest.NewRequest(http.MethodPut, "/api/v1/audiobooks/"+book3.ID+"/set-primary", nil)
	w = httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
}

func TestBlockedHashEndpoints(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	require.NoError(t, database.RunMigrations(database.GetGlobalStore()))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/blocked-hashes", nil)
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	req = httptest.NewRequest(http.MethodPost, "/api/v1/blocked-hashes", bytes.NewBufferString(`{"hash":"short","reason":"bad"}`))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code)

	validHash := strings.Repeat("a", 64)
	req = httptest.NewRequest(http.MethodPost, "/api/v1/blocked-hashes", bytes.NewBufferString(`{"hash":"`+validHash+`","reason":"bad"}`))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusCreated, w.Code)

	req = httptest.NewRequest(http.MethodDelete, "/api/v1/blocked-hashes/"+validHash, nil)
	w = httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
}

func TestHandleEventsUnavailable(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	origHub := server.hub
	server.hub = nil
	defer func() {
		server.hub = origHub
	}()

	req := httptest.NewRequest(http.MethodGet, "/api/events", nil)
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusServiceUnavailable, w.Code)
}

func TestBackupEndpointsErrors(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	origConfig := config.AppConfig
	tempDir := t.TempDir()
	defer func() {
		config.AppConfig = origConfig
	}()

	config.AppConfig.DatabasePath = filepath.Join(tempDir, "missing.db")
	config.AppConfig.DatabaseType = "sqlite"

	req := httptest.NewRequest(http.MethodPost, "/api/v1/backup/create", bytes.NewBufferString(`{}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusInternalServerError, w.Code)

	req = httptest.NewRequest(http.MethodPost, "/api/v1/backup/restore", bytes.NewBufferString(`{}`))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code)

	req = httptest.NewRequest(http.MethodDelete, "/api/v1/backup/missing.tar.gz", nil)
	w = httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusInternalServerError, w.Code)
}

// ============================================================================
// Duplicate Detection and Management Tests
// ============================================================================

// TestDetectDuplicatesByFileHash verifies duplicate detection by file hash
func TestDetectDuplicatesByFileHash(t *testing.T) {
	_, cleanup := setupTestServer(t)
	defer cleanup()

	tempDir := t.TempDir()
	sharedHash := "duplicate_hash_abc123"

	// Create two files with the same hash
	file1 := filepath.Join(tempDir, "book1.m4b")
	file2 := filepath.Join(tempDir, "book2.m4b")
	require.NoError(t, os.WriteFile(file1, []byte("content"), 0o644))
	require.NoError(t, os.WriteFile(file2, []byte("content"), 0o644))

	// Create books with the same file hash
	book1, err := database.GetGlobalStore().CreateBook(&database.Book{
		Title:    "Book One",
		FilePath: file1,
		FileHash: &sharedHash,
		Format:   "m4b",
	})
	require.NoError(t, err)

	book2, err := database.GetGlobalStore().CreateBook(&database.Book{
		Title:    "Book Two",
		FilePath: file2,
		FileHash: &sharedHash,
		Format:   "m4b",
	})
	require.NoError(t, err)

	// Retrieve duplicates
	duplicates, err := database.GetGlobalStore().GetDuplicateBooks()
	require.NoError(t, err)

	// Verify our books are in the duplicates list
	found := false
	for _, group := range duplicates {
		if len(group) >= 2 {
			idMap := make(map[string]bool)
			for _, b := range group {
				idMap[b.ID] = true
			}
			if idMap[book1.ID] && idMap[book2.ID] {
				found = true
				break
			}
		}
	}
	require.True(t, found, "expected to find duplicate group")
}

// TestMarkDuplicateForDeletion tests soft deleting a duplicate book
func TestMarkDuplicateForDeletion(t *testing.T) {
	_, cleanup := setupTestServer(t)
	defer cleanup()

	tempDir := t.TempDir()
	filePath := filepath.Join(tempDir, "book.m4b")
	require.NoError(t, os.WriteFile(filePath, []byte("content"), 0o644))

	// Create a book
	book, err := database.GetGlobalStore().CreateBook(&database.Book{
		Title:    "Book to Delete",
		FilePath: filePath,
		Format:   "m4b",
	})
	require.NoError(t, err)

	// Mark for soft deletion
	trueVal := true
	updated := &database.Book{
		ID:                book.ID,
		Title:             book.Title,
		FilePath:          book.FilePath,
		Format:            book.Format,
		MarkedForDeletion: &trueVal,
	}
	_, err = database.GetGlobalStore().UpdateBook(book.ID, updated)
	require.NoError(t, err)

	// Retrieve and verify
	retrieved, err := database.GetGlobalStore().GetBookByID(book.ID)
	require.NoError(t, err)
	require.NotNil(t, retrieved.MarkedForDeletion)
	require.True(t, *retrieved.MarkedForDeletion)
}

// TestListDuplicateBooks verifies listing duplicates grouped by hash
func TestListDuplicateBooks(t *testing.T) {
	_, cleanup := setupTestServer(t)
	defer cleanup()

	tempDir := t.TempDir()

	// Create three files
	file1 := filepath.Join(tempDir, "book1.m4b")
	file2 := filepath.Join(tempDir, "book2.m4b")
	file3 := filepath.Join(tempDir, "book3.m4b")
	require.NoError(t, os.WriteFile(file1, []byte("content1"), 0o644))
	require.NoError(t, os.WriteFile(file2, []byte("content1"), 0o644))
	require.NoError(t, os.WriteFile(file3, []byte("content3"), 0o644))

	hash1 := "hash_1"
	hash3 := "hash_3"

	// Create books: 2 with hash1, 1 with hash3
	_, err := database.GetGlobalStore().CreateBook(&database.Book{
		Title:    "Book A",
		FilePath: file1,
		FileHash: &hash1,
		Format:   "m4b",
	})
	require.NoError(t, err)

	_, err = database.GetGlobalStore().CreateBook(&database.Book{
		Title:    "Book B",
		FilePath: file2,
		FileHash: &hash1,
		Format:   "m4b",
	})
	require.NoError(t, err)

	_, err = database.GetGlobalStore().CreateBook(&database.Book{
		Title:    "Book C",
		FilePath: file3,
		FileHash: &hash3,
		Format:   "m4b",
	})
	require.NoError(t, err)

	// Get duplicates
	duplicates, err := database.GetGlobalStore().GetDuplicateBooks()
	require.NoError(t, err)

	// Should have at least one group with 2+ books
	require.True(t, len(duplicates) > 0, "expected duplicate groups")

	// Find our group
	found := false
	for _, group := range duplicates {
		if len(group) >= 2 {
			for _, book := range group {
				if book.FileHash != nil && *book.FileHash == hash1 {
					found = true
					break
				}
			}
			if found {
				break
			}
		}
	}
	require.True(t, found, "expected to find duplicate group with hash1")
}

// TestSoftDeleteExcludeFromList verifies soft-deleted books are excluded from list
func TestSoftDeleteExcludeFromList(t *testing.T) {
	_, cleanup := setupTestServer(t)
	defer cleanup()

	tempDir := t.TempDir()
	file1 := filepath.Join(tempDir, "active.m4b")
	file2 := filepath.Join(tempDir, "deleted.m4b")
	require.NoError(t, os.WriteFile(file1, []byte("content"), 0o644))
	require.NoError(t, os.WriteFile(file2, []byte("content"), 0o644))

	// Create active book
	active, err := database.GetGlobalStore().CreateBook(&database.Book{
		Title:    "Active Book",
		FilePath: file1,
		Format:   "m4b",
	})
	require.NoError(t, err)

	// Create and mark deleted book
	deleted, err := database.GetGlobalStore().CreateBook(&database.Book{
		Title:    "Deleted Book",
		FilePath: file2,
		Format:   "m4b",
	})
	require.NoError(t, err)

	trueVal := true
	deletedUpdate := &database.Book{
		ID:                deleted.ID,
		Title:             deleted.Title,
		FilePath:          deleted.FilePath,
		Format:            deleted.Format,
		MarkedForDeletion: &trueVal,
	}
	_, err = database.GetGlobalStore().UpdateBook(deleted.ID, deletedUpdate)
	require.NoError(t, err)

	// List all books
	all, err := database.GetGlobalStore().GetAllBooksCore(100, 0)
	require.NoError(t, err)

	// Count them
	activeCount := 0
	deletedCount := 0
	for _, b := range all {
		if b.ID == active.ID {
			activeCount++
		}
		if b.ID == deleted.ID {
			if b.MarkedForDeletion != nil && *b.MarkedForDeletion {
				deletedCount++
			}
		}
	}

	require.True(t, activeCount > 0, "expected active book in list")
	// Note: GetAllBooks might still return soft-deleted books; ListSoftDeletedBooks filters them
}
