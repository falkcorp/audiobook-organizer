// file: internal/transcode/transcode_integration_test.go
// version: 2.0.0
// guid: c9789340-1670-48d9-88e7-383f2c998be1
// last-edited: 2026-10-18

//go:build integration

package transcode_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/testutil"
	"github.com/falkcorp/audiobook-organizer/internal/transcode"
)

// mockProgressReporter implements operations.ProgressReporter for testing.
type mockProgressReporter struct {
	progressUpdates []progressUpdate
	logEntries      []logEntry
	canceled        bool
}

type progressUpdate struct {
	current int
	total   int
	message string
}

type logEntry struct {
	level   string
	message string
	detail  *string
}

func (m *mockProgressReporter) UpdateProgress(current, total int, message string) error {
	m.progressUpdates = append(m.progressUpdates, progressUpdate{current, total, message})
	return nil
}

func (m *mockProgressReporter) Log(level, message string, detail *string) error {
	m.logEntries = append(m.logEntries, logEntry{level, message, detail})
	return nil
}

func (m *mockProgressReporter) IsCanceled() bool {
	return m.canceled
}

// synthesizeBook generates three short tone MP3s in a temp book directory
// (no committed audio fixtures) and returns the directory and file paths.
func synthesizeBook(t *testing.T) (string, []string) {
	t.Helper()
	bookDir := filepath.Join(t.TempDir(), "tone-book")
	var files []string
	for i := 0; i < 3; i++ {
		p := filepath.Join(bookDir, fmt.Sprintf("track%d.mp3", i+1))
		testutil.SynthesizeMP3(t, p, 2, 440+100*i)
		files = append(files, p)
	}
	return bookDir, files
}

// ffprobeChapterCount uses ffprobe to count chapters in an audio file.
func ffprobeChapterCount(t *testing.T, filePath string) int {
	t.Helper()
	ffprobePath, err := exec.LookPath("ffprobe")
	require.NoError(t, err, "ffprobe must be available")

	cmd := exec.Command(ffprobePath,
		"-v", "quiet",
		"-print_format", "json",
		"-show_chapters",
		filePath,
	)
	output, err := cmd.Output()
	require.NoError(t, err, "ffprobe should succeed on %s", filePath)

	var result struct {
		Chapters []json.RawMessage `json:"chapters"`
	}
	require.NoError(t, json.Unmarshal(output, &result))
	return len(result.Chapters)
}

// ffprobeValidate checks that a file is a valid audio file using ffprobe.
func ffprobeValidate(t *testing.T, filePath string) {
	t.Helper()
	ffprobePath, err := exec.LookPath("ffprobe")
	require.NoError(t, err)

	cmd := exec.Command(ffprobePath,
		"-v", "error",
		"-print_format", "json",
		"-show_format",
		"-show_streams",
		filePath,
	)
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "ffprobe validation failed for %s: %s", filePath, string(output))

	var result struct {
		Format struct {
			FormatName string `json:"format_name"`
			Duration   string `json:"duration"`
		} `json:"format"`
		Streams []struct {
			CodecType string `json:"codec_type"`
			CodecName string `json:"codec_name"`
		} `json:"streams"`
	}
	require.NoError(t, json.Unmarshal(output, &result))
	assert.NotEmpty(t, result.Format.FormatName, "format_name should not be empty")
	assert.NotEmpty(t, result.Format.Duration, "duration should not be empty")

	// Verify at least one audio stream exists
	hasAudio := false
	for _, s := range result.Streams {
		if s.CodecType == "audio" {
			hasAudio = true
			break
		}
	}
	assert.True(t, hasAudio, "M4B should contain at least one audio stream")
}

// TestTranscodeIntegration_M4BConversion tests the Transcode function directly
// using real ffmpeg and generated tone MP3s. The registered library.transcode op
// is covered in internal/server (TestLibraryTranscodeOp_RegisteredOpCreatesVersion).
func TestTranscodeIntegration_M4BConversion(t *testing.T) {
	// Skip if ffmpeg is not available
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not found")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe not found")
	}

	// Set up integration environment with real Pebble store
	env, cleanup := testutil.SetupIntegration(t)
	t.Cleanup(cleanup)

	// Generate synthetic tone MP3s in a temp directory
	bookDir, copiedFiles := synthesizeBook(t)

	// Create a Book record in the database; FilePath is the base path the
	// output M4B is named after.
	book := &database.Book{
		Title:    "Synthetic Tone Book",
		FilePath: filepath.Join(bookDir, "tone-book.mp3"),
		Format:   "mp3",
	}
	createdBook, err := env.Store.CreateBook(book)
	require.NoError(t, err, "CreateBook should succeed")
	require.NotEmpty(t, createdBook.ID, "Book should have an assigned ID")

	// Create BookFile rows for each MP3 (what Transcode reads its inputs from)
	for i, filePath := range copiedFiles {
		fi, err := os.Stat(filePath)
		require.NoError(t, err)
		require.NoError(t, env.Store.CreateBookFile(&database.BookFile{
			ID:               fmt.Sprintf("tone-file-%d", i+1),
			BookID:           createdBook.ID,
			FilePath:         filePath,
			OriginalFilename: filepath.Base(filePath),
			Format:           "mp3",
			Codec:            "mp3",
			TrackNumber:      i + 1,
			TrackCount:       len(copiedFiles),
			FileSize:         fi.Size(),
		}), "CreateBookFile should succeed for track %d", i+1)
	}

	// Set up transcode options
	opts := transcode.TranscodeOpts{
		BookID:       createdBook.ID,
		OutputFormat: "m4b",
		Bitrate:      64,
		KeepOriginal: true,
	}

	// Create mock progress reporter
	progress := &mockProgressReporter{}

	// Run transcode with a generous timeout (5 minutes for 3 MP3 files)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	t.Cleanup(cancel)

	outputPath, err := transcode.Transcode(ctx, opts, env.Store, progress)
	require.NoError(t, err, "Transcode should complete without error")

	// Verify: output M4B file exists
	require.FileExists(t, outputPath, "Output M4B file should exist")

	// Verify: output path has .m4b extension
	assert.Equal(t, ".m4b", filepath.Ext(outputPath), "Output file should have .m4b extension")

	// Verify: the M4B file is a valid audio file
	ffprobeValidate(t, outputPath)

	// Verify: the M4B file has chapters (one per input MP3)
	chapterCount := ffprobeChapterCount(t, outputPath)
	assert.Equal(t, len(copiedFiles), chapterCount,
		"M4B should have %d chapters (one per input file), got %d", len(copiedFiles), chapterCount)

	// Verify: progress reporter received updates
	assert.NotEmpty(t, progress.progressUpdates, "Progress reporter should have received updates")
	assert.NotEmpty(t, progress.logEntries, "Progress reporter should have received log entries")

	// Verify: the last progress update indicates completion
	lastUpdate := progress.progressUpdates[len(progress.progressUpdates)-1]
	assert.Equal(t, 5, lastUpdate.current, "Final progress should be 5")
	assert.Equal(t, 5, lastUpdate.total, "Final progress total should be 5")
	assert.Equal(t, "Complete", lastUpdate.message, "Final progress message should be 'Complete'")

	// Verify: original MP3 files still exist (KeepOriginal=true)
	for _, mp3 := range copiedFiles {
		assert.FileExists(t, mp3, "Original MP3 should still exist when KeepOriginal=true")
	}

	// Clean up the output M4B
	t.Cleanup(func() { os.Remove(outputPath) })
}
