// file: internal/server/version_lifecycle_test.go
// version: 1.6.3
// guid: 3a4b5c6d-7e8f-9a0b-1c2d-3e4f5a6b7c8d
// last-edited: 2026-10-27

package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/versions"
	"github.com/gin-gonic/gin"
)

func setupVersionLifecycleServer(t *testing.T) (*Server, database.Store) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	pebblePath := filepath.Join(t.TempDir(), "pebble")
	store, err := database.NewPebbleStoreInMemory(pebblePath)
	if err != nil {
		t.Fatalf("open pebble: %v", err)
	}
	store.WaitForWarmup() // PurgeVersion's BookFilesAtPath guard needs the memdb
	origStore := database.GetGlobalStore()
	database.SetGlobalStore(store)
	t.Cleanup(func() {
		database.SetGlobalStore(origStore)
		store.Close()
	})

	// A version purge deletes files only inside the library root (the book
	// purge's rule), so the server under test has one.
	prevRoot := config.AppConfig.RootDir
	config.AppConfig.RootDir = filepath.Join(t.TempDir(), "lib")
	if err := os.MkdirAll(config.AppConfig.RootDir, 0o755); err != nil {
		t.Fatalf("library root: %v", err)
	}
	t.Cleanup(func() { config.AppConfig.RootDir = prevRoot })

	srv := newTestServer(t, store)
	return srv, store
}

// libraryDir makes a folder inside the test server's library root (/lib/Gotham
// in the stories the tests tell), where a purge is allowed to delete.
func libraryDir(t *testing.T, name string) string {
	t.Helper()
	dir := filepath.Join(config.AppConfig.RootDir, "Gotham", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("library dir: %v", err)
	}
	return dir
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

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/audiobooks/b1/versions/"+ver.ID, nil)
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
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/audiobooks/b2/versions/"+ver.ID, nil)
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

	req := httptest.NewRequest(http.MethodPost, "/api/v1/audiobooks/b1/versions/"+ver.ID+"/restore", nil)
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

	req := httptest.NewRequest(http.MethodPost, "/api/v1/audiobooks/b1/versions/"+ver.ID+"/restore", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for non-trash version, got %d", w.Code)
	}
}

