// file: internal/server/server_bulk_delete_test.go
// version: 1.2.0
// guid: a1b2c3d4-e5f6-7890-abcd-ef1234567890
// last-edited: 2026-07-05

package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// bulkDeleteResponse is the common response shape for bulk-delete endpoints.
type bulkDeleteResponse struct {
	Deleted int      `json:"deleted"`
	Skipped int      `json:"skipped"`
	Errors  []string `json:"errors"`
	Total   int      `json:"total"`
}

func postJSON(server *Server, path string, body interface{}) *httptest.ResponseRecorder {
	data, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	return w
}

// ---------- Authors bulk-delete ----------

func TestBulkDeleteAuthors_AllEmpty(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	store := database.GetGlobalStore()

	// Create authors with no books
	a1, err := store.CreateAuthor("Author One")
	require.NoError(t, err)
	a2, err := store.CreateAuthor("Author Two")
	require.NoError(t, err)

	w := postJSON(server, "/api/v1/authors/bulk-delete", map[string]interface{}{
		"ids": []int{a1.ID, a2.ID},
	})

	assert.Equal(t, http.StatusOK, w.Code)

	var envelope struct {
		Data bulkDeleteResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &envelope))

	resp := envelope.Data
	assert.Equal(t, 2, resp.Deleted)
	assert.Equal(t, 0, resp.Skipped)
	assert.Empty(t, resp.Errors)
	assert.Equal(t, 2, resp.Total)

	// Verify authors are actually gone (GetAuthorByID returns nil, nil for missing)
	a, err := store.GetAuthorByID(a1.ID)
	assert.NoError(t, err)
	assert.Nil(t, a)
	a, err = store.GetAuthorByID(a2.ID)
	assert.NoError(t, err)
	assert.Nil(t, a)
}

func TestBulkDeleteAuthors_SkipsAuthorsWithBooks(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	store := database.GetGlobalStore()

	// Create two authors
	authorWithBooks, err := store.CreateAuthor("Has Books")
	require.NoError(t, err)
	authorEmpty, err := store.CreateAuthor("No Books")
	require.NoError(t, err)

	// Create a book linked to the first author
	_, err = store.CreateBook(&database.Book{
		Title:    "Test Book",
		AuthorID: &authorWithBooks.ID,
		FilePath: "/tmp/test.m4b",
	})
	require.NoError(t, err)

	w := postJSON(server, "/api/v1/authors/bulk-delete", map[string]interface{}{
		"ids": []int{authorWithBooks.ID, authorEmpty.ID},
	})

	assert.Equal(t, http.StatusOK, w.Code)

	var wrapper struct {
		Data bulkDeleteResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &wrapper))
	resp := wrapper.Data

	assert.Equal(t, 1, resp.Deleted)
	assert.Equal(t, 1, resp.Skipped)
	assert.Empty(t, resp.Errors)
	assert.Equal(t, 2, resp.Total)

	// Author with books should still exist
	a, err := store.GetAuthorByID(authorWithBooks.ID)
	assert.NoError(t, err)
	assert.Equal(t, "Has Books", a.Name)
}

func TestBulkDeleteAuthors_InvalidBody(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/authors/bulk-delete",
		bytes.NewReader([]byte(`{"bad": true}`)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestBulkDeleteAuthors_EmptyIDs(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	w := postJSON(server, "/api/v1/authors/bulk-delete", map[string]interface{}{
		"ids": []int{},
	})

	// Gin binding:"required" accepts empty slices — returns 200 with zero results
	assert.Equal(t, http.StatusOK, w.Code)

	var wrapper struct {
		Data bulkDeleteResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &wrapper))
	resp := wrapper.Data
	assert.Equal(t, 0, resp.Deleted)
	assert.Equal(t, 0, resp.Total)
}

func TestBulkDeleteAuthors_NonexistentIDs(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	// IDs that don't exist — GetBooksByAuthorIDCore returns empty, DeleteAuthor may error or succeed
	w := postJSON(server, "/api/v1/authors/bulk-delete", map[string]interface{}{
		"ids": []int{99999, 99998},
	})

	assert.Equal(t, http.StatusOK, w.Code)

	var wrapper struct {
		Data bulkDeleteResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &wrapper))
	resp := wrapper.Data

	// Either deleted (no books to block) or errored — total should match
	assert.Equal(t, 2, resp.Total)
	assert.Equal(t, 0, resp.Skipped)
}

