// file: internal/fingerprint/window_plan.go
// version: 1.0.0
// guid: bb7c215d-dd96-4cc0-955c-e127b00999ba
// last-edited: 2026-09-19

package fingerprint

import (
	"errors"
	"fmt"
	"math"
)

// Windowed fingerprints (design: .claude/notes/windowed-fingerprint-design-2026-09-12.md).
//
// Every print stored before this code existed covers the first 120 s of a
// file, which for most Audible titles is the shared publisher intro. A window
// is a print of WindowLengthSec seconds cut at a fraction of the file's
// duration, so it samples the book's own content.

// WindowPipelineID names the exact decode chain that produced a window:
// ffmpeg cuts at an input seek and resamples to 11025 Hz mono s16le (the rate
// Chromaprint works at internally, so fpcalc does no resampling), and fpcalc
// reads that PCM from stdin with -raw. Prints made by different pipelines are
// never compared with each other (see WindowSetSimilarity). Bump the /vN
// suffix on any change to either argument vector.
const WindowPipelineID = "ffpcm-s16le-11025-mono/v1"

// WindowSetWS1 is the only window set defined so far: 10/50/90% for files of
// 10 minutes or more, 50% only for 150 s to 10 min, one covers-whole window
// for anything shorter.
const WindowSetWS1 = "ws1"

// WindowLengthSec is the length of every ws1 window except the covers-whole
// window of a short file.
const WindowLengthSec = 120

// WindowSampleRate is the PCM rate handed to fpcalc.
const WindowSampleRate = 11025

// WindowAlgorithm is the fpcalc -algorithm value passed explicitly, so the
// stored prints do not depend on an fpcalc default.
const WindowAlgorithm = 2

// ws1 duration thresholds, in seconds.
const (
	// WindowShortFileMaxSec: at or below this, the file gets one window that
	// covers the whole of it.
	WindowShortFileMaxSec = 150
	// WindowLongFileMinSec: at or above this, the file gets slots 1000, 5000
	// and 9000. Between the two thresholds it gets slot 5000 only.
	WindowLongFileMinSec = 600
)

// WindowKind distinguishes the print shapes that can sit side by side for one
// file. A whole-file print is another kind, so it can be added later as more
// rows with no schema change.
type WindowKind string

const (
	// WindowKindHead is a print that starts at offset 0. The legacy 120 s
	// AcoustIDFingerprint is read as a head window.
	WindowKindHead WindowKind = "head"
	// WindowKindWindow is a print cut at a fraction of the duration.
	WindowKindWindow WindowKind = "window"
	// WindowKindWhole is a print of the entire file (not produced yet).
	WindowKindWhole WindowKind = "whole"
)

// DurationSource records where DurationUsed came from. A piped window always
// reports about its own length, so fpcalc's report on a window is never a
// source.
type DurationSource string

const (
	// DurationSourceFingerprint is the duration fpcalc reported when it read
	// the file directly for the legacy head print
	// (BookFile.AcoustIDFingerprintDurationSec).
	DurationSourceFingerprint DurationSource = "fingerprint"
	// DurationSourceBookFile is BookFile.Duration.
	DurationSourceBookFile DurationSource = "book_file"
	// DurationSourceFFprobe is a fresh ffprobe of the file.
	DurationSourceFFprobe DurationSource = "ffprobe"
)

// DurationUsed is the duration the window offsets were computed from, and its
// source. Both are stored with every window so a later re-plan can tell
// whether the offsets would move.
type DurationUsed struct {
	Sec    float64
	Source DurationSource
}

// ErrUnknownDuration is returned when no usable duration is available.
// Planning refuses rather than guessing, because a wrong duration moves every
// window.
var ErrUnknownDuration = errors.New("fingerprint: file duration unknown; refusing to plan windows")

// ErrUnknownWindowSet is returned for a window set this build does not define.
var ErrUnknownWindowSet = errors.New("fingerprint: unknown window set")

