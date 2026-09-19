// file: internal/fingerprint/window_similarity_test.go
// version: 1.0.0
// guid: c4b8f8ce-efbc-42c4-bb0a-87ade27a87f5
// last-edited: 2026-09-19

package fingerprint

import (
	"encoding/binary"
	"errors"
	"math/rand/v2"
	"testing"
)

// frameStream is a deterministic pseudo-random stream of chromaprint frames:
// frame i of stream seed is the same wherever it is sliced from.
func frameStream(seed uint64, n int) []uint32 {
	r := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	out := make([]uint32, n)
	for i := range out {
		out[i] = r.Uint32()
	}
	return out
}

func packFrames(f []uint32) []byte {
	b := make([]byte, len(f)*4)
	for i, v := range f {
		binary.LittleEndian.PutUint32(b[i*4:], v)
	}
	return b
}

const testWinFrames = 968 // ~120 s

// window cuts testWinFrames frames out of content starting at frame start.
func window(content []uint32, start, slot int, dur float64) WindowPrint {
	return WindowPrint{
		Kind: WindowKindWindow, SlotBP: slot, WindowSet: WindowSetWS1,
		LengthSec: 120, DurationUsedSec: dur, DurationSource: DurationSourceBookFile,
		Frames: testWinFrames, Raw: packFrames(content[start : start+testWinFrames]),
		Algorithm: WindowAlgorithm, Pipeline: WindowPipelineID,
		FpcalcVersion: "1.6.1", FFmpegVersion: "8.0.1",
	}
}

func head(content []uint32, dur float64) WindowPrint {
	w := window(content, 0, 0, dur)
	w.Kind = WindowKindHead
	return w
}

// bookSet is a ws1 set for a book whose content is the frame stream c,
// cutting windows at 10/50/90% of dur.
func bookSet(c []uint32, dur float64) []WindowPrint {
	set := []WindowPrint{head(c, dur)}
	for _, slot := range []int{1000, 5000, 9000} {
		start := int(float64(slot) / 10000 * dur * FramesPerSec)
		start = min(start, len(c)-testWinFrames)
		set = append(set, window(c, start, slot, dur))
	}
	return set
}

func TestWindowSetSimilarity_ShiftedCopyScoresHigh(t *testing.T) {
	const dur = 3600.0
	content := frameStream(1, frames(dur)+100)
	// B is A with 3 s of new material inserted at the start: every frame of
	// A sits 25 frames later in B, and B is 3 s longer (within 2%).
	insert := frames(3)
	shifted := append(frameStream(99, insert), content...)

	res, err := WindowSetSimilarity(bookSet(content, dur), bookSet(shifted, dur+3))
	if err != nil {
		t.Fatal(err)
	}
	if res.LowConfidence {
		t.Error("durations within 2% marked low-confidence")
	}
	if len(res.Slots) != 3 {
		t.Fatalf("compared %d slots, want 3", len(res.Slots))
	}
	if res.Score < 0.99 {
		t.Errorf("shifted copy scored %.3f, want >= 0.99 (slots %+v)", res.Score, res.Slots)
	}
	for _, s := range res.Slots {
		if s.OverlapFrames < frames(WindowMinOverlapSec) {
			t.Errorf("slot %d overlap %d below the 60 s minimum", s.SlotBPA, s.OverlapFrames)
		}
	}
	// Control: with no shift search the same pair would look unrelated.
	if s, _ := bestShiftSimilarity(bookSet(content, dur)[2].Raw, bookSet(shifted, dur+3)[2].Raw, 0); s.Similarity > 0.6 {
		t.Errorf("unshifted comparison already %.3f; test does not exercise the shift", s.Similarity)
	}
}

func TestWindowSetSimilarity_SharedHeadDifferentMiddlesScoresLow(t *testing.T) {
	const dur = 3600.0
	a := frameStream(1, frames(dur)+100)
	b := frameStream(2, frames(dur)+100)
	// Same publisher intro: the first 150 s are identical.
	copy(b[:frames(150)], a[:frames(150)])

	res, err := WindowSetSimilarity(bookSet(a, dur), bookSet(b, dur))
	if err != nil {
		t.Fatal(err)
	}
	if !res.HeadCompared || res.HeadSimilarity < 0.99 {
		t.Errorf("head: compared=%v sim=%.3f, want an identical head reported", res.HeadCompared, res.HeadSimilarity)
	}
	if res.Score > 0.6 {
		t.Errorf("different books sharing an intro scored %.3f; the head must not lift the score", res.Score)
	}
}

