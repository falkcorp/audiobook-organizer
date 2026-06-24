// file: internal/scanner/integration_format_test.go
// version: 1.1.0
// guid: b2c3d4e5-f6a7-8901-bcde-f12345678901
// last-edited: 2026-06-24

package scanner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
)

// TestIntegrationRealWorldMixedFormats tests scanner with realistic mixed format audiobook directories
func TestIntegrationRealWorldMixedFormats(t *testing.T) {
	config.AppConfig.SupportedExtensions = []string{".m4b", ".mp3", ".m4a", ".flac"}
	config.AppConfig.ConcurrentScans = 2

	tmpDir, err := os.MkdirTemp("", "integration-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create realistic directory structure
	testStructure := map[string][]string{
		"Author A/Book 1 - M4B": {
			"Book 1.m4b",
		},
		"Author B/Book 2 - MP3": {
			"Chapter 01.mp3",
			"Chapter 02.mp3",
			"Chapter 03.mp3",
		},
		"Author C/Book 3 - M4A": {
			"Part 1.m4a",
			"Part 2.m4a",
		},
		"Author D/Book 4 - FLAC": {
			"Track01.flac",
			"Track02.flac",
			"Track03.flac",
			"Track04.flac",
		},
		"Author E/Book 5 - Mixed": {
			"Intro.mp3",
			"Main.m4b",
			"Bonus.m4a",
		},
	}

	totalFiles := 0
	for dir, files := range testStructure {
		dirPath := filepath.Join(tmpDir, dir)
		if err := os.MkdirAll(dirPath, 0755); err != nil {
			t.Fatalf("Failed to create directory %s: %v", dir, err)
		}

		for _, file := range files {
			filePath := filepath.Join(dirPath, file)
			if err := os.WriteFile(filePath, []byte("test audio content"), 0644); err != nil {
				t.Fatalf("Failed to create file %s: %v", file, err)
			}
			totalFiles++
		}
	}

	// Test scanning
	books, err := ScanDirectoryParallel(tmpDir, 4, nil)
	if err != nil {
		t.Fatalf("Integration scan failed: %v", err)
	}

	// AP-5: "Author B/Book 2 - MP3" contains Chapter 01-03.mp3 with no tags
	// but sequential names — they are now grouped into ONE multi-file book
	// instead of 3 shattered single-file books. Expected book count = 11:
	//   Author A: 1 (m4b)
	//   Author B: 1 (multi-file, Chapter 01-03.mp3 → SegmentFiles)
	//   Author C: 2 (Part 1 + Part 2 m4a — no sequential pattern match)
	//   Author D: 4 (Track01-04.flac — "TrackNN" not a pattern-quorum match)
	//   Author E: 3 (Intro.mp3 + Main.m4b + Bonus.m4a — mixed formats, no group)
	expectedBooks := totalFiles - 2 // 13 - 2: Chapter 02 + Chapter 03 absorbed
	if len(books) != expectedBooks {
		t.Errorf("Expected %d total books, got %d", expectedBooks, len(books))
	}

	// Verify format distribution
	formatCounts := map[string]int{
		".m4b":  0,
		".mp3":  0,
		".m4a":  0,
		".flac": 0,
	}

	for _, book := range books {
		formatCounts[book.Format]++
	}

	expectedCounts := map[string]int{
		".m4b":  2, // Book 1 + Book 5 Main
		".mp3":  2, // Book 2 as multi-file (1) + Book 5 Intro (1)
		".m4a":  3, // Book 3 (2) + Book 5 Bonus
		".flac": 4, // Book 4
	}

	for format, expected := range expectedCounts {
		if formatCounts[format] != expected {
			t.Errorf("Format %s: expected %d files, got %d", format, expected, formatCounts[format])
		}
	}
}

// TestIntegrationProcessingMixedFormats tests end-to-end processing with multiple formats
func TestIntegrationProcessingMixedFormats(t *testing.T) {
	config.AppConfig.SupportedExtensions = []string{".m4b", ".mp3", ".m4a"}
	config.AppConfig.ConcurrentScans = 2
	config.AppConfig.EnableAIParsing = false // Disable AI for faster tests

	tmpDir, err := os.MkdirTemp("", "processing-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create test files with naming patterns
	testBooks := []struct {
		path   string
		format string
	}{
		{"Harry Potter/Harry Potter and the Philosopher's Stone.m4b", ".m4b"},
		{"Tolkien/The Hobbit - Chapter 01.mp3", ".mp3"},
		{"Tolkien/The Hobbit - Chapter 02.mp3", ".mp3"},
		{"Stephen King/The Shining.m4a", ".m4a"},
	}

	for _, book := range testBooks {
		dirPath := filepath.Dir(filepath.Join(tmpDir, book.path))
		if err := os.MkdirAll(dirPath, 0755); err != nil {
			t.Fatalf("Failed to create directory: %v", err)
		}

		filePath := filepath.Join(tmpDir, book.path)
		if err := os.WriteFile(filePath, []byte("audio data"), 0644); err != nil {
			t.Fatalf("Failed to create file: %v", err)
		}
	}

	// Scan and process
	books, err := ScanDirectory(tmpDir, nil)
	if err != nil {
		t.Fatalf("Scan failed: %v", err)
	}

	if len(books) != len(testBooks) {
		t.Errorf("Expected %d books, got %d", len(testBooks), len(books))
	}

	// Process books (metadata extraction)
	err = ProcessBooksParallel(context.Background(), books, 2, nil, nil)
	if err != nil {
		t.Fatalf("Processing failed: %v", err)
	}

	// Verify all books have format set correctly
	for _, book := range books {
		if book.Format == "" {
			t.Errorf("Book %s has empty format", book.FilePath)
		}

		ext := filepath.Ext(book.FilePath)
		if book.Format != ext {
			t.Errorf("Book %s: format mismatch (expected %s, got %s)", book.FilePath, ext, book.Format)
		}
	}
}

// TestIntegrationLargeScaleMixedFormats tests performance with many files
func TestIntegrationLargeScaleMixedFormats(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping large-scale test in short mode")
	}

	config.AppConfig.SupportedExtensions = []string{".m4b", ".mp3", ".m4a", ".flac"}

	tmpDir, err := os.MkdirTemp("", "largescale-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create 100 authors with 3 books each, mixed formats
	formats := []string{".m4b", ".mp3", ".m4a", ".flac"}
	expectedFiles := 0

	for author := 1; author <= 100; author++ {
		for book := 1; book <= 3; book++ {
			format := formats[(author+book)%len(formats)]
			dirPath := filepath.Join(tmpDir, fmt.Sprintf("Author%d", author), fmt.Sprintf("Book%d", book))

			if err := os.MkdirAll(dirPath, 0755); err != nil {
				t.Fatalf("Failed to create directory: %v", err)
			}

			filePath := filepath.Join(dirPath, "audiobook"+format)
			if err := os.WriteFile(filePath, []byte("data"), 0644); err != nil {
				t.Fatalf("Failed to create file: %v", err)
			}
			expectedFiles++
		}
	}

	// Test with different worker counts
	workerCounts := []int{1, 2, 4, 8}
	for _, workers := range workerCounts {
		t.Run(fmt.Sprintf("workers_%d", workers), func(t *testing.T) {
			books, err := ScanDirectoryParallel(tmpDir, workers, nil)
			if err != nil {
				t.Fatalf("Scan with %d workers failed: %v", workers, err)
			}

			if len(books) != expectedFiles {
				t.Errorf("With %d workers: expected %d files, got %d", workers, expectedFiles, len(books))
			}

			// Verify format distribution is balanced
			formatCounts := make(map[string]int)
			for _, book := range books {
				formatCounts[book.Format]++
			}

			// Each format should have roughly 25% of files
			for format := range formatCounts {
				expected := expectedFiles / len(formats)
				actual := formatCounts[format]
				// Allow 10% variance
				if actual < expected-30 || actual > expected+30 {
					t.Errorf("Format %s distribution off: expected ~%d, got %d", format, expected, actual)
				}
			}
		})
	}
}
