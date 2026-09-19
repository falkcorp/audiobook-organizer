// file: internal/audiobooks/purge_parent_cleanup_boundary_test.go
// version: 1.1.0
// guid: 0a9c4e7b-8f13-4d26-b5a0-d17e3c6f98b2
// last-edited: 2026-09-19

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
	// The book owns no book_file rows and its directory is empty: a book that
	// owns rows is refused outright (purge_orphans_test.go), so the empty
	// directory is the only directory shape the purge still removes.
	softDeleted(t, store, "dirbook", bookDir)

	res, err := svc.PurgeSoftDeletedBooks(context.Background(), true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.FilesDeleted != 1 {
		t.Fatalf("FilesDeleted = %d, want 1 (errors: %v)", res.FilesDeleted, res.Errors)
	}
	if _, err := os.Stat(bookDir); !os.IsNotExist(err) {
		t.Fatalf("empty book dir %s not removed: %v", bookDir, err)
	}
	if _, err := os.Stat(parent); err != nil {
		t.Errorf("parent %s outside RootDir was removed: %v", parent, err)
	}
}

// A directory book that still owns a book_file row is not purged: its segment
// stays on disk and its row keeps a book to belong to.
func TestPurge_DirectoryBookOwningFilesIsRefused(t *testing.T) {
	svc, store, base := setupPurgeBoundary(t)
	bookDir := filepath.Join(base, "lib", "Author", "Book")
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
	if res.Purged != 0 || res.SkippedOwnsFiles != 1 || res.FilesDeleted != 0 {
		t.Fatalf("result = %+v, want Purged=0 SkippedOwnsFiles=1 FilesDeleted=0", res)
	}
	if _, err := os.Stat(seg); err != nil {
		t.Errorf("segment %s removed from disk: %v", seg, err)
	}
	if b, _ := store.GetBookByID("dirbook"); b == nil {
		t.Error("book owning a file row was hard-deleted")
	}
}
