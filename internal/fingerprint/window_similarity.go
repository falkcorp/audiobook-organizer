// file: internal/fingerprint/window_similarity.go
// version: 1.0.0
// guid: 8ab73d65-1fc9-4267-b61c-1be25fcee612
// last-edited: 2026-09-19

package fingerprint

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"slices"
)

// Window matching (design section (c), "Matching").

// WindowDurationTolerance: two files whose planned durations differ by at
// most this fraction are treated as the same edit, so window slots line up
// and only a bounded shift is searched.
const WindowDurationTolerance = 0.02

// WindowShiftSlackSec is added to the expected slot drift to form the shift
// search bound, covering an insertion or trim at an unknown position.
const WindowShiftSlackSec = 5.0

// WindowMinOverlapSec is the least overlap a shifted comparison may keep.
const WindowMinOverlapSec = 60.0

// WindowLowConfidenceShiftSec bounds the shift search when the durations
// differ by more than WindowDurationTolerance.
const WindowLowConfidenceShiftSec = 10.0

var (
	// ErrIncompatibleWindows is returned when the two sets were not produced
	// the same way (pipeline, window set, algorithm, or tool version), or a
	// set disagrees with itself about its planned duration. Such prints are
	// not comparable, and no score is better than a misleading one.
	ErrIncompatibleWindows = errors.New("fingerprint: window sets are not comparable")
	// ErrNoComparableWindows is returned when neither set has a non-head
	// window.
	ErrNoComparableWindows = errors.New("fingerprint: no comparable windows")
)

// SlotScore is the result for one compared window pair.
type SlotScore struct {
	SlotBPA int
	SlotBPB int
	// Similarity is the fraction of agreeing bits at the best shift.
	Similarity float64
	// ShiftFrames is the best shift (B relative to A, in frames).
	ShiftFrames int
	// OverlapFrames is the number of frames compared at that shift.
	OverlapFrames int
}

// WindowSimilarity is the outcome of WindowSetSimilarity.
type WindowSimilarity struct {
	// Score is the median of the per-slot similarities, head excluded.
	Score float64
	Slots []SlotScore
	// LowConfidence is true when the durations differed by more than
	// WindowDurationTolerance and every pair was compared.
	LowConfidence bool
	// HeadCompared reports whether both sets had a comparable head window;
	// HeadSimilarity is meaningful only then. The head never feeds Score:
	// it is usually the shared publisher intro.
	HeadCompared   bool
	HeadSimilarity float64
}

// WindowSetSimilarity compares two files' window sets.
//
// Non-head windows must all share one Pipeline, WindowSet, Algorithm and tool
// versions across both sets, or ErrIncompatibleWindows is returned. Head
// windows are compared with each other only when they match on the same
// fields; a head from another pipeline (the legacy compressed head print) is
// skipped, not refused.
//
// When the planned durations are within WindowDurationTolerance, windows are
// paired by SlotBP and each pair gets a shift search of
// ceil((|f*(dA-dB)| + 5 s) * fps) frames with at least 60 s of overlap. When
// they differ by more, every window of A is compared with every window of B
// at +/-10 s, each A slot keeps its best match, and the result is marked
// LowConfidence. No edge trim is applied.
func WindowSetSimilarity(a, b []WindowPrint) (WindowSimilarity, error) {
	headA, winA := splitHead(a)
	headB, winB := splitHead(b)
	if len(winA) == 0 || len(winB) == 0 {
		return WindowSimilarity{}, ErrNoComparableWindows
	}
	ref := winA[0]
	for _, w := range slices.Concat(winA, winB) {
		if !sameProvenance(ref, w) {
			return WindowSimilarity{}, fmt.Errorf("%w: %s", ErrIncompatibleWindows, provenanceDiff(ref, w))
		}
	}
	dA, err := setDuration(winA)
	if err != nil {
		return WindowSimilarity{}, err
	}
	dB, err := setDuration(winB)
	if err != nil {
		return WindowSimilarity{}, err
	}

	var res WindowSimilarity
	if headA != nil && headB != nil && sameProvenance(*headA, *headB) {
		if s, ok := bestShiftSimilarity(headA.Raw, headB.Raw, frames(WindowShiftSlackSec)); ok {
			res.HeadCompared = true
			res.HeadSimilarity = s.Similarity
		}
	}

	if math.Abs(dA-dB) <= WindowDurationTolerance*math.Max(dA, dB) {
		bySlot := make(map[int]WindowPrint, len(winB))
		for _, w := range winB {
			bySlot[w.SlotBP] = w
		}
		for _, wa := range winA {
			wb, ok := bySlot[wa.SlotBP]
			if !ok {
				continue
			}
			f := float64(wa.SlotBP) / 10000
			bound := frames(math.Abs(f*(dA-dB)) + WindowShiftSlackSec)
			if s, ok := bestShiftSimilarity(wa.Raw, wb.Raw, bound); ok {
				s.SlotBPA, s.SlotBPB = wa.SlotBP, wb.SlotBP
				res.Slots = append(res.Slots, s)
			}
		}
	} else {
		res.LowConfidence = true
		bound := frames(WindowLowConfidenceShiftSec)
		for _, wa := range winA {
			var best SlotScore
			found := false
			for _, wb := range winB {
				s, ok := bestShiftSimilarity(wa.Raw, wb.Raw, bound)
				if ok && (!found || s.Similarity > best.Similarity) {
					s.SlotBPA, s.SlotBPB = wa.SlotBP, wb.SlotBP
					best, found = s, true
				}
			}
			if found {
				res.Slots = append(res.Slots, best)
			}
		}
	}
	if len(res.Slots) == 0 {
		return res, ErrNoComparableWindows
	}
	scores := make([]float64, len(res.Slots))
	for i, s := range res.Slots {
		scores[i] = s.Similarity
	}
	res.Score = median(scores)
	return res, nil
}

