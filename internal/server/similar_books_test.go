// file: internal/server/similar_books_test.go
// version: 1.0.2
// guid: 1e2f3a4b-5c6d-7e8f-9a0b-1c2d3e4f5a6b
// last-edited: 2026-05-03

package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/search"
	"github.com/gin-gonic/gin"
)

func setupSimilarBooksServer(t *testing.T) *Server {
	t.Helper()
	gin.SetMode(gin.TestMode)

	pebblePath := filepath.Join(t.TempDir(), "pebble")
	store, err := database.NewPebbleStore(pebblePath)
	if err != nil {
		t.Fatalf("open pebble: %v", err)
	}
	origStore := database.GetGlobalStore()
	database.SetGlobalStore(store)
	t.Cleanup(func() {
		database.SetGlobalStore(origStore)
		store.Close()
	})

	idx, err := search.Open(filepath.Join(t.TempDir(), "bleve"))
	if err != nil {
		t.Fatalf("bleve open: %v", err)
	}
	t.Cleanup(func() { _ = idx.Close() })

	srv := NewServer(store)
	srv.setSearchIndex(idx)

	// Create an author and books.
	author, _ := store.CreateAuthor("Brandon Sanderson")
	authorID := author.ID

	_, _ = store.CreateBook(&database.Book{
		ID: "b1", Title: "The Way of Kings", FilePath: "/tmp/b1", Format: "m4b", AuthorID: &authorID,
	})
	_, _ = store.CreateBook(&database.Book{
		ID: "b2", Title: "Words of Radiance", FilePath: "/tmp/b2", Format: "m4b", AuthorID: &authorID,
	})
	_, _ = store.CreateBook(&database.Book{
		ID: "b3", Title: "Oathbringer", FilePath: "/tmp/b3", Format: "m4b", AuthorID: &authorID,
	})

	// Index all books for Bleve.
	for _, doc := range []search.BookDocument{
		{BookID: "b1", Title: "The Way of Kings", Author: "Brandon Sanderson", Format: "m4b"},
		{BookID: "b2", Title: "Words of Radiance", Author: "Brandon Sanderson", Format: "m4b"},
		{BookID: "b3", Title: "Oathbringer", Author: "Brandon Sanderson", Format: "m4b"},
	} {
		_ = idx.IndexBook(doc)
	}

	return srv
}

func TestHandleSimilarBooks_ReturnsSimilar(t *testing.T) {
	srv := setupSimilarBooksServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/audiobooks/b1/similar", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var envelope struct {
		Data struct {
			Books []database.Book `json:"books"`
			Count int             `json:"count"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// Should return b2 and b3 but not b1 itself.
	for _, b := range envelope.Data.Books {
		if b.ID == "b1" {
			t.Error("similar books should not include the source book")
		}
	}
	if envelope.Data.Count == 0 {
		t.Error("expected at least one similar book")
	}
}

func TestHandleSimilarBooks_NotFound(t *testing.T) {
	srv := setupSimilarBooksServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/audiobooks/nonexistent/similar", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestHandleSimilarBooks_NoAuthorNoSeries(t *testing.T) {
	gin.SetMode(gin.TestMode)

	pebblePath := filepath.Join(t.TempDir(), "pebble")
	store, err := database.NewPebbleStore(pebblePath)
	if err != nil {
		t.Fatalf("open pebble: %v", err)
	}
	origStore := database.GetGlobalStore()
	database.SetGlobalStore(store)
	t.Cleanup(func() {
		database.SetGlobalStore(origStore)
		store.Close()
	})

	idx, err := search.Open(filepath.Join(t.TempDir(), "bleve"))
	if err != nil {
		t.Fatalf("bleve open: %v", err)
	}
	t.Cleanup(func() { _ = idx.Close() })

	srv := NewServer(store)
	srv.setSearchIndex(idx)

	// Book with no author/series.
	_, _ = store.CreateBook(&database.Book{
		ID: "lonely", Title: "Lonely Book", FilePath: "/tmp/lonely", Format: "m4b",
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/audiobooks/lonely/similar", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp struct {
		Books []database.Book `json:"books"`
		Count int             `json:"count"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Count != 0 {
		t.Errorf("expected 0 results for book with no author/series, got %d", resp.Count)
	}
}
