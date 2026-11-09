// file: internal/versions/swap_test.go
// version: 1.0.1
// guid: 7d4e6f3b-9c5a-4a70-b8c5-3d7e0f1b9a99

package versions

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
)

func readTestFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func TestVersionSwap_BasicRoundTrip(t *testing.T) {
	store, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("pebble: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	// Set up a book with two versions, each with one file.
	bookDir := t.TempDir()
	bookFilePath := filepath.Join(bookDir, "Book.m4b")
	writeTestFile(t, bookFilePath, "active-content-v1")

	book, err := store.CreateBook(&database.Book{
		ID: "b1", Title: "Test Book", FilePath: bookFilePath, Format: "m4b",
	})
	if err != nil {
		t.Fatalf("create book: %v", err)
	}

	activeVer, err := store.CreateBookVersion(&database.BookVersion{
		BookID: book.ID, Status: database.BookVersionStatusActive,
		Format: "m4b", Source: "imported",
	})
	if err != nil {
		t.Fatalf("create active version: %v", err)
	}

	altVer, err := store.CreateBookVersion(&database.BookVersion{
		BookID: book.ID, Status: database.BookVersionStatusAlt,
		Format: "mp3", Source: "deluge",
	})
	if err != nil {
		t.Fatalf("create alt version: %v", err)
	}

	// Active version file is at book root.
	if err := store.CreateBookFile(&database.BookFile{
		ID: "f1", BookID: book.ID, VersionID: activeVer.ID,
		FilePath: bookFilePath, Format: "m4b",
	}); err != nil {
		t.Fatalf("create file f1: %v", err)
	}

	// Alt version file is in .versions/alt/.
	altFilePath := filepath.Join(bookDir, ".versions", altVer.ID, "Book.mp3")
	writeTestFile(t, altFilePath, "alt-content-mp3")
	if err := store.CreateBookFile(&database.BookFile{
		ID: "f2", BookID: book.ID, VersionID: altVer.ID,
		FilePath: altFilePath, Format: "mp3",
	}); err != nil {
		t.Fatalf("create file f2: %v", err)
	}

	// Run the swap: active → .versions/{activeID}, alt → book root.
	var steps []string
	err = RunVersionSwap(context.Background(), store, VersionSwapParams{
		BookID:        book.ID,
		FromVersionID: activeVer.ID,
		ToVersionID:   altVer.ID,
	}, func(step string, pct int) {
		steps = append(steps, step)
	}, nil, nil)
	if err != nil {
		t.Fatalf("swap: %v", err)
	}

	// Verify DB status transitions.
	from, _ := store.GetBookVersion(activeVer.ID)
	if from.Status != database.BookVersionStatusAlt {
		t.Errorf("from status = %q, want alt", from.Status)
	}
	to, _ := store.GetBookVersion(altVer.ID)
	if to.Status != database.BookVersionStatusActive {
		t.Errorf("to status = %q, want active", to.Status)
	}

	// Verify filesystem: alt's file should now be at book root.
	expectedNewPrimary := filepath.Join(bookDir, "Book.mp3")
	if _, err := os.Stat(expectedNewPrimary); err != nil {
		t.Errorf("new primary file missing at %s: %v", expectedNewPrimary, err)
	} else if got := readTestFile(t, expectedNewPrimary); got != "alt-content-mp3" {
		t.Errorf("new primary content = %q, want alt-content-mp3", got)
	}

	// Old active file should be in .versions/{activeID}/.
	expectedOldInSlot := filepath.Join(bookDir, ".versions", activeVer.ID, "Book.m4b")
	if _, err := os.Stat(expectedOldInSlot); err != nil {
		t.Errorf("old primary not in version slot: %v", err)
	} else if got := readTestFile(t, expectedOldInSlot); got != "active-content-v1" {
		t.Errorf("old primary content = %q, want active-content-v1", got)
	}

	// Verify BookFile paths updated in DB.
	files, _ := store.GetBookFiles(book.ID)
	pathMap := map[string]string{}
	for _, f := range files {
		pathMap[f.ID] = f.FilePath
	}
	if pathMap["f1"] != expectedOldInSlot {
		t.Errorf("f1 path = %s, want %s", pathMap["f1"], expectedOldInSlot)
	}
	if pathMap["f2"] != expectedNewPrimary {
		t.Errorf("f2 path = %s, want %s", pathMap["f2"], expectedNewPrimary)
	}

	// Progress callbacks fired.
	if len(steps) < 4 {
		t.Errorf("expected at least 4 progress steps, got %d: %v", len(steps), steps)
	}
}

func TestVersionSwap_BookNotFound(t *testing.T) {
	store, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("pebble: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	err = RunVersionSwap(context.Background(), store, VersionSwapParams{
		BookID:        "nonexistent",
		FromVersionID: "v1",
		ToVersionID:   "v2",
	}, nil, nil, nil)
	if err == nil {
		t.Error("expected error on nonexistent version")
	}
}
