// file: internal/sweep/temp_cleanup_test.go
// version: 1.1.0
// guid: c3b2a1f0-9087-6543-2109-fedcba987654

package sweep

import (
	"github.com/falkcorp/audiobook-organizer/internal/pathutil"
	"os"
	"path/filepath"
	"testing"
)

func TestCleanupOrphanedTempFiles_RemovesTempFiles(t *testing.T) {
	tmpDir := t.TempDir()

	// Create temp files that should be removed
	tempFiles := []string{
		"book.tmp.m4b",
		"audio.tmp.m4a",
		"track.tmp.mp3",
		"voice.tmp.flac",
		"remux.remux.tmp",
	}

	for _, name := range tempFiles {
		path := filepath.Join(tmpDir, name)
		if err := os.WriteFile(path, []byte("data"), 0o644); err != nil {
			t.Fatalf("write file %s: %v", name, err)
		}
	}

	// Create a file that should NOT be removed
	goodFile := filepath.Join(tmpDir, "good_book.m4b")
	if err := os.WriteFile(goodFile, []byte("data"), 0o644); err != nil {
		t.Fatalf("write good file: %v", err)
	}

	removed := CleanupOrphanedTempFiles(tmpDir, pathutil.AppDirs{}, nil, "")
	if removed != len(tempFiles) {
		t.Errorf("expected %d files removed, got %d", len(tempFiles), removed)
	}

	// Verify temp files are gone
	for _, name := range tempFiles {
		path := filepath.Join(tmpDir, name)
		if _, err := os.Stat(path); err == nil {
			t.Errorf("temp file %s should have been removed", name)
		}
	}

	// Verify good file still exists
	if _, err := os.Stat(goodFile); err != nil {
		t.Errorf("good file should still exist: %v", err)
	}
}

func TestCleanupOrphanedTempFiles_HandlesEmptyDir(t *testing.T) {
	tmpDir := t.TempDir()
	removed := CleanupOrphanedTempFiles(tmpDir, pathutil.AppDirs{}, nil, "")
	if removed != 0 {
		t.Errorf("expected 0 removed from empty dir, got %d", removed)
	}
}

func TestIsOrphanedTempFile_MatchesPatterns(t *testing.T) {
	tests := []struct {
		name     string
		expected bool
	}{
		{"book.tmp.m4b", true},
		{"BOOK.TMP.M4B", true},
		{"audio.tmp.m4a", true},
		{"track.tmp.mp3", true},
		{"voice.tmp.flac", true},
		{"remux.remux.tmp", true},
		{"good_book.m4b", false},
		{"archive.tar.gz", false},
		{"tmp.txt", false},
	}

	for _, tc := range tests {
		result := isOrphanedTempFile(tc.name)
		if result != tc.expected {
			t.Errorf("isOrphanedTempFile(%q): expected %v, got %v", tc.name, tc.expected, result)
		}
	}
}
