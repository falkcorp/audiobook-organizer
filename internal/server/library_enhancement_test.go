// file: internal/server/library_enhancement_test.go
// version: 1.1.0
// guid: 0336376b-882f-41df-b8ab-7e27526cdb1d
// last-edited: 2026-05-01

package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// createTestBook is a helper that creates a book in the DB with a real temp file.
func createTestBook(t *testing.T, title string) *database.Book {
	t.Helper()
	tempFile := filepath.Join(t.TempDir(), title+".m4b")
	require.NoError(t, os.WriteFile(tempFile, []byte("audio"), 0o644))
	book, err := database.GetGlobalStore().CreateBook(&database.Book{
		Title:    title,
		FilePath: tempFile,
		Format:   "m4b",
	})
	require.NoError(t, err)
	return book
}

// createTestBookWithFields creates a book with optional narrator and duration set at creation time.
// Uses narrator (in BookSummary) instead of genre/language (not in BookSummary) so that
// GetAllBookSummaries-backed sorting and field filtering work correctly.
func createTestBookWithFields(t *testing.T, title string, narrator *string, duration *int) *database.Book {
	t.Helper()
	tempFile := filepath.Join(t.TempDir(), title+".m4b")
	require.NoError(t, os.WriteFile(tempFile, []byte("audio"), 0o644))
	book, err := database.GetGlobalStore().CreateBook(&database.Book{
		Title:    title,
		FilePath: tempFile,
		Format:   "m4b",
		Narrator: narrator,
		Duration: duration,
	})
	require.NoError(t, err)
	return book
}

// jsonBody encodes v as JSON bytes for use in request bodies.
func jsonBody(t *testing.T, v any) *bytes.Buffer {
	t.Helper()
	data, err := json.Marshal(v)
	require.NoError(t, err)
	return bytes.NewBuffer(data)
}

// parseJSONResponse unmarshals the recorder body into a generic map.
func parseJSONResponse(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var result map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &result))
	return result
}

// getTagsFromResponse extracts the "tags" array from a JSON response.
func getTagsFromResponse(t *testing.T, resp map[string]any) []string {
	t.Helper()
	data, ok := resp["data"].(map[string]any)
	require.True(t, ok, "response should have 'data' key")
	raw, ok := data["tags"]
	require.True(t, ok, "data should have 'tags' key")
	arr, ok := raw.([]any)
	require.True(t, ok, "tags should be an array")
	tags := make([]string, len(arr))
	for i, v := range arr {
		tags[i] = v.(string)
	}
	return tags
}

// ── 1. Tag CRUD Operations ──────────────────────────────────────────────────

