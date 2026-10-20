// file: internal/fingerprint/window_lsh.go
// version: 1.0.1
// guid: da1962cc-7911-4882-8b7e-1d58442414ef
// last-edited: 2026-10-20

package fingerprint

import (
	"encoding/binary"
	"errors"
	"slices"
)

// Window-print LSH (02-PR8, spec 02 section 3.5 "Window-print candidate
// generation").
//
// Subprints (lsh.go) samples 2-frame units at fixed POSITIONS of a print, so
// two prints of the same audio cut at different offsets share none of them.
// That is exactly the chapter-fragment case: a fragment's window and its
// parent's window cover overlapping audio at unrelated offsets. WindowBands
// is position independent instead: it takes the SET of every 2-frame shingle
// (two consecutive little-endian uint32 frames, the same 8-byte unit Subprints
// uses) in one window and computes a MinHash signature over that set, grouped
// into WindowBandCount bands of WindowBandRows rows. A shift changes which
// shingles sit at which position but not the set, so shifted copies share
// bands; noise removes some shingles from the set, so the band agreement
// falls off with the shingle-set Jaccard similarity J as roughly J^r.
//
// Parameters, chosen by TestWindowBands_ParameterSweep on synthetic prints
// (120 s windows, 969 frames; one copy with 1 percent of its bits flipped):
//
//   - r = 1, b = 16. A 1 percent bit flip keeps an 8-byte shingle with
//     p = 0.99^64 = 0.53, so J is about p/(2-p) = 0.36 and r = 1 is the only
//     row count that reaches 0.95 recall at two band hits inside the 5 GB
//     index budget (r = 2 needs b near 40, two and a half times the keys).
//   - r = 1 also makes the fragment "containment" (the share of a fragment's
//     band keys the parent has) track the slice fraction itself instead of
//     its r-th power.
//
// Measured (TestWindowBands_ParameterSweep, 1,000 pairs per row, recall =
// share of noisy copies reaching WindowMinBandHits shared bands):
//
//	b=8  r=1: recall 0.846    b=32 r=2: recall 0.926
//	b=12 r=1: recall 0.965    b=40 r=2: recall 0.965
//	b=16 r=1: recall 0.992    b=48 r=2: recall 0.980
//	b=20 r=1: recall 0.999    false-candidate rate 0.000 on unrelated windows
//	b=24 r=1: recall 0.999    for every row
//
// At b=16, r=1: recall 0.994 and false-candidate rate 0.000 (500 pairs each,
// the two guard tests), 0.014 when unrelated windows share a 3 s sting; mean
// containment of a slice is the slice fraction (0.952 at 95 percent, 0.795 at
// 80, 0.498 at 50); a 3 s shift shares 14 of 16 bands.
//
// Re-tuning b or r changes every key: bump WindowBandVersion, which is part of
// every fpwinidx: key and of the fpwinidx_meta: value, so a re-tune is a new
// index version the build op rewrites, never a silent mix of two schemes.

// WindowBandVersion is the band-scheme version byte stored in every
// fpwinidx: key and at the head of every fpwinidx_meta: value.
const WindowBandVersion byte = 1

// WindowBandCount is b, the number of bands per window.
const WindowBandCount = 16

// WindowBandRows is r, the number of MinHash rows combined into one band.
const WindowBandRows = 1

// WindowMinBandHits is the number of band keys a candidate file must share
// with the query before the collector takes it seriously.
const WindowMinBandHits = 2

// WindowMinDistinctShingles is the least number of distinct shingles a window
// must have to be banded. A window of near-constant frames (silence, a test
// tone) has a handful of shingles, and every other such window would share all
// of its bands: indexing it would make a hot bucket, not evidence.
const WindowMinDistinctShingles = 32

