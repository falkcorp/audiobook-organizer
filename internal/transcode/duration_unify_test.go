// file: internal/transcode/duration_unify_test.go
// version: 1.0.0
// guid: 6a1e4c8b-3f7d-4b9e-a2c6-8d0f5b3e7c19
// last-edited: 2026-07-18

package transcode

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"testing"
)

// b4GenSilentMP3 writes a silent mono MP3 of the given duration (seconds) to
// dir/name via ffmpeg's lavfi anullsrc source.
func b4GenSilentMP3(t *testing.T, ffmpegPath, dir, name string, seconds int) string {
	t.Helper()
	out := filepath.Join(dir, name)
	cmd := exec.Command(ffmpegPath,
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "anullsrc=r=44100:cl=mono",
		"-t", fmt.Sprintf("%d", seconds),
		"-c:a", "libmp3lame",
		out,
	)
	if err := cmd.Run(); err != nil {
		t.Fatalf("ffmpeg generate fixture: %v", err)
	}
	return out
}

// TestProbeDuration_UnifiedProbe_RealFixture and
// TestProbeFileDuration_UnifiedProbe_RealFixture are regression tests for
// TODO item 20 / AP-3b (duration-extractor consolidation): both unexported
// probers in this file now wrap the shared
// internal/audioutil.ProbeDurationSeconds instead of independently shelling
// out to ffprobe. probeDuration must keep returning float64 seconds
// (BuildChapterMetadataWithProber multiplies by 1000 for chapter timestamps)
// and probeFileDuration must keep returning int64 MICROSECONDS (the transcode
// progress reporter sums these across input files).
func TestProbeDuration_UnifiedProbe_RealFixture(t *testing.T) {
	ffprobePath, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe not on PATH — skipping real-audio duration test")
	}
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not on PATH — skipping real-audio duration test")
	}

	dir := t.TempDir()
	path := b4GenSilentMP3(t, ffmpegPath, dir, "b4-transcode-probe.mp3", 5)

	dur, err := probeDuration(ffprobePath, path)
	if err != nil {
		t.Fatalf("probeDuration: %v", err)
	}
	if dur < 4.0 || dur > 6.0 {
		t.Errorf("expected duration near 5.0s (float64 seconds), got %.3f", dur)
	}
}

func TestProbeFileDuration_UnifiedProbe_RealFixture(t *testing.T) {
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe not on PATH — skipping real-audio duration test")
	}
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not on PATH — skipping real-audio duration test")
	}

	dir := t.TempDir()
	path := b4GenSilentMP3(t, ffmpegPath, dir, "b4-transcode-probefile.mp3", 5)

	us := probeFileDuration(path)
	if us < 4_000_000 || us > 6_000_000 {
		t.Errorf("expected duration near 5,000,000us (int64 microseconds), got %d", us)
	}
}

// TestProbeFileDuration_UnifiedProbe_MissingBinary preserves the original
// best-effort contract: probeFileDuration returns 0 (never an error) when
// ffprobe cannot be resolved or the probe fails.
func TestProbeFileDuration_UnifiedProbe_MissingFile(t *testing.T) {
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe not on PATH — skipping real-audio duration test")
	}
	us := probeFileDuration(filepath.Join(t.TempDir(), "does-not-exist.mp3"))
	if us != 0 {
		t.Errorf("expected 0 for a missing file, got %d", us)
	}
}
