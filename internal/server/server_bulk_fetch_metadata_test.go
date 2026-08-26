// file: internal/server/server_bulk_fetch_metadata_test.go
// version: 1.1.0
// guid: 2b1c0d9e-8f7a-6b5c-4d3e-2f1a0b9c8d7e
// last-edited: 2026-08-20

package server

import (
	"bytes"
	json "encoding/json/v2"
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

func TestBulkFetchMetadata_MixedResults(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	// Stub OpenLibrary.
	mux := http.NewServeMux()
	mux.HandleFunc("/search.json", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		title := q.Get("title")
		if title == url.QueryEscape("NoResults") || title == "NoResults" {
			_ = json.MarshalWrite(w, map[string]any{"numFound": 0, "start": 0, "docs": []any{}})
			return
		}
		_ = json.MarshalWrite(w, map[string]any{
			"numFound": 1,
			"start":    0,
			"docs": []map[string]any{
				{
					"title":              "Fetched Title",
					"author_name":        []string{"Meta Author"},
					"first_publish_year": 2020,
					"isbn":               []string{"1234567890"},
					"publisher":          []string{"Meta Pub"},
					"language":           []string{"eng"},
				},
			},
		})
	})
	ol := httptest.NewServer(mux)
	defer ol.Close()
	useOnlyOpenLibrary(t, ol.URL)

	// Book that should update (has missing publisher/language/year/isbn/author).
	tempFile := filepath.Join(t.TempDir(), "bulk1.m4b")
	require.NoError(t, os.WriteFile(tempFile, []byte("audio"), 0o644))
	book1, err := database.GetGlobalStore().CreateBook(&database.Book{Title: "Book One", FilePath: tempFile, Format: "m4b"})
	require.NoError(t, err)

	// Book with missing title.
	tempFile2 := filepath.Join(t.TempDir(), "bulk2.m4b")
	require.NoError(t, os.WriteFile(tempFile2, []byte("audio"), 0o644))
	book2, err := database.GetGlobalStore().CreateBook(&database.Book{Title: "", FilePath: tempFile2, Format: "m4b"})
	require.NoError(t, err)

	// Book whose title returns no results.
	tempFile3 := filepath.Join(t.TempDir(), "bulk3.m4b")
	require.NoError(t, os.WriteFile(tempFile3, []byte("audio"), 0o644))
	book3, err := database.GetGlobalStore().CreateBook(&database.Book{Title: "NoResults", FilePath: tempFile3, Format: "m4b"})
	require.NoError(t, err)

	payload := map[string]any{
		"book_ids": []string{book1.ID, book2.ID, "missing-id", book3.ID},
	}
	body, err := json.Marshal(payload)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/metadata/bulk-fetch", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var resp struct {
		Data struct {
			UpdatedCount int `json:"updated_count"`
			TotalCount   int `json:"total_count"`
			Results      []struct {
				BookID  string `json:"book_id"`
				Status  string `json:"status"`
				Message string `json:"message"`
			} `json:"results"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, 4, resp.Data.TotalCount)
	assert.GreaterOrEqual(t, resp.Data.UpdatedCount, 1)

	byID := map[string]struct {
		Status  string
		Message string
	}{}
	for _, r := range resp.Data.Results {
		byID[r.BookID] = struct {
			Status  string
			Message string
		}{Status: r.Status, Message: r.Message}
	}

	assert.Equal(t, "updated", byID[book1.ID].Status)
	assert.Equal(t, "skipped", byID[book2.ID].Status)
	assert.Equal(t, "missing title", byID[book2.ID].Message)
	assert.Equal(t, "not_found", byID["missing-id"].Status)
	assert.Equal(t, "no metadata found from any source", byID[book3.ID].Message)
}

func TestBulkFetchMetadata_OnlyMissingFalse_AllowsOverwrite(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	// Stub OpenLibrary with a different publisher.
	mux := http.NewServeMux()
	mux.HandleFunc("/search.json", func(w http.ResponseWriter, r *http.Request) {
		_ = json.MarshalWrite(w, map[string]any{
			"numFound": 1,
			"start":    0,
			"docs": []map[string]any{
				{
					"title":              "Fetched Title",
					"publisher":          []string{"Overwrite Pub"},
					"author_name":        []string{"Meta Author"},
					"first_publish_year": 2020,
					"isbn":               []string{"1234567890"},
					"language":           []string{"eng"},
				},
			},
		})
	})
	ol := httptest.NewServer(mux)
	defer ol.Close()
	useOnlyOpenLibrary(t, ol.URL)

	tempFile := filepath.Join(t.TempDir(), "bulk-overwrite.m4b")
	require.NoError(t, os.WriteFile(tempFile, []byte("audio"), 0o644))
	existingPublisher := "Existing Pub"
	book, err := database.GetGlobalStore().CreateBook(&database.Book{Title: "Book One", FilePath: tempFile, Format: "m4b", Publisher: &existingPublisher})
	require.NoError(t, err)

	payload := map[string]any{
		"book_ids":     []string{book.ID},
		"only_missing": false,
	}
	body, err := json.Marshal(payload)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/metadata/bulk-fetch", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	updated, err := database.GetGlobalStore().GetBookByID(book.ID)
	require.NoError(t, err)
	require.NotNil(t, updated)
	require.NotNil(t, updated.Publisher)
	assert.Equal(t, "Overwrite Pub", *updated.Publisher)
}
