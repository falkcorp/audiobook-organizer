// file: internal/covers/history_test.go
// version: 1.0.0
// guid: f6a7b8c9-0123-def0-1234-56789abcdef0
// last-edited: 2026-05-11

package covers

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestListCoverHistory(t *testing.T) {
	// Create temporary directory structure
	tmpDir := t.TempDir()
	bookID := "book123"
	histDir := filepath.Join(tmpDir, "covers", "history", bookID)

	if err := os.MkdirAll(histDir, 0755); err != nil {
		t.Fatalf("failed to create history dir: %v", err)
	}

	// Create test cover files with different timestamps
	files := []struct {
		name string
		data string
	}{
		{"cover1.jpg", "first"},
		{"cover2.png", "second"},
		{"cover3.jpeg", "third"},
	}

	for i, f := range files {
		path := filepath.Join(histDir, f.name)
		if err := os.WriteFile(path, []byte(f.data), 0644); err != nil {
			t.Fatalf("failed to create test file %q: %v", f.name, err)
		}
		// Set different modification times
		modTime := time.Now().Add(time.Duration(-i*10) * time.Minute)
		if err := os.Chtimes(path, modTime, modTime); err != nil {
			t.Fatalf("failed to set mtime: %v", err)
		}
	}

	// Test listing covers
	covers, err := ListCoverHistory(bookID, tmpDir)
	if err != nil {
		t.Errorf("ListCoverHistory unexpected error: %v", err)
	}

	if len(covers) != 3 {
		t.Errorf("ListCoverHistory returned %d covers, want 3", len(covers))
	}

	// Verify sorting (newest first)
	if len(covers) >= 2 {
		if covers[0].ModTime <= covers[1].ModTime {
			t.Errorf("covers not sorted newest first: %s > %s", covers[0].ModTime, covers[1].ModTime)
		}
	}

	// Verify URL format
	for _, cover := range covers {
		if !contains([]string{".jpg", ".png", ".jpeg"}, filepath.Ext(cover.Filename)) {
			t.Errorf("unexpected file extension in %q", cover.Filename)
		}
	}
}

func TestListCoverHistoryEmpty(t *testing.T) {
	tmpDir := t.TempDir()
	bookID := "nonexistent"

	covers, err := ListCoverHistory(bookID, tmpDir)
	if err != nil {
		t.Errorf("ListCoverHistory for nonexistent book unexpected error: %v", err)
	}
	if len(covers) != 0 {
		t.Errorf("ListCoverHistory for nonexistent book returned %d covers, want 0", len(covers))
	}
}

func TestRestoreCoverFile(t *testing.T) {
	tmpDir := t.TempDir()
	bookID := "book123"
	histDir := filepath.Join(tmpDir, "covers", "history", bookID)
	coversDir := filepath.Join(tmpDir, "covers")

	if err := os.MkdirAll(histDir, 0755); err != nil {
		t.Fatalf("failed to create history dir: %v", err)
	}
	if err := os.MkdirAll(coversDir, 0755); err != nil {
		t.Fatalf("failed to create covers dir: %v", err)
	}

	// Create a historical cover file
	testData := []byte("historical cover data")
	histPath := filepath.Join(histDir, "old_cover.jpg")
	if err := os.WriteFile(histPath, testData, 0644); err != nil {
		t.Fatalf("failed to create history file: %v", err)
	}

	// Restore the cover
	result, err := RestoreCoverFile(bookID, "old_cover.jpg", tmpDir)
	if err != nil {
		t.Errorf("RestoreCoverFile unexpected error: %v", err)
	}

	// Verify the file was created in the correct location
	expectedPath := filepath.Join(coversDir, bookID+".jpg")
	if result != expectedPath {
		t.Errorf("RestoreCoverFile returned %q, want %q", result, expectedPath)
	}

	// Verify the content
	content, err := os.ReadFile(result)
	if err != nil {
		t.Errorf("failed to read restored cover: %v", err)
	}
	if string(content) != string(testData) {
		t.Errorf("restored cover content mismatch: got %q, want %q", string(content), string(testData))
	}
}

func TestRestoreCoverFilePathTraversal(t *testing.T) {
	tmpDir := t.TempDir()
	bookID := "book123"
	histDir := filepath.Join(tmpDir, "covers", "history", bookID)

	if err := os.MkdirAll(histDir, 0755); err != nil {
		t.Fatalf("failed to create history dir: %v", err)
	}

	// Create a valid test file
	validPath := filepath.Join(histDir, "cover.jpg")
	if err := os.WriteFile(validPath, []byte("test"), 0644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	tests := []struct {
		name              string
		filename          string
		wantPathTraversal bool // true = function should reject this input as path traversal
	}{
		{"valid filename succeeds", "cover.jpg", false},
		{"path with slash rejected", "dir/cover.jpg", true},
		{"path with backslash rejected", "dir\\cover.jpg", true},
		{"parent dir reference rejected", "../cover.jpg", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := RestoreCoverFile(bookID, tt.filename, tmpDir)
			if tt.wantPathTraversal {
				// Should be rejected for path traversal
				if err != os.ErrInvalid {
					t.Errorf("RestoreCoverFile(%q) error = %v, want os.ErrInvalid for path traversal", tt.filename, err)
				}
			} else {
				// Valid filename with existing file - should succeed
				if err != nil {
					t.Errorf("RestoreCoverFile(%q) unexpected error: %v", tt.filename, err)
				}
			}
		})
	}
}

func TestRestoreCoverFileNotFound(t *testing.T) {
	tmpDir := t.TempDir()
	bookID := "book123"

	_, err := RestoreCoverFile(bookID, "nonexistent.jpg", tmpDir)
	if err == nil {
		t.Errorf("RestoreCoverFile for nonexistent file expected error")
	}
}

// Helper function
func contains(items []string, target string) bool {
	return slices.Contains(items, target)
}
