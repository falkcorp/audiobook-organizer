// file: internal/scanner/save_book_to_database_test.go
// version: 2.0.1
// guid: 0f1e2d3c-4b5a-6978-8899-aabbccddeeff
// last-edited: 2026-10-29

// NOTE(fable5 T022): Ported from SQLiteStore to PebbleStore.

package scanner

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// setupPebbleStore provides a PebbleDB-backed store and cleanup hook.
func setupPebbleStore(t *testing.T) (*database.PebbleStore, func()) {
	t.Helper()

	dbPath := t.TempDir()
	store, err := database.NewPebbleStore(dbPath)
	if err != nil {
		t.Fatalf("create pebble store: %v", err)
	}

	// Run migrations to ensure schema is up-to-date
	if err := database.RunMigrations(store); err != nil {
		t.Fatalf("run migrations: %v", err)
	}

	return store, func() {
		_ = store.Close()
	}
}

func TestSaveBookToDatabase_GlobalStoreCreateAndUpdate(t *testing.T) {
	store, cleanup := setupPebbleStore(t)
	defer cleanup()

	prevStore := database.GetGlobalStore()
	database.SetGlobalStore(store)
	SetStore(store)
	t.Cleanup(func() {
		database.SetGlobalStore(prevStore)
		SetStore(nil)
	})

	prevConfig := config.AppConfig
	t.Cleanup(func() {
		config.AppConfig = prevConfig
	})

	rootDir := t.TempDir()
	config.AppConfig.RootDir = rootDir

	filePath := filepath.Join(rootDir, "book.m4b")
	if err := os.WriteFile(filePath, []byte("test"), 0o644); err != nil {
		t.Fatalf("write temp file: %v", err)
	}

	book := &Book{
		FilePath:  filePath,
		Title:     "Test Title",
		Author:    "Test Author",
		Series:    "Test Series",
		Position:  1,
		Format:    ".m4b",
		Duration:  120,
		Narrator:  "Test Narrator",
		Language:  "en",
		Publisher: "Test Publisher",
	}

	if _, err := saveBookToDatabase(context.Background(), book); err != nil {
		t.Fatalf("saveBookToDatabase create failed: %v", err)
	}

	book.Title = "Updated Title"
	if _, err := saveBookToDatabase(context.Background(), book); err != nil {
		t.Fatalf("saveBookToDatabase update failed: %v", err)
	}

	author, err := store.GetAuthorByName("Test Author")
	if err != nil || author == nil {
		t.Fatalf("expected author to exist, err=%v", err)
	}

	series, err := store.GetSeriesByName("Test Series", &author.ID)
	if err != nil || series == nil {
		t.Fatalf("expected series to exist, err=%v", err)
	}

	saved, err := store.GetBookByFilePath(filePath)
	if err != nil || saved == nil {
		t.Fatalf("expected saved book, err=%v", err)
	}
	if saved.Title != "Updated Title" {
		t.Errorf("expected updated title, got %q", saved.Title)
	}
}

func TestSaveBookToDatabase_BlocklistSkips(t *testing.T) {
	store, cleanup := setupPebbleStore(t)
	defer cleanup()
	prevStore := database.GetGlobalStore()
	database.SetGlobalStore(store)
	SetStore(store)
	t.Cleanup(func() {
		database.SetGlobalStore(prevStore)
		SetStore(nil)
	})

	prevConfig := config.AppConfig
	t.Cleanup(func() {
		config.AppConfig = prevConfig
	})

	rootDir := t.TempDir()
	config.AppConfig.RootDir = rootDir

	filePath := filepath.Join(rootDir, "blocked.m4b")
	if err := os.WriteFile(filePath, []byte("blocked"), 0o644); err != nil {
		t.Fatalf("write temp file: %v", err)
	}

	hash, err := ComputeFileHash(filePath)
	if err != nil {
		t.Fatalf("ComputeFileHash failed: %v", err)
	}
	if _, err := store.CreateAuthor("Blocked Author"); err != nil {
		t.Fatalf("CreateAuthor failed: %v", err)
	}
	if err := store.AddBlockedHash(hash, "test"); err != nil {
		t.Fatalf("AddBlockedHash failed: %v", err)
	}

	book := &Book{
		FilePath: filePath,
		Title:    "Blocked Book",
		Author:   "Blocked Author",
		Format:   ".m4b",
	}

	if _, err := saveBookToDatabase(context.Background(), book); err != nil {
		t.Fatalf("saveBookToDatabase blocklist failed: %v", err)
	}

	saved, err := store.GetBookByFilePath(filePath)
	if err == nil && saved != nil {
		t.Error("expected blocked book to be skipped")
	}
}

// testDedupScanHooks is a test implementation of ScanHooks that records
// OnImportDedup calls for assertion.
type testDedupScanHooks struct{ calls []string }

func (h *testDedupScanHooks) OnBookScanned(_, _ string) {}
func (h *testDedupScanHooks) OnImportDedup(bookID string) {
	h.calls = append(h.calls, bookID)
}

// TestSaveBookToDatabase_DedupOnImportHook verifies that the dedup-on-import
// hook fires exactly once per newly created book and is NOT called when an
// existing book is updated via the same code path. This is the contract the
// scanner-side and server-side code both depend on: the hook is "new book,
// you should embed + Layer1 check this now", not a general "saveBook ran"
// notification.
func TestSaveBookToDatabase_DedupOnImportHook(t *testing.T) {
	store, cleanup := setupPebbleStore(t)
	defer cleanup()

	prevStore := database.GetGlobalStore()
	database.SetGlobalStore(store)
	SetStore(store)
	t.Cleanup(func() {
		database.SetGlobalStore(prevStore)
		SetStore(nil)
	})

	prevConfig := config.AppConfig
	t.Cleanup(func() { config.AppConfig = prevConfig })
	config.AppConfig.RootDir = t.TempDir()

	// Install scan hooks and make sure we uninstall on test exit so other
	// tests in the same package aren't affected.
	th := &testDedupScanHooks{}
	SetScanHooks(th)
	t.Cleanup(func() { SetScanHooks(nil) })

	filePath := filepath.Join(config.AppConfig.RootDir, "dedup-hook.m4b")
	if err := os.WriteFile(filePath, []byte("hook test"), 0o644); err != nil {
		t.Fatalf("write temp file: %v", err)
	}

	book := &Book{
		FilePath: filePath,
		Title:    "Hook Test",
		Author:   "Hook Author",
		Format:   ".m4b",
	}

	// First save: new book → hook MUST fire exactly once.
	if _, err := saveBookToDatabase(context.Background(), book); err != nil {
		t.Fatalf("saveBookToDatabase create failed: %v", err)
	}
	if len(th.calls) != 1 {
		t.Fatalf("expected 1 hook call on create, got %d: %v", len(th.calls), th.calls)
	}
	firstCallID := th.calls[0]
	if firstCallID == "" {
		t.Error("expected non-empty book ID in hook call")
	}

	// Second save (same file path): existing book → hook MUST NOT fire
	// again. Updating an existing book isn't a new import event and
	// shouldn't re-trigger dedup processing.
	book.Title = "Hook Test Updated"
	if _, err := saveBookToDatabase(context.Background(), book); err != nil {
		t.Fatalf("saveBookToDatabase update failed: %v", err)
	}
	if len(th.calls) != 1 {
		t.Errorf("expected hook call count to stay at 1 after update, got %d: %v", len(th.calls), th.calls)
	}
}
