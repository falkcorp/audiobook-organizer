// file: internal/server/server_write_back_test.go
// version: 1.2.1
// guid: d2e3f4a5-b6c7-8d9e-0f1a-2b3c4d5e6f7a

package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBatchWriteBackEndpoint_MissingBookIDs(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	body, _ := json.Marshal(map[string]any{"book_ids": []string{}, "rename": false})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/audiobooks/batch-write-back", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestBatchWriteBackEndpoint_ReturnsSummary(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	store := database.GetGlobalStore()
	book, err := store.CreateBook(&database.Book{
		Title:    "Batch Write-back Test",
		FilePath: filepath.Join(t.TempDir(), "test.m4b"),
		Format:   "m4b",
	})
	require.NoError(t, err)

	body, err := json.Marshal(map[string]any{
		"book_ids": []string{book.ID, "missing-id"},
		"rename":   false,
	})
	require.NoError(t, err)

	req := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/audiobooks/batch-write-back",
		bytes.NewBuffer(body),
	)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	// The endpoint is async — it returns an operation ID, not inline results
	var wrapper struct {
		Data struct {
			OperationID string `json:"operation_id"`
			Message     string `json:"message"`
			BookCount   int    `json:"book_count"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &wrapper))
	resp := wrapper.Data

	assert.NotEmpty(t, resp.OperationID)
	assert.Equal(t, 2, resp.BookCount)
	assert.Contains(t, resp.Message, "2 books")
}
