// file: internal/audiobooks/purge_parent_cleanup_boundary_test.go
// version: 1.2.0
// guid: 0a9c4e7b-8f13-4d26-b5a0-d17e3c6f98b2
// last-edited: 2026-10-06

package audiobooks

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// After purging a book's files, PurgeSoftDeletedBooks removes the parent
// directories it emptied, up to RootDir. A path in a SIBLING of the root
// ("<base>/lib2" next to "<base>/lib") is not under RootDir. Until 2026-10-06
// such a file was deleted (only its parents were spared); now nothing outside
// the root is removed from disk at all (purgePathInsideRoot), and the purge
// reports the file it kept.

func setupPurgeBoundary(t *testing.T) (*AudiobookService, *database.PebbleStore, string) {
	t.Helper()
	store, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	// The purge's book_file lookup is fail-closed until memdb publishes, and
	// warmup is async: purging straight away failed with "memdb is not
	// serving" (FLAKE-PURGEWARMUP).
	store.WaitForWarmup()
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

// requireKeptOutsideRoot asserts the purge removed the row but nothing on
// disk, and reported why.
func requireKeptOutsideRoot(t *testing.T, res *PurgeResult, store *database.PebbleStore, id, path, reason string) {
	t.Helper()
	if res.Purged != 1 || res.FilesDeleted != 0 {
		t.Fatalf("result = %+v, want Purged=1 FilesDeleted=0", res)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("%s removed from disk: %v", path, err)
	}
	if b, _ := store.GetBookByID(id); b != nil {
		t.Errorf("book %s row still present; the row purge is not what is refused", id)
	}
	found := false
	for _, e := range res.Errors {
		if strings.Contains(e, id+": book purged, file kept:") && strings.Contains(e, reason) {
			found = true
		}
	}
	if !found {
		t.Errorf("errors %q do not report %s kept (%q)", res.Errors, path, reason)
	}
}

func TestPurge_SingleFileInSiblingIsKeptAndReported(t *testing.T) {
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
	requireKeptOutsideRoot(t, res, store, "single", file, "outside the library root")
}

func TestPurge_DirectoryBookInSiblingIsKeptAndReported(t *testing.T) {
	svc, store, base := setupPurgeBoundary(t)
	bookDir := filepath.Join(base, "lib2", "Author", "Book")
	if err := os.MkdirAll(bookDir, 0o755); err != nil {
		t.Fatal(err)
	}
	softDeleted(t, store, "dirbook", bookDir)

	res, err := svc.PurgeSoftDeletedBooks(context.Background(), true, nil)
	if err != nil {
		t.Fatal(err)
	}
	requireKeptOutsideRoot(t, res, store, "dirbook", bookDir, "outside the library root")
}

// A file far from the root (not a sibling: an unrelated directory) is kept.
func TestPurge_FileOutsideRootIsKeptAndReported(t *testing.T) {
	svc, store, _ := setupPurgeBoundary(t)
	elsewhere := t.TempDir()
	file := filepath.Join(elsewhere, "downloads", "a.m4b")
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	softDeleted(t, store, "outside", file)

	res, err := svc.PurgeSoftDeletedBooks(context.Background(), true, nil)
	if err != nil {
		t.Fatal(err)
	}
	requireKeptOutsideRoot(t, res, store, "outside", file, "outside the library root")
}

// A ".." escape that names a file outside the root through the root's own
// prefix is outside, however it is spelled.
func TestPurge_DotDotEscapeIsKeptAndReported(t *testing.T) {
	svc, store, base := setupPurgeBoundary(t)
	file := filepath.Join(base, "secret.m4b")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	escaped := config.AppConfig.RootDir + "/Author/../../secret.m4b"
	softDeleted(t, store, "escape", escaped)

	res, err := svc.PurgeSoftDeletedBooks(context.Background(), true, nil)
	if err != nil {
		t.Fatal(err)
	}
	requireKeptOutsideRoot(t, res, store, "escape", file, "outside the library root")
}

// A path inside the root lexically that resolves outside it through a
// symlinked directory is kept.
func TestPurge_SymlinkEscapeIsKeptAndReported(t *testing.T) {
	svc, store, base := setupPurgeBoundary(t)
	outside := filepath.Join(base, "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(outside, "a.m4b")
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(config.AppConfig.RootDir, "Linked")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	softDeleted(t, store, "symlinked", filepath.Join(link, "a.m4b"))

	res, err := svc.PurgeSoftDeletedBooks(context.Background(), true, nil)
	if err != nil {
		t.Fatal(err)
	}
	requireKeptOutsideRoot(t, res, store, "symlinked", target, "does not resolve inside the library root")
}

// The library root itself, as a book's directory, is never removed even when
// it is empty.
func TestPurge_RootItselfIsKeptAndReported(t *testing.T) {
	svc, store, _ := setupPurgeBoundary(t)
	root := config.AppConfig.RootDir
	softDeleted(t, store, "rootbook", root)

	res, err := svc.PurgeSoftDeletedBooks(context.Background(), true, nil)
	if err != nil {
		t.Fatal(err)
	}
	requireKeptOutsideRoot(t, res, store, "rootbook", root, "is the library root itself")
}

// No RootDir configured: nothing is inside it, so nothing is deleted.
func TestPurge_NoRootConfiguredDeletesNothing(t *testing.T) {
	svc, store, base := setupPurgeBoundary(t)
	file := filepath.Join(base, "lib", "a.m4b")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	config.AppConfig.RootDir = ""
	softDeleted(t, store, "noroot", file)

	res, err := svc.PurgeSoftDeletedBooks(context.Background(), true, nil)
	if err != nil {
		t.Fatal(err)
	}
	requireKeptOutsideRoot(t, res, store, "noroot", file, "no library root")
}

// Inside the root the file is removed, and the parents it emptied are
// removed up to -- not including -- the root.
func TestPurge_InsideRootRemovesFileAndParentsUpToRoot(t *testing.T) {
	svc, store, _ := setupPurgeBoundary(t)
	root := config.AppConfig.RootDir
	parent := filepath.Join(root, "Author", "Series")
	if err := os.MkdirAll(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(parent, "a.m4b")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	softDeleted(t, store, "inside", file)

	res, err := svc.PurgeSoftDeletedBooks(context.Background(), true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.FilesDeleted != 1 || len(res.Errors) != 0 {
		t.Fatalf("result = %+v, want FilesDeleted=1 and no errors", res)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatalf("%s not removed: %v", file, err)
	}
	if _, err := os.Stat(filepath.Join(root, "Author")); !os.IsNotExist(err) {
		t.Errorf("emptied parent %s not removed: %v", filepath.Join(root, "Author"), err)
	}
	if _, err := os.Stat(root); err != nil {
		t.Errorf("library root removed: %v", err)
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
