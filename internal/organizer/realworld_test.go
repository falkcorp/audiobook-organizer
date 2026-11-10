// file: internal/organizer/realworld_test.go
// version: 1.0.1
// guid: 2b3c4d5e-6f7a-8b9c-0d1e-2f3a4b5c6d7e

package organizer

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/config"
)

// TestRealWorldFileParsing tests the organizer with 1000 random real-world file paths
func TestRealWorldFileParsing(t *testing.T) {
	// Read sample file list
	sampleFile := filepath.Join("..", "..", "testdata", "sample-1000-files.txt")
	files, err := readFileList(sampleFile)
	if err != nil {
		t.Fatalf("failed to read sample file list: %v", err)
	}

	if len(files) < 800 {
		t.Fatalf("expected at least 800 files, got %d", len(files))
	}

	t.Logf("Testing with %d real-world file paths", len(files))

	// Statistics
	stats := &ParsingStats{
		ByExtension:   make(map[string]int),
		ByAuthorCount: make(map[bool]int),
		BySeriesCount: make(map[bool]int),
	}

	tmpDir := t.TempDir()
	org := &Organizer{
		config: &config.Config{
			RootDir:             tmpDir,
			FolderNamingPattern: "{author}/{series}",
			FileNamingPattern:   "{title}",
		},
	}

	audioExtensions := map[string]bool{
		".m4b": true, ".mp3": true, ".m4a": true,
		".aac": true, ".flac": true, ".ogg": true, ".opus": true,
	}

	successCount := 0
	audioFileCount := 0

	for i, filePath := range files {
		ext := strings.ToLower(filepath.Ext(filePath))
		stats.ByExtension[ext]++

		// Only test audio files for organization
		if !audioExtensions[ext] {
			continue
		}

		audioFileCount++

		// Extract basic metadata from path
		book := extractMetadataFromPath(filePath)
		if book == nil {
			t.Logf("File %d: Could not extract metadata from: %s", i+1, filePath)
			continue
		}

		// Track statistics
		stats.ByAuthorCount[book.Author != nil]++
		stats.BySeriesCount[book.Series != nil]++

		// Try to generate target path
		targetPath, err := org.generateTargetPath(book)
		if err != nil {
			t.Errorf("File %d: Failed to generate target path for %s: %v", i+1, filePath, err)
			continue
		}

		// Validate target path
		if targetPath == "" {
			t.Errorf("File %d: Empty target path for %s", i+1, filePath)
			continue
		}

		// Verify target path has correct extension (case-insensitive)
		targetExt := strings.ToLower(filepath.Ext(targetPath))
		sourceExt := strings.ToLower(ext)
		if targetExt != sourceExt {
			t.Errorf("File %d: Extension mismatch: source=%s, target=%s", i+1, ext, filepath.Ext(targetPath))
			continue
		}

		successCount++

		// Log first 10 successful conversions
		if successCount <= 10 {
			t.Logf("Success %d:\n  Source: %s\n  Target: %s", successCount, filePath, targetPath)
		}
	}

	// Print statistics
	t.Logf("\n=== PARSING STATISTICS ===")
	t.Logf("Total files: %d", len(files))
	t.Logf("Audio files: %d", audioFileCount)
	t.Logf("Successfully organized: %d", successCount)
	t.Logf("Success rate: %.1f%%", float64(successCount)/float64(audioFileCount)*100)

	t.Logf("\n=== FILE EXTENSIONS ===")
	for ext, count := range stats.ByExtension {
		if count > 5 {
			t.Logf("  %s: %d", ext, count)
		}
	}

	t.Logf("\n=== METADATA EXTRACTION ===")
	t.Logf("Files with author: %d (%.1f%%)", stats.ByAuthorCount[true],
		float64(stats.ByAuthorCount[true])/float64(audioFileCount)*100)
	t.Logf("Files with series: %d (%.1f%%)", stats.BySeriesCount[true],
		float64(stats.BySeriesCount[true])/float64(audioFileCount)*100)

	// Require at least 80% success rate for audio files
	if successCount < int(float64(audioFileCount)*0.8) {
		t.Errorf("Success rate too low: %d/%d (%.1f%%), expected at least 80%%",
			successCount, audioFileCount, float64(successCount)/float64(audioFileCount)*100)
	}
}

