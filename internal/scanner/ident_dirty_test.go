// file: internal/scanner/ident_dirty_test.go
// version: 1.0.1
// guid: 5573453b-2e51-4af2-8502-ac8cec772b10
// last-edited: 2026-10-29

package scanner

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

func identDirty(t *testing.T, s *database.PebbleStore) []database.IdentDirtyEntry {
	t.Helper()
	page, err := s.IdentDirtyPage("", 10)
	if err != nil {
		t.Fatal(err)
	}
	return page
}

// A new primary book is marked for the identification driver; a rescan
// that changes its title marks it again; a rescan that changes nothing does
// not.
func TestScannerMarksDirtyOnTitleChange(t *testing.T) {
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
	rootDir := t.TempDir()
	config.AppConfig.RootDir = rootDir
	filePath := filepath.Join(rootDir, "book.m4b")
	if err := os.WriteFile(filePath, []byte("test"), 0o644); err != nil {
		t.Fatal(err)
	}

	book := &Book{FilePath: filePath, Title: "Title 000123", Author: "Author 07", Format: ".m4b", Duration: 120}
	if _, err := saveBookToDatabase(context.Background(), book); err != nil {
		t.Fatal(err)
	}
	saved, err := store.GetBookByFilePath(filePath)
	if err != nil || saved == nil {
		t.Fatalf("saved book: %v", err)
	}
	page := identDirty(t, store)
	if len(page) != 1 || page[0].BookID != saved.ID {
		t.Fatalf("after import the dirty set is %v; want the new book %s", page, saved.ID)
	}
	if _, err := store.ClearIdentDirty(saved.ID, page[0].Epoch); err != nil {
		t.Fatal(err)
	}

	// Unchanged rescan: no mark.
	if _, err := saveBookToDatabase(context.Background(), book); err != nil {
		t.Fatal(err)
	}
	if page := identDirty(t, store); len(page) != 0 {
		t.Fatalf("an unchanged rescan marked %v", page)
	}

	book.Title = "Title 000124"
	if _, err := saveBookToDatabase(context.Background(), book); err != nil {
		t.Fatal(err)
	}
	page = identDirty(t, store)
	if len(page) != 1 || page[0].BookID != saved.ID {
		t.Fatalf("after a title change the dirty set is %v; want %s", page, saved.ID)
	}
}
