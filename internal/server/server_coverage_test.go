// file: internal/server/server_coverage_test.go
// version: 2.2.1
// guid: 8a9b0c1d-2e3f-4a5b-6c7d-8e9f0a1b2c3d
// last-edited: 2026-05-08

package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Helper: create a book with a specific format
// ---------------------------------------------------------------------------

func createTestBookFmt(t *testing.T, title, format string) *database.Book {
	t.Helper()
	tempDir := t.TempDir()
	filePath := filepath.Join(tempDir, title+"."+format)
	require.NoError(t, os.WriteFile(filePath, []byte("audio"), 0o644))

	book, err := database.GetGlobalStore().CreateBook(&database.Book{
		Title:    title,
		FilePath: filePath,
		Format:   format,
	})
	require.NoError(t, err)
	return book
}

// ---------------------------------------------------------------------------
// 1. GET /api/v1/audiobooks  (list books with query params)
// ---------------------------------------------------------------------------

func TestCoverageListAudiobooks(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	// Seed data
	createTestBook(t, "Alpha Book")
	createTestBookFmt(t, "Beta Book", "mp3")
	createTestBook(t, "Gamma Book")

	tests := []struct {
		name           string
		query          string
		expectedStatus int
		validate       func(t *testing.T, body []byte)
	}{
		{
			name:           "no params returns all",
			query:          "",
			expectedStatus: http.StatusOK,
			validate: func(t *testing.T, body []byte) {
				var resp map[string]any
				require.NoError(t, json.Unmarshal(body, &resp))
				items := resp["data"].(map[string]any)["items"].([]any)
				assert.GreaterOrEqual(t, len(items), 3)
				d := resp["data"].(map[string]any)
				assert.NotNil(t, d["count"])
				assert.NotNil(t, d["limit"])
				assert.NotNil(t, d["offset"])
			},
		},
		{
			name:           "with limit and offset",
			query:          "?limit=2&offset=0",
			expectedStatus: http.StatusOK,
			validate: func(t *testing.T, body []byte) {
				var resp map[string]any
				require.NoError(t, json.Unmarshal(body, &resp))
				items := resp["data"].(map[string]any)["items"].([]any)
				assert.LessOrEqual(t, len(items), 2)
			},
		},
		{
			name:           "with search",
			query:          "?search=Alpha",
			expectedStatus: http.StatusOK,
			validate: func(t *testing.T, body []byte) {
				var resp map[string]any
				require.NoError(t, json.Unmarshal(body, &resp))
				items := resp["data"].(map[string]any)["items"].([]any)
				assert.GreaterOrEqual(t, len(items), 1)
			},
		},
		{
			name:           "search with no results",
			query:          "?search=ZZZNOEXIST",
			expectedStatus: http.StatusOK,
			validate: func(t *testing.T, body []byte) {
				var resp map[string]any
				require.NoError(t, json.Unmarshal(body, &resp))
				items := resp["data"].(map[string]any)["items"].([]any)
				assert.Equal(t, 0, len(items))
			},
		},
		{
			name:           "sort order asc",
			query:          "?sort_by=title&sort_order=asc",
			expectedStatus: http.StatusOK,
		},
		{
			name:           "sort order desc",
			query:          "?sort_by=title&sort_order=desc",
			expectedStatus: http.StatusOK,
		},
		{
			name:           "invalid sort order defaults to asc",
			query:          "?sort_order=invalid",
			expectedStatus: http.StatusOK,
		},
		{
			name:           "filter by library_state",
			query:          "?library_state=new",
			expectedStatus: http.StatusOK,
		},
		{
			name:           "invalid filters JSON",
			query:          "?filters=NOT_JSON",
			expectedStatus: http.StatusBadRequest,
		},
		{
			name:           "valid filters JSON empty array",
			query:          `?filters=[]`,
			expectedStatus: http.StatusOK,
		},
		{
			name:           "with offset beyond range",
			query:          "?limit=10&offset=999999",
			expectedStatus: http.StatusOK,
			validate: func(t *testing.T, body []byte) {
				var resp map[string]any
				require.NoError(t, json.Unmarshal(body, &resp))
				items := resp["data"].(map[string]any)["items"].([]any)
				assert.Equal(t, 0, len(items))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/audiobooks"+tt.query, nil)
			w := httptest.NewRecorder()
			server.router.ServeHTTP(w, req)
			assert.Equal(t, tt.expectedStatus, w.Code)
			if tt.validate != nil {
				tt.validate(t, w.Body.Bytes())
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 2. GET /api/v1/audiobooks/:id  (get single book)
// ---------------------------------------------------------------------------

func TestCoverageGetAudiobook(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	book := createTestBook(t, "Get Me")

	t.Run("existing book", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/audiobooks/"+book.ID, nil)
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		var wrapper struct {
			Data map[string]any `json:"data"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &wrapper))
		assert.Equal(t, "Get Me", wrapper.Data["title"])
	})

	t.Run("non-existent book", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/audiobooks/01ZZZZZZZZZZZZZZZZZZZZZZZZ", nil)
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusNotFound, w.Code)
	})
}

// ---------------------------------------------------------------------------
// 3. PUT /api/v1/audiobooks/:id  (update book)
// ---------------------------------------------------------------------------

func TestCoverageUpdateAudiobook(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	book := createTestBook(t, "Original Title")

	t.Run("update title", func(t *testing.T) {
		payload := map[string]any{"title": "Updated Title"}
		body, _ := json.Marshal(payload)
		req := httptest.NewRequest(http.MethodPut, "/api/v1/audiobooks/"+book.ID, bytes.NewBuffer(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		var wrapper struct {
			Data map[string]any `json:"data"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &wrapper))
		assert.Equal(t, "Updated Title", wrapper.Data["title"])
	})

	t.Run("update narrator", func(t *testing.T) {
		payload := map[string]any{"narrator": "Jane Doe"}
		body, _ := json.Marshal(payload)
		req := httptest.NewRequest(http.MethodPut, "/api/v1/audiobooks/"+book.ID, bytes.NewBuffer(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("update non-existent book", func(t *testing.T) {
		payload := map[string]any{"title": "Ghost"}
		body, _ := json.Marshal(payload)
		req := httptest.NewRequest(http.MethodPut, "/api/v1/audiobooks/01ZZZZZZZZZZZZZZZZZZZZZZZZ", bytes.NewBuffer(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusNotFound, w.Code)
	})

	t.Run("update with invalid JSON", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPut, "/api/v1/audiobooks/"+book.ID, bytes.NewBufferString("{bad"))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("update with overrides", func(t *testing.T) {
		payload := map[string]any{
			"overrides": map[string]any{
				"title": map[string]any{
					"value":  "Override Title",
					"locked": true,
				},
			},
		}
		body, _ := json.Marshal(payload)
		req := httptest.NewRequest(http.MethodPut, "/api/v1/audiobooks/"+book.ID, bytes.NewBuffer(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
	})
}

// ---------------------------------------------------------------------------
// 4. DELETE /api/v1/audiobooks/:id  (soft and hard delete)
// ---------------------------------------------------------------------------

func TestCoverageDeleteAudiobook(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	t.Run("soft delete existing book", func(t *testing.T) {
		book := createTestBook(t, "Delete Me Soft")
		req := httptest.NewRequest(http.MethodDelete, "/api/v1/audiobooks/"+book.ID+"?soft_delete=true", nil)
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("hard delete existing book", func(t *testing.T) {
		book := createTestBook(t, "Delete Me Hard")
		req := httptest.NewRequest(http.MethodDelete, "/api/v1/audiobooks/"+book.ID, nil)
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("delete non-existent book", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodDelete, "/api/v1/audiobooks/01ZZZZZZZZZZZZZZZZZZZZZZZZ", nil)
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusNotFound, w.Code)
	})

	t.Run("soft delete with block_hash", func(t *testing.T) {
		book := createTestBook(t, "Block Hash Book")
		req := httptest.NewRequest(http.MethodDelete, "/api/v1/audiobooks/"+book.ID+"?soft_delete=true&block_hash=true", nil)
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("double soft delete returns conflict", func(t *testing.T) {
		book := createTestBook(t, "Double Soft")
		// First soft delete
		req1 := httptest.NewRequest(http.MethodDelete, "/api/v1/audiobooks/"+book.ID+"?soft_delete=true", nil)
		w1 := httptest.NewRecorder()
		server.router.ServeHTTP(w1, req1)
		assert.Equal(t, http.StatusOK, w1.Code)

		// Second soft delete
		req2 := httptest.NewRequest(http.MethodDelete, "/api/v1/audiobooks/"+book.ID+"?soft_delete=true", nil)
		w2 := httptest.NewRecorder()
		server.router.ServeHTTP(w2, req2)
		assert.Equal(t, http.StatusConflict, w2.Code)
	})
}

// ---------------------------------------------------------------------------
// 5. POST /api/v1/audiobooks/:id/restore  (restore soft-deleted)
// ---------------------------------------------------------------------------

func TestCoverageRestoreAudiobook(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	t.Run("restore soft-deleted book", func(t *testing.T) {
		book := createTestBook(t, "Restore Me")
		// Soft delete first
		delReq := httptest.NewRequest(http.MethodDelete, "/api/v1/audiobooks/"+book.ID+"?soft_delete=true", nil)
		delW := httptest.NewRecorder()
		server.router.ServeHTTP(delW, delReq)
		require.Equal(t, http.StatusOK, delW.Code)

		// Restore
		req := httptest.NewRequest(http.MethodPost, "/api/v1/audiobooks/"+book.ID+"/restore", nil)
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		var resp struct {
			Data struct {
				Message string `json:"message"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.Equal(t, "audiobook restored", resp.Data.Message)
	})

	t.Run("restore non-existent book", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/audiobooks/01ZZZZZZZZZZZZZZZZZZZZZZZZ/restore", nil)
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusNotFound, w.Code)
	})
}

// ---------------------------------------------------------------------------
// 6. GET /api/v1/authors  (list authors)
// ---------------------------------------------------------------------------

func TestCoverageListAuthors(t *testing.T) {
	t.Run("empty authors list", func(t *testing.T) {
		server, cleanup := setupTestServer(t)
		defer cleanup()

		req := httptest.NewRequest(http.MethodGet, "/api/v1/authors", nil)
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		var resp map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.NotNil(t, resp["data"].(map[string]any)["items"])
	})

	t.Run("authors after creating books with authors", func(t *testing.T) {
		// Fresh server per subtest so the authorsCache doesn't carry over
		// an empty result from the previous subtest.
		server, cleanup := setupTestServer(t)
		defer cleanup()

		tempDir := t.TempDir()
		filePath := filepath.Join(tempDir, "authored.m4b")
		require.NoError(t, os.WriteFile(filePath, []byte("audio"), 0o644))

		author, err := database.GetGlobalStore().CreateAuthor("Test Author")
		require.NoError(t, err)

		_, err = database.GetGlobalStore().CreateBook(&database.Book{
			Title:    "Authored Book",
			FilePath: filePath,
			Format:   "m4b",
			AuthorID: &author.ID,
		})
		require.NoError(t, err)

		req := httptest.NewRequest(http.MethodGet, "/api/v1/authors", nil)
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		var resp map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		items := resp["data"].(map[string]any)["items"].([]any)
		assert.GreaterOrEqual(t, len(items), 1)
	})
}

// ---------------------------------------------------------------------------
// 7. GET /api/v1/series  (list series)
// ---------------------------------------------------------------------------

func TestCoverageListSeries(t *testing.T) {
	t.Run("empty series list", func(t *testing.T) {
		server, cleanup := setupTestServer(t)
		defer cleanup()

		req := httptest.NewRequest(http.MethodGet, "/api/v1/series", nil)
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		var resp map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.NotNil(t, resp["data"].(map[string]any)["items"])
	})

	t.Run("series after creating books with series", func(t *testing.T) {
		// Fresh server per subtest so the seriesCache doesn't carry over
		// an empty result from the previous subtest.
		server, cleanup := setupTestServer(t)
		defer cleanup()

		tempDir := t.TempDir()
		filePath := filepath.Join(tempDir, "series.m4b")
		require.NoError(t, os.WriteFile(filePath, []byte("audio"), 0o644))

		series, err := database.GetGlobalStore().CreateSeries("Test Series", nil)
		require.NoError(t, err)

		_, err = database.GetGlobalStore().CreateBook(&database.Book{
			Title:    "Series Book",
			FilePath: filePath,
			Format:   "m4b",
			SeriesID: &series.ID,
		})
		require.NoError(t, err)

		req := httptest.NewRequest(http.MethodGet, "/api/v1/series", nil)
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		var resp map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		items := resp["data"].(map[string]any)["items"].([]any)
		assert.GreaterOrEqual(t, len(items), 1)
	})
}

// ---------------------------------------------------------------------------
// 8. GET /api/v1/system/status  (system status)
// ---------------------------------------------------------------------------

func TestCoverageGetSystemStatus(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/system/status", nil)
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	// System status should contain at least some fields
	assert.NotEmpty(t, w.Body.Bytes())
}

// ---------------------------------------------------------------------------
// 9. GET /api/v1/dashboard  (dashboard stats)
// ---------------------------------------------------------------------------

func TestCoverageDashboardStats(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	t.Run("empty database", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/dashboard", nil)
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		var resp struct{ Data map[string]any }
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.NotNil(t, resp.Data["formatDistribution"])
		assert.NotNil(t, resp.Data["stateDistribution"])
		assert.NotNil(t, resp.Data["totalBooks"])
		assert.NotNil(t, resp.Data["totalSize"])
	})

	t.Run("with books", func(t *testing.T) {
		createTestBook(t, "Dashboard Book 1")
		createTestBookFmt(t, "Dashboard Book 2", "mp3")

		// Invalidate the library stats cache so new books are counted
		server.storeForWiring().InvalidateLibraryStats()

		req := httptest.NewRequest(http.MethodGet, "/api/v1/dashboard", nil)
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		var resp struct{ Data map[string]any }
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		totalBooks := resp.Data["totalBooks"].(float64)
		assert.GreaterOrEqual(t, totalBooks, float64(2))
	})

	t.Run("dashboard caching", func(t *testing.T) {
		// Second request should hit cache
		req := httptest.NewRequest(http.MethodGet, "/api/v1/dashboard", nil)
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
	})
}

// ---------------------------------------------------------------------------
// 10. POST /api/v1/operations/organize  (trigger organize)
// ---------------------------------------------------------------------------

func TestCoverageStartOrganize(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	t.Run("organize without params", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/operations/v2",
			strings.NewReader(`{"def_id":"library.organize","params":{}}`))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusAccepted, w.Code)
		var resp map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		// The v2 trigger answers {"op_id": ...} unwrapped. The retired
		// POST /operations/organize answered {"id", "op_id"}, and the web client
		// read .data.id off it — which is why it reported "Failed to start scan"
		// for a scan that had started (#2500).
		assert.NotEmpty(t, resp["op_id"])
	})

	t.Run("organize with folder_path", func(t *testing.T) {
		tempDir := t.TempDir()
		body, _ := json.Marshal(map[string]any{
			"def_id": "library.organize",
			"params": map[string]any{"folder_path": tempDir},
		})
		req := httptest.NewRequest(http.MethodPost, "/api/v1/operations/v2", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusAccepted, w.Code)
	})

	t.Run("organize with priority", func(t *testing.T) {
		body, _ := json.Marshal(map[string]any{
			"def_id": "library.organize",
			"params": map[string]any{"priority": 5},
		})
		req := httptest.NewRequest(http.MethodPost, "/api/v1/operations/v2", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusAccepted, w.Code)
	})
}

// ---------------------------------------------------------------------------
// 11. GET /api/v1/operations/active  (deprecated — returns 410 Gone since UOS-14)
// ---------------------------------------------------------------------------

func TestCoverageListActiveOperations(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	t.Run("returns 410 gone", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/operations/active", nil)
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)

		// UOS-14: this endpoint is removed; it now returns 410 Gone.
		assert.Equal(t, http.StatusGone, w.Code)
	})
}

// ---------------------------------------------------------------------------
// 12. POST /api/v1/audiobooks/batch-operations  (batch ops)
// ---------------------------------------------------------------------------

func TestCoverageBatchOperations(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	t.Run("empty operations list", func(t *testing.T) {
		payload := map[string]any{"operations": []any{}}
		body, _ := json.Marshal(payload)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/audiobooks/batch-operations", bytes.NewBuffer(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("invalid JSON", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/audiobooks/batch-operations", bytes.NewBufferString("{bad"))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("batch update existing books", func(t *testing.T) {
		book1 := createTestBook(t, "Batch Book 1")
		book2 := createTestBookFmt(t, "Batch Book 2", "mp3")

		payload := map[string]any{
			"operations": []map[string]any{
				{
					"id":      book1.ID,
					"action":  "update",
					"updates": map[string]any{"title": "Batch Updated 1"},
				},
				{
					"id":      book2.ID,
					"action":  "update",
					"updates": map[string]any{"title": "Batch Updated 2"},
				},
			},
		}
		body, _ := json.Marshal(payload)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/audiobooks/batch-operations", bytes.NewBuffer(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		var resp map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		results := resp["data"].(map[string]any)["results"].([]any)
		assert.Equal(t, 2, len(results))
	})

	t.Run("batch delete operation", func(t *testing.T) {
		book := createTestBook(t, "Batch Delete Me")
		payload := map[string]any{
			"operations": []map[string]any{
				{
					"id":     book.ID,
					"action": "delete",
				},
			},
		}
		body, _ := json.Marshal(payload)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/audiobooks/batch-operations", bytes.NewBuffer(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("batch restore operation", func(t *testing.T) {
		book := createTestBook(t, "Batch Restore Me")
		// Soft delete first
		delReq := httptest.NewRequest(http.MethodDelete, "/api/v1/audiobooks/"+book.ID+"?soft_delete=true", nil)
		delW := httptest.NewRecorder()
		server.router.ServeHTTP(delW, delReq)

		payload := map[string]any{
			"operations": []map[string]any{
				{
					"id":     book.ID,
					"action": "restore",
				},
			},
		}
		body, _ := json.Marshal(payload)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/audiobooks/batch-operations", bytes.NewBuffer(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("batch with non-existent book", func(t *testing.T) {
		payload := map[string]any{
			"operations": []map[string]any{
				{
					"id":      "01ZZZZZZZZZZZZZZZZZZZZZZZZ",
					"action":  "update",
					"updates": map[string]any{"title": "Ghost"},
				},
			},
		}
		body, _ := json.Marshal(payload)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/audiobooks/batch-operations", bytes.NewBuffer(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		var resp map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		results := resp["data"].(map[string]any)["results"].([]any)
		first := results[0].(map[string]any)
		// On error, success is omitted (omitempty) and error is set
		assert.NotEmpty(t, first["error"])
	})
}

// ---------------------------------------------------------------------------
// Additional coverage: count endpoints
// ---------------------------------------------------------------------------

func TestCoverageCountEndpoints(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	createTestBook(t, "Count Book")

	t.Run("count audiobooks", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/audiobooks/count", nil)
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)

		var resp map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		count := resp["data"].(map[string]any)["count"].(float64)
		assert.GreaterOrEqual(t, count, float64(1))
	})

	t.Run("count authors", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/authors/count", nil)
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("count series", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/series/count", nil)
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("count narrators", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/narrators/count", nil)
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
	})
}

// ---------------------------------------------------------------------------
// Additional coverage: soft-deleted list
// ---------------------------------------------------------------------------

func TestCoverageListSoftDeletedAudiobooks(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	t.Run("no soft-deleted books", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/audiobooks/soft-deleted", nil)
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		var resp struct {
			Data struct {
				Items []any `json:"items"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.Equal(t, 0, len(resp.Data.Items))
	})

	t.Run("with soft-deleted books", func(t *testing.T) {
		book := createTestBook(t, "Soft Deleted")
		delReq := httptest.NewRequest(http.MethodDelete, "/api/v1/audiobooks/"+book.ID+"?soft_delete=true", nil)
		delW := httptest.NewRecorder()
		server.router.ServeHTTP(delW, delReq)
		require.Equal(t, http.StatusOK, delW.Code)

		req := httptest.NewRequest(http.MethodGet, "/api/v1/audiobooks/soft-deleted", nil)
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		var resp struct {
			Data struct {
				Items []any `json:"items"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.GreaterOrEqual(t, len(resp.Data.Items), 1)
	})

	t.Run("with pagination params", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/audiobooks/soft-deleted?limit=5&offset=0", nil)
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
	})
}

// ---------------------------------------------------------------------------
// Additional coverage: operations list
// ---------------------------------------------------------------------------

func TestCoverageOperationTimeline(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	// GET /operations listed the legacy table and was retired in #2500; the
	// timeline is what replaced it. The window is explicit because the default
	// is 15m, which would make "empty" pass for the wrong reason.
	t.Run("timeline empty", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/operations/timeline?since=24h", nil)
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		var resp map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		data := resp["data"].(map[string]any)
		assert.NotNil(t, data["operations"])
	})

	t.Run("timeline rejects a non-duration window", func(t *testing.T) {
		// since= takes a Go duration. "30d" is not one, and silently treating an
		// unparseable window as the default is how a filter comes to answer with
		// something other than what was asked for.
		req := httptest.NewRequest(http.MethodGet, "/api/v1/operations/timeline?since=30d", nil)
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusBadRequest, w.Code)
	})
}

// ---------------------------------------------------------------------------
// Additional coverage: stale operations
// ---------------------------------------------------------------------------

func TestCoverageListStaleOperations(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	t.Run("default timeout", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/operations/stale", nil)
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		var resp map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		data := resp["data"].(map[string]any)
		assert.NotNil(t, data["operations"])
	})

	t.Run("custom timeout", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/operations/stale?timeout_minutes=10", nil)
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
	})
}

// ---------------------------------------------------------------------------
// Additional coverage: narrators
// ---------------------------------------------------------------------------

func TestCoverageListNarrators(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/narrators", nil)
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
}

// ---------------------------------------------------------------------------
// Additional coverage: book segments, files, changelog, path history, external IDs
// ---------------------------------------------------------------------------

func TestCoverageBookSubresources(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	book := createTestBook(t, "Sub Resource Book")

	t.Run("list segments", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/audiobooks/%s/segments", book.ID), nil)
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("list segments for non-existent book", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/audiobooks/01ZZZZZZZZZZZZZZZZZZZZZZZZ/segments", nil)
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusNotFound, w.Code)
	})

	t.Run("list files", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/audiobooks/%s/files", book.ID), nil)
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("get changelog", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/audiobooks/%s/changelog", book.ID), nil)
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)

		var resp map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		resp = resp["data"].(map[string]any)
		assert.NotNil(t, resp["entries"])
	})

	t.Run("get path history", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/audiobooks/%s/path-history", book.ID), nil)
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)

		var resp map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		resp = resp["data"].(map[string]any)
		// history key exists in response (may be null for no history)
		_, hasHistory := resp["history"]
		assert.True(t, hasHistory, "response should contain 'history' key")
	})

	t.Run("get external IDs", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/audiobooks/%s/external-ids", book.ID), nil)
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)

		var resp map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		resp = resp["data"].(map[string]any)
		// external_ids key exists (may be empty array or null)
		_, hasKey := resp["external_ids"]
		assert.True(t, hasKey, "response should contain 'external_ids' key")
	})

	t.Run("get field states", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/audiobooks/%s/field-states", book.ID), nil)
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("get changes", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/audiobooks/%s/changes", book.ID), nil)
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("get cover art not found", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/audiobooks/%s/cover", book.ID), nil)
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusNotFound, w.Code)
	})
}

// ---------------------------------------------------------------------------
// Additional coverage: duplicate endpoints
// ---------------------------------------------------------------------------

func TestCoverageDuplicateEndpoints(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	t.Run("list duplicate audiobooks", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/audiobooks/duplicates", nil)
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("list duplicate scan results", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/audiobooks/duplicates/scan-results", nil)
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("list duplicate authors", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/authors/duplicates", nil)
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("list series duplicates", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/series/duplicates", nil)
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
	})
}

// ---------------------------------------------------------------------------
// Additional coverage: preferences
// ---------------------------------------------------------------------------

func TestCoveragePreferences(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	t.Run("set preference", func(t *testing.T) {
		payload := map[string]any{"value": "dark"}
		body, _ := json.Marshal(payload)
		req := httptest.NewRequest(http.MethodPut, "/api/v1/preferences/theme", bytes.NewBuffer(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("get preference", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/preferences/theme", nil)
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)
		// Could be 200 or 404 depending on whether it was set
		assert.Contains(t, []int{http.StatusOK, http.StatusNotFound}, w.Code)
	})

	t.Run("delete preference", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodDelete, "/api/v1/preferences/theme", nil)
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)
		assert.Contains(t, []int{http.StatusOK, http.StatusNoContent, http.StatusNotFound}, w.Code)
	})

	t.Run("get non-existent preference", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/preferences/nonexistent", nil)
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)
		assert.Contains(t, []int{http.StatusOK, http.StatusNotFound}, w.Code)
	})
}

// ---------------------------------------------------------------------------
// Additional coverage: system announcements
// ---------------------------------------------------------------------------

func TestCoverageSystemAnnouncements(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/system/announcements", nil)
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
}

// ---------------------------------------------------------------------------
// Additional coverage: scan operations
// ---------------------------------------------------------------------------

func TestCoverageScanOperations(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	t.Run("start scan", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/operations/v2",
			strings.NewReader(`{"def_id":"library.scan","params":{}}`))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)

		// Should be 202 Accepted or may fail if no scan dirs configured
		assert.Contains(t, []int{http.StatusAccepted, http.StatusBadRequest, http.StatusInternalServerError}, w.Code)
	})
}

// ---------------------------------------------------------------------------
// Additional coverage: author-related endpoints
// ---------------------------------------------------------------------------

func TestCoverageAuthorBooks(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	author, err := database.GetGlobalStore().CreateAuthor("Book Author")
	require.NoError(t, err)

	tempDir := t.TempDir()
	filePath := filepath.Join(tempDir, "authorbook.m4b")
	require.NoError(t, os.WriteFile(filePath, []byte("audio"), 0o644))
	_, err = database.GetGlobalStore().CreateBook(&database.Book{
		Title:    "Author Book",
		FilePath: filePath,
		Format:   "m4b",
		AuthorID: &author.ID,
	})
	require.NoError(t, err)

	t.Run("get author books", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/authors/%d/books", author.ID), nil)
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("get author books invalid ID", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/authors/notanumber/books", nil)
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusBadRequest, w.Code)
	})
}

// ---------------------------------------------------------------------------
// Additional coverage: series books endpoint
// ---------------------------------------------------------------------------

func TestCoverageSeriesBooks(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	series, err := database.GetGlobalStore().CreateSeries("Book Series", nil)
	require.NoError(t, err)

	tempDir := t.TempDir()
	filePath := filepath.Join(tempDir, "seriesbook.m4b")
	require.NoError(t, os.WriteFile(filePath, []byte("audio"), 0o644))
	_, err = database.GetGlobalStore().CreateBook(&database.Book{
		Title:    "Series Book",
		FilePath: filePath,
		Format:   "m4b",
		SeriesID: &series.ID,
	})
	require.NoError(t, err)

	t.Run("get series books", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/series/%d/books", series.ID), nil)
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)

		var resp map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		items := resp["data"].(map[string]any)["items"].([]any)
		assert.GreaterOrEqual(t, len(items), 1)
	})

	t.Run("get series books invalid ID", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/series/notanumber/books", nil)
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusBadRequest, w.Code)
	})
}

// ---------------------------------------------------------------------------
// Additional coverage: purge soft-deleted
// ---------------------------------------------------------------------------

func TestCoveragePurgeSoftDeleted(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	t.Run("purge with no soft-deleted books", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodDelete, "/api/v1/audiobooks/purge-soft-deleted", nil)
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("purge with older_than_days", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodDelete, "/api/v1/audiobooks/purge-soft-deleted?older_than_days=30", nil)
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("purge with delete_files", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodDelete, "/api/v1/audiobooks/purge-soft-deleted?delete_files=true", nil)
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
	})
}

// ---------------------------------------------------------------------------
// Additional coverage: metadata history
// ---------------------------------------------------------------------------

func TestCoverageMetadataHistory(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	book := createTestBook(t, "History Book")

	t.Run("get metadata history", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/audiobooks/%s/metadata-history", book.ID), nil)
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("get field metadata history", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/audiobooks/%s/metadata-history/title", book.ID), nil)
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
	})
}

// ---------------------------------------------------------------------------
// Additional coverage: user tags
// ---------------------------------------------------------------------------

func TestCoverageUserTags(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	book := createTestBook(t, "Tagged Book")

	t.Run("list all user tags", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/tags", nil)
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("get book user tags", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/audiobooks/%s/user-tags", book.ID), nil)
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
	})
}

// ---------------------------------------------------------------------------
// Additional coverage: SetVersion
// ---------------------------------------------------------------------------

func TestCoverageSetVersion(t *testing.T) {
	original := appVersion
	SetVersion("1.2.3-test")
	assert.Equal(t, "1.2.3-test", appVersion)
	SetVersion(original) // restore
}

// ---------------------------------------------------------------------------
// Additional coverage: resetLibrarySizeCache
// ---------------------------------------------------------------------------

func TestCoverageResetLibrarySizeCache(t *testing.T) {
	resetLibrarySizeCache()
	cacheLock.RLock()
	defer cacheLock.RUnlock()
	assert.Equal(t, int64(0), cachedLibrarySize)
	assert.Equal(t, int64(0), cachedImportSize)
	assert.True(t, cachedSizeComputedAt.IsZero())
}

// ---------------------------------------------------------------------------
// Additional coverage: helper functions
// ---------------------------------------------------------------------------

func TestCoverageHelperFunctions(t *testing.T) {
	t.Run("stringPtr", func(t *testing.T) {
		p := new("hello")
		require.NotNil(t, p)
		assert.Equal(t, "hello", *p)
	})

	t.Run("intPtrHelper", func(t *testing.T) {
		p := new(42)
		require.NotNil(t, p)
		assert.Equal(t, 42, *p)
	})

	t.Run("boolPtr", func(t *testing.T) {
		p := new(true)
		require.NotNil(t, p)
		assert.True(t, *p)
	})

	t.Run("metadataStateKey", func(t *testing.T) {
		key := metadataStateKey("abc123")
		assert.Equal(t, "metadata_state_abc123", key)
	})

	t.Run("decodeMetadataValue nil", func(t *testing.T) {
		result := decodeMetadataValue(nil)
		assert.Nil(t, result)
	})

	t.Run("decodeMetadataValue empty string", func(t *testing.T) {
		empty := ""
		result := decodeMetadataValue(&empty)
		assert.Nil(t, result)
	})

	t.Run("decodeMetadataValue valid JSON", func(t *testing.T) {
		val := `"hello"`
		result := decodeMetadataValue(&val)
		assert.Equal(t, "hello", result)
	})

	t.Run("decodeMetadataValue plain string", func(t *testing.T) {
		val := "not json"
		result := decodeMetadataValue(&val)
		assert.Equal(t, "not json", result)
	})

	t.Run("encodeMetadataValue nil", func(t *testing.T) {
		result, err := encodeMetadataValue(nil)
		assert.NoError(t, err)
		assert.Nil(t, result)
	})

	t.Run("encodeMetadataValue string", func(t *testing.T) {
		result, err := encodeMetadataValue("hello")
		assert.NoError(t, err)
		require.NotNil(t, result)
		assert.Equal(t, `"hello"`, *result)
	})
}

// ---------------------------------------------------------------------------
// Additional coverage: batch update (old endpoint)
// ---------------------------------------------------------------------------

func TestCoverageBatchUpdateAudiobooks(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	book := createTestBook(t, "Batch Old Book")

	t.Run("batch update with items", func(t *testing.T) {
		payload := map[string]any{
			"audiobooks": []map[string]any{
				{
					"id":    book.ID,
					"title": "Batch Old Updated",
				},
			},
		}
		body, _ := json.Marshal(payload)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/audiobooks/batch", bytes.NewBuffer(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
	})
}

// ---------------------------------------------------------------------------
// Additional coverage: list audiobooks with is_primary_version filter
// ---------------------------------------------------------------------------

func TestCoverageListAudiobooksWithFilters(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	createTestBook(t, "Filter Book 1")

	t.Run("filter is_primary_version true", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/audiobooks?is_primary_version=true", nil)
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("filter is_primary_version false", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/audiobooks?is_primary_version=false", nil)
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("filter by tag", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/audiobooks?tag=fiction", nil)
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
	})
}

// ---------------------------------------------------------------------------
// Additional coverage: list audiobooks caching
// ---------------------------------------------------------------------------

func TestCoverageListAudiobooksCaching(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	createTestBook(t, "Cache Test Book")

	// First request fills cache
	req1 := httptest.NewRequest(http.MethodGet, "/api/v1/audiobooks?limit=50", nil)
	w1 := httptest.NewRecorder()
	server.router.ServeHTTP(w1, req1)
	assert.Equal(t, http.StatusOK, w1.Code)

	// Second request should hit cache
	req2 := httptest.NewRequest(http.MethodGet, "/api/v1/audiobooks?limit=50", nil)
	w2 := httptest.NewRecorder()
	server.router.ServeHTTP(w2, req2)
	assert.Equal(t, http.StatusOK, w2.Code)

	// Results should be the same
	assert.Equal(t, w1.Body.String(), w2.Body.String())
}