// TestRealWorldPatternVariations tests different pattern configurations with real data
func TestRealWorldPatternVariations(t *testing.T) {
	sampleFile := filepath.Join("..", "..", "testdata", "sample-1000-files.txt")
	files, err := readFileList(sampleFile)
	if err != nil {
		t.Fatalf("failed to read sample file list: %v", err)
	}

	patterns := []struct {
		name       string
		folderPat  string
		filePat    string
		minSuccess int
	}{
		{
			name:       "Author only",
			folderPat:  "{author}",
			filePat:    "{title}",
			minSuccess: 600,
		},
		{
			name:       "Author and Series",
			folderPat:  "{author}/{series}",
			filePat:    "{title}",
			minSuccess: 400,
		},
		{
			name:       "Series with number",
			folderPat:  "{author}/{series}",
			filePat:    "{series_number} - {title}",
			minSuccess: 100,
		},
		{
			name:       "With narrator",
			folderPat:  "{author}",
			filePat:    "{title} - {narrator}",
			minSuccess: 50,
		},
	}

	audioExtensions := map[string]bool{
		".m4b": true, ".mp3": true, ".m4a": true,
		".aac": true, ".flac": true, ".ogg": true, ".opus": true,
	}

	// Filter to audio files only
	audioFiles := make([]string, 0, 800)
	for _, file := range files {
		if audioExtensions[strings.ToLower(filepath.Ext(file))] {
			audioFiles = append(audioFiles, file)
		}
	}

	t.Logf("Testing %d audio files with %d pattern variations", len(audioFiles), len(patterns))

	for _, pattern := range patterns {
		t.Run(pattern.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			org := &Organizer{
				config: &config.Config{
					RootDir:             tmpDir,
					FolderNamingPattern: pattern.folderPat,
					FileNamingPattern:   pattern.filePat,
				},
			}

			successCount := 0
			for _, filePath := range audioFiles {
				book := extractMetadataFromPath(filePath)
				if book == nil {
					continue
				}

				targetPath, err := org.generateTargetPath(book)
				if err != nil || targetPath == "" {
					continue
				}

				// Verify path is valid
				if !strings.Contains(targetPath, tmpDir) {
					t.Errorf("Invalid target path (missing root): %s", targetPath)
					continue
				}

				successCount++
			}

			successRate := float64(successCount) / float64(len(audioFiles)) * 100
			t.Logf("Pattern: %s -> %s", pattern.folderPat, pattern.filePat)
			t.Logf("Success: %d/%d (%.1f%%)", successCount, len(audioFiles), successRate)

			if successCount < pattern.minSuccess {
				t.Errorf("Expected at least %d successful paths, got %d",
					pattern.minSuccess, successCount)
			}
		})
	}
}

// ParsingStats tracks statistics about file parsing
type ParsingStats struct {
	ByExtension   map[string]int
	ByAuthorCount map[bool]int
	BySeriesCount map[bool]int
}

// readFileList reads a list of file paths from a text file
func readFileList(filename string) ([]string, error) {
	file, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var files []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			files = append(files, line)
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return files, nil
}