func TestTagCRUD(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	t.Run("list all tags returns empty initially", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/tags", nil)
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		resp := parseJSONResponse(t, w)
		respData := resp["data"].(map[string]any)
		tags := respData["tags"].([]any)
		assert.Empty(t, tags)
	})

	// Create a book for subsequent tag tests
	book := createTestBook(t, "TagCRUDBook")

	t.Run("add tag to book", func(t *testing.T) {
		body := jsonBody(t, map[string]string{"tag": "scifi"})
		req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/audiobooks/%s/user-tags", book.ID), body)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		resp := parseJSONResponse(t, w)
		tags := getTagsFromResponse(t, resp)
		assert.Contains(t, tags, "scifi")
	})

	t.Run("add second tag to same book", func(t *testing.T) {
		body := jsonBody(t, map[string]string{"tag": "litrpg"})
		req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/audiobooks/%s/user-tags", book.ID), body)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		resp := parseJSONResponse(t, w)
		tags := getTagsFromResponse(t, resp)
		assert.Len(t, tags, 2)
		assert.Contains(t, tags, "scifi")
		assert.Contains(t, tags, "litrpg")
	})

	t.Run("add duplicate tag is idempotent", func(t *testing.T) {
		body := jsonBody(t, map[string]string{"tag": "scifi"})
		req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/audiobooks/%s/user-tags", book.ID), body)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		resp := parseJSONResponse(t, w)
		tags := getTagsFromResponse(t, resp)
		assert.Len(t, tags, 2, "duplicate add should not create extra tag")
	})

	t.Run("get book tags", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/audiobooks/%s/user-tags", book.ID), nil)
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		resp := parseJSONResponse(t, w)
		tags := getTagsFromResponse(t, resp)
		assert.Len(t, tags, 2)
		assert.Contains(t, tags, "scifi")
		assert.Contains(t, tags, "litrpg")
	})

	t.Run("list all tags shows counts", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/tags", nil)
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		resp := parseJSONResponse(t, w)
		respData := resp["data"].(map[string]any)
		rawTags := respData["tags"].([]any)
		assert.Len(t, rawTags, 2)
		// Each entry should have tag and count
		for _, raw := range rawTags {
			entry := raw.(map[string]any)
			assert.NotEmpty(t, entry["tag"])
			assert.Equal(t, float64(1), entry["count"])
		}
	})

	t.Run("set book tags replaces all", func(t *testing.T) {
		body := jsonBody(t, map[string][]string{"tags": {"fantasy", "epic"}})
		req := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/api/v1/audiobooks/%s/user-tags", book.ID), body)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		resp := parseJSONResponse(t, w)
		tags := getTagsFromResponse(t, resp)
		assert.Len(t, tags, 2)
		assert.Contains(t, tags, "fantasy")
		assert.Contains(t, tags, "epic")
		assert.NotContains(t, tags, "scifi", "old tags should be replaced")
		assert.NotContains(t, tags, "litrpg", "old tags should be replaced")
	})

	t.Run("remove tag from book", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/api/v1/audiobooks/%s/user-tags/fantasy", book.ID), nil)
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		resp := parseJSONResponse(t, w)
		tags := getTagsFromResponse(t, resp)
		assert.Len(t, tags, 1)
		assert.Contains(t, tags, "epic")
		assert.NotContains(t, tags, "fantasy")
	})

	t.Run("remove non-existent tag returns success", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/api/v1/audiobooks/%s/user-tags/nonexistent", book.ID), nil)
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
	})
}

// ── 2. Tag Batch Operations ─────────────────────────────────────────────────

