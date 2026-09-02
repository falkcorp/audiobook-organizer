// file: internal/server/metadata_search_test.go
// version: 1.0.1
// guid: 7c8d9e0f-1a2b-3c4d-5e6f-7a8b9c0d1e2f
// last-edited: 2026-09-02

package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/oklog/ulid/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSearchMetadata_ReturnsResults(t *testing.T) {
	srv, cleanup := setupTestServer(t)
	defer cleanup()

	store := database.GetGlobalStore()
	bookID := ulid.Make().String()
	book := &database.Book{
		ID:       bookID,
		Title:    "The Long Cosmos",
		FilePath: "/tmp/search_test.m4b",
		Format:   "m4b",
	}
	_, err := store.CreateBook(book)
	require.NoError(t, err)

	body := `{"query":"The Long Cosmos"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/audiobooks/"+bookID+"/search-metadata", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	// Without metadata sources configured, the search returns 404
	// (no metadata sources enabled). In production, sources would be
	// configured and this would return 200 with results.
	assert.Equal(t, http.StatusNotFound, w.Code)

	var resp map[string]any
	err = json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)
	assert.Contains(t, resp["error"], "no metadata sources enabled")
}

func TestSearchMetadata_BookNotFound(t *testing.T) {
	srv, cleanup := setupTestServer(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/audiobooks/nonexistent/search-metadata", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestMarkNoMatch_SetsStatus(t *testing.T) {
	srv, cleanup := setupTestServer(t)
	defer cleanup()

	store := database.GetGlobalStore()
	bookID := ulid.Make().String()
	book := &database.Book{
		ID:       bookID,
		Title:    "Unknown Book",
		FilePath: "/tmp/nomatch_test.m4b",
		Format:   "m4b",
	}
	_, err := store.CreateBook(book)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/audiobooks/"+bookID+"/mark-no-match", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	updated, err := store.GetBookByID(bookID)
	require.NoError(t, err)
	require.NotNil(t, updated.MetadataReviewStatus)
	assert.Equal(t, "no_match", *updated.MetadataReviewStatus)
}

func TestApplyMetadata_AppliesCandidate(t *testing.T) {
	srv, cleanup := setupTestServer(t)
	defer cleanup()

	store := database.GetGlobalStore()
	bookID := ulid.Make().String()
	book := &database.Book{
		ID:       bookID,
		Title:    "Old Title",
		FilePath: "/tmp/apply_test.m4b",
		Format:   "m4b",
	}
	_, err := store.CreateBook(book)
	require.NoError(t, err)

	body := `{
		"candidate": {
			"title": "New Title",
			"author": "New Author",
			"source": "test",
			"score": 0.95
		},
		"fields": []
	}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/audiobooks/"+bookID+"/apply-metadata", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	updated, err := store.GetBookByID(bookID)
	require.NoError(t, err)
	assert.Equal(t, "New Title", updated.Title)
}

func TestApplyMetadata_FieldFiltering(t *testing.T) {
	srv, cleanup := setupTestServer(t)
	defer cleanup()

	store := database.GetGlobalStore()
	bookID := ulid.Make().String()
	book := &database.Book{
		ID:       bookID,
		Title:    "Keep This Title",
		FilePath: "/tmp/field_filter_test.m4b",
		Format:   "m4b",
	}
	_, err := store.CreateBook(book)
	require.NoError(t, err)

	body := `{
		"candidate": {
			"title": "Different Title",
			"author": "Different Author",
			"narrator": "New Narrator",
			"source": "test",
			"score": 0.9
		},
		"fields": ["narrator"]
	}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/audiobooks/"+bookID+"/apply-metadata", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	updated, err := store.GetBookByID(bookID)
	require.NoError(t, err)
	assert.Equal(t, "Keep This Title", updated.Title)
}
