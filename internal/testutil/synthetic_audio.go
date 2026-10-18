// file: internal/testutil/synthetic_audio.go
// version: 1.0.0
// guid: 2c41790f-743f-4a6c-be6d-9a7b9bd38719
// last-edited: 2026-10-18

//go:build integration

package testutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

// SynthesizeMP3 writes a short generated sine-tone MP3 to path using ffmpeg's
// lavfi source, so integration tests need no committed audio. freqHz varies the
// tone per track. Skips the test when ffmpeg or its MP3 encoder is missing.
func SynthesizeMP3(t *testing.T, path string, seconds, freqHz int) {
	t.Helper()
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not found")
	}
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	out, err := exec.Command(ffmpeg, "-y", "-v", "error",
		"-f", "lavfi", "-i", "sine=frequency="+strconv.Itoa(freqHz)+":duration="+strconv.Itoa(seconds),
		"-c:a", "libmp3lame", "-b:a", "64k", path).CombinedOutput()
	if err != nil {
		t.Skipf("ffmpeg could not synthesize an MP3 (libmp3lame missing?): %v: %s", err, out)
	}
}