func TestBulkDeleteAuthors_MixedResults(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	store := database.GetGlobalStore()

	// Create three authors
	a1, err := store.CreateAuthor("Empty Author 1")
	require.NoError(t, err)
	a2, err := store.CreateAuthor("Author With Book")
	require.NoError(t, err)
	a3, err := store.CreateAuthor("Empty Author 2")
	require.NoError(t, err)

	// Link a book to a2
	_, err = store.CreateBook(&database.Book{
		Title:    "A Book",
		AuthorID: &a2.ID,
		FilePath: "/tmp/book.m4b",
	})
	require.NoError(t, err)

	w := postJSON(server, "/api/v1/authors/bulk-delete", map[string]interface{}{
		"ids": []int{a1.ID, a2.ID, a3.ID},
	})

	assert.Equal(t, http.StatusOK, w.Code)

	var wrapper struct {
		Data bulkDeleteResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &wrapper))
	resp := wrapper.Data

	assert.Equal(t, 2, resp.Deleted)
	assert.Equal(t, 1, resp.Skipped)
	assert.Equal(t, 3, resp.Total)
}

// ---------- Authors bulk-delete with mock store ----------

func TestBulkDeleteAuthors_StoreError(t *testing.T) {
	mock := &database.MockStore{
		GetBooksByAuthorIDCoreFunc: func(authorID int) ([]database.BookCore, error) {
			if authorID == 1 {
				return nil, fmt.Errorf("db connection lost")
			}
			return nil, nil
		},
		DeleteAuthorFunc: func(id int) error {
			return nil
		},
	}
	server, cleanup := setupTestServerWithStore(t, mock)
	defer cleanup()

	w := postJSON(server, "/api/v1/authors/bulk-delete", map[string]interface{}{
		"ids": []int{1, 2},
	})

	assert.Equal(t, http.StatusOK, w.Code)

	var wrapper struct {
		Data bulkDeleteResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &wrapper))
	resp := wrapper.Data

	assert.Equal(t, 1, resp.Deleted) // id=2 succeeded
	assert.Equal(t, 0, resp.Skipped)
	assert.Len(t, resp.Errors, 1) // id=1 errored
	assert.Equal(t, 2, resp.Total)
}

func TestBulkDeleteAuthors_DeleteError(t *testing.T) {
	mock := &database.MockStore{
		GetBooksByAuthorIDCoreFunc: func(authorID int) ([]database.BookCore, error) {
			return nil, nil // no books
		},
		DeleteAuthorFunc: func(id int) error {
			return fmt.Errorf("delete failed for %d", id)
		},
	}
	server, cleanup := setupTestServerWithStore(t, mock)
	defer cleanup()

	w := postJSON(server, "/api/v1/authors/bulk-delete", map[string]interface{}{
		"ids": []int{1},
	})

	assert.Equal(t, http.StatusOK, w.Code)

	var wrapper struct {
		Data bulkDeleteResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &wrapper))
	resp := wrapper.Data

	assert.Equal(t, 0, resp.Deleted)
	assert.Equal(t, 0, resp.Skipped)
	assert.Len(t, resp.Errors, 1)
	assert.Contains(t, resp.Errors[0], "delete failed")
}

// ---------- Series bulk-delete ----------

func TestBulkDeleteSeries_AllEmpty(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	store := database.GetGlobalStore()

	s1, err := store.CreateSeries("Series One", nil)
	require.NoError(t, err)
	s2, err := store.CreateSeries("Series Two", nil)
	require.NoError(t, err)

	w := postJSON(server, "/api/v1/series/bulk-delete", map[string]interface{}{
		"ids": []int{s1.ID, s2.ID},
	})

	assert.Equal(t, http.StatusOK, w.Code)

	var envelope struct {
		Data bulkDeleteResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &envelope))

	resp := envelope.Data
	assert.Equal(t, 2, resp.Deleted)
	assert.Equal(t, 0, resp.Skipped)
	assert.Empty(t, resp.Errors)
	assert.Equal(t, 2, resp.Total)

	// Verify series are gone (GetSeriesByID returns nil, nil for missing)
	s, err := store.GetSeriesByID(s1.ID)
	assert.NoError(t, err)
	assert.Nil(t, s)
	s, err = store.GetSeriesByID(s2.ID)
	assert.NoError(t, err)
	assert.Nil(t, s)
}