func TestTagBatchOperations(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	book1 := createTestBook(t, "BatchBook1")
	book2 := createTestBook(t, "BatchBook2")
	book3 := createTestBook(t, "BatchBook3")

	t.Run("batch add tags to multiple books", func(t *testing.T) {
		body := jsonBody(t, map[string]any{
			"book_ids":    []string{book1.ID, book2.ID, book3.ID},
			"add_tags":    []string{"scifi", "new"},
			"remove_tags": []string{},
		})
		req := httptest.NewRequest(http.MethodPost, "/api/v1/audiobooks/batch-tags", body)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		resp := parseJSONResponse(t, w)
		data := resp["data"].(map[string]any)
		assert.Equal(t, float64(3), data["updated"])

		// Verify each book has both tags
		for _, bookID := range []string{book1.ID, book2.ID, book3.ID} {
			tags, err := database.GetGlobalStore().GetBookTags(bookID)
			require.NoError(t, err)
			assert.Contains(t, tags, "scifi")
			assert.Contains(t, tags, "new")
		}
	})

	t.Run("batch remove tags", func(t *testing.T) {
		body := jsonBody(t, map[string]any{
			"book_ids":    []string{book1.ID, book2.ID},
			"add_tags":    []string{},
			"remove_tags": []string{"new"},
		})
		req := httptest.NewRequest(http.MethodPost, "/api/v1/audiobooks/batch-tags", body)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)

		// book1 and book2 should only have "scifi"
		for _, bookID := range []string{book1.ID, book2.ID} {
			tags, err := database.GetGlobalStore().GetBookTags(bookID)
			require.NoError(t, err)
			assert.Equal(t, []string{"scifi"}, tags)
		}
		// book3 should still have both
		tags3, err := database.GetGlobalStore().GetBookTags(book3.ID)
		require.NoError(t, err)
		assert.Contains(t, tags3, "scifi")
		assert.Contains(t, tags3, "new")
	})

	t.Run("batch with empty book_ids returns error", func(t *testing.T) {
		body := jsonBody(t, map[string]any{
			"book_ids":    []string{},
			"add_tags":    []string{"test"},
			"remove_tags": []string{},
		})
		req := httptest.NewRequest(http.MethodPost, "/api/v1/audiobooks/batch-tags", body)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("batch with no tags returns error", func(t *testing.T) {
		body := jsonBody(t, map[string]any{
			"book_ids":    []string{book1.ID},
			"add_tags":    []string{},
			"remove_tags": []string{},
		})
		req := httptest.NewRequest(http.MethodPost, "/api/v1/audiobooks/batch-tags", body)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
		resp := parseJSONResponse(t, w)
		assert.Contains(t, resp["error"], "at least one of add_tags or remove_tags is required")
	})
}

// ── 3. Tag Input Validation ─────────────────────────────────────────────────

func TestTagValidation(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	book := createTestBook(t, "ValidationBook")

	t.Run("add empty tag returns error", func(t *testing.T) {
		body := jsonBody(t, map[string]string{"tag": ""})
		req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/audiobooks/%s/user-tags", book.ID), body)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("set tags filters empty strings", func(t *testing.T) {
		body := jsonBody(t, map[string][]string{"tags": {"valid", "", "also-valid"}})
		req := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/api/v1/audiobooks/%s/user-tags", book.ID), body)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		resp := parseJSONResponse(t, w)
		tags := getTagsFromResponse(t, resp)
		assert.Len(t, tags, 2, "empty string should be filtered out")
		assert.Contains(t, tags, "valid")
		assert.Contains(t, tags, "also-valid")
	})

	t.Run("tags are normalized to lowercase", func(t *testing.T) {
		// Clear existing tags first
		body := jsonBody(t, map[string][]string{"tags": {}})
		req := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/api/v1/audiobooks/%s/user-tags", book.ID), body)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)
		require.Equal(t, http.StatusOK, w.Code)

		// Add tag with mixed case
		body = jsonBody(t, map[string]string{"tag": "SciFi"})
		req = httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/audiobooks/%s/user-tags", book.ID), body)
		req.Header.Set("Content-Type", "application/json")
		w = httptest.NewRecorder()
		server.router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		resp := parseJSONResponse(t, w)
		tags := getTagsFromResponse(t, resp)
		assert.Contains(t, tags, "scifi", "tag should be normalized to lowercase")
		assert.NotContains(t, tags, "SciFi", "original case should not be preserved")
	})
}

// ── 4. List Books with Tag Filter ───────────────────────────────────────────

func TestListBooksWithTagFilter(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	book1 := createTestBook(t, "TagFilterBook1")
	book2 := createTestBook(t, "TagFilterBook2")
	_ = createTestBook(t, "TagFilterBook3") // no tag

	// Tag books 1 and 2 with "scifi"
	require.NoError(t, database.GetGlobalStore().AddBookTag(book1.ID, "scifi"))
	require.NoError(t, database.GetGlobalStore().AddBookTag(book2.ID, "scifi"))

	t.Run("filter by tag returns only tagged books", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/audiobooks?tag=scifi", nil)
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		resp := parseJSONResponse(t, w)
		items := resp["data"].(map[string]any)["items"].([]any)
		assert.Len(t, items, 2, "should return only the 2 tagged books")
	})

	t.Run("filter by non-existent tag returns empty", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/audiobooks?tag=nonexistent", nil)
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		resp := parseJSONResponse(t, w)
		items := resp["data"].(map[string]any)["items"].([]any)
		assert.Empty(t, items)
	})

	t.Run("filter by tag with other filters", func(t *testing.T) {
		// Set library_state on book1
		organized := "organized"
		book1.LibraryState = &organized
		_, err := database.GetGlobalStore().UpdateBook(book1.ID, book1)
		require.NoError(t, err)

		req := httptest.NewRequest(http.MethodGet, "/api/v1/audiobooks?tag=scifi&library_state=organized", nil)
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		resp := parseJSONResponse(t, w)
		items := resp["data"].(map[string]any)["items"].([]any)
		assert.Len(t, items, 1, "should return only the book with both tag and library_state match")
	})
}

// ── 5. Server-Side Sorting ──────────────────────────────────────────────────

