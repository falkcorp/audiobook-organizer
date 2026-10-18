// file: internal/testutil/synthetic_audio.go
// version: 1.2.0
// guid: 2c41790f-743f-4a6c-be6d-9a7b9bd38719
// last-edited: 2026-10-18

//go:build integration

package testutil

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// SynthesizeMP3 writes a short generated sine-tone MP3 to path using ffmpeg's
// lavfi source, so integration tests need no committed audio. freqHz varies the
// tone per track. Skips the test only when ffmpeg is missing or lacks the
// libmp3lame encoder; any other ffmpeg failure fails the test.
func SynthesizeMP3(t *testing.T, path string, seconds, freqHz int) {
	t.Helper()
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not found")
	}
	encoders, err := exec.Command(ffmpeg, "-hide_banner", "-encoders").Output()
	require.NoError(t, err, "ffmpeg -encoders failed")
	if !strings.Contains(string(encoders), "libmp3lame") {
		t.Skip("ffmpeg has no libmp3lame encoder")
	}
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	out, err := exec.Command(ffmpeg, "-y", "-v", "error",
		"-f", "lavfi", "-i", fmt.Sprintf("sine=frequency=%d:duration=%d", freqHz, seconds),
		"-c:a", "libmp3lame", "-b:a", "64k", path).CombinedOutput()
	require.NoError(t, err, "ffmpeg could not synthesize %s: %s", path, out)
}

// SynthesizeToneBook generates three short tone MP3s (track1.mp3..track3.mp3)
// in dir and returns their paths.
func SynthesizeToneBook(t *testing.T, dir string) []string {
	t.Helper()
	var files []string
	for i := 0; i < 3; i++ {
		p := filepath.Join(dir, fmt.Sprintf("track%d.mp3", i+1))
		SynthesizeMP3(t, p, 2, 440+100*i)
		files = append(files, p)
	}
	return files
}

// FFprobeValidate fails the test when ffprobe cannot read filePath or finds no
// audio stream, and returns the file's chapter count.
func FFprobeValidate(t *testing.T, filePath string) (chapters int) {
	t.Helper()
	ffprobe, err := exec.LookPath("ffprobe")
	require.NoError(t, err, "ffprobe must be available")
	out, err := exec.Command(ffprobe, "-v", "error", "-print_format", "json",
		"-show_format", "-show_streams", "-show_chapters", filePath).CombinedOutput()
	require.NoError(t, err, "ffprobe failed for %s: %s", filePath, out)
	var res struct {
		Format struct {
			FormatName string `json:"format_name"`
			Duration   string `json:"duration"`
		} `json:"format"`
		Streams []struct {
			CodecType string `json:"codec_type"`
		} `json:"streams"`
		Chapters []json.RawMessage `json:"chapters"`
	}
	require.NoError(t, json.Unmarshal(out, &res))
	assert.NotEmpty(t, res.Format.FormatName, "format_name should not be empty")
	assert.NotEmpty(t, res.Format.Duration, "duration should not be empty")
	hasAudio := false
	for _, s := range res.Streams {
		hasAudio = hasAudio || s.CodecType == "audio"
	}
	assert.True(t, hasAudio, "%s should contain an audio stream", filePath)
	return len(res.Chapters)
}
