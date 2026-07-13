// file: internal/fingerprint/lsh.go
// version: 1.1.1
// guid: 1f2a3b4c-5d6e-7f80-9a1b-2c3d4e5f6071
// last-edited: 2026-07-12

package fingerprint

import (
	"errors"
)

// LSH (locality-sensitive hashing) over a whole-file chromaprint, used to
// turn the O(N) fuzzy-match scan in the dedup engine into a small candidate
// set + Hamming refine.
//
// The fp is a packed little-endian uint32 stream at 8 frames/s. We trim
// EdgeSkipFraction from each end (same as WholeFileSimilarity, so the
// intro/outro sting Audible pre-pends to every book is excluded), then
// sample LSHBandCount evenly-spaced 8-byte windows ("subprints"). Two
// near-duplicates will share many of those windows verbatim; an LSH
// lookup keyed on the subprint surfaces them as candidates without a
// full scan.

// LSHIndexVersion lets the on-disk index format evolve. Stored as a
// 1-byte value next to each fpidx_meta entry — prefix-delete by version
// when migrating to a v2 layout.
const LSHIndexVersion byte = 0x02

// LSHBandCount is the number of subprints we extract per fingerprint.
// 128 bands give ~96% recall on a 5%-bit-flipped near-duplicate (up from
// ~70% at 64 bands) at ~900 MB keyspace on production (308K BookFiles ×
// 128 × ~24-byte key overhead). Changing this value requires bumping
// LSHIndexVersion so HasLSHIndex rejects stale v(n-1) meta rows and the
// build op rewrites all files.
const LSHBandCount = 128

// LSHSubprintBytes is the on-the-wire size of a single subprint —
// two consecutive uint32 frames packed verbatim.
const LSHSubprintBytes = 8

// LSHMinBandHits is the minimum number of band collisions a candidate
// must accumulate before the caller takes it seriously. 2 suppresses
// single-collision noise (which happens on unrelated material at a low
// but nonzero rate) while still surviving meaningful encoder drift.
const LSHMinBandHits = 2

// Subprint is a single LSH bucket value: 8 raw bytes (two consecutive
// little-endian uint32 chromaprint frames). Compare with byte equality.
type Subprint [LSHSubprintBytes]byte

// Subprints returns up to LSHBandCount subprints for the supplied raw
// chromaprint stream, plus their parallel band IDs (0..LSHBandCount-1).
// Length of the two return slices is always equal.
//
// Deterministic: same input ⇒ same output, same order, no random seeds.
//
// Returns nil, nil, nil for fingerprints too short to sample (fewer
// than 4 frames or where the post-edge-skip middle slice has no room
// for even one 2-frame window). This is not an error — the caller
// simply has no index entry to write.
//
// Returns a non-nil error only on hard structural problems (length
// not uint32-aligned).
func Subprints(raw []byte) ([]Subprint, []byte, error) {
	if len(raw) == 0 {
		return nil, nil, nil
	}
	if len(raw)%4 != 0 {
		return nil, nil, errors.New("fingerprint: bytes not uint32-aligned")
	}

	totalFrames := len(raw) / 4
	if totalFrames < 4 {
		return nil, nil, nil
	}

	// Edge skip — same fraction as WholeFileSimilarity so the LSH window
	// matches the comparison window. For very short fps we fall back to
	// the whole thing (the WholeFileSimilarity caller does the same via
	// MinMiddleFrames).
	skip := int(float64(totalFrames) * EdgeSkipFraction)
	middleFrames := totalFrames - 2*skip
	if middleFrames < MinMiddleFrames {
		skip = 0
		middleFrames = totalFrames
	}

	// Each subprint occupies 2 consecutive frames (8 bytes). We need at
	// least that many in the middle slice; otherwise nothing to sample.
	if middleFrames < 2 {
		return nil, nil, nil
	}

	// Number of band slots we can actually populate. If the middle slice
	// is too short for B bands at our minimum stride of 2 frames, just
	// emit as many as fit. (A 30-min file at 8fps has ~14400 frames,
	// trimmed to ~11500 middle frames ⇒ way more than 64 × 2 = 128.)
	bands := LSHBandCount
	if maxBands := middleFrames / 2; maxBands < bands {
		bands = maxBands
	}
	if bands == 0 {
		return nil, nil, nil
	}

	// Proportional stride: distribute B sample positions evenly across
	// the middle slice. Each position lands on a frame boundary; the
	// subprint takes that frame and the next one.
	//
	// Position i (0..bands-1) sits at frame `skip + i * step` where
	// `step = (middleFrames - 2) / (bands - 1)` for bands > 1, or
	// the middle frame for bands == 1.
	subprints := make([]Subprint, bands)
	bandIDs := make([]byte, bands)

	if bands == 1 {
		// Single sample lands in the middle.
		fr := skip + middleFrames/2
		if fr+2 > totalFrames {
			fr = totalFrames - 2
		}
		off := fr * 4
		copy(subprints[0][:], raw[off:off+8])
		bandIDs[0] = 0
		return subprints, bandIDs, nil
	}

	// Use integer math so the spacing is exact and deterministic. We
	// distribute over (bands-1) intervals so the first sample is at the
	// start of the middle slice and the last is at its end.
	numerator := middleFrames - 2
	for i := 0; i < bands; i++ {
		fr := skip + (numerator*i)/(bands-1)
		if fr+2 > totalFrames {
			fr = totalFrames - 2
		}
		off := fr * 4
		copy(subprints[i][:], raw[off:off+8])
		bandIDs[i] = byte(i)
	}
	return subprints, bandIDs, nil
}