func TestServerSideSorting(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	// Create books with varying fields — including a nil-narrator book upfront
	// so all subtests see all 4 books (the list cache caches results after the first request).
	bookA := createTestBookWithFields(t, "Zebra Adventures", stringPtr("Charlie"), intPtrHelper(3600))
	bookB := createTestBookWithFields(t, "Alpha Quest", stringPtr("Aaron"), intPtrHelper(7200))
	bookC := createTestBookWithFields(t, "Middle Ground", stringPtr("Michael"), intPtrHelper(1800))
	bookD := createTestBook(t, "NoGenreBook") // nil narrator, nil duration

	// Helper to extract titles from list response
	extractTitles := func(t *testing.T, w *httptest.ResponseRecorder) []string {
		t.Helper()
		resp := parseJSONResponse(t, w)
		items := resp["data"].(map[string]any)["items"].([]any)
		titles := make([]string, len(items))
		for i, item := range items {
			m := item.(map[string]any)
			titles[i] = m["title"].(string)
		}
		return titles
	}
	// Tag all books with "sorttest" so we can use tag= filter to bypass the list cache.
	// Without a post-filter, GetAudiobooks returns cached results before applySorting runs,
	// which means different sort orders within the same test would all see the first sort.
	for _, id := range []string{bookA.ID, bookB.ID, bookC.ID, bookD.ID} {
		require.NoError(t, database.GetGlobalStore().AddBookTag(id, "sorttest"))
	}

	// Base URL includes tag filter to ensure cache bypass and sorting actually runs each time
	baseURL := "/api/v1/audiobooks?tag=sorttest"

	t.Run("sort by title ascending", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, baseURL+"&sort_by=title&sort_order=asc", nil)
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		titles := extractTitles(t, w)
		require.Len(t, titles, 4)
		assert.Equal(t, "Alpha Quest", titles[0])
		assert.Equal(t, "Middle Ground", titles[1])
		assert.Equal(t, "NoGenreBook", titles[2])
		assert.Equal(t, "Zebra Adventures", titles[3])
	})

	t.Run("sort by title descending", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, baseURL+"&sort_by=title&sort_order=desc", nil)
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		titles := extractTitles(t, w)
		require.Len(t, titles, 4)
		assert.Equal(t, "Zebra Adventures", titles[0])
		assert.Equal(t, "NoGenreBook", titles[1])
		assert.Equal(t, "Middle Ground", titles[2])
		assert.Equal(t, "Alpha Quest", titles[3])
	})

	t.Run("sort by duration ascending", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, baseURL+"&sort_by=duration&sort_order=asc", nil)
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		titles := extractTitles(t, w)
		require.Len(t, titles, 4)
		// nil (0) < 1800 < 3600 < 7200
		assert.Equal(t, "NoGenreBook", titles[0])
		assert.Equal(t, "Middle Ground", titles[1])
		assert.Equal(t, "Zebra Adventures", titles[2])
		assert.Equal(t, "Alpha Quest", titles[3])
	})

	t.Run("sort by narrator ascending", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, baseURL+"&sort_by=narrator&sort_order=asc", nil)
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		titles := extractTitles(t, w)
		require.Len(t, titles, 4)
		// "" (nil) < Aaron < Charlie < Michael
		assert.Equal(t, "NoGenreBook", titles[0])
		assert.Equal(t, "Alpha Quest", titles[1])
		assert.Equal(t, "Zebra Adventures", titles[2])
		assert.Equal(t, "Middle Ground", titles[3])
	})

	t.Run("sort by created_at", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, baseURL+"&sort_by=created_at&sort_order=asc", nil)
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		resp := parseJSONResponse(t, w)
		items := resp["data"].(map[string]any)["items"].([]any)
		assert.Len(t, items, 4, "should return all tagged books")
	})

	t.Run("invalid sort_by is ignored", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, baseURL+"&sort_by=INVALID", nil)
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code, "invalid sort_by should not cause an error")
		resp := parseJSONResponse(t, w)
		items := resp["data"].(map[string]any)["items"].([]any)
		assert.Len(t, items, 4)
	})

	t.Run("invalid sort_order defaults to asc", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, baseURL+"&sort_by=title&sort_order=INVALID", nil)
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		titles := extractTitles(t, w)
		require.Len(t, titles, 4)
		// Invalid sort_order defaults to asc, so Alpha first
		assert.Equal(t, "Alpha Quest", titles[0])
	})

	t.Run("sort with nil fields handles gracefully", func(t *testing.T) {
		// NoGenreBook has nil narrator — sorting still works without panic
		req := httptest.NewRequest(http.MethodGet, baseURL+"&sort_by=narrator&sort_order=asc", nil)
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code, "sorting should handle nil fields gracefully")
		titles := extractTitles(t, w)
		require.Len(t, titles, 4)
		// nil narrator sorts before any non-nil narrator
		assert.Equal(t, "NoGenreBook", titles[0])
	})
}

