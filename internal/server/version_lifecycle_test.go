// file: internal/server/version_lifecycle_test.go
// version: 1.1.2
// guid: 3a4b5c6d-7e8f-9a0b-1c2d-3e4f5a6b7c8d
// last-edited: 2026-05-03

package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/versions"
	"github.com/gin-gonic/gin"
)

func setupVersionLifecycleServer(t *testing.T) (*Server, database.Store) {
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

func TestHandleTrashVersion(t *testing.T) {
	srv, store := setupVersionLifecycleServer(t)

	_, err := store.CreateBook(&database.Book{
		ID: "b1", Title: "Test Book", FilePath: "/tmp/b1.m4b", Format: "m4b",
	})
	if err != nil {
		t.Fatalf("create book: %v", err)
	}
	ver, err := store.CreateBookVersion(&database.BookVersion{
		BookID: "b1", Status: database.BookVersionStatusActive, Format: "m4b", Source: "imported",
	})
	if err != nil {
		t.Fatalf("create version: %v", err)
	}

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/books/b1/versions/"+ver.ID, nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	updated, _ := store.GetBookVersion(ver.ID)
	if updated.Status != database.BookVersionStatusTrash {
		t.Errorf("expected status trash, got %s", updated.Status)
	}
}

func TestHandleTrashVersion_BookMismatch(t *testing.T) {
	srv, store := setupVersionLifecycleServer(t)

	_, _ = store.CreateBook(&database.Book{ID: "b1", Title: "Book 1", FilePath: "/tmp/b1"})
	_, _ = store.CreateBook(&database.Book{ID: "b2", Title: "Book 2", FilePath: "/tmp/b2"})
	ver, _ := store.CreateBookVersion(&database.BookVersion{
		BookID: "b1", Status: database.BookVersionStatusActive, Format: "m4b", Source: "imported",
	})

	// Try to trash with wrong book ID
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/books/b2/versions/"+ver.ID, nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for mismatch, got %d", w.Code)
	}
}

func TestHandleRestoreVersion(t *testing.T) {
	srv, store := setupVersionLifecycleServer(t)

	_, _ = store.CreateBook(&database.Book{ID: "b1", Title: "Test Book", FilePath: "/tmp/b1"})
	ver, _ := store.CreateBookVersion(&database.BookVersion{
		BookID: "b1", Status: database.BookVersionStatusTrash, Format: "m4b", Source: "imported",
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/books/b1/versions/"+ver.ID+"/restore", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	updated, _ := store.GetBookVersion(ver.ID)
	if updated.Status != database.BookVersionStatusAlt {
		t.Errorf("expected status alt, got %s", updated.Status)
	}
}

func TestHandleRestoreVersion_NotInTrash(t *testing.T) {
	srv, store := setupVersionLifecycleServer(t)

	_, _ = store.CreateBook(&database.Book{ID: "b1", Title: "Test Book", FilePath: "/tmp/b1"})
	ver, _ := store.CreateBookVersion(&database.BookVersion{
		BookID: "b1", Status: database.BookVersionStatusActive, Format: "m4b", Source: "imported",
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/books/b1/versions/"+ver.ID+"/restore", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for non-trash version, got %d", w.Code)
	}
}

func TestAutoPromoteAlt(t *testing.T) {
	gin.SetMode(gin.TestMode)

	pebblePath := filepath.Join(t.TempDir(), "pebble")
	store, err := database.NewPebbleStore(pebblePath)
	if err != nil {
		t.Fatalf("open pebble: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	_, _ = store.CreateBook(&database.Book{ID: "b1", Title: "Test Book", FilePath: "/tmp/b1"})

	// Create two alt versions with different ingest dates.
	older := time.Now().Add(-48 * time.Hour)
	newer := time.Now().Add(-1 * time.Hour)

	v1, _ := store.CreateBookVersion(&database.BookVersion{
		BookID: "b1", Status: database.BookVersionStatusAlt, Format: "mp3",
		Source: "imported", IngestDate: older,
	})
	v2, _ := store.CreateBookVersion(&database.BookVersion{
		BookID: "b1", Status: database.BookVersionStatusAlt, Format: "m4b",
		Source: "imported", IngestDate: newer,
	})

	if err := versions.AutoPromoteAlt(store, "b1"); err != nil {
		t.Fatalf("auto-promote: %v", err)
	}

	got1, _ := store.GetBookVersion(v1.ID)
	got2, _ := store.GetBookVersion(v2.ID)

	// The newer alt should become active.
	if got2.Status != database.BookVersionStatusActive {
		t.Errorf("newer version status = %s, want active", got2.Status)
	}
	if got1.Status != database.BookVersionStatusAlt {
		t.Errorf("older version status = %s, want alt", got1.Status)
	}
}

func TestHandleHardDeleteVersion(t *testing.T) {
	srv, store := setupVersionLifecycleServer(t)

	_, _ = store.CreateBook(&database.Book{ID: "b1", Title: "Test Book", FilePath: "/tmp/b1"})
	ver, _ := store.CreateBookVersion(&database.BookVersion{
		BookID: "b1", Status: database.BookVersionStatusInactivePurged, Format: "m4b", Source: "imported",
	})

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/purged-versions/"+ver.ID, nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var envelope struct {
		Data map[string]interface{} `json:"data"`
	}
	json.Unmarshal(w.Body.Bytes(), &envelope)
	if envelope.Data["deleted"] != ver.ID {
		t.Errorf("expected deleted=%s, got %v", ver.ID, envelope.Data["deleted"])
	}

	// Should be gone from store.
	gone, _ := store.GetBookVersion(ver.ID)
	if gone != nil {
		t.Error("version should be deleted")
	}
}

func TestHandleHardDeleteVersion_NotPurged(t *testing.T) {
	srv, store := setupVersionLifecycleServer(t)

	_, _ = store.CreateBook(&database.Book{ID: "b1", Title: "Test Book", FilePath: "/tmp/b1"})
	ver, _ := store.CreateBookVersion(&database.BookVersion{
		BookID: "b1", Status: database.BookVersionStatusActive, Format: "m4b", Source: "imported",
	})

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/purged-versions/"+ver.ID, nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for non-purged, got %d", w.Code)
	}
}
