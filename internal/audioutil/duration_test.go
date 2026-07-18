// file: internal/audioutil/duration_test.go
// version: 1.0.0
// guid: 4b0e6d3a-7c1f-4a52-9d8e-2f6a1c9b0d47
// last-edited: 2026-07-18

package audioutil

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// b4RequireFFmpeg skips the test when ffmpeg/ffprobe are not on PATH — these
// tests generate a real audio fixture and probe it, rather than relying on
// LFS-gated testdata fixtures that may not be checked out.
func b4RequireFFmpeg(t *testing.T) (ffmpegPath, ffprobePath string) {
	t.Helper()
	fp, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not on PATH — skipping real-audio duration test")
	}
	pp, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe not on PATH — skipping real-audio duration test")
	}
	return fp, pp
}

// b4GenerateSilentAudio writes a silent audio file of the given duration
// (seconds) to dir/name using ffmpeg's lavfi anullsrc source, and returns the
// full path. Fails the test on any ffmpeg error.
func b4GenerateSilentAudio(t *testing.T, ffmpegPath, dir, name string, seconds int) string {
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

func TestProbeDurationSeconds_RealFixture(t *testing.T) {
	ffmpegPath, _ := b4RequireFFmpeg(t)
	dir := t.TempDir()
	path := b4GenerateSilentAudio(t, ffmpegPath, dir, "b4-3s.mp3", 3)

	secs, err := ProbeDurationSeconds(context.Background(), "", path)
	if err != nil {
		t.Fatalf("ProbeDurationSeconds: %v", err)
	}
	// mp3 encoders pad slightly; allow generous tolerance.
	if secs < 2.5 || secs > 3.5 {
		t.Errorf("expected duration near 3s, got %.3f", secs)
	}
}

func TestProbeDurationSeconds_ExplicitFFprobePath(t *testing.T) {
	ffmpegPath, ffprobePath := b4RequireFFmpeg(t)
	dir := t.TempDir()
	path := b4GenerateSilentAudio(t, ffmpegPath, dir, "b4-2s.mp3", 2)

	secs, err := ProbeDurationSeconds(context.Background(), ffprobePath, path)
	if err != nil {
		t.Fatalf("ProbeDurationSeconds: %v", err)
	}
	if secs < 1.5 || secs > 2.5 {
		t.Errorf("expected duration near 2s, got %.3f", secs)
	}
}

func TestProbeDurationSeconds_MissingFile(t *testing.T) {
	b4RequireFFmpeg(t)
	_, err := ProbeDurationSeconds(context.Background(), "", filepath.Join(t.TempDir(), "does-not-exist.mp3"))
	if err == nil {
		t.Error("expected an error for a missing file")
	}
}

func TestProbeDurationSeconds_MissingFFprobeBinary(t *testing.T) {
	_, err := ProbeDurationSeconds(context.Background(), filepath.Join(t.TempDir(), "no-such-ffprobe-binary"), "irrelevant.mp3")
	if err == nil {
		t.Error("expected an error when the ffprobe binary path does not exist")
	}
}

func TestProbeDurationSeconds_UnparseableOutput(t *testing.T) {
	// Use `true`-like binary substitute: a tiny shell script that prints
	// non-numeric stdout, to exercise the ParseFloat failure branch without
	// depending on a real ffprobe build.
	dir := t.TempDir()
	fakeProbe := filepath.Join(dir, "fake-ffprobe.sh")
	script := "#!/bin/sh\necho not-a-number\n"
	if err := os.WriteFile(fakeProbe, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake probe script: %v", err)
	}
	_, err := ProbeDurationSeconds(context.Background(), fakeProbe, filepath.Join(dir, "whatever.mp3"))
	if err == nil {
		t.Error("expected a parse error for non-numeric ffprobe output")
	}
}

func TestProbeDurationSeconds_ContextTimeout(t *testing.T) {
	dir := t.TempDir()
	slowProbe := filepath.Join(dir, "slow-ffprobe.sh")
	script := "#!/bin/sh\nsleep 5\necho 1.0\n"
	if err := os.WriteFile(slowProbe, []byte(script), 0o755); err != nil {
		t.Fatalf("write slow probe script: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := ProbeDurationSeconds(ctx, slowProbe, filepath.Join(dir, "whatever.mp3"))
	if err == nil {
		t.Error("expected an error when ctx times out before ffprobe finishes")
	}
}
