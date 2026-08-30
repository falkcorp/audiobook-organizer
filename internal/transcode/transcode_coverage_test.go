// file: internal/transcode/transcode_coverage_test.go
// version: 1.1.0

package transcode

import (
	"fmt"
	"github.com/falkcorp/audiobook-organizer/internal/pathutil"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// --- TranscodeOpts coverage ---

func TestCoverage_TranscodeOpts(t *testing.T) {
	opts := TranscodeOpts{
		BookID:       "book-123",
		OutputFormat: "m4b",
		Bitrate:      64,
		KeepOriginal: true,
	}
	if opts.BookID != "book-123" {
		t.Error("BookID not set")
	}
	if opts.Bitrate != 64 {
		t.Error("Bitrate not set")
	}
}

// --- CollectInputFiles coverage ---

func TestCoverage_CollectInputFiles_AllMissing(t *testing.T) {
	bookFiles := []database.BookFile{
		{ID: "s1", FilePath: "/nonexistent/track1.mp3", TrackNumber: 1, Missing: true},
		{ID: "s2", FilePath: "/nonexistent/track2.mp3", TrackNumber: 2, Missing: true},
	}

	book := &database.Book{ID: "test-id"}
	_, err := CollectInputFiles(book, bookFiles)
	if err == nil {
		t.Error("expected error when all files are missing")
	}
}

func TestCoverage_CollectInputFiles_MixedMissing(t *testing.T) {
	tmpDir := t.TempDir()

	// Create one real file
	realFile := filepath.Join(tmpDir, "track1.mp3")
	if err := os.WriteFile(realFile, []byte("audio"), 0644); err != nil {
		t.Fatal(err)
	}

	bookFiles := []database.BookFile{
		{ID: "s1", FilePath: realFile, TrackNumber: 1, Missing: false},
		{ID: "s2", FilePath: "/nonexistent/track2.mp3", TrackNumber: 2, Missing: true},
	}

	book := &database.Book{ID: "test-id"}
	files, err := CollectInputFiles(book, bookFiles)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(files) != 1 {
		t.Errorf("expected 1 file, got %d", len(files))
	}
}

func TestCoverage_CollectInputFiles_FileDoesNotExist(t *testing.T) {
	bookFiles := []database.BookFile{
		{ID: "s1", FilePath: "/nonexistent/path.mp3", TrackNumber: 1, Missing: false},
	}

	book := &database.Book{ID: "test-id"}
	_, err := CollectInputFiles(book, bookFiles)
	if err == nil {
		t.Error("expected error when file does not exist on disk")
	}
}

func TestCoverage_CollectInputFiles_SortByPathTiebreaker(t *testing.T) {
	tmpDir := t.TempDir()

	files := make([]string, 3)
	for i := 0; i < 3; i++ {
		f := filepath.Join(tmpDir, fmt.Sprintf("track_%c.mp3", 'c'-i))
		if err := os.WriteFile(f, []byte("audio"), 0644); err != nil {
			t.Fatal(err)
		}
		files[i] = f
	}

	// All same track number - should sort by path
	bookFiles := []database.BookFile{
		{ID: "s1", FilePath: files[0], TrackNumber: 1, Missing: false}, // track_c
		{ID: "s2", FilePath: files[1], TrackNumber: 1, Missing: false}, // track_b
		{ID: "s3", FilePath: files[2], TrackNumber: 1, Missing: false}, // track_a
	}

	book := &database.Book{ID: "test-id"}
	result, err := CollectInputFiles(book, bookFiles)
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 3 {
		t.Fatalf("expected 3, got %d", len(result))
	}
	// Should be sorted alphabetically by path
	if result[0] >= result[1] || result[1] >= result[2] {
		t.Errorf("files not sorted by path: %v", result)
	}
}

// --- BuildConcatFile coverage ---

func TestCoverage_BuildConcatFile_EmptyList(t *testing.T) {
	concatPath, err := BuildConcatFile([]string{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer os.Remove(concatPath)

	data, _ := os.ReadFile(concatPath)
	if len(strings.TrimSpace(string(data))) != 0 {
		t.Error("expected empty content for empty file list")
	}
}

func TestCoverage_BuildConcatFile_SingleFile(t *testing.T) {
	files := []string{"/tmp/single.mp3"}
	concatPath, err := BuildConcatFile(files)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(concatPath)

	data, _ := os.ReadFile(concatPath)
	if !strings.Contains(string(data), "/tmp/single.mp3") {
		t.Error("concat file should contain the file path")
	}
}

// --- BuildChapterMetadataWithProber coverage ---

func TestCoverage_BuildChapterMetadataWithProber_SingleFile(t *testing.T) {
	prober := func(path string) (float64, error) {
		return 120.0, nil
	}

	metaPath, err := BuildChapterMetadataWithProber([]string{"track1.mp3"}, prober)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(metaPath)

	data, _ := os.ReadFile(metaPath)
	content := string(data)
	if !strings.Contains(content, ";FFMETADATA1") {
		t.Error("missing header")
	}
	if !strings.Contains(content, "title=Chapter 1") {
		t.Error("missing chapter title")
	}
	if !strings.Contains(content, "START=0") {
		t.Error("missing START=0")
	}
	if !strings.Contains(content, "END=120000") {
		t.Error("missing END=120000")
	}
}

func TestCoverage_BuildChapterMetadataWithProber_ProberError(t *testing.T) {
	prober := func(path string) (float64, error) {
		return 0, fmt.Errorf("probe failed")
	}

	_, err := BuildChapterMetadataWithProber([]string{"track1.mp3"}, prober)
	if err == nil {
		t.Error("expected error from prober")
	}
}

// --- CleanupStaleTempFiles coverage ---

func TestCoverage_CleanupStaleTempFiles(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a stale temp file
	stalePath := filepath.Join(tmpDir, "book-transcode.tmp.m4b")
	if err := os.WriteFile(stalePath, []byte("temp"), 0644); err != nil {
		t.Fatal(err)
	}
	// Set mod time to the past
	pastTime := time.Now().Add(-2 * time.Hour)
	os.Chtimes(stalePath, pastTime, pastTime)

	// Create a fresh temp file (should not be cleaned)
	freshPath := filepath.Join(tmpDir, "fresh-transcode.tmp.m4b")
	if err := os.WriteFile(freshPath, []byte("temp"), 0644); err != nil {
		t.Fatal(err)
	}

	// Create a .ch.m4b file
	chPath := filepath.Join(tmpDir, "book.ch.m4b")
	if err := os.WriteFile(chPath, []byte("temp"), 0644); err != nil {
		t.Fatal(err)
	}
	os.Chtimes(chPath, pastTime, pastTime)

	// Create a regular file (should not be touched)
	regularPath := filepath.Join(tmpDir, "regular.m4b")
	if err := os.WriteFile(regularPath, []byte("data"), 0644); err != nil {
		t.Fatal(err)
	}
	os.Chtimes(regularPath, pastTime, pastTime)

	cleaned := CleanupStaleTempFiles(tmpDir, pathutil.AppDirs{}, 1*time.Hour)
	if cleaned != 2 {
		t.Errorf("expected 2 cleaned, got %d", cleaned)
	}

	// Verify stale files are gone
	if _, err := os.Stat(stalePath); !os.IsNotExist(err) {
		t.Error("stale transcode tmp should be removed")
	}
	if _, err := os.Stat(chPath); !os.IsNotExist(err) {
		t.Error("stale .ch.m4b should be removed")
	}

	// Verify fresh and regular files remain
	if _, err := os.Stat(freshPath); os.IsNotExist(err) {
		t.Error("fresh file should not be removed")
	}
	if _, err := os.Stat(regularPath); os.IsNotExist(err) {
		t.Error("regular file should not be removed")
	}
}

func TestCoverage_CleanupStaleTempFiles_EmptyDir(t *testing.T) {
	tmpDir := t.TempDir()
	cleaned := CleanupStaleTempFiles(tmpDir, pathutil.AppDirs{}, 1*time.Hour)
	if cleaned != 0 {
		t.Errorf("expected 0 cleaned, got %d", cleaned)
	}
}

func TestCoverage_CleanupStaleTempFiles_NonexistentDir(t *testing.T) {
	cleaned := CleanupStaleTempFiles("/nonexistent/path", pathutil.AppDirs{}, 1*time.Hour)
	if cleaned != 0 {
		t.Errorf("expected 0 cleaned, got %d", cleaned)
	}
}

// --- StartCleanupTicker coverage ---

func TestCoverage_StartCleanupTicker(t *testing.T) {
	tmpDir := t.TempDir()

	stop := StartCleanupTicker(tmpDir, pathutil.AppDirs{}, 100*time.Millisecond, 1*time.Hour)
	// Wait a bit for at least one tick
	time.Sleep(250 * time.Millisecond)
	stop()
	// No panic = success
}

// --- FindFFmpeg / FindFFprobe coverage ---

func TestCoverage_FindFFmpeg(t *testing.T) {
	path, err := FindFFmpeg()
	if err != nil {
		t.Skip("ffmpeg not available on this system")
	}
	if path == "" {
		t.Error("path should not be empty when ffmpeg found")
	}
}

func TestCoverage_FindFFprobe(t *testing.T) {
	path, err := FindFFprobe()
	if err != nil {
		t.Skip("ffprobe not available on this system")
	}
	if path == "" {
		t.Error("path should not be empty when ffprobe found")
	}
}
