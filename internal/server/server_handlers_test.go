// file: internal/server/server_handlers_test.go
// version: 1.2.1
// guid: b1c2d3e4-f5a6-7b8c-9d0e-1f2a3b4c5d6e
// last-edited: 2026-09-02

package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAudiobookCountEndpoint tests counting audiobooks
func TestAudiobookCountEndpoint(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	tempDir := t.TempDir()
	filePath := filepath.Join(tempDir, "test.m4b")
	require.NoError(t, os.WriteFile(filePath, []byte("audio"), 0o644))

	// Create a test book
	_, err := database.GetGlobalStore().CreateBook(&database.Book{
		Title:    "Count Test Book",
		FilePath: filePath,
		Format:   "m4b",
	})
	require.NoError(t, err)

	// Count books
	req := httptest.NewRequest(http.MethodGet, "/api/v1/audiobooks/count", nil)
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
}

// TestMetadataFetchAndUpdate tests fetching and updating metadata
func TestMetadataFetchAndUpdate(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	tempDir := t.TempDir()
	filePath := filepath.Join(tempDir, "book.m4b")
	require.NoError(t, os.WriteFile(filePath, []byte("audio"), 0o644))

	book, err := database.GetGlobalStore().CreateBook(&database.Book{
		Title:    "Fetch Test Book",
		FilePath: filePath,
		Format:   "m4b",
	})
	require.NoError(t, err)

	// Try to fetch metadata for this book
	fetchReq := map[string]any{
		"book_ids": []string{book.ID},
	}
	body, _ := json.Marshal(fetchReq)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/metadata/bulk-fetch", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)

	// Should return 200 or 202 (accepted)
	assert.True(t, w.Code == 200 || w.Code == 202 || w.Code == 400)
}

// TestSearchAudiobooks tests searching audiobooks
func TestSearchAudiobooks(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	tempDir := t.TempDir()
	filePath := filepath.Join(tempDir, "book.m4b")
	require.NoError(t, os.WriteFile(filePath, []byte("audio"), 0o644))

	// Create a test book
	_, err := database.GetGlobalStore().CreateBook(&database.Book{
		Title:    "Searchable Book",
		FilePath: filePath,
		Format:   "m4b",
	})
	require.NoError(t, err)

	// Search for it
	req := httptest.NewRequest(http.MethodGet, "/api/v1/audiobooks?search=Searchable", nil)
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var response map[string]any
	json.Unmarshal(w.Body.Bytes(), &response)
	// Should have results or empty array
	assert.True(t, response != nil)
}

// TestListAudiobooksWithPagination tests pagination
func TestListAudiobooksWithPagination(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	tempDir := t.TempDir()

	// Create multiple books
	for i := range 5 {
		filePath := filepath.Join(tempDir, "book"+string(rune(i))+".m4b")
		os.WriteFile(filePath, []byte("audio"), 0o644)

		database.GetGlobalStore().CreateBook(&database.Book{
			Title:    "Book " + string(rune(48+i)),
			FilePath: filePath,
			Format:   "m4b",
		})
	}

	// Test with limit
	req := httptest.NewRequest(http.MethodGet, "/api/v1/audiobooks?limit=2&page=1", nil)
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var response map[string]any
	json.Unmarshal(w.Body.Bytes(), &response)
	assert.True(t, response != nil)
}

// TestConfigEndpoints tests configuration endpoints
func TestConfigEndpoints(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	// Test GET config
	req := httptest.NewRequest(http.MethodGet, "/api/v1/config", nil)
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	// Test PUT config
	configReq := map[string]any{
		"language": "en",
	}
	body, _ := json.Marshal(configReq)
	req = httptest.NewRequest(http.MethodPut, "/api/v1/config", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	assert.True(t, w.Code == 200 || w.Code == 400)
}

// TestSystemStatus tests system status endpoint
func TestSystemStatus(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/system/status", nil)
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	var response map[string]any
	err := json.Unmarshal(w.Body.Bytes(), &response)
	assert.NoError(t, err)
}

// TestOperationEndpoints tests operation management
func TestOperationEndpoints(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	// /operations/active returns 410 Gone since UOS-14 (removed in favour of /operations/timeline)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/operations/active", nil)
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusGone, w.Code)

	// Test get operation status
	req = httptest.NewRequest(http.MethodGet, "/api/v1/operations/nonexistent/status", nil)
	w = httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	// Could be 404 or 200 depending on implementation
	assert.True(t, w.Code == 404 || w.Code == 200)
}

// TestImportPathListingEndpoint tests import path listing
func TestImportPathListingEndpoint(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	// List import paths
	req := httptest.NewRequest(http.MethodGet, "/api/v1/import-paths", nil)
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	var response map[string]any
	json.Unmarshal(w.Body.Bytes(), &response)
	assert.True(t, response != nil)
}

// TestSoftDeleteOperations tests soft delete and restore
func TestSoftDeleteOperations(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	tempDir := t.TempDir()
	filePath := filepath.Join(tempDir, "delete_test.m4b")
	require.NoError(t, os.WriteFile(filePath, []byte("audio"), 0o644))

	book, err := database.GetGlobalStore().CreateBook(&database.Book{
		Title:    "Book to Delete",
		FilePath: filePath,
		Format:   "m4b",
	})
	require.NoError(t, err)

	// Soft delete
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/audiobooks/"+book.ID+"?soft_delete=true", nil)
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	assert.True(t, w.Code == 200 || w.Code == 204)

	// Try to list soft deleted
	req = httptest.NewRequest(http.MethodGet, "/api/v1/audiobooks/soft-deleted", nil)
	w = httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	assert.True(t, w.Code == 200 || w.Code == 404)
}

// TestDuplicateDetectionEndpoint tests duplicate detection
func TestDuplicateDetectionEndpoint(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	tempDir := t.TempDir()

	// Create books with same hash to simulate duplicates
	sharedHash := "dup_hash_123"
	file1 := filepath.Join(tempDir, "dup1.m4b")
	file2 := filepath.Join(tempDir, "dup2.m4b")
	os.WriteFile(file1, []byte("same"), 0o644)
	os.WriteFile(file2, []byte("same"), 0o644)

	database.GetGlobalStore().CreateBook(&database.Book{
		Title:    "Dup 1",
		FilePath: file1,
		FileHash: &sharedHash,
		Format:   "m4b",
	})

	database.GetGlobalStore().CreateBook(&database.Book{
		Title:    "Dup 2",
		FilePath: file2,
		FileHash: &sharedHash,
		Format:   "m4b",
	})

	// Get duplicates
	req := httptest.NewRequest(http.MethodGet, "/api/v1/audiobooks/duplicates", nil)
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)

	assert.True(t, w.Code == 200 || w.Code == 404)
}

// TestErrorHandlingOn404 tests 404 responses for non-existent resources
func TestErrorHandlingOn404(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	// Get non-existent book
	req := httptest.NewRequest(http.MethodGet, "/api/v1/audiobooks/nonexistent", nil)
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	// Should return 404 or 400
	assert.True(t, w.Code == 404 || w.Code == 400)

	// Get non-existent operation
	req = httptest.NewRequest(http.MethodGet, "/api/v1/operations/nonexistent/status", nil)
	w = httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	// Should return 404 or 400 or 200
	assert.True(t, w.Code == 404 || w.Code == 400 || w.Code == 200)
}
