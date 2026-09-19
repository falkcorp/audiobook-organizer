// file: internal/audiobooks/purge_orphans_test.go
// version: 1.0.0
// guid: 3f6b1d8e-2a47-4c95-8e0b-7d4a9c2e51f6
// last-edited: 2026-09-19

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
	"github.com/falkcorp/audiobook-organizer/internal/merge"
)

// A dedup merge (merge.MergeBooks) soft-deletes the loser and deliberately
// leaves its book_file rows on it — the loser's audio is its own version. The
// scheduled auto-purge then hard-deleted the loser via store.DeleteBook, which
// tears down the book row and its indexes but never the book_file rows, so
// every file row the loser owned was left naming a book that no longer exists.
// These tests drive the real merge path and the real purge against a real
// PebbleStore and assert the invariant directly: no book_file row may name a
// book that has no row.

// assertNoOrphanBookFiles fails for every book_file row, owned by any of
// bookIDs, whose book has no row (live or soft-deleted).
//
// It reads Pebble (GetBookFiles is a book_file:<id>: prefix scan), NOT
// GetAllBookFilesCore: that is served from memdb, and DeleteBookFromMemDB drops
// the deleted book's file rows from the projection while leaving them in
// Pebble — so a memdb-based check cannot see the orphans DeleteBook creates
// until the next restart re-warms memdb from disk.
func assertNoOrphanBookFiles(t *testing.T, store *database.PebbleStore, bookIDs ...string) {
	t.Helper()
	seen := 0
	for _, id := range bookIDs {
		files, err := store.GetBookFiles(id)
		if err != nil {
			t.Fatalf("GetBookFiles(%s): %v", id, err)
		}
		seen += len(files)
		if len(files) == 0 {
			continue
		}
		b, err := store.GetBookByID(id)
		if err != nil {
			t.Fatalf("GetBookByID(%s): %v", id, err)
		}
		if b == nil {
			for _, f := range files {
				t.Errorf("orphaned book_file %s (%s): its book %s has no row", f.ID, f.FilePath, id)
			}
		}
	}
	if seen == 0 {
		t.Fatal("fixture is vacuous: none of the books own a book_file row")
	}
}