// ── 6. Server-Side Field Filtering ──────────────────────────────────────────

func TestServerSideFieldFiltering(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	// Books use narrator (present in BookSummary) instead of genre/language
	// (not in BookSummary) so field filters work end-to-end with GetAllBookSummaries.
	bookA := createTestBookWithFields(t, "The Great Adventure", stringPtr("Mary English"), nil)
	bookB := createTestBookWithFields(t, "Mystery Mansion", stringPtr("John English"), nil)
	bookC := createTestBookWithFields(t, "Le Petit Prince", stringPtr("Pierre French"), nil)
	_ = bookA
	_ = bookB
	_ = bookC

	// Helper to build the filters query param
	buildFiltersParam := func(filters []FieldFilter) string {
		data, _ := json.Marshal(filters)
		return url.QueryEscape(string(data))
	}

	// Helper to extract titles from list response
	extractTitles := func(t *testing.T, w *httptest.ResponseRecorder) []string {
		t.Helper()
		resp := parseJSONResponse(t, w)
		items := resp["data"].(map[string]any)["items"].([]any)
		titles := make([]string, len(items))
		for i, item := range items {
			m := item.(map[string]any)
			titles[i] = m["title"].(string)
		}
		return titles
	}

	t.Run("filter by single field", func(t *testing.T) {
		filters := buildFiltersParam([]FieldFilter{
			{Field: "narrator", Value: "Mary", Negated: false},
		})
		req := httptest.NewRequest(http.MethodGet, "/api/v1/audiobooks?filters="+filters, nil)
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		titles := extractTitles(t, w)
		assert.Len(t, titles, 1)
		assert.Equal(t, "The Great Adventure", titles[0])
	})

	t.Run("filter by multiple fields AND", func(t *testing.T) {
		filters := buildFiltersParam([]FieldFilter{
			{Field: "narrator", Value: "English", Negated: false},
			{Field: "narrator", Value: "Mary", Negated: false},
		})
		req := httptest.NewRequest(http.MethodGet, "/api/v1/audiobooks?filters="+filters, nil)
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		titles := extractTitles(t, w)
		assert.Len(t, titles, 1)
		assert.Equal(t, "The Great Adventure", titles[0])
	})

	t.Run("negated filter excludes", func(t *testing.T) {
		filters := buildFiltersParam([]FieldFilter{
			{Field: "narrator", Value: "Mary", Negated: true},
		})
		req := httptest.NewRequest(http.MethodGet, "/api/v1/audiobooks?filters="+filters, nil)
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		titles := extractTitles(t, w)
		assert.Len(t, titles, 2)
		assert.NotContains(t, titles, "The Great Adventure")
		assert.Contains(t, titles, "Mystery Mansion")
		assert.Contains(t, titles, "Le Petit Prince")
	})

	t.Run("case insensitive matching", func(t *testing.T) {
		filters := buildFiltersParam([]FieldFilter{
			{Field: "narrator", Value: "MARY", Negated: false},
		})
		req := httptest.NewRequest(http.MethodGet, "/api/v1/audiobooks?filters="+filters, nil)
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		titles := extractTitles(t, w)
		assert.Len(t, titles, 1)
		assert.Equal(t, "The Great Adventure", titles[0])
	})

	t.Run("partial match contains", func(t *testing.T) {
		filters := buildFiltersParam([]FieldFilter{
			{Field: "title", Value: "great", Negated: false},
		})
		req := httptest.NewRequest(http.MethodGet, "/api/v1/audiobooks?filters="+filters, nil)
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		titles := extractTitles(t, w)
		assert.Len(t, titles, 1)
		assert.Equal(t, "The Great Adventure", titles[0])
	})

	t.Run("unknown field returns 400", func(t *testing.T) {
		// This subtest asserted the opposite until 2026-08-14: that an unknown
		// field answered 200 with an empty list. That behaviour was the bug.
		//
		// An unknown field matched nothing, so the endpoint reported count 0 —
		// an answer indistinguishable from a truthful "no books match". A typo,
		// a renamed field, and a genuinely empty result all looked identical.
		// It hid thirteen fields the search bar offers and the backend had never
		// implemented, including year, isbn13 and duration; measured on
		// production, duration:1 returned 0 while duration_seconds:1 returned
		// 25,090 over the same rows.
		//
		// The expectation is inverted deliberately rather than deleted, because
		// the request is still well-formed and still has to be answered — just
		// answered honestly.
		filters := buildFiltersParam([]FieldFilter{
			{Field: "unknown_field", Value: "anything", Negated: false},
		})
		req := httptest.NewRequest(http.MethodGet, "/api/v1/audiobooks?filters="+filters, nil)
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code,
			"an unrecognised filter field must be rejected, not answered with an empty list")
		resp := parseJSONResponse(t, w)
		errorMsg, ok := resp["error"].(string)
		require.True(t, ok, "error field should be a string")
		assert.Contains(t, errorMsg, "unknown_field",
			"the rejection must name the offending field")
	})

	t.Run("invalid filters JSON returns 400", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/audiobooks?filters=NOT_JSON", nil)
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code, "malformed JSON should return 400")
		resp := parseJSONResponse(t, w)
		errorMsg, ok := resp["error"].(string)
		require.True(t, ok, "error field should be a string")
		assert.Contains(t, errorMsg, "invalid filters parameter")
	})

	t.Run("combined sort and filter", func(t *testing.T) {
		filters := buildFiltersParam([]FieldFilter{
			{Field: "narrator", Value: "English", Negated: false},
		})
		req := httptest.NewRequest(http.MethodGet, "/api/v1/audiobooks?sort_by=title&sort_order=desc&filters="+filters, nil)
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		titles := extractTitles(t, w)
		assert.Len(t, titles, 2)
		// desc: The Great Adventure > Mystery Mansion (both have "English" narrator)
		assert.Equal(t, "The Great Adventure", titles[0])
		assert.Equal(t, "Mystery Mansion", titles[1])
	})
}

