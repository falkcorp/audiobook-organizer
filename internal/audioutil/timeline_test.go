// file: internal/audioutil/timeline_test.go
// version: 1.0.1
// guid: 7fd599c0-529b-4987-acdd-d87d40c32802
// last-edited: 2026-10-12

package audioutil

import (
	"math"
	"testing"
)

const epsilon = 1e-6

func floatsClose(a, b float64) bool {
	return math.Abs(a-b) < epsilon
}

func TestSynthesizeChapters_TitleFallsBackToFilename(t *testing.T) {
	tracks := []TrackInfo{
		{Title: "The Odyssey: Book 01", Filename: "odyssey_01_homer_butler_64kb.mp3", DurationSec: 1386.057143},
		{Title: "", Filename: "odyssey_02_homer_butler_64kb.mp3", DurationSec: 1402.644898},
	}
	chs := SynthesizeChapters(tracks)
	if len(chs) != 2 {
		t.Fatalf("SynthesizeChapters returned %d chapters, want 2", len(chs))
	}
	if chs[0].ID != 0 || chs[1].ID != 1 {
		t.Errorf("chapter IDs = %d, %d, want 0, 1", chs[0].ID, chs[1].ID)
	}
	if chs[0].Title != "The Odyssey: Book 01" {
		t.Errorf("chs[0].Title = %q, want %q", chs[0].Title, "The Odyssey: Book 01")
	}
	if chs[1].Title != "odyssey_02_homer_butler_64kb.mp3" {
		t.Errorf("chs[1].Title (fallback) = %q, want filename", chs[1].Title)
	}
	if !floatsClose(chs[0].StartSec, 0) {
		t.Errorf("chs[0].StartSec = %v, want 0", chs[0].StartSec)
	}
	if !floatsClose(chs[0].EndSec, 1386.057143) {
		t.Errorf("chs[0].EndSec = %v, want 1386.057143", chs[0].EndSec)
	}
	if !floatsClose(chs[1].StartSec, chs[0].EndSec) {
		t.Errorf("chs[1].StartSec = %v, want to match chs[0].EndSec = %v", chs[1].StartSec, chs[0].EndSec)
	}
	wantEnd := 1386.057143 + 1402.644898
	if !floatsClose(chs[1].EndSec, wantEnd) {
		t.Errorf("chs[1].EndSec = %v, want %v", chs[1].EndSec, wantEnd)
	}
}

func TestSynthesizeChapters_Empty(t *testing.T) {
	got := SynthesizeChapters(nil)
	if len(got) != 0 {
		t.Fatalf("SynthesizeChapters(nil) = %v, want empty", got)
	}
}
