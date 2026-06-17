// file: internal/versions/ingest_test.go
// version: 1.1.0
// guid: 4f2a3b0c-5d6e-4a70-b8c5-3d7e0f1b9a99
// last-edited: 2026-06-17

package versions

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o775); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

// allowDir registers dir as an import path so paths beneath it pass the
// CreateIngestVersion allow-list gate (go/path-injection). Temp dirs live
// outside the default allow-list prefixes, so tests must opt them in.
func allowDir(t *testing.T, store *database.PebbleStore, dir string) {
	t.Helper()
	if _, err := store.CreateImportPath(dir, "test-allow"); err != nil {
		t.Fatalf("allow import path: %v", err)
	}
}

func TestCreateIngestVersion_NewBook(t *testing.T) {
	store, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("pebble: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	dir := t.TempDir()
	allowDir(t, store, dir)
	filePath := filepath.Join(dir, "Book.m4b")
	writeTestFile(t, filePath, "audio-data-for-hash")

	book, _ := store.CreateBook(&database.Book{
		Title: "New Book", FilePath: filePath, Format: "m4b",
	})

	ver, err := CreateIngestVersion(store, IngestVersionParams{
		BookID: book.ID, FilePath: filePath, Format: "m4b", Source: "imported",
	})
	if err != nil {
		t.Fatalf("create version: %v", err)
	}
	if ver.Status != database.BookVersionStatusActive {
		t.Errorf("first version status = %q, want active", ver.Status)
	}
	if ver.Source != "imported" {
		t.Errorf("source = %q", ver.Source)
	}
}

func TestCreateIngestVersion_SecondVersionIsAlt(t *testing.T) {
	store, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("pebble: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	dir := t.TempDir()
	allowDir(t, store, dir)
	book, _ := store.CreateBook(&database.Book{
		Title: "Book", FilePath: filepath.Join(dir, "Book.m4b"), Format: "m4b",
	})

	// First version → active.
	v1, _ := CreateIngestVersion(store, IngestVersionParams{
		BookID: book.ID, FilePath: filepath.Join(dir, "Book.m4b"), Format: "m4b", Source: "imported",
	})
	if v1.Status != database.BookVersionStatusActive {
		t.Fatalf("v1 status = %q, want active", v1.Status)
	}

	// Second version → alt.
	v2, err := CreateIngestVersion(store, IngestVersionParams{
		BookID: book.ID, FilePath: filepath.Join(dir, "Book.mp3"), Format: "mp3", Source: "deluge",
	})
	if err != nil {
		t.Fatalf("v2: %v", err)
	}
	if v2.Status != database.BookVersionStatusAlt {
		t.Errorf("v2 status = %q, want alt", v2.Status)
	}
}

func TestCreateIngestVersion_FingerprintBlocksPurged(t *testing.T) {
	store, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("pebble: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	// Use an allowed path so the rejection under test is the fingerprint gate,
	// not the path allow-list gate.
	dir := t.TempDir()
	allowDir(t, store, dir)
	filePath := filepath.Join(dir, "new.m4b")

	// Create a purged version with a known torrent hash.
	_, _ = store.CreateBookVersion(&database.BookVersion{
		BookID: "old-book", Status: database.BookVersionStatusInactivePurged,
		Format: "m4b", Source: "deluge", TorrentHash: "blocked-hash",
	})

	book, _ := store.CreateBook(&database.Book{
		Title: "New Import", FilePath: filePath, Format: "m4b",
	})

	_, err = CreateIngestVersion(store, IngestVersionParams{
		BookID: book.ID, FilePath: filePath, Format: "m4b",
		Source: "deluge", TorrentHash: "blocked-hash",
	})
	if err == nil {
		t.Error("expected fingerprint rejection")
	}
}

func TestHashFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.bin")
	writeTestFile(t, path, "hello world")

	hash, err := HashFile(path)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if len(hash) != 64 {
		t.Errorf("hash length = %d, want 64 (SHA-256 hex)", len(hash))
	}
}

func TestCreateIngestVersion_FileHashUpdated(t *testing.T) {
	store, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("pebble: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	dir := t.TempDir()
	allowDir(t, store, dir)
	filePath := filepath.Join(dir, "Book.m4b")
	writeTestFile(t, filePath, "audio-content-to-hash")

	book, _ := store.CreateBook(&database.Book{
		Title: "Hash Test", FilePath: filePath, Format: "m4b",
	})
	_ = store.CreateBookFile(&database.BookFile{
		ID: "f1", BookID: book.ID, FilePath: filePath, Format: "m4b",
	})

	ver, _ := CreateIngestVersion(store, IngestVersionParams{
		BookID: book.ID, FilePath: filePath, Format: "m4b", Source: "imported",
	})

	files, _ := store.GetBookFiles(book.ID)
	found := false
	for _, f := range files {
		if f.ID == "f1" {
			found = true
			if f.FileHash == "" {
				t.Errorf("file hash not populated")
			}
			if f.VersionID != ver.ID {
				t.Errorf("version_id = %q, want %q", f.VersionID, ver.ID)
			}
		}
	}
	if !found {
		t.Error("file f1 not found")
	}
}
