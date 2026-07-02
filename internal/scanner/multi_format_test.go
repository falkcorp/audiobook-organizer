// file: internal/scanner/multi_format_test.go
// version: 1.1.0
// guid: a1b2c3d4-e5f6-7890-abcd-ef1234567890
// last-edited: 2026-07-01

package scanner

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
)

// TestMultiFormatSupport verifies that the scanner correctly identifies all supported formats
func TestMultiFormatSupport(t *testing.T) {
	// Initialize test configuration
	config.AppConfig.SupportedExtensions = []string{".m4b", ".mp3", ".m4a", ".flac", ".aac", ".ogg", ".wma"}

	testCases := []struct {
		name     string
		files    []string
		expected int
		formats  map[string]bool
	}{
		{
			name:     "M4B files only",
			files:    []string{"book1.m4b", "book2.m4b", "book3.m4b"},
			expected: 3,
			formats:  map[string]bool{".m4b": true},
		},
		{
			name:     "MP3 files only",
			files:    []string{"chapter01.mp3", "chapter02.mp3", "chapter03.mp3"},
			expected: 3,
			formats:  map[string]bool{".mp3": true},
		},
		{
			name:     "M4A files only",
			files:    []string{"audio1.m4a", "audio2.m4a"},
			expected: 2,
			formats:  map[string]bool{".m4a": true},
		},
		{
			name:     "FLAC files only",
			files:    []string{"high_quality.flac", "lossless.flac"},
			expected: 2,
			formats:  map[string]bool{".flac": true},
		},
		{
			name:     "Mixed formats",
			files:    []string{"book.m4b", "chapter.mp3", "track.m4a", "audio.flac", "file.aac", "sound.ogg", "media.wma"},
			expected: 7,
			formats:  map[string]bool{".m4b": true, ".mp3": true, ".m4a": true, ".flac": true, ".aac": true, ".ogg": true, ".wma": true},
		},
		{
			name:     "Unsupported formats excluded",
			files:    []string{"book.m4b", "video.mp4", "chapter.mp3", "doc.txt", "image.jpg"},
			expected: 2,
			formats:  map[string]bool{".m4b": true, ".mp3": true},
		},
		{
			name:     "Case insensitive extensions",
			files:    []string{"Book1.M4B", "Chapter.MP3", "Audio.M4A", "Track.FLAC"},
			expected: 4,
			formats:  map[string]bool{".m4b": true, ".mp3": true, ".m4a": true, ".flac": true},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Create temporary directory
			tmpDir, err := os.MkdirTemp("", "scanner-test-*")
			if err != nil {
				t.Fatalf("Failed to create temp dir: %v", err)
			}
			defer os.RemoveAll(tmpDir)

			// Create test files
			for _, file := range tc.files {
				path := filepath.Join(tmpDir, file)
				if err := os.WriteFile(path, []byte("test content"), 0644); err != nil {
					t.Fatalf("Failed to create test file %s: %v", file, err)
				}
			}

			// Scan directory
			books, err := ScanDirectory(context.Background(), tmpDir, nil)
			if err != nil {
				t.Fatalf("ScanDirectory failed: %v", err)
			}

			// Verify count
			if len(books) != tc.expected {
				t.Errorf("Expected %d books, got %d", tc.expected, len(books))
			}

			// Verify formats
			foundFormats := make(map[string]bool)
			for _, book := range books {
				foundFormats[book.Format] = true
			}

			for format := range tc.formats {
				if !foundFormats[format] {
					t.Errorf("Expected format %s not found in scanned books", format)
				}
			}
		})
	}
}

// TestFormatConfiguration verifies the default configuration includes all required formats
func TestFormatConfiguration(t *testing.T) {
	requiredFormats := []string{".m4b", ".mp3", ".m4a", ".flac"}
	config.AppConfig.SupportedExtensions = []string{".m4b", ".mp3", ".m4a", ".aac", ".ogg", ".flac", ".wma"}

	for _, format := range requiredFormats {
		found := false
		for _, ext := range config.AppConfig.SupportedExtensions {
			if ext == format {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Required format %s not in configuration", format)
		}
	}
}

// TestParallelScanWithMixedFormats ensures parallel scanning works correctly with multiple formats
func TestParallelScanWithMixedFormats(t *testing.T) {
	config.AppConfig.SupportedExtensions = []string{".m4b", ".mp3", ".m4a", ".flac"}

	tmpDir, err := os.MkdirTemp("", "parallel-scan-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create multiple subdirectories with different formats
	formats := []string{".m4b", ".mp3", ".m4a", ".flac"}
	expectedCount := 0

	for i, format := range formats {
		subDir := filepath.Join(tmpDir, "dir"+string(rune(i+'A')))
		if err := os.MkdirAll(subDir, 0755); err != nil {
			t.Fatalf("Failed to create subdir: %v", err)
		}

		// Create 3 files of this format in each directory
		for j := 1; j <= 3; j++ {
			fileName := filepath.Join(subDir, "file"+string(rune(j+'0'))+format)
			if err := os.WriteFile(fileName, []byte("test"), 0644); err != nil {
				t.Fatalf("Failed to create file: %v", err)
			}
			expectedCount++
		}
	}

	// Test with different worker counts
	for workers := 1; workers <= 4; workers++ {
		t.Run("workers_"+string(rune(workers+'0')), func(t *testing.T) {
			books, err := ScanDirectoryParallel(context.Background(), tmpDir, workers, nil)
			if err != nil {
				t.Fatalf("Parallel scan failed: %v", err)
			}

			if len(books) != expectedCount {
				t.Errorf("With %d workers: expected %d books, got %d", workers, expectedCount, len(books))
			}

			// Verify all formats are represented
			formatCount := make(map[string]int)
			for _, book := range books {
				formatCount[book.Format]++
			}

			for _, format := range formats {
				if count := formatCount[format]; count != 3 {
					t.Errorf("Format %s: expected 3 files, got %d", format, count)
				}
			}
		})
	}
}

// TestFormatPreservation ensures file format is correctly stored in Book struct
func TestFormatPreservation(t *testing.T) {
	config.AppConfig.SupportedExtensions = []string{".m4b", ".mp3", ".m4a", ".flac"}

	tmpDir, err := os.MkdirTemp("", "format-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	testFiles := map[string]string{
		"test.m4b":  ".m4b",
		"test.mp3":  ".mp3",
		"test.m4a":  ".m4a",
		"test.flac": ".flac",
	}

	for fileName := range testFiles {
		path := filepath.Join(tmpDir, fileName)
		if err := os.WriteFile(path, []byte("test"), 0644); err != nil {
			t.Fatalf("Failed to create file: %v", err)
		}
	}

	books, err := ScanDirectory(context.Background(), tmpDir, nil)
	if err != nil {
		t.Fatalf("Scan failed: %v", err)
	}

	for _, book := range books {
		fileName := filepath.Base(book.FilePath)
		if expectedFormat, exists := testFiles[fileName]; exists {
			if book.Format != expectedFormat {
				t.Errorf("File %s: expected format %s, got %s", fileName, expectedFormat, book.Format)
			}
		} else {
			t.Errorf("Unexpected file %s found in scan results", fileName)
		}
	}
}
