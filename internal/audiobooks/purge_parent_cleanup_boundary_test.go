// file: internal/audiobooks/purge_parent_cleanup_boundary_test.go
// version: 1.0.0
// guid: 0a9c4e7b-8f13-4d26-b5a0-d17e3c6f98b2
// last-edited: 2026-09-12

package audiobooks

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// After purging a book's files, PurgeSoftDeletedBooks removes the parent
// directories it emptied, up to RootDir. A directory in a SIBLING of the root
// ("<base>/lib2" next to "<base>/lib") is not under RootDir, so its parents
// must be left alone. A bare prefix check walked up and deleted them.

func setupPurgeBoundary(t *testing.T) (*AudiobookService, *database.PebbleStore, string) {
	t.Helper()
	store, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	prev := config.AppConfig.RootDir
	t.Cleanup(func() { config.AppConfig.RootDir = prev })
	base := t.TempDir()
	config.AppConfig.RootDir = filepath.Join(base, "lib")
	if err := os.MkdirAll(config.AppConfig.RootDir, 0o755); err != nil {
		t.Fatal(err)
	}
	return NewAudiobookService(store), store, base
}

func softDeleted(t *testing.T, store *database.PebbleStore, id, path string) {
	t.Helper()
	yes := true
	now := time.Now().Add(-time.Hour)
	if _, err := store.CreateBook(&database.Book{ID: id, Title: id, FilePath: path, MarkedForDeletion: &yes, MarkedForDeletionAt: &now}); err != nil {
		t.Fatal(err)
	}
}

func TestPurge_SingleFileInSiblingKeepsParents(t *testing.T) {
	svc, store, base := setupPurgeBoundary(t)
	parent := filepath.Join(base, "lib2", "Author")
	if err := os.MkdirAll(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(parent, "a.m4b")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	softDeleted(t, store, "single", file)

	res, err := svc.PurgeSoftDeletedBooks(context.Background(), true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.FilesDeleted != 1 {
		t.Fatalf("FilesDeleted = %d, want 1 (errors: %v)", res.FilesDeleted, res.Errors)
	}
	if _, err := os.Stat(parent); err != nil {
		t.Errorf("parent %s outside RootDir was removed: %v", parent, err)
	}
}

func TestPurge_DirectoryBookInSiblingKeepsParents(t *testing.T) {
	svc, store, base := setupPurgeBoundary(t)
	parent := filepath.Join(base, "lib2", "Author")
	bookDir := filepath.Join(parent, "Book")
	if err := os.MkdirAll(bookDir, 0o755); err != nil {
		t.Fatal(err)
	}
	seg := filepath.Join(bookDir, "01.mp3")
	if err := os.WriteFile(seg, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	softDeleted(t, store, "dirbook", bookDir)
	if err := store.CreateBookFile(&database.BookFile{ID: "dirbook-f", BookID: "dirbook", FilePath: seg}); err != nil {
		t.Fatal(err)
	}

	res, err := svc.PurgeSoftDeletedBooks(context.Background(), true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.FilesDeleted != 1 {
		t.Fatalf("FilesDeleted = %d, want 1 (errors: %v)", res.FilesDeleted, res.Errors)
	}
	if _, err := os.Stat(parent); err != nil {
		t.Errorf("parent %s outside RootDir was removed: %v", parent, err)
	}
}