// extractMetadataFromPath attempts to extract basic metadata from a file path
// This is a simplified version - in production, the scanner package would handle this
func extractMetadataFromPath(filePath string) *database.Book {
	// Get filename without extension
	base := filepath.Base(filePath)
	ext := filepath.Ext(base)
	nameWithoutExt := strings.TrimSuffix(base, ext)

	// Get directory name (potential author or series)
	dir := filepath.Dir(filePath)
	parentDir := filepath.Base(dir)

	book := &database.Book{
		Title:    cleanTitle(nameWithoutExt),
		FilePath: filePath,
	}

	// Try to extract author from filename patterns
	// Pattern: "Author - Title"
	if parts := strings.Split(nameWithoutExt, " - "); len(parts) >= 2 {
		book.Author = &database.Author{Name: cleanAuthorName(parts[0])}
		book.Title = cleanTitle(parts[1])
	} else if isLikelyAuthorDir(parentDir) {
		// Use directory as author if it looks like an author name
		book.Author = &database.Author{Name: cleanAuthorName(parentDir)}
	} else {
		book.Author = &database.Author{Name: "Unknown Author"}
	}

	// Try to extract series information
	if series, position := extractSeriesInfo(nameWithoutExt); series != "" {
		book.Series = &database.Series{Name: series}
		if position > 0 {
			book.SeriesSequence = &position
		}
	}

	// Try to extract narrator
	if narrator := extractNarrator(nameWithoutExt); narrator != "" {
		book.Narrator = &narrator
	}

	return book
}

// cleanTitle removes common prefixes and cleans up title
func cleanTitle(title string) string {
	title = strings.TrimSpace(title)

	// Remove leading numbers like "01 ", "001 ", etc.
	if len(title) > 0 && title[0] >= '0' && title[0] <= '9' {
		parts := strings.SplitN(title, " ", 2)
		if len(parts) == 2 {
			title = parts[1]
		}
	}

	// Remove book number indicators
	title = strings.TrimPrefix(title, "Book ")
	title = strings.TrimSpace(title)

	if title == "" {
		return "Unknown Title"
	}

	return title
}

// cleanAuthorName cleans up author name
func cleanAuthorName(name string) string {
	name = strings.TrimSpace(name)

	// Remove "by" prefix
	name = strings.TrimPrefix(name, "by ")
	name = strings.TrimPrefix(name, "By ")

	// Remove common file markers
	name = strings.Split(name, "(")[0]
	name = strings.Split(name, "[")[0]
	name = strings.TrimSpace(name)

	if name == "" {
		return "Unknown Author"
	}

	return name
}

// isLikelyAuthorDir checks if a directory name looks like an author name
func isLikelyAuthorDir(dir string) bool {
	dir = strings.ToLower(dir)

	// Skip common non-author directories
	skipDirs := []string{
		"books", "audiobooks", "bt", "incomplete", "downloads",
		"media", "audio", "library", "collection", "eb2",
	}

	for _, skip := range skipDirs {
		if strings.Contains(dir, skip) {
			return false
		}
	}

	// Author names typically have spaces or commas
	return strings.Contains(dir, " ") || strings.Contains(dir, ",")
}

// extractSeriesInfo attempts to extract series name and position
func extractSeriesInfo(text string) (string, int) {
	// Pattern: "Series Name Book 3 - Title" or "Series Book 03"
	if strings.Contains(text, "Book ") {
		parts := strings.Split(text, "Book ")
		if len(parts) >= 2 {
			seriesPart := strings.TrimSpace(parts[0])
			numPart := strings.TrimSpace(parts[1])

			// Extract number - simplified parsing
			var position int
			numStr := ""
			for _, ch := range numPart {
				if ch >= '0' && ch <= '9' {
					numStr += string(ch)
				} else if numStr != "" {
					break
				}
			}

			if numStr != "" {
				// Simple conversion
				for _, ch := range numStr {
					position = position*10 + int(ch-'0')
				}
			}

			if seriesPart != "" {
				return seriesPart, position
			}
		}
	}

	// Pattern: "Series Name #3" or "Series Name 03"
	// ... (simplified for test purposes)

	return "", 0
}

// extractNarrator attempts to extract narrator from filename
func extractNarrator(text string) string {
	// Look for patterns like "narrated by X" or "read by X"
	lower := strings.ToLower(text)
	if idx := strings.Index(lower, "narrated by "); idx != -1 {
		narrator := text[idx+12:]
		// Take until next special char
		for i, ch := range narrator {
			if ch == '(' || ch == '[' || ch == '-' {
				return strings.TrimSpace(narrator[:i])
			}
		}
		return strings.TrimSpace(narrator)
	}

	return ""
}