// ChooseDuration picks the duration to plan windows from, in the design's
// priority order: the fingerprint-reported duration, then BookFile.Duration,
// then ffprobe. probe is called only when both stored values are unusable and
// may be nil. It returns ErrUnknownDuration when nothing usable is found.
func ChooseDuration(fingerprintSec, bookFileSec float64, probe func() (float64, error)) (DurationUsed, error) {
	if usableDuration(fingerprintSec) {
		return DurationUsed{Sec: fingerprintSec, Source: DurationSourceFingerprint}, nil
	}
	if usableDuration(bookFileSec) {
		return DurationUsed{Sec: bookFileSec, Source: DurationSourceBookFile}, nil
	}
	if probe != nil {
		sec, err := probe()
		if err != nil {
			return DurationUsed{}, fmt.Errorf("%w: ffprobe: %v", ErrUnknownDuration, err)
		}
		if usableDuration(sec) {
			return DurationUsed{Sec: sec, Source: DurationSourceFFprobe}, nil
		}
	}
	return DurationUsed{}, ErrUnknownDuration
}

func usableDuration(sec float64) bool {
	return sec > 0 && !math.IsNaN(sec) && !math.IsInf(sec, 0)
}

// WindowSpec is one planned window: where to cut and how much.
type WindowSpec struct {
	Kind WindowKind
	// SlotBP is the planned position as a fraction of the duration in basis
	// points (5000 = 50%). 0 for a head, a whole print, and the covers-whole
	// window of a short file.
	SlotBP int
	// OffsetSec is the input seek, already clamped to [0, dur-len] and
	// rounded to milliseconds (the precision passed to ffmpeg).
	OffsetSec float64
	// LengthSec is the cut length.
	LengthSec float64
	// CoversWhole is true when the window spans the entire file.
	CoversWhole bool
	// WindowSet is the set this window belongs to ("ws1").
	WindowSet string
	// Duration is the duration the offset was computed from.
	Duration DurationUsed
}

// PlanWindows returns the windows to cut for a file of the given duration.
// The ws1 rules (design section (b), open question (g)1 at its stated
// default):
//
//	dur <= 150 s        one window at offset 0, len = dur, CoversWhole
//	150 s < dur < 600 s slot 5000 only, len 120
//	dur >= 600 s        slots 1000, 5000, 9000, len 120
//
// Offsets are SlotBP/10000 * dur clamped to [0, dur-len]. It refuses to plan
// when the duration is not a positive finite number or has no source.
func PlanWindows(dur DurationUsed, windowSet string) ([]WindowSpec, error) {
	if windowSet != WindowSetWS1 {
		return nil, fmt.Errorf("%w: %q", ErrUnknownWindowSet, windowSet)
	}
	if !usableDuration(dur.Sec) || dur.Source == "" {
		return nil, ErrUnknownDuration
	}
	d := dur.Sec
	if d <= WindowShortFileMaxSec {
		return []WindowSpec{{
			Kind:        WindowKindWindow,
			SlotBP:      0,
			OffsetSec:   0,
			LengthSec:   roundMillis(d),
			CoversWhole: true,
			WindowSet:   windowSet,
			Duration:    dur,
		}}, nil
	}
	slots := []int{5000}
	if d >= WindowLongFileMinSec {
		slots = []int{1000, 5000, 9000}
	}
	out := make([]WindowSpec, 0, len(slots))
	for _, bp := range slots {
		off := float64(bp) / 10000 * d
		off = math.Min(off, d-WindowLengthSec)
		off = math.Max(off, 0)
		out = append(out, WindowSpec{
			Kind:      WindowKindWindow,
			SlotBP:    bp,
			OffsetSec: roundMillis(off),
			LengthSec: WindowLengthSec,
			WindowSet: windowSet,
			Duration:  dur,
		})
	}
	return out, nil
}

// roundMillis rounds down to a whole millisecond so the stored offset equals
// the %.3f value handed to ffmpeg and never exceeds the clamp.
func roundMillis(sec float64) float64 {
	return math.Floor(sec*1000) / 1000
}
