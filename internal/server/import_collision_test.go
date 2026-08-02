// file: internal/server/import_collision_test.go
// version: 1.0.3
// guid: 9c0d1e2f-3a4b-5c6d-7e8f-9a0b1c2d3e4f
// last-edited: 2026-06-17

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
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/gin-gonic/gin"
)

func setupImportCollisionServer(t *testing.T) (*Server, database.Store) {
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

	srv := NewServer(store)
	return srv, store
}

func TestHandleImportCollisionPreview_NoCollisions(t *testing.T) {
	srv, _ := setupImportCollisionServer(t)

	// Create a temp file that doesn't match anything.
	tmpFile := filepath.Join(t.TempDir(), "new-book.m4b")
	_ = os.WriteFile(tmpFile, []byte("brand new content"), 0o644)

	body, _ := json.Marshal(map[string]string{
		"file_path": tmpFile,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/import/collision-preview", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Collisions   []merge.CollisionCandidate `json:"collisions"`
		Count        int                        `json:"count"`
		HasCollision bool                       `json:"has_collision"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.HasCollision {
		t.Error("expected no collision for a brand new file")
	}
}

func TestHandleImportCollisionPreview_MissingFilePath(t *testing.T) {
	srv, _ := setupImportCollisionServer(t)

	body, _ := json.Marshal(map[string]string{})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/import/collision-preview", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing file_path, got %d", w.Code)
	}
}

func TestHandleImportCollisionPreview_FileHashCollision(t *testing.T) {
	srv, store := setupImportCollisionServer(t)

	// Create a temp file and compute its hash.
	tmpFile := filepath.Join(t.TempDir(), "existing.m4b")
	content := []byte("this is some audio content for hash matching")
	_ = os.WriteFile(tmpFile, content, 0o644)

	// Register the temp dir as an import path so the collision check's
	// file-hash + metadata steps pass the path allow-list gate.
	if _, err := store.CreateImportPath(filepath.Dir(tmpFile), "test-allow"); err != nil {
		t.Fatalf("allow import path: %v", err)
	}

	hash := merge.QuickHash(tmpFile)
	if hash == "" {
		t.Fatal("failed to compute hash")
	}

	// Create a book with matching file hash.
	_, _ = store.CreateBook(&database.Book{
		ID: "b-existing", Title: "Existing Book", FilePath: "/lib/existing.m4b",
		FileHash: &hash,
	})

	body, _ := json.Marshal(map[string]string{
		"file_path": tmpFile,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/import/collision-preview", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var envelope struct {
		Data struct {
			Collisions   []merge.CollisionCandidate `json:"collisions"`
			Count        int                        `json:"count"`
			HasCollision bool                       `json:"has_collision"`
		} `json:"data"`
	}
	json.Unmarshal(w.Body.Bytes(), &envelope)
	if !envelope.Data.HasCollision {
		t.Error("expected collision for matching file hash")
	}

	found := false
	for _, c := range envelope.Data.Collisions {
		if c.MatchType == "file_hash" && c.BookID == "b-existing" {
			found = true
		}
	}
	if !found {
		t.Error("expected file_hash collision candidate for b-existing")
	}
}

func TestQuickHash(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "test.bin")
	_ = os.WriteFile(tmpFile, []byte("hello world"), 0o644)

	h := merge.QuickHash(tmpFile)
	if h == "" {
		t.Error("expected non-empty hash")
	}

	// Same content should produce same hash.
	tmpFile2 := filepath.Join(t.TempDir(), "test2.bin")
	_ = os.WriteFile(tmpFile2, []byte("hello world"), 0o644)

	h2 := merge.QuickHash(tmpFile2)
	if h != h2 {
		t.Errorf("expected same hash, got %s vs %s", h, h2)
	}

	// Nonexistent file returns empty.
	if merge.QuickHash("/nonexistent/path") != "" {
		t.Error("expected empty hash for nonexistent file")
	}
}
