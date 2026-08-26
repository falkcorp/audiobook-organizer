// file: internal/itunes/integration_test.go
// version: 1.0.0
// guid: e232c482-7e40-4c0c-87bd-4e88e4f7b3ef
//go:build integration

package itunes

import (
	"os"
	"path/filepath"
	"testing"
)

// TestParseRealLibrary validates parsing against the real iTunes library copy.
func TestParseRealLibrary(t *testing.T) {
	libraryPath := filepath.Join("..", "..", "testdata", "itunes", "iTunes Music Library.xml")
	if _, err := os.Stat(libraryPath); err != nil {
		t.Skipf("Real iTunes library not found: %v", err)
	}

	library, err := ParseLibrary(libraryPath)
	if err != nil {
		t.Fatalf("Failed to parse library: %v", err)
	}

	t.Logf("Parsed library with %d tracks", len(library.Tracks))
	t.Logf("Found %d playlists", len(library.Playlists))

	audiobookCount := 0
	for _, track := range library.Tracks {
		if IsAudiobook(track) {
			audiobookCount++
		}
	}

	if audiobookCount == 0 {
		t.Error("Expected to find at least one audiobook")
	}
}

// TestFullImportWorkflow exercises validation and conversion on a real library.
func TestFullImportWorkflow(t *testing.T) {
	libraryPath := filepath.Join("..", "..", "testdata", "itunes", "iTunes Music Library.xml")
	if _, err := os.Stat(libraryPath); err != nil {
		t.Skipf("Real iTunes library not found: %v", err)
	}

	opts := ImportOptions{
		LibraryPath:     libraryPath,
		ImportMode:      ImportModeImport,
		ImportPlaylists: true,
		SkipDuplicates:  true,
	}

	result, err := ValidateImport(opts)
	if err != nil {
		t.Fatalf("Validation failed: %v", err)
	}

	t.Logf("Validation results: total=%d audiobooks=%d files_found=%d files_missing=%d", result.TotalTracks, result.AudiobookTracks, result.FilesFound, result.FilesMissing)

	library, err := ParseLibrary(libraryPath)
	if err != nil {
		t.Fatalf("Failed to parse library: %v", err)
	}

	var firstAudiobook *Track
	for _, track := range library.Tracks {
		if IsAudiobook(track) {
			firstAudiobook = track
			break
		}
	}

	if firstAudiobook == nil {
		t.Fatal("No audiobooks found to test conversion")
	}

	book, err := ConvertTrack(firstAudiobook, opts)
	if err != nil {
		t.Fatalf("Failed to convert track: %v", err)
	}

	if book.Title == "" {
		t.Error("Expected converted audiobook to have a title")
	}
}
