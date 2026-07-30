// file: internal/audioutil/chapters_test.go
// version: 1.0.0
// guid: 0699787d-233a-4b4c-a830-c5069e762c00
// last-edited: 2026-07-29

package audioutil

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// odysseyM4B is the real, committed 115 MB fixture with 6 embedded chapters.
// See testdata/abs-fixtures/README.md item 4 for the ABS ground truth this
// mirrors: the single-file m4b's 6 real embedded chapters are reported as-is.
const odysseyM4B = "../../testdata/audio/librivox/odyssey_butler_librivox/odyssey_complete.m4b"

// odysseyMP3Track1 is one of the 6 per-chapter mp3 fixtures for the same
// book. Individual tracks carry no embedded chapter metadata (confirmed via
// `ffprobe -show_chapters` returning an empty chapters array), which is the
// no-chapters-is-not-an-error case ProbeChapters must handle.
const odysseyMP3Track1 = "../../testdata/audio/librivox/odyssey_butler_librivox/odyssey_01_homer_butler_64kb.mp3"

func requireFFprobe(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe not found on PATH, skipping")
	}
}

func requireFixture(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Skipf("fixture missing at %s, skipping: %v", path, err)
	}
}

func TestProbeChapters_NoChapters_NotAnError(t *testing.T) {
	requireFFprobe(t)
	requireFixture(t, odysseyMP3Track1)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	chs, err := ProbeChapters(ctx, "", odysseyMP3Track1)
	if err != nil {
		t.Fatalf("ProbeChapters(%s) returned error, want nil for a file with no chapters: %v", odysseyMP3Track1, err)
	}
	if chs != nil {
		t.Fatalf("ProbeChapters(%s) = %v, want nil for a file with no chapters", odysseyMP3Track1, chs)
	}
}

func TestProbeChapters_MissingFile_ReturnsWrappedError(t *testing.T) {
	requireFFprobe(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := ProbeChapters(ctx, "", filepath.Join(t.TempDir(), "does-not-exist.m4b"))
	if err == nil {
		t.Fatal("ProbeChapters on a missing file returned nil error, want a wrapped error")
	}
}

func TestProbeChapters_BadFFprobePath_ReturnsWrappedError(t *testing.T) {
	requireFixture(t, odysseyMP3Track1)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := ProbeChapters(ctx, "/nonexistent/ffprobe-binary-xyz", odysseyMP3Track1)
	if err == nil {
		t.Fatal("ProbeChapters with a nonexistent ffprobe binary returned nil error, want a wrapped error")
	}
}

// TestProbeChapters_OdysseyM4B_SixEmbeddedChapters is the integration test
// against the real 115 MB fixture. Ground truth (captured directly via
// `ffprobe -v error -show_chapters -print_format json` against this exact
// file, matching the task's stated values): 6 chapters, first starts at
// 0.000000 titled "Chapter 1: odyssey_01_homer_butler_64kb", starts strictly
// increasing, last EndSec ~= 9975.428 (total duration ~= 9975.48s).
func TestProbeChapters_OdysseyM4B_SixEmbeddedChapters(t *testing.T) {
	requireFFprobe(t)
	requireFixture(t, odysseyM4B)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	chs, err := ProbeChapters(ctx, "", odysseyM4B)
	if err != nil {
		t.Fatalf("ProbeChapters(%s) error: %v", odysseyM4B, err)
	}
	if len(chs) != 6 {
		t.Fatalf("ProbeChapters(%s) returned %d chapters, want 6", odysseyM4B, len(chs))
	}

	if chs[0].StartSec != 0 {
		t.Errorf("chs[0].StartSec = %v, want 0", chs[0].StartSec)
	}
	wantFirstTitle := "Chapter 1: odyssey_01_homer_butler_64kb"
	if chs[0].Title != wantFirstTitle {
		t.Errorf("chs[0].Title = %q, want %q", chs[0].Title, wantFirstTitle)
	}

	for i := 1; i < len(chs); i++ {
		if chs[i].StartSec <= chs[i-1].StartSec {
			t.Errorf("chapter starts not monotonically increasing at index %d: chs[%d].StartSec=%v <= chs[%d].StartSec=%v",
				i, i, chs[i].StartSec, i-1, chs[i-1].StartSec)
		}
		// ABS's own contract: each chapter's start immediately follows the
		// previous chapter's end (no gaps) for embedded m4b chapters.
		if !floatsClose(chs[i].StartSec, chs[i-1].EndSec) {
			t.Errorf("chapter %d start (%v) does not match chapter %d end (%v)", i, chs[i].StartSec, i-1, chs[i-1].EndSec)
		}
	}

	const wantTotalDuration = 9975.428
	lastEnd := chs[len(chs)-1].EndSec
	if diff := lastEnd - wantTotalDuration; diff > 0.01 || diff < -0.01 {
		t.Errorf("last chapter EndSec = %v, want ~= %v (within 0.01s)", lastEnd, wantTotalDuration)
	}

	for i, c := range chs {
		if c.ID != i {
			t.Errorf("chs[%d].ID = %d, want %d", i, c.ID, i)
		}
	}
}

// TestProbeChapters_JSONShape documents (via a table-free smoke check) that
// ProbeChapters is parsing ffprobe's real JSON shape: start_time/end_time as
// strings, not the sibling start/end integer+time_base pair. This guards
// against silently falling back to the int fields, which the task explicitly
// says must NOT be preferred.
func TestProbeChapters_JSONShape(t *testing.T) {
	// Sanity-check our own assumption about ffprobe's JSON shape using a
	// literal captured sample (see task ground truth), independent of the
	// fixture files, so this test never skips.
	sample := []byte(`{
		"chapters": [
			{
				"id": 0,
				"time_base": "1/1000",
				"start": 0,
				"start_time": "0.000000",
				"end": 1386057,
				"end_time": "1386.057000",
				"tags": {"title": "Chapter 1: odyssey_01_homer_butler_64kb"}
			}
		]
	}`)
	var parsed ffprobeChaptersOutput
	if err := json.Unmarshal(sample, &parsed); err != nil {
		t.Fatalf("failed to unmarshal sample ffprobe chapters JSON: %v", err)
	}
	if len(parsed.Chapters) != 1 {
		t.Fatalf("parsed %d chapters, want 1", len(parsed.Chapters))
	}
	if parsed.Chapters[0].StartTime != "0.000000" {
		t.Errorf("StartTime = %q, want %q", parsed.Chapters[0].StartTime, "0.000000")
	}
	if parsed.Chapters[0].EndTime != "1386.057000" {
		t.Errorf("EndTime = %q, want %q", parsed.Chapters[0].EndTime, "1386.057000")
	}
}