// ── 7. User Preferences (Column Config) ─────────────────────────────────────

func TestUserPreferencesColumnConfig(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()
	_ = server // use the server to verify DB works end-to-end

	// Test the underlying store directly since there's no REST endpoint for generic preferences.
	// This validates the persistence layer that the frontend column config feature relies on.

	t.Run("get non-existent preference returns nil", func(t *testing.T) {
		pref, err := database.GetGlobalStore().GetUserPreference("library_column_config")
		assert.NoError(t, err)
		assert.Nil(t, pref)
	})

	t.Run("save and retrieve column config", func(t *testing.T) {
		configJSON := `{"visibleColumns":["title","author"]}`
		err := database.GetGlobalStore().SetUserPreference("library_column_config", configJSON)
		require.NoError(t, err)

		pref, err := database.GetGlobalStore().GetUserPreference("library_column_config")
		require.NoError(t, err)
		require.NotNil(t, pref)
		require.NotNil(t, pref.Value)
		assert.Equal(t, configJSON, *pref.Value)
	})

	t.Run("update existing preference", func(t *testing.T) {
		updatedJSON := `{"visibleColumns":["title","author","narrator","genre"]}`
		err := database.GetGlobalStore().SetUserPreference("library_column_config", updatedJSON)
		require.NoError(t, err)

		pref, err := database.GetGlobalStore().GetUserPreference("library_column_config")
		require.NoError(t, err)
		require.NotNil(t, pref)
		require.NotNil(t, pref.Value)
		assert.Equal(t, updatedJSON, *pref.Value)
	})
}