func TestBulkDeleteSeries_SkipsSeriesWithBooks(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	store := database.GetGlobalStore()

	seriesWithBooks, err := store.CreateSeries("Has Books", nil)
	require.NoError(t, err)
	seriesEmpty, err := store.CreateSeries("No Books", nil)
	require.NoError(t, err)

	_, err = store.CreateBook(&database.Book{
		Title:    "Book In Series",
		SeriesID: &seriesWithBooks.ID,
		FilePath: "/tmp/series-book.m4b",
	})
	require.NoError(t, err)

	w := postJSON(server, "/api/v1/series/bulk-delete", map[string]interface{}{
		"ids": []int{seriesWithBooks.ID, seriesEmpty.ID},
	})

	assert.Equal(t, http.StatusOK, w.Code)

	var wrapper struct {
		Data bulkDeleteResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &wrapper))
	resp := wrapper.Data

	assert.Equal(t, 1, resp.Deleted)
	assert.Equal(t, 1, resp.Skipped)
	assert.Empty(t, resp.Errors)
	assert.Equal(t, 2, resp.Total)

	// Series with books should still exist
	s, err := store.GetSeriesByID(seriesWithBooks.ID)
	assert.NoError(t, err)
	assert.Equal(t, "Has Books", s.Name)
}

func TestBulkDeleteSeries_InvalidBody(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/series/bulk-delete",
		bytes.NewReader([]byte(`not json`)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestBulkDeleteSeries_EmptyIDs(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	w := postJSON(server, "/api/v1/series/bulk-delete", map[string]interface{}{
		"ids": []int{},
	})

	// Gin binding:"required" accepts empty slices — returns 200 with zero results
	assert.Equal(t, http.StatusOK, w.Code)

	var wrapper struct {
		Data bulkDeleteResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &wrapper))
	resp := wrapper.Data
	assert.Equal(t, 0, resp.Deleted)
	assert.Equal(t, 0, resp.Total)
}

func TestBulkDeleteSeries_MixedResults(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	store := database.GetGlobalStore()

	s1, err := store.CreateSeries("Empty 1", nil)
	require.NoError(t, err)
	s2, err := store.CreateSeries("With Book", nil)
	require.NoError(t, err)
	s3, err := store.CreateSeries("Empty 2", nil)
	require.NoError(t, err)

	_, err = store.CreateBook(&database.Book{
		Title:    "Linked Book",
		SeriesID: &s2.ID,
		FilePath: "/tmp/linked.m4b",
	})
	require.NoError(t, err)

	w := postJSON(server, "/api/v1/series/bulk-delete", map[string]interface{}{
		"ids": []int{s1.ID, s2.ID, s3.ID},
	})

	assert.Equal(t, http.StatusOK, w.Code)

	var wrapper struct {
		Data bulkDeleteResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &wrapper))
	resp := wrapper.Data

	assert.Equal(t, 2, resp.Deleted)
	assert.Equal(t, 1, resp.Skipped)
	assert.Equal(t, 3, resp.Total)
}

// ---------- Series bulk-delete with mock store ----------

func TestBulkDeleteSeries_StoreError(t *testing.T) {
	mock := &database.MockStore{
		GetBooksBySeriesIDFunc: func(seriesID int) ([]database.Book, error) {
			if seriesID == 1 {
				return nil, fmt.Errorf("db read error")
			}
			return nil, nil
		},
		DeleteSeriesFunc: func(id int) error {
			return nil
		},
	}
	server, cleanup := setupTestServerWithStore(t, mock)
	defer cleanup()

	w := postJSON(server, "/api/v1/series/bulk-delete", map[string]interface{}{
		"ids": []int{1, 2},
	})

	assert.Equal(t, http.StatusOK, w.Code)

	var wrapper struct {
		Data bulkDeleteResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &wrapper))
	resp := wrapper.Data

	assert.Equal(t, 1, resp.Deleted)
	assert.Equal(t, 0, resp.Skipped)
	assert.Len(t, resp.Errors, 1)
	assert.Equal(t, 2, resp.Total)
}

func TestBulkDeleteSeries_DeleteError(t *testing.T) {
	mock := &database.MockStore{
		GetBooksBySeriesIDFunc: func(seriesID int) ([]database.Book, error) {
			return nil, nil
		},
		DeleteSeriesFunc: func(id int) error {
			return fmt.Errorf("cannot delete series %d", id)
		},
	}
	server, cleanup := setupTestServerWithStore(t, mock)
	defer cleanup()

	w := postJSON(server, "/api/v1/series/bulk-delete", map[string]interface{}{
		"ids": []int{5},
	})

	assert.Equal(t, http.StatusOK, w.Code)

	var wrapper struct {
		Data bulkDeleteResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &wrapper))
	resp := wrapper.Data

	assert.Equal(t, 0, resp.Deleted)
	assert.Equal(t, 0, resp.Skipped)
	assert.Len(t, resp.Errors, 1)
	assert.Contains(t, resp.Errors[0], "cannot delete series")
}
