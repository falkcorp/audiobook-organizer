// file: internal/server/playlist_handlers_test.go
// version: 1.0.1
// guid: 8b4d6f3e-9c4a-4a70-b8c5-3d7e0f1b9a89

package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/search"
	"github.com/gin-gonic/gin"
)

// setupPlaylistTestServer wires a PebbleStore + Bleve index + Gin
// server for the playlist HTTP tests. Seeds three books + their
// search docs so smart-playlist evaluation has something to match.
func setupPlaylistTestServer(t *testing.T) *Server {
	t.Helper()

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
	srv.setSearchIndex(idx) // test-only setter

	seedRows := []struct {
		id, title, author, format string
		year                      int
	}{
		{"b1", "The Way of Kings", "Sanderson", "m4b", 2010},
		{"b2", "Words of Radiance", "Sanderson", "m4b", 2014},
		{"b3", "The Fifth Season", "Jemisin", "mp3", 2015},
	}
	for _, r := range seedRows {
		year := r.year
		if _, err := store.CreateBook(&database.Book{
			ID: r.id, Title: r.title, FilePath: "/tmp/" + r.id, Format: r.format, PrintYear: &year,
		}); err != nil {
			t.Fatalf("seed book: %v", err)
		}
		_ = idx.IndexBook(search.BookDocument{
			BookID: r.id, Title: r.title, Author: r.author, Format: r.format, Year: r.year,
		})
	}
	return srv
}

// decodeJSON is a shared helper for decoding gin test responses.
func decodeJSON(t *testing.T, body *bytes.Buffer, v any) {
	t.Helper()
	if err := json.Unmarshal(body.Bytes(), v); err != nil {
		t.Fatalf("decode response: %v (body=%s)", err, body.String())
	}
}

func doJSONReq(srv *Server, method, path string, payload any) *httptest.ResponseRecorder {
	var body []byte
	if payload != nil {
		body, _ = json.Marshal(payload)
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)
	return w
}

