// file: internal/mediainfo/duration_unify_test.go
// version: 1.0.1
// guid: 8c7d1a2e-4f9b-4d6a-9e2c-1a5b7d3e9f04
// last-edited: 2026-11-04

package mediainfo

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/testutil/tooltest"
)

// TestRealDurationSec_UnifiedProbe_RealFixture is a regression test for TODO
// item 20 / AP-3b (duration-extractor consolidation): realDurationSec now
// routes through the shared internal/audioutil.ProbeDurationSeconds instead
// of shelling out to ffprobe directly. This proves the reroute still returns
// the correct rounded integer seconds for a real audio file.
func TestRealDurationSec_UnifiedProbe_RealFixture(t *testing.T) {
	ffmpegPath := tooltest.Require(t, "ffmpeg")
	tooltest.Require(t, "ffprobe")

	dir := t.TempDir()
	path := filepath.Join(dir, "b4-real-duration.mp3")
	cmd := exec.Command(ffmpegPath,
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "anullsrc=r=44100:cl=mono",
		"-t", fmt.Sprintf("%d", 4),
		"-c:a", "libmp3lame",
		path,
	)
	if err := cmd.Run(); err != nil {
		t.Fatalf("ffmpeg generate fixture: %v", err)
	}

	secs, err := realDurationSec(path)
	if err != nil {
		t.Fatalf("expected a real audio fixture to probe, got %v", err)
	}
	if secs < 3 || secs > 5 {
		t.Errorf("expected duration near 4s (int, rounded), got %d", secs)
	}
}
