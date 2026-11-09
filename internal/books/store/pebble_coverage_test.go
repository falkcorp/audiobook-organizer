// file: internal/books/store/pebble_coverage_test.go
// version: 1.1.1
// guid: 06d2f26e-2d45-49d9-9a4e-3c7b2b90de60
// last-edited: 2026-11-09

package store

import (
	"encoding/json"
	"testing"

	"github.com/cockroachdb/pebble/v2"
)

func TestPebbleUpdateBookAndImportPaths(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()
	pebbleStore := store.(*PebbleStore)

	author1, err := store.CreateAuthor("Pebble Author 1")
	if err != nil {
		t.Fatalf("CreateAuthor failed: %v", err)
	}
	author2, err := store.CreateAuthor("Pebble Author 2")
	if err != nil {
		t.Fatalf("CreateAuthor failed: %v", err)
	}
	series1, err := store.CreateSeries("Pebble Series 1", &author1.ID)
	if err != nil {
		t.Fatalf("CreateSeries failed: %v", err)
	}
	series2, err := store.CreateSeries("Pebble Series 2", &author2.ID)
	if err != nil {
		t.Fatalf("CreateSeries failed: %v", err)
	}

	hash1 := "hash-1"
	hash2 := "hash-2"
	orig1 := "orig-1"
	orig2 := "orig-2"
	org1 := "org-1"
	org2 := "org-2"

	book, err := store.CreateBook(&Book{
		Title:             "Pebble Book",
		FilePath:          "/tmp/pebble-a.mp3",
		AuthorID:          &author1.ID,
		SeriesID:          &series1.ID,
		FileHash:          &hash1,
		OriginalFileHash:  &orig1,
		OrganizedFileHash: &org1,
	})
	if err != nil {
		t.Fatalf("CreateBook failed: %v", err)
	}

	book.FilePath = "/tmp/pebble-b.mp3"
	book.AuthorID = &author2.ID
	book.SeriesID = &series2.ID
	book.FileHash = &hash2
	book.OriginalFileHash = &orig2
	book.OrganizedFileHash = &org2

	if _, err := pebbleStore.UpdateBook(book.ID, book); err != nil {
		t.Fatalf("UpdateBook failed: %v", err)
	}
	if err := pebbleStore.DeleteBook(book.ID); err != nil {
		t.Fatalf("DeleteBook failed: %v", err)
	}

	importPath, err := store.CreateImportPath("/tmp/pebble-import-a", "Pebble Import")
	if err != nil {
		t.Fatalf("CreateImportPath failed: %v", err)
	}
	importPath.Path = "/tmp/pebble-import-b"
	importPath.Enabled = false
	if err := pebbleStore.UpdateImportPath(importPath.ID, importPath); err != nil {
		t.Fatalf("UpdateImportPath failed: %v", err)
	}
	if err := pebbleStore.DeleteImportPath(importPath.ID); err != nil {
		t.Fatalf("DeleteImportPath failed: %v", err)
	}
}

func TestPebbleDuplicateBooksLegacyKeys(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()
	pebbleStore := store.(*PebbleStore)

	hash := "legacy-hash"
	// Use the canonical "book:{ULID}" key format (the old "book:id:{id}" prefix
	// was a bug that has been fixed; books are stored as "book:{id}").
	book1 := Book{ID: "01HLEGACY00000000000000001", Title: "Legacy One", FileHash: &hash}
	book2 := Book{ID: "01HLEGACY00000000000000002", Title: "Legacy Two", FileHash: &hash}
	data1, err := json.Marshal(book1)
	if err != nil {
		t.Fatalf("marshal book1 failed: %v", err)
	}
	data2, err := json.Marshal(book2)
	if err != nil {
		t.Fatalf("marshal book2 failed: %v", err)
	}

	if err := pebbleStore.db.Set([]byte("book:"+book1.ID), data1, nil); err != nil {
		t.Fatalf("set book1 failed: %v", err)
	}
	if err := pebbleStore.db.Set([]byte("book:"+book2.ID), data2, nil); err != nil {
		t.Fatalf("set book2 failed: %v", err)
	}

	dups, err := pebbleStore.GetDuplicateBooks()
	if err != nil {
		t.Fatalf("GetDuplicateBooks failed: %v", err)
	}
	if len(dups) == 0 {
		t.Fatal("expected duplicate groups")
	}
}

func TestPebbleCreateUserDuplicate(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()
	pebbleStore := store.(*PebbleStore)

	if _, err := pebbleStore.CreateUser("User", "user@example.com", "algo", "hash", []string{"user"}, "active"); err != nil {
		t.Fatalf("CreateUser failed: %v", err)
	}
	if _, err := pebbleStore.CreateUser("User", "other@example.com", "algo", "hash", []string{"user"}, "active"); err == nil {
		t.Fatal("expected duplicate username error")
	}
	if _, err := pebbleStore.CreateUser("Other", "user@example.com", "algo", "hash", []string{"user"}, "active"); err == nil {
		t.Fatal("expected duplicate email error")
	}
}

func TestNewPebbleStoreExistingCounters(t *testing.T) {
	tempDir := t.TempDir()
	store, err := NewPebbleStore(tempDir)
	if err != nil {
		t.Fatalf("NewPebbleStore failed: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store failed: %v", err)
	}

	store, err = NewPebbleStore(tempDir)
	if err != nil {
		t.Fatalf("NewPebbleStore reopen failed: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store failed: %v", err)
	}
}

func TestPebbleMigrateImportPathKeysLegacy(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()
	pebbleStore := store.(*PebbleStore)

	if err := pebbleStore.db.Set([]byte("library:path:/tmp/legacy"), []byte("1"), pebble.Sync); err != nil {
		t.Fatalf("failed to seed legacy path key: %v", err)
	}
	if err := pebbleStore.db.Set([]byte("library:1"), []byte("payload"), pebble.Sync); err != nil {
		t.Fatalf("failed to seed legacy key: %v", err)
	}
	if err := pebbleStore.db.Set([]byte("counter:library"), []byte("7"), pebble.Sync); err != nil {
		t.Fatalf("failed to seed legacy counter: %v", err)
	}

	if err := pebbleStore.migrateImportPathKeys(); err != nil {
		t.Fatalf("migrateImportPathKeys failed: %v", err)
	}

	if _, closer, err := pebbleStore.db.Get([]byte("import_path:path:/tmp/legacy")); err != nil {
		t.Fatalf("expected migrated path key: %v", err)
	} else {
		closer.Close()
	}
	if _, closer, err := pebbleStore.db.Get([]byte("import_path:1")); err != nil {
		t.Fatalf("expected migrated key: %v", err)
	} else {
		closer.Close()
	}
	if _, closer, err := pebbleStore.db.Get([]byte("counter:import_path")); err != nil {
		t.Fatalf("expected migrated counter: %v", err)
	} else {
		closer.Close()
	}
	if _, closer, err := pebbleStore.db.Get([]byte("library:path:/tmp/legacy")); err == nil {
		closer.Close()
		t.Fatal("expected legacy path key to be removed")
	}
	if _, closer, err := pebbleStore.db.Get([]byte("counter:library")); err == nil {
		closer.Close()
		t.Fatal("expected legacy counter to be removed")
	}
}
