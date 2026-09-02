// file: internal/transcode/transcode_test.go
// version: 1.0.1
// guid: a9b8c7d6-e5f4-3210-fedc-ba9876543210
// last-edited: 2026-09-02

package transcode

import (
	"os"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

func TestBuildConcatFile(t *testing.T) {
	files := []string{"/tmp/track01.mp3", "/tmp/track02.mp3", "/tmp/track03.mp3"}
	concatPath, err := BuildConcatFile(files)
	if err != nil {
		t.Fatalf("BuildConcatFile failed: %v", err)
	}
	defer os.Remove(concatPath)

	data, err := os.ReadFile(concatPath)
	if err != nil {
		t.Fatalf("failed to read concat file: %v", err)
	}
	content := string(data)
	for _, f := range files {
		if !strings.Contains(content, f) {
			t.Errorf("concat file missing %s", f)
		}
	}
	lines := strings.Split(strings.TrimSpace(content), "\n")
	if len(lines) != 3 {
		t.Errorf("expected 3 lines, got %d", len(lines))
	}
}

func TestBuildConcatFile_EscapesSingleQuotes(t *testing.T) {
	files := []string{"/tmp/it's a test.mp3"}
	concatPath, err := BuildConcatFile(files)
	if err != nil {
		t.Fatalf("BuildConcatFile failed: %v", err)
	}
	defer os.Remove(concatPath)

	data, err := os.ReadFile(concatPath)
	if err != nil {
		t.Fatalf("failed to read concat file: %v", err)
	}
	if !strings.Contains(string(data), `'\''`) {
		t.Error("single quote not properly escaped")
	}
}

func TestBuildChapterMetadataWithProber(t *testing.T) {
	files := []string{"track1.mp3", "track2.mp3", "track3.mp3"}
	durations := map[string]float64{
		"track1.mp3": 300.0,
		"track2.mp3": 600.0,
		"track3.mp3": 450.0,
	}
	prober := func(path string) (float64, error) {
		return durations[path], nil
	}

	metaPath, err := BuildChapterMetadataWithProber(files, prober)
	if err != nil {
		t.Fatalf("BuildChapterMetadataWithProber failed: %v", err)
	}
	defer os.Remove(metaPath)

	data, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatalf("failed to read metadata file: %v", err)
	}
	content := string(data)

	if !strings.Contains(content, ";FFMETADATA1") {
		t.Error("missing FFMETADATA1 header")
	}
	if !strings.Contains(content, "[CHAPTER]") {
		t.Error("missing CHAPTER section")
	}
	if !strings.Contains(content, "title=Chapter 1") {
		t.Error("missing Chapter 1 title")
	}
	if !strings.Contains(content, "title=Chapter 3") {
		t.Error("missing Chapter 3 title")
	}
	// Chapter 1: START=0, END=300000
	if !strings.Contains(content, "START=0") {
		t.Error("Chapter 1 should start at 0")
	}
	if !strings.Contains(content, "END=300000") {
		t.Error("Chapter 1 should end at 300000")
	}
	// Chapter 2: START=300000, END=900000
	if !strings.Contains(content, "START=300000") {
		t.Error("Chapter 2 should start at 300000")
	}
	// Chapter 3: START=900000
	if !strings.Contains(content, "START=900000") {
		t.Error("Chapter 3 should start at 900000")
	}
}

func TestCollectInputFiles_SingleFile(t *testing.T) {
	// Create a temp file to use as the book file
	tmp, err := os.CreateTemp("", "test-audio-*.mp3")
	if err != nil {
		t.Fatal(err)
	}
	tmp.Close()
	defer os.Remove(tmp.Name())

	book := &database.Book{
		ID:       "test-id",
		FilePath: tmp.Name(),
	}

	files, err := CollectInputFiles(book, nil)
	if err != nil {
		t.Fatalf("CollectInputFiles failed: %v", err)
	}
	if len(files) != 1 || files[0] != tmp.Name() {
		t.Errorf("expected [%s], got %v", tmp.Name(), files)
	}
}

func TestCollectInputFiles_Segments(t *testing.T) {
	// Create temp files
	var tmpFiles []string
	for range 3 {
		tmp, err := os.CreateTemp("", "test-seg-*.mp3")
		if err != nil {
			t.Fatal(err)
		}
		tmp.Close()
		tmpFiles = append(tmpFiles, tmp.Name())
		defer os.Remove(tmp.Name())
	}

	bookFiles := []database.BookFile{
		{ID: "s1", FilePath: tmpFiles[0], TrackNumber: 3},
		{ID: "s2", FilePath: tmpFiles[1], TrackNumber: 1},
		{ID: "s3", FilePath: tmpFiles[2], TrackNumber: 2},
	}

	book := &database.Book{ID: "test-id"}
	files, err := CollectInputFiles(book, bookFiles)
	if err != nil {
		t.Fatalf("CollectInputFiles failed: %v", err)
	}
	if len(files) != 3 {
		t.Fatalf("expected 3 files, got %d", len(files))
	}
	// Should be sorted by track number: 1, 2, 3
	if files[0] != tmpFiles[1] {
		t.Errorf("expected track 1 first, got %s", files[0])
	}
	if files[1] != tmpFiles[2] {
		t.Errorf("expected track 2 second, got %s", files[1])
	}
	if files[2] != tmpFiles[0] {
		t.Errorf("expected track 3 third, got %s", files[2])
	}
}

func TestCollectInputFiles_NoFile(t *testing.T) {
	book := &database.Book{ID: "test-id", FilePath: ""}
	_, err := CollectInputFiles(book, nil)
	if err == nil {
		t.Error("expected error for empty file path")
	}
}

func TestCollectInputFiles_MissingFile(t *testing.T) {
	book := &database.Book{ID: "test-id", FilePath: "/nonexistent/file.mp3"}
	_, err := CollectInputFiles(book, nil)
	if err == nil {
		t.Error("expected error for missing file")
	}
}