func TestWindowSetSimilarity_DurationMismatchIsLowConfidence(t *testing.T) {
	a := frameStream(1, frames(3600)+100)
	res, err := WindowSetSimilarity(bookSet(a, 3600), bookSet(a, 4000))
	if err != nil {
		t.Fatal(err)
	}
	if !res.LowConfidence {
		t.Error("11% duration difference not marked low-confidence")
	}
	// Same content cut at different offsets: slot 1000 of A is not slot 1000
	// of B, but all-pairs still finds whatever matches.
	if len(res.Slots) != 3 {
		t.Errorf("got %d slot results, want one per A window", len(res.Slots))
	}
}

func TestWindowSetSimilarity_RefusesMixedSets(t *testing.T) {
	const dur = 3600.0
	c := frameStream(1, frames(dur)+100)
	mutate := map[string]func(*WindowPrint){
		"pipeline":       func(w *WindowPrint) { w.Pipeline = "ffpcm-s16le-11025-mono/v2" },
		"window set":     func(w *WindowPrint) { w.WindowSet = "ws2" },
		"algorithm":      func(w *WindowPrint) { w.Algorithm = 1 },
		"fpcalc version": func(w *WindowPrint) { w.FpcalcVersion = "1.5.1" },
		"ffmpeg version": func(w *WindowPrint) { w.FFmpegVersion = "7.1" },
	}
	for name, m := range mutate {
		t.Run(name+" across sets", func(t *testing.T) {
			b := bookSet(c, dur)
			for i := range b {
				m(&b[i])
			}
			if _, err := WindowSetSimilarity(bookSet(c, dur), b); !errors.Is(err, ErrIncompatibleWindows) {
				t.Fatalf("err = %v, want ErrIncompatibleWindows", err)
			}
		})
		t.Run(name+" within one set", func(t *testing.T) {
			a := bookSet(c, dur)
			m(&a[3])
			if _, err := WindowSetSimilarity(a, bookSet(c, dur)); !errors.Is(err, ErrIncompatibleWindows) {
				t.Fatalf("err = %v, want ErrIncompatibleWindows", err)
			}
		})
	}
	t.Run("one set planned from two durations", func(t *testing.T) {
		a := bookSet(c, dur)
		a[2].DurationUsedSec = 3700
		if _, err := WindowSetSimilarity(a, bookSet(c, dur)); !errors.Is(err, ErrIncompatibleWindows) {
			t.Fatalf("err = %v, want ErrIncompatibleWindows", err)
		}
	})
}

func TestWindowSetSimilarity_LegacyHeadSkippedNotRefused(t *testing.T) {
	const dur = 3600.0
	c := frameStream(1, frames(dur)+100)
	a := bookSet(c, dur)
	// The legacy head print: no pipeline, different byte space.
	a[0].Pipeline, a[0].FpcalcVersion, a[0].FFmpegVersion = "", "", ""
	res, err := WindowSetSimilarity(a, bookSet(c, dur))
	if err != nil {
		t.Fatal(err)
	}
	if res.HeadCompared {
		t.Error("a legacy head was compared with a pipeline head")
	}
	if res.Score < 0.99 {
		t.Errorf("identical windows scored %.3f", res.Score)
	}
}

func TestWindowSetSimilarity_NoComparableWindows(t *testing.T) {
	c := frameStream(1, 5000)
	if _, err := WindowSetSimilarity([]WindowPrint{head(c, 600)}, bookSet(c, 600)); !errors.Is(err, ErrNoComparableWindows) {
		t.Errorf("head-only set err = %v", err)
	}
	// Same duration but disjoint slots: nothing pairs.
	a := []WindowPrint{window(c, 0, 1000, 600)}
	b := []WindowPrint{window(c, 0, 9000, 600)}
	if _, err := WindowSetSimilarity(a, b); !errors.Is(err, ErrNoComparableWindows) {
		t.Errorf("disjoint slots err = %v", err)
	}
}

func TestMedian(t *testing.T) {
	if m := median([]float64{0.9, 0.1, 0.5}); m != 0.5 {
		t.Errorf("odd median = %v", m)
	}
	if m := median([]float64{0.9, 0.1, 0.5, 0.7}); m != 0.6 {
		t.Errorf("even median = %v", m)
	}
}
