// file: internal/audioutil/timeline_test.go
// version: 1.0.0
// guid: 7fd599c0-529b-4987-acdd-d87d40c32802
// last-edited: 2026-07-29

package audioutil

import (
	"math"
	"testing"
)

const epsilon = 1e-6

func floatsClose(a, b float64) bool {
	return math.Abs(a-b) < epsilon
}

func TestCumulativeOffsets_Empty(t *testing.T) {
	got := CumulativeOffsets(nil)
	if len(got) != 0 {
		t.Fatalf("CumulativeOffsets(nil) = %v, want empty", got)
	}
}

func TestCumulativeOffsets_Single(t *testing.T) {
	got := CumulativeOffsets([]float64{42.5})
	want := []float64{0}
	if len(got) != len(want) || !floatsClose(got[0], want[0]) {
		t.Fatalf("CumulativeOffsets([42.5]) = %v, want %v", got, want)
	}
}

// TestCumulativeOffsets_RealOdysseyTracks reproduces the real ABS ground-truth
// startOffset values captured from a live Audiobookshelf 2.36.0 server for the
// 6-file Odyssey (Butler/LibriVox) book: 0, 1386.057143, 2788.702041,
// 4309.211429, ... (see testdata/abs-fixtures/README.md item 3). The input
// durations here are the real ffprobe-reported durations of the 6 committed
// mp3 fixtures. This test intentionally uses exact literal float values (not
// rounded) and a small epsilon, per the task's float-precision requirement.
func TestCumulativeOffsets_RealOdysseyTracks(t *testing.T) {
	durations := []float64{
		1386.057143,
		1402.644898,
		1520.509388,
		2619.767800,
		1673.221224,
		1373.230658,
	}
	want := []float64{
		0,
		1386.057143,
		2788.702041,
		4309.211429,
		6928.979229,
		8602.200453,
	}
	got := CumulativeOffsets(durations)
	if len(got) != len(want) {
		t.Fatalf("CumulativeOffsets returned %d offsets, want %d", len(got), len(want))
	}
	for i := range want {
		if !floatsClose(got[i], want[i]) {
			t.Errorf("offset[%d] = %v, want %v (diff %v)", i, got[i], want[i], math.Abs(got[i]-want[i]))
		}
	}
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

func TestShiftChapters(t *testing.T) {
	in := []Chapter{
		{ID: 0, StartSec: 0, EndSec: 100.5, Title: "Chapter 1"},
		{ID: 1, StartSec: 100.5, EndSec: 250.25, Title: "Chapter 2"},
	}
	got := ShiftChapters(in, 4309.211429)
	if len(got) != 2 {
		t.Fatalf("ShiftChapters returned %d chapters, want 2", len(got))
	}
	if !floatsClose(got[0].StartSec, 4309.211429) {
		t.Errorf("got[0].StartSec = %v, want %v", got[0].StartSec, 4309.211429)
	}
	if !floatsClose(got[0].EndSec, 4309.211429+100.5) {
		t.Errorf("got[0].EndSec = %v, want %v", got[0].EndSec, 4309.211429+100.5)
	}
	if !floatsClose(got[1].StartSec, 4309.211429+100.5) {
		t.Errorf("got[1].StartSec = %v, want %v", got[1].StartSec, 4309.211429+100.5)
	}
	if got[0].Title != "Chapter 1" || got[1].Title != "Chapter 2" {
		t.Errorf("ShiftChapters must not alter titles, got %q, %q", got[0].Title, got[1].Title)
	}
	if got[0].ID != 0 || got[1].ID != 1 {
		t.Errorf("ShiftChapters must not alter IDs, got %d, %d", got[0].ID, got[1].ID)
	}
	// Original slice must not be mutated.
	if !floatsClose(in[0].StartSec, 0) {
		t.Errorf("ShiftChapters mutated its input: in[0].StartSec = %v, want 0", in[0].StartSec)
	}
}

func TestShiftChapters_ZeroOffsetEmpty(t *testing.T) {
	got := ShiftChapters(nil, 0)
	if len(got) != 0 {
		t.Fatalf("ShiftChapters(nil, 0) = %v, want empty", got)
	}
}