func TestPlaylist_CreateAndGetStatic(t *testing.T) {
	gin.SetMode(gin.TestMode)
	srv := setupPlaylistTestServer(t)

	w := doJSONReq(srv, http.MethodPost, "/api/v1/playlists", gin.H{
		"name":     "My Faves",
		"type":     "static",
		"book_ids": []string{"b1", "b2"},
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var created struct {
		Data *database.UserPlaylist `json:"data"`
	}
	decodeJSON(t, w.Body, &created)
	if created.Data == nil || created.Data.ID == "" {
		t.Fatalf("missing playlist in response: %+v", created)
	}

	// GET returns both the playlist and the ordered book_ids.
	w = doJSONReq(srv, http.MethodGet, "/api/v1/playlists/"+created.Data.ID, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("get: %d %s", w.Code, w.Body.String())
	}
	var got struct {
		Data struct {
			Playlist *database.UserPlaylist `json:"playlist"`
			BookIDs  []string               `json:"book_ids"`
		} `json:"data"`
	}
	decodeJSON(t, w.Body, &got)
	if len(got.Data.BookIDs) != 2 || got.Data.BookIDs[0] != "b1" {
		t.Errorf("book_ids = %v, want [b1 b2]", got.Data.BookIDs)
	}
}

func TestPlaylist_CreateRejectsBadType(t *testing.T) {
	gin.SetMode(gin.TestMode)
	srv := setupPlaylistTestServer(t)

	w := doJSONReq(srv, http.MethodPost, "/api/v1/playlists", gin.H{
		"name": "bad", "type": "weird",
	})
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 on bad type, got %d", w.Code)
	}
}

func TestPlaylist_CreateSmartValidatesQuery(t *testing.T) {
	gin.SetMode(gin.TestMode)
	srv := setupPlaylistTestServer(t)

	// Missing query.
	w := doJSONReq(srv, http.MethodPost, "/api/v1/playlists", gin.H{
		"name": "no-query", "type": "smart",
	})
	if w.Code != http.StatusBadRequest {
		t.Errorf("want 400 for smart w/o query, got %d", w.Code)
	}

	// Bad query.
	w = doJSONReq(srv, http.MethodPost, "/api/v1/playlists", gin.H{
		"name": "bad-query", "type": "smart", "query": `title:"unterminated`,
	})
	if w.Code != http.StatusBadRequest {
		t.Errorf("want 400 for invalid query, got %d", w.Code)
	}
}

func TestPlaylist_GetSmartEvaluates(t *testing.T) {
	gin.SetMode(gin.TestMode)
	srv := setupPlaylistTestServer(t)

	w := doJSONReq(srv, http.MethodPost, "/api/v1/playlists", gin.H{
		"name": "Sanderson Only", "type": "smart", "query": "author:sanderson",
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var created struct {
		Data *database.UserPlaylist `json:"data"`
	}
	decodeJSON(t, w.Body, &created)

	w = doJSONReq(srv, http.MethodGet, "/api/v1/playlists/"+created.Data.ID, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("get: %d %s", w.Code, w.Body.String())
	}
	var got struct {
		Data struct {
			BookIDs []string `json:"book_ids"`
		} `json:"data"`
	}
	decodeJSON(t, w.Body, &got)
	if len(got.Data.BookIDs) != 2 {
		t.Errorf("smart playlist eval returned %d books, want 2", len(got.Data.BookIDs))
	}

	// Materialized cache should have been persisted.
	pl, _ := srv.Store().GetUserPlaylist(created.Data.ID)
	if len(pl.MaterializedBookIDs) != 2 {
		t.Errorf("materialized cache = %v, want 2 entries", pl.MaterializedBookIDs)
	}
}

func TestPlaylist_UpdateStatic(t *testing.T) {
	gin.SetMode(gin.TestMode)
	srv := setupPlaylistTestServer(t)

	w := doJSONReq(srv, http.MethodPost, "/api/v1/playlists", gin.H{
		"name": "rename-me", "type": "static", "book_ids": []string{"b1"},
	})
	var created struct {
		Data *database.UserPlaylist `json:"data"`
	}
	decodeJSON(t, w.Body, &created)

	newName := "renamed"
	w = doJSONReq(srv, http.MethodPut, "/api/v1/playlists/"+created.Data.ID, gin.H{
		"name":     newName,
		"book_ids": []string{"b1", "b2"},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("update: %d %s", w.Code, w.Body.String())
	}

	// Changing query on a static playlist is a type-mismatch error.
	w = doJSONReq(srv, http.MethodPut, "/api/v1/playlists/"+created.Data.ID, gin.H{
		"query": "author:sanderson",
	})
	if w.Code != http.StatusBadRequest {
		t.Errorf("want 400 on query-on-static, got %d", w.Code)
	}
}

func TestPlaylist_AddAndRemoveBooks(t *testing.T) {
	gin.SetMode(gin.TestMode)
	srv := setupPlaylistTestServer(t)

	w := doJSONReq(srv, http.MethodPost, "/api/v1/playlists", gin.H{
		"name": "book-ops", "type": "static", "book_ids": []string{"b1"},
	})
	var created struct {
		Data *database.UserPlaylist `json:"data"`
	}
	decodeJSON(t, w.Body, &created)

	// Add b2 + b3 (and a dupe of b1, should be deduped).
	w = doJSONReq(srv, http.MethodPost, "/api/v1/playlists/"+created.Data.ID+"/books", gin.H{
		"book_ids": []string{"b2", "b3", "b1"},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("add: %d %s", w.Code, w.Body.String())
	}
	var after struct {
		Data *database.UserPlaylist `json:"data"`
	}
	decodeJSON(t, w.Body, &after)
	if len(after.Data.BookIDs) != 3 {
		t.Errorf("after add = %v, want 3 ids", after.Data.BookIDs)
	}

	// Remove b2.
	w = doJSONReq(srv, http.MethodDelete, "/api/v1/playlists/"+created.Data.ID+"/books/b2", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("remove: %d %s", w.Code, w.Body.String())
	}
	decodeJSON(t, w.Body, &after)
	if len(after.Data.BookIDs) != 2 {
		t.Errorf("after remove = %v, want 2 ids", after.Data.BookIDs)
	}
	for _, id := range after.Data.BookIDs {
		if id == "b2" {
			t.Errorf("b2 still present after remove")
		}
	}
}

func TestPlaylist_Reorder(t *testing.T) {
	gin.SetMode(gin.TestMode)
	srv := setupPlaylistTestServer(t)

	w := doJSONReq(srv, http.MethodPost, "/api/v1/playlists", gin.H{
		"name": "reorder-me", "type": "static", "book_ids": []string{"b1", "b2", "b3"},
	})
	var created struct {
		Data *database.UserPlaylist `json:"data"`
	}
	decodeJSON(t, w.Body, &created)

	// Valid reorder — same set.
	w = doJSONReq(srv, http.MethodPost, "/api/v1/playlists/"+created.Data.ID+"/reorder", gin.H{
		"book_ids": []string{"b3", "b1", "b2"},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("reorder: %d %s", w.Code, w.Body.String())
	}
	var reordered struct {
		Data *database.UserPlaylist `json:"data"`
	}
	decodeJSON(t, w.Body, &reordered)
	if reordered.Data.BookIDs[0] != "b3" {
		t.Errorf("reordered first = %q, want b3", reordered.Data.BookIDs[0])
	}

	// Invalid reorder — different set.
	w = doJSONReq(srv, http.MethodPost, "/api/v1/playlists/"+created.Data.ID+"/reorder", gin.H{
		"book_ids": []string{"b1", "b2"},
	})
	if w.Code != http.StatusBadRequest {
		t.Errorf("want 400 on set-change reorder, got %d", w.Code)
	}
}

func TestPlaylist_Materialize(t *testing.T) {
	gin.SetMode(gin.TestMode)
	srv := setupPlaylistTestServer(t)

	// Create smart playlist.
	w := doJSONReq(srv, http.MethodPost, "/api/v1/playlists", gin.H{
		"name": "Sanderson Live", "type": "smart", "query": "author:sanderson",
	})
	var smart struct {
		Data *database.UserPlaylist `json:"data"`
	}
	decodeJSON(t, w.Body, &smart)

	// Materialize.
	w = doJSONReq(srv, http.MethodPost, "/api/v1/playlists/"+smart.Data.ID+"/materialize", nil)
	if w.Code != http.StatusCreated {
		t.Fatalf("materialize: %d %s", w.Code, w.Body.String())
	}
	var materialized struct {
		Data *database.UserPlaylist `json:"data"`
	}
	decodeJSON(t, w.Body, &materialized)
	if materialized.Data.Type != database.UserPlaylistTypeStatic {
		t.Errorf("materialized type = %q, want static", materialized.Data.Type)
	}
	if len(materialized.Data.BookIDs) != 2 {
		t.Errorf("materialized book_ids = %v, want 2", materialized.Data.BookIDs)
	}

	// Original smart playlist is still there and still smart.
	src, _ := srv.Store().GetUserPlaylist(smart.Data.ID)
	if src == nil || src.Type != database.UserPlaylistTypeSmart {
		t.Errorf("source playlist changed or missing: %+v", src)
	}
}

func TestPlaylist_Delete(t *testing.T) {
	gin.SetMode(gin.TestMode)
	srv := setupPlaylistTestServer(t)

	w := doJSONReq(srv, http.MethodPost, "/api/v1/playlists", gin.H{
		"name": "delete-me", "type": "static",
	})
	var created struct {
		Data *database.UserPlaylist `json:"data"`
	}
	decodeJSON(t, w.Body, &created)

	w = doJSONReq(srv, http.MethodDelete, "/api/v1/playlists/"+created.Data.ID, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", w.Code, w.Body.String())
	}

	// Subsequent GET should 404.
	w = doJSONReq(srv, http.MethodGet, "/api/v1/playlists/"+created.Data.ID, nil)
	if w.Code != http.StatusNotFound {
		t.Errorf("after delete GET = %d, want 404", w.Code)
	}
}

func TestPlaylist_ListFiltering(t *testing.T) {
	gin.SetMode(gin.TestMode)
	srv := setupPlaylistTestServer(t)

	_ = doJSONReq(srv, http.MethodPost, "/api/v1/playlists", gin.H{"name": "a", "type": "static"})
	_ = doJSONReq(srv, http.MethodPost, "/api/v1/playlists", gin.H{"name": "b", "type": "static"})
	_ = doJSONReq(srv, http.MethodPost, "/api/v1/playlists", gin.H{"name": "c", "type": "smart", "query": "*"})

	w := doJSONReq(srv, http.MethodGet, "/api/v1/playlists?type=static", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("list: %d %s", w.Code, w.Body.String())
	}
	var listResp struct {
		Items  []database.UserPlaylist `json:"items"`
		Count  int                     `json:"count"`
		Limit  int                     `json:"limit"`
		Offset int                     `json:"offset"`
	}
	decodeJSON(t, w.Body, &listResp)
	if listResp.Count != 2 {
		t.Errorf("static count = %d, want 2", listResp.Count)
	}

	w = doJSONReq(srv, http.MethodGet, "/api/v1/playlists?type=smart", nil)
	decodeJSON(t, w.Body, &listResp)
	if listResp.Count != 1 {
		t.Errorf("smart count = %d, want 1", listResp.Count)
	}
}
