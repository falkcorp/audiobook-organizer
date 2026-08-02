// file: internal/server/cover_history_test.go
// version: 1.0.3
// guid: 5e6f7a8b-9c0d-1e2f-3a4b-5c6d7e8f9a0b
// last-edited: 2026-05-07

package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/covers"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/gin-gonic/gin"
)

func setupCoverHistoryServer(t *testing.T) (*Server, database.Store, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	rootDir := t.TempDir()
	origCfg := config.AppConfig
	config.AppConfig.RootDir = rootDir

	pebblePath := filepath.Join(t.TempDir(), "pebble")
	store, err := database.NewPebbleStore(pebblePath)
	if err != nil {
		t.Fatalf("open pebble: %v", err)
	}
	origStore := database.GetGlobalStore()
	database.SetGlobalStore(store)
	t.Cleanup(func() {
		config.AppConfig = origCfg
		database.SetGlobalStore(origStore)
		store.Close()
	})

	srv := NewServer(store)
	return srv, store, rootDir
}

func TestHandleListCoverHistory_Empty(t *testing.T) {
	srv, _, _ := setupCoverHistoryServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/audiobooks/b1/cover-history", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Covers []covers.CoverHistoryEntry `json:"covers"`
		Count  int                        `json:"count"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Count != 0 {
		t.Errorf("expected 0 covers, got %d", resp.Count)
	}
}

func TestHandleListCoverHistory_WithCovers(t *testing.T) {
	srv, _, rootDir := setupCoverHistoryServer(t)

	// Create cover history directory with some image files.
	histDir := filepath.Join(rootDir, "covers", "history", "b1")
	if err := os.MkdirAll(histDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Create test cover files.
	_ = os.WriteFile(filepath.Join(histDir, "cover-2025-01-01.jpg"), []byte("jpeg data"), 0o644)
	_ = os.WriteFile(filepath.Join(histDir, "cover-2025-02-01.png"), []byte("png data"), 0o644)
	_ = os.WriteFile(filepath.Join(histDir, "notes.txt"), []byte("not an image"), 0o644) // should be skipped

	req := httptest.NewRequest(http.MethodGet, "/api/v1/audiobooks/b1/cover-history", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var envelope struct {
		Data struct {
			Covers []covers.CoverHistoryEntry `json:"covers"`
			Count  int                        `json:"count"`
		} `json:"data"`
	}
	json.Unmarshal(w.Body.Bytes(), &envelope)
	if envelope.Data.Count != 2 {
		t.Errorf("expected 2 covers (skipping .txt), got %d", envelope.Data.Count)
	}
}

func TestHandleRestoreCover(t *testing.T) {
	srv, store, rootDir := setupCoverHistoryServer(t)

	_, _ = store.CreateBook(&database.Book{
		ID: "b1", Title: "Test Book", FilePath: "/tmp/b1",
	})

	// Create cover history file.
	histDir := filepath.Join(rootDir, "covers", "history", "b1")
	_ = os.MkdirAll(histDir, 0o755)
	coverData := []byte("restored cover data")
	_ = os.WriteFile(filepath.Join(histDir, "old-cover.jpg"), coverData, 0o644)

	// Ensure covers dir exists.
	_ = os.MkdirAll(filepath.Join(rootDir, "covers"), 0o755)

	body, _ := json.Marshal(map[string]string{"filename": "old-cover.jpg"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/audiobooks/b1/cover-history/restore", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Verify the cover was copied.
	restoredPath := filepath.Join(rootDir, "covers", "b1.jpg")
	data, err := os.ReadFile(restoredPath)
	if err != nil {
		t.Fatalf("read restored cover: %v", err)
	}
	if string(data) != string(coverData) {
		t.Error("restored cover data mismatch")
	}
}

func TestHandleRestoreCover_BookNotFound(t *testing.T) {
	srv, _, _ := setupCoverHistoryServer(t)

	body, _ := json.Marshal(map[string]string{"filename": "cover.jpg"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/audiobooks/nonexistent/cover-history/restore", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestHandleRestoreCover_CoverFileNotFound(t *testing.T) {
	srv, store, _ := setupCoverHistoryServer(t)

	_, _ = store.CreateBook(&database.Book{
		ID: "b1", Title: "Test Book", FilePath: "/tmp/b1",
	})

	body, _ := json.Marshal(map[string]string{"filename": "nonexistent.jpg"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/audiobooks/b1/cover-history/restore", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}
