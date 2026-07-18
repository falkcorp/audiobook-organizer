// file: internal/fingerprint/duration_unify_test.go
// version: 1.0.0
// guid: 2d9f6b1c-8a3e-4c7d-b0f5-6e1a9c4d7b28
// last-edited: 2026-07-18

package fingerprint

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestProbeDuration_UnifiedProbe_RealFixture is a regression test for TODO
// item 20 / AP-3b (duration-extractor consolidation): fpcalc.go's unexported
// probeDuration now wraps the shared internal/audioutil.ProbeDurationSeconds
// (previously it parsed ffprobe's JSON output independently). This proves the
// reroute still returns the correct float64 seconds for a real audio file —
// FileSegments relies on this value being seconds (not ms, not rounded) to
// compute the 7 fingerprint segment offsets.
func TestProbeDuration_UnifiedProbe_RealFixture(t *testing.T) {
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not on PATH — skipping real-audio duration test")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe not on PATH — skipping real-audio duration test")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "b4-probe-duration.mp3")
	cmd := exec.Command(ffmpegPath,
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "anullsrc=r=44100:cl=mono",
		"-t", fmt.Sprintf("%d", 6),
		"-c:a", "libmp3lame",
		path,
	)
	if err := cmd.Run(); err != nil {
		t.Fatalf("ffmpeg generate fixture: %v", err)
	}

	dur, err := probeDuration(path)
	if err != nil {
		t.Fatalf("probeDuration: %v", err)
	}
	if dur < 5.0 || dur > 7.0 {
		t.Errorf("expected duration near 6.0s (float64 seconds), got %.3f", dur)
	}
}