// WindowBand is one band of one window's MinHash signature.
type WindowBand struct {
	// Band is the band index, 0..WindowBandCount-1. Bands are only ever
	// compared with the same index.
	Band byte
	// Hash is the band value: the MinHash row (r = 1) or a mix of the r rows.
	Hash uint64
}

// windowBandSeeds are the per-row hash seeds, fixed for WindowBandVersion.
var windowBandSeeds = makeWindowBandSeeds(WindowBandCount * WindowBandRows)

func makeWindowBandSeeds(n int) []uint64 {
	seeds := make([]uint64, n)
	// Golden-ratio increments through splitmix64: deterministic and spread.
	x := uint64(0x5f8a_02e8_0000_0000) ^ uint64(WindowBandVersion)
	for i := range seeds {
		x += 0x9e37_79b9_7f4a_7c15
		seeds[i] = mix64(x)
	}
	return seeds
}

// mix64 is the splitmix64 finalizer, a fast bijective 64-bit mixer.
func mix64(x uint64) uint64 {
	x ^= x >> 30
	x *= 0xbf58_476d_1ce4_e5b9
	x ^= x >> 27
	x *= 0x94d0_49bb_1331_11eb
	x ^= x >> 31
	return x
}

// WindowBands returns the WindowBandCount bands of one window print. raw is a
// window's Raw: little-endian uint32 frames.
//
// It returns nil, nil when the window is too short or too uniform to band
// (fewer than WindowMinDistinctShingles distinct shingles), and an error only
// when raw is not uint32 aligned. Deterministic: the same raw always gives the
// same bands. It never reads audio.
func WindowBands(raw []byte) ([]WindowBand, error) {
	return windowBandsWith(raw, WindowBandCount, WindowBandRows, windowBandSeeds)
}

// windowBandsWith is WindowBands for any (b, r), for the parameter sweep.
// seeds must hold at least b*r values.
func windowBandsWith(raw []byte, b, r int, seeds []uint64) ([]WindowBand, error) {
	if len(raw)%4 != 0 {
		return nil, errors.New("fingerprint: window bytes not uint32-aligned")
	}
	shingles := windowShingles(raw)
	if len(shingles) < WindowMinDistinctShingles {
		return nil, nil
	}
	rows := b * r
	mins := make([]uint64, rows)
	for k := range mins {
		mins[k] = ^uint64(0)
	}
	for _, s := range shingles {
		for k := range rows {
			if h := mix64(s ^ seeds[k]); h < mins[k] {
				mins[k] = h
			}
		}
	}
	out := make([]WindowBand, b)
	for band := range b {
		h := mins[band*r]
		for row := 1; row < r; row++ {
			h = mix64(h ^ mins[band*r+row] ^ uint64(row))
		}
		out[band] = WindowBand{Band: byte(band), Hash: h}
	}
	return out, nil
}

// windowShingles returns the distinct 2-frame shingles of raw, sorted.
func windowShingles(raw []byte) []uint64 {
	n := len(raw) / 4
	if n < 2 {
		return nil
	}
	out := make([]uint64, 0, n-1)
	for i := 0; i+1 < n; i++ {
		out = append(out, binary.LittleEndian.Uint64(raw[i*4:]))
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// WindowBandContainment is |a ∩ b| / |a| over (Band, Hash) keys: the share of
// a's band keys that b also has. It is the fragment-to-parent containment
// measure (a = the fragment's bands, b = the parent's). 0 when a is empty.
func WindowBandContainment(a, b []WindowBand) float64 {
	if len(a) == 0 {
		return 0
	}
	have := make(map[WindowBand]struct{}, len(b))
	for _, x := range b {
		have[x] = struct{}{}
	}
	seen := make(map[WindowBand]struct{}, len(a))
	shared := 0
	for _, x := range a {
		if _, dup := seen[x]; dup {
			continue
		}
		seen[x] = struct{}{}
		if _, ok := have[x]; ok {
			shared++
		}
	}
	return float64(shared) / float64(len(seen))
}