// TestHandleTrashRestoreVersion_CanonicalAudiobooksPath walks the whole
// /audiobooks/:id/versions/:vid... lifecycle (trash, restore, re-trash,
// purge-now) through the real router. The former /books/:id/versions/:vid...
// aliases answer 410 now (wire_versions_gone_test.go).
func TestHandleTrashRestoreVersion_CanonicalAudiobooksPath(t *testing.T) {
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

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/audiobooks/b1/versions/"+ver.ID, nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("DELETE versions: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	updated, _ := store.GetBookVersion(ver.ID)
	if updated.Status != database.BookVersionStatusTrash {
		t.Errorf("expected status trash, got %s", updated.Status)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/v1/audiobooks/b1/versions/"+ver.ID+"/restore", nil)
	w = httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("POST restore: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	updated, _ = store.GetBookVersion(ver.ID)
	if updated.Status != database.BookVersionStatusAlt {
		t.Errorf("expected status alt, got %s", updated.Status)
	}

	// purge-now only takes a trashed version, so trash the restored alt first.
	req = httptest.NewRequest(http.MethodDelete, "/api/v1/audiobooks/b1/versions/"+ver.ID, nil)
	w = httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("DELETE versions (re-trash): expected 200, got %d: %s", w.Code, w.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/api/v1/audiobooks/b1/versions/"+ver.ID+"/purge-now", nil)
	w = httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("POST purge-now: expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

// TestHandleTrashVersion_StampsTrashedAt: the trash handler is the one path
// into the trash, and the 14-day window counts from the stamp it leaves.
// Restore must clear it.
func TestHandleTrashVersion_StampsTrashedAt(t *testing.T) {
	srv, store := setupVersionLifecycleServer(t)

	_, _ = store.CreateBook(&database.Book{ID: "b1", Title: "Test Book", FilePath: "/tmp/b1.m4b"})
	ver, err := store.CreateBookVersion(&database.BookVersion{
		BookID: "b1", Status: database.BookVersionStatusAlt, Format: "m4b", Source: "imported",
		// Created long before it is trashed: the window must not count from here.
		CreatedAt: time.Now().Add(-30 * 24 * time.Hour),
	})
	if err != nil {
		t.Fatalf("create version: %v", err)
	}

	before := time.Now()
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/audiobooks/b1/versions/"+ver.ID, nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("DELETE: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	after := time.Now()

	got, _ := store.GetBookVersion(ver.ID)
	if got.TrashedAt == nil {
		t.Fatal("trashed version has no TrashedAt")
	}
	if got.TrashedAt.Before(before) || got.TrashedAt.After(after) {
		t.Errorf("TrashedAt = %v, want within [%v, %v]", got.TrashedAt, before, after)
	}

	// The daily cleanup must leave it alone: it was trashed seconds ago.
	if res, _ := CleanupTrashedVersions(store, srv.versionPurgeGuard()); res.Purged != 0 {
		t.Fatalf("cleanup purged %d version(s) trashed seconds ago", res.Purged)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/v1/audiobooks/b1/versions/"+ver.ID+"/restore", nil)
	w = httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("restore: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	got, _ = store.GetBookVersion(ver.ID)
	if got.TrashedAt != nil {
		t.Errorf("restored version still has TrashedAt %v", got.TrashedAt)
	}
}

// TestHandlePurgeVersion_RefusesUntrashed: "Purge now" deletes files from
// disk. On an active version that meant the book's playable files; on an alt
// it skipped the trash window entirely. Both are refused, and the file stays.
func TestHandlePurgeVersion_RefusesUntrashed(t *testing.T) {
	for _, status := range []string{
		database.BookVersionStatusActive,
		database.BookVersionStatusAlt,
		database.BookVersionStatusPending,
		database.BookVersionStatusSwappingIn,
		database.BookVersionStatusSwappingOut,
		database.BookVersionStatusInactivePurged,
		database.BookVersionStatusBlockedForRedownload,
	} {
		t.Run(status, func(t *testing.T) {
			srv, store := setupVersionLifecycleServer(t)

			bookDir := t.TempDir()
			audio := filepath.Join(bookDir, "book.m4b")
			if err := os.WriteFile(audio, []byte("audio"), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := store.CreateBook(&database.Book{ID: "b1", Title: "Test Book", FilePath: audio}); err != nil {
				t.Fatalf("create book: %v", err)
			}
			ver, err := store.CreateBookVersion(&database.BookVersion{
				BookID: "b1", Status: status, Format: "m4b", Source: "imported",
			})
			if err != nil {
				t.Fatalf("create version: %v", err)
			}
			if err := store.CreateBookFile(&database.BookFile{
				BookID: "b1", FilePath: audio, VersionID: ver.ID, TrackNumber: 1,
			}); err != nil {
				t.Fatalf("create book file: %v", err)
			}

			req := httptest.NewRequest(http.MethodPost, "/api/v1/audiobooks/b1/versions/"+ver.ID+"/purge-now", nil)
			w := httptest.NewRecorder()
			srv.router.ServeHTTP(w, req)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("purge-now on %s: expected 400, got %d: %s", status, w.Code, w.Body.String())
			}
			if _, err := os.Stat(audio); err != nil {
				t.Errorf("purge-now on %s removed the file: %v", status, err)
			}
			got, _ := store.GetBookVersion(ver.ID)
			if got.Status != status {
				t.Errorf("status = %s, want unchanged %s", got.Status, status)
			}
		})
	}
}

// TestHandlePurgeVersion_TrashedPurgesFilesKeepsRows: the allowed case.
// The version's files are removed from disk; its book_file row is kept.
func TestHandlePurgeVersion_TrashedPurgesFilesKeepsRows(t *testing.T) {
	srv, store := setupVersionLifecycleServer(t)

	bookDir := libraryDir(t, "Year One")
	audio := filepath.Join(bookDir, "book.m4b")
	if err := os.WriteFile(audio, []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateBook(&database.Book{ID: "b1", Title: "Test Book", FilePath: audio}); err != nil {
		t.Fatalf("create book: %v", err)
	}
	ver, err := store.CreateBookVersion(&database.BookVersion{
		BookID: "b1", Status: database.BookVersionStatusTrash, Format: "m4b", Source: "imported",
	})
	if err != nil {
		t.Fatalf("create version: %v", err)
	}
	if err := store.CreateBookFile(&database.BookFile{
		BookID: "b1", FilePath: audio, VersionID: ver.ID, TrackNumber: 1,
	}); err != nil {
		t.Fatalf("create book file: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/audiobooks/b1/versions/"+ver.ID+"/purge-now", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("purge-now: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(audio); !os.IsNotExist(err) {
		t.Errorf("purged version's file still on disk (stat err %v)", err)
	}
	got, _ := store.GetBookVersion(ver.ID)
	if got.Status != database.BookVersionStatusInactivePurged || got.TrashedAt != nil {
		t.Errorf("status=%s TrashedAt=%v, want inactive_purged and nil", got.Status, got.TrashedAt)
	}
	files, err := store.GetBookFiles("b1")
	if err != nil {
		t.Fatalf("get book files: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("book_file rows = %d, want 1: purge must not delete book_file rows", len(files))
	}
}

func TestAutoPromoteAlt(t *testing.T) {
	gin.SetMode(gin.TestMode)

	pebblePath := filepath.Join(t.TempDir(), "pebble")
	store, err := database.NewPebbleStoreInMemory(pebblePath)
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
		Data map[string]any `json:"data"`
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