// frames converts seconds to a whole number of chromaprint frames, rounding up.
func frames(sec float64) int {
	return int(math.Ceil(sec * FramesPerSec))
}

func splitHead(set []WindowPrint) (*WindowPrint, []WindowPrint) {
	var head *WindowPrint
	var rest []WindowPrint
	for i := range set {
		if set[i].Kind == WindowKindHead {
			if head == nil {
				head = &set[i]
			}
			continue
		}
		rest = append(rest, set[i])
	}
	return head, rest
}

func sameProvenance(a, b WindowPrint) bool {
	return a.Pipeline == b.Pipeline && a.WindowSet == b.WindowSet && a.Algorithm == b.Algorithm &&
		a.FpcalcVersion == b.FpcalcVersion && a.FFmpegVersion == b.FFmpegVersion
}

func provenanceDiff(a, b WindowPrint) string {
	return fmt.Sprintf("pipeline %q/%q window_set %q/%q algorithm %d/%d fpcalc %q/%q ffmpeg %q/%q",
		a.Pipeline, b.Pipeline, a.WindowSet, b.WindowSet, a.Algorithm, b.Algorithm,
		a.FpcalcVersion, b.FpcalcVersion, a.FFmpegVersion, b.FFmpegVersion)
}

// setDuration returns the one planned duration a set's windows share.
func setDuration(set []WindowPrint) (float64, error) {
	d := set[0].DurationUsedSec
	if !usableDuration(d) {
		return 0, fmt.Errorf("%w: window has no planned duration", ErrIncompatibleWindows)
	}
	for _, w := range set[1:] {
		if w.DurationUsedSec != d {
			return 0, fmt.Errorf("%w: one set planned from two durations (%.3f, %.3f)",
				ErrIncompatibleWindows, d, w.DurationUsedSec)
		}
	}
	return d, nil
}

// bestShiftSimilarity slides b against a by -bound..+bound frames and returns
// the best bit agreement over the overlap. Shifts whose overlap is below
// WindowMinOverlapSec are skipped, unless the shorter print is itself under
// that length, in which case the overlap must cover all of it.
func bestShiftSimilarity(a, b []byte, bound int) (SlotScore, bool) {
	fa, fb := len(a)/4, len(b)/4
	if fa == 0 || fb == 0 || len(a)%4 != 0 || len(b)%4 != 0 {
		return SlotScore{}, false
	}
	minOverlap := min(frames(WindowMinOverlapSec), fa, fb)
	var best SlotScore
	found := false
	for shift := -bound; shift <= bound; shift++ {
		// Frame i of a lines up with frame i+shift of b.
		startA := max(0, -shift)
		endA := min(fa, fb-shift)
		n := endA - startA
		if n < minOverlap {
			continue
		}
		var agree int
		for i := startA; i < endA; i++ {
			x := binary.LittleEndian.Uint32(a[i*4:]) ^ binary.LittleEndian.Uint32(b[(i+shift)*4:])
			agree += 32 - int(popcount(x))
		}
		sim := float64(agree) / float64(32*n)
		if !found || sim > best.Similarity {
			best = SlotScore{Similarity: sim, ShiftFrames: shift, OverlapFrames: n}
			found = true
		}
	}
	return best, found
}

func median(v []float64) float64 {
	s := slices.Clone(v)
	slices.Sort(s)
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}