// mergeFixture creates two books, each with one real on-disk file under
// RootDir, merges them via the real merge service (keep wins), and ages the
// loser's soft-delete stamp past a 30-day cutoff.
func mergeFixture(t *testing.T, store *database.PebbleStore, root string, loserPath string) (keepID, loserID string, keepPath string) {
	t.Helper()
	keepPath = filepath.Join(root, "Keep", "keep.m4b")
	for _, p := range []string{keepPath, loserPath} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("audio"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	keep, err := store.CreateBook(&database.Book{Title: "Keep", FilePath: keepPath})
	if err != nil {
		t.Fatal(err)
	}
	loser, err := store.CreateBook(&database.Book{Title: "Loser", FilePath: loserPath})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateBookFile(&database.BookFile{BookID: keep.ID, FilePath: keepPath, FileSize: 5, Duration: 60}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateBookFile(&database.BookFile{BookID: loser.ID, FilePath: loserPath, FileSize: 5, Duration: 60}); err != nil {
		t.Fatal(err)
	}

	res, err := merge.NewService(store).MergeBooks([]string{keep.ID, loser.ID}, keep.ID)
	if err != nil {
		t.Fatalf("MergeBooks: %v", err)
	}
	if res.SoftDeleted != 1 {
		t.Fatalf("precondition: merge soft-deleted %d books, want 1", res.SoftDeleted)
	}
	old := time.Now().AddDate(0, 0, -40)
	if _, err := store.ModifyBook(loser.ID, func(b *database.Book) error {
		b.MarkedForDeletionAt = &old
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return keep.ID, loser.ID, keepPath
}

func TestPurge_MergeLoserNeverOrphansBookFiles(t *testing.T) {
	svc, store, _ := setupPurgeBoundary(t)
	lib := setupRoot(t)
	loserPath := filepath.Join(lib, "Loser", "loser.mp3")
	keepID, loserID, _ := mergeFixture(t, store, lib, loserPath)

	days := 30
	res, err := svc.PurgeSoftDeletedBooks(context.Background(), false, &days)
	if err != nil {
		t.Fatal(err)
	}
	if res.Attempted != 1 {
		t.Fatalf("precondition: purge attempted %d books, want the 1 aged loser", res.Attempted)
	}

	assertNoOrphanBookFiles(t, store, keepID, loserID)

	// The loser still owns its file row, so it must not have been purged and
	// the refusal must be reported, not swallowed.
	if res.Purged != 0 {
		t.Errorf("Purged = %d, want 0: the loser still owns a book_file row", res.Purged)
	}
	if b, _ := store.GetBookByID(loserID); b == nil {
		t.Errorf("loser %s was hard-deleted while it still owned book_file rows", loserID)
	}
	if !containsSubstr(res.Errors, loserID) {
		t.Errorf("refusal for %s not reported in PurgeResult.Errors: %v", loserID, res.Errors)
	}
}

// With delete-files enabled, a loser whose FilePath is the survivor's own file
// (duplicate book rows over one file — the commonest dedup merge) must never
// get that file os.Remove()d out from under the survivor.
func TestPurge_DeleteFilesNeverRemovesSurvivorsFile(t *testing.T) {
	svc, store, _ := setupPurgeBoundary(t)
	lib := setupRoot(t)
	keepPath := filepath.Join(lib, "Keep", "keep.m4b")
	// The loser book row names the survivor's file; it owns no file row of
	// its own (the dedupe-on-scan rule keeps one row per path).
	if err := os.MkdirAll(filepath.Dir(keepPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keepPath, []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	keep, err := store.CreateBook(&database.Book{Title: "Keep", FilePath: keepPath})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateBookFile(&database.BookFile{BookID: keep.ID, FilePath: keepPath, FileSize: 5, Duration: 60}); err != nil {
		t.Fatal(err)
	}
	loser, err := store.CreateBook(&database.Book{Title: "Loser", FilePath: keepPath})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := merge.NewService(store).MergeBooks([]string{keep.ID, loser.ID}, keep.ID); err != nil {
		t.Fatalf("MergeBooks: %v", err)
	}
	old := time.Now().AddDate(0, 0, -40)
	if _, err := store.ModifyBook(loser.ID, func(b *database.Book) error {
		b.MarkedForDeletionAt = &old
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	days := 30
	res, err := svc.PurgeSoftDeletedBooks(context.Background(), true, &days)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(keepPath); err != nil {
		t.Fatalf("purge removed the survivor's file %s: %v (result %+v)", keepPath, err, res)
	}
	assertNoOrphanBookFiles(t, store, keep.ID, loser.ID)
}

// A soft-deleted book that owns no file rows is still purged — the guard
// refuses only what would orphan rows.
func TestPurge_FilelessSoftDeletedBookStillPurged(t *testing.T) {
	svc, store, _ := setupPurgeBoundary(t)
	softDeleted(t, store, "fileless", "")
	old := time.Now().AddDate(0, 0, -40)
	if _, err := store.ModifyBook("fileless", func(b *database.Book) error {
		b.MarkedForDeletionAt = &old
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	days := 30
	res, err := svc.PurgeSoftDeletedBooks(context.Background(), false, &days)
	if err != nil {
		t.Fatal(err)
	}
	if res.Purged != 1 {
		t.Fatalf("Purged = %d, want 1 (errors %v)", res.Purged, res.Errors)
	}
}

func setupRoot(t *testing.T) string {
	t.Helper()
	// setupPurgeBoundary pointed config.AppConfig.RootDir at <base>/lib.
	return config.AppConfig.RootDir
}

func containsSubstr(ss []string, sub string) bool {
	for _, s := range ss {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
