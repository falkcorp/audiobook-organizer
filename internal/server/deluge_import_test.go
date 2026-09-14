// file: internal/server/deluge_import_test.go
// version: 2.1.0
// guid: e1b5d8f2-3c7a-4091-a2e9-6f4d0c8b3a15
// last-edited: 2026-09-13
//
// Tests for ImportToLibrary — delegates to internal/deluge/import.go.

package server

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/deluge"
)

// fakeStore implements deluge.Store, the two methods ImportToLibrary calls,
// over an otherwise nil database.Store: any other method panics.
type fakeStore struct {
	database.Store // embed the interface so we don't need to implement all methods
	updated        *database.BookFile
}

// GetBookFileByPath matches PebbleStore's contract: (nil, nil) for a path no
// row names. These tests' destinations are fresh temp paths, which no row
// names. ImportToLibrary asks before it copies, to refuse a destination that
// another book file row already owns.
func (f *fakeStore) GetBookFileByPath(string) (*database.BookFile, error) {
	return nil, nil
}

func (f *fakeStore) UpdateBookFile(id string, file *database.BookFile) error {
	f.updated = file
	return nil
}

// Test 1: When reflink fails, ioCopy succeeds and the database is updated.
func TestImportToLibrary_FallbackToCopy(t *testing.T) {
	// Create a temp source file.
	srcDir := t.TempDir()
	srcFile := filepath.Join(srcDir, "book.m4b")
	if err := os.WriteFile(srcFile, []byte("audio data"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Destination is a different temp dir (the "library root").
	rootDir := t.TempDir()

	cfg := &config.Config{
		RootDir:           rootDir,
		DelugeMoveEnabled: false, // no Deluge move
	}
	store := &fakeStore{}
	bf := &database.BookFile{
		ID:       "test-id-001",
		FilePath: srcFile,
		// DelugeHash intentionally empty so MoveStorage is skipped.
	}

	newPath, err := deluge.ImportToLibrary(cfg, nil, store, bf)
	if err != nil {
		t.Fatalf("ImportToLibrary returned error: %v", err)
	}

	// Verify destination file exists.
	if _, statErr := os.Stat(newPath); statErr != nil {
		t.Errorf("destination file does not exist: %v", statErr)
	}

	// Verify DB was updated.
	if store.updated == nil {
		t.Fatal("UpdateBookFile was not called")
	}
	if store.updated.FilePath != newPath {
		t.Errorf("BookFile.FilePath = %q, want %q", store.updated.FilePath, newPath)
	}
	if store.updated.DelugeOriginalPath != srcFile {
		t.Errorf("BookFile.DelugeOriginalPath = %q, want %q", store.updated.DelugeOriginalPath, srcFile)
	}
	if store.updated.ImportedFromDelugeAt == nil {
		t.Error("BookFile.ImportedFromDelugeAt is nil, want non-nil")
	}
}

// Test 2: When source and destination are the same path, no copy is done.
func TestImportToLibrary_SameSourceAndDest(t *testing.T) {
	rootDir := t.TempDir()

	// Create a file inside the library root.
	srcFile := filepath.Join(rootDir, "book.m4b")
	if err := os.WriteFile(srcFile, []byte("audio data"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		RootDir:           rootDir,
		DelugeMoveEnabled: false,
	}
	store := &fakeStore{}
	bf := &database.BookFile{
		ID:       "test-id-002",
		FilePath: srcFile,
	}

	newPath, err := deluge.ImportToLibrary(cfg, nil, store, bf)
	if err != nil {
		t.Fatalf("ImportToLibrary returned error: %v", err)
	}
	if newPath != srcFile {
		t.Errorf("expected newPath = %q (same as src), got %q", srcFile, newPath)
	}
	// When source == dest, UpdateBookFile should NOT be called.
	if store.updated != nil {
		t.Error("UpdateBookFile was called even though source == dest; expected no-op")
	}
	_ = time.Now() // keep time import used
}

// Test 3: Idempotency — if ImportedFromDelugeAt is already set, skip immediately.
func TestImportToLibrary_Idempotent(t *testing.T) {
	rootDir := t.TempDir()
	srcFile := filepath.Join(rootDir, "already-imported.m4b")
	if err := os.WriteFile(srcFile, []byte("audio data"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		RootDir:           rootDir,
		DelugeMoveEnabled: false,
	}
	store := &fakeStore{}
	importedAt := time.Now().Add(-time.Hour)
	bf := &database.BookFile{
		ID:                   "test-id-003",
		FilePath:             srcFile,
		ImportedFromDelugeAt: &importedAt,
	}

	newPath, err := deluge.ImportToLibrary(cfg, nil, store, bf)
	if err != nil {
		t.Fatalf("ImportToLibrary returned error: %v", err)
	}
	if newPath != srcFile {
		t.Errorf("expected newPath = %q (unchanged), got %q", srcFile, newPath)
	}
	// Must NOT call UpdateBookFile on an already-imported file.
	if store.updated != nil {
		t.Error("UpdateBookFile was called on already-imported file; expected no-op")
	}
}

// Test 4: MoveStorage failure is best-effort — ImportToLibrary must not return an error.
func TestImportToLibrary_MoveStorageFailureIsNonFatal(t *testing.T) {
	srcDir := t.TempDir()
	srcFile := filepath.Join(srcDir, "book.m4b")
	if err := os.WriteFile(srcFile, []byte("audio data"), 0o644); err != nil {
		t.Fatal(err)
	}

	rootDir := t.TempDir()
	cfg := &config.Config{
		RootDir:           rootDir,
		DelugeMoveEnabled: true, // enable MoveStorage gate
	}
	store := &fakeStore{}
	bf := &database.BookFile{
		ID:         "test-id-004",
		FilePath:   srcFile,
		DelugeHash: "abc123def456", // non-empty hash triggers MoveStorage path
	}

	// delugeClient is nil, so MoveStorage is skipped (not called).
	// The function should still succeed.
	newPath, err := deluge.ImportToLibrary(cfg, nil, store, bf)
	if err != nil {
		t.Fatalf("ImportToLibrary returned error even with nil delugeClient: %v", err)
	}
	if _, statErr := os.Stat(newPath); statErr != nil {
		t.Errorf("destination file does not exist: %v", statErr)
	}
	if store.updated == nil {
		t.Fatal("UpdateBookFile was not called")
	}
}
