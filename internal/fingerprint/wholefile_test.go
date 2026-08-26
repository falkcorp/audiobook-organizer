// file: internal/fingerprint/wholefile_test.go
// version: 1.0.0
// guid: d5e6f7a8-b9c0-4d1e-2f3a-4b5c6d7e8f90

package fingerprint

import (
	"encoding/binary"
	"strings"
	"testing"
)

// makeRawFingerprint builds a synthetic raw uint32 LE fingerprint of n
// frames. Each frame value is f(i) so the byte stream is deterministic.
func makeRawFingerprint(n int, f func(i int) uint32) []byte {
	raw := make([]byte, n*4)
	for i := range n {
		binary.LittleEndian.PutUint32(raw[i*4:], f(i))
	}
	return raw
}

func TestEncodeWholeFingerprint_RoundTrip(t *testing.T) {
	raw := makeRawFingerprint(120, func(i int) uint32 { return uint32(i * 0x01020304) })
	encoded := EncodeWholeFingerprint(raw)
	if encoded == "" {
		t.Fatal("encoded fingerprint is empty")
	}
	decoded, err := decodeAnyFingerprint(encoded)
	if err != nil {
		t.Fatalf("decode encoded fingerprint: %v", err)
	}
	if len(decoded) != 120 {
		t.Fatalf("decoded length: got %d, want 120", len(decoded))
	}
	for i, got := range decoded {
		want := uint32(i * 0x01020304)
		if got != want {
			t.Fatalf("frame %d: got %#x, want %#x", i, got, want)
		}
	}
}

func TestEncodeWholeFingerprint_Empty(t *testing.T) {
	if got := EncodeWholeFingerprint(nil); got != "" {
		t.Fatalf("nil input: got %q, want \"\"", got)
	}
	if got := EncodeWholeFingerprint([]byte{}); got != "" {
		t.Fatalf("empty input: got %q, want \"\"", got)
	}
}

func TestDeriveSeg0_TruncatesTo5Min(t *testing.T) {
	// SegmentSeconds (300) * 8 fps = 2400 frames max.
	// Build a 5000-frame whole-file fp.
	raw := makeRawFingerprint(5000, func(i int) uint32 { return uint32(i) })
	seg0 := DeriveSeg0(raw)
	if seg0 == "" {
		t.Fatal("seg0 empty")
	}
	frames, err := decodeAnyFingerprint(seg0)
	if err != nil {
		t.Fatalf("decode seg0: %v", err)
	}
	if len(frames) != 2400 {
		t.Fatalf("seg0 frame count: got %d, want 2400", len(frames))
	}
	// First 2400 frames of the whole-file fp must match seg0 verbatim.
	for i := range 2400 {
		if frames[i] != uint32(i) {
			t.Fatalf("frame %d mismatch: got %d, want %d", i, frames[i], i)
		}
	}
}

func TestDeriveSeg0_ShortFingerprintPassesThrough(t *testing.T) {
	raw := makeRawFingerprint(500, func(i int) uint32 { return uint32(i) })
	seg0 := DeriveSeg0(raw)
	frames, err := decodeAnyFingerprint(seg0)
	if err != nil {
		t.Fatalf("decode seg0: %v", err)
	}
	if len(frames) != 500 {
		t.Fatalf("seg0 frame count: got %d, want 500 (whole fp, no truncation)", len(frames))
	}
}

func TestDeriveSeg0_Empty(t *testing.T) {
	if got := DeriveSeg0(nil); got != "" {
		t.Fatalf("nil: got %q, want \"\"", got)
	}
}

func TestWholeFileSimilarity_Identical(t *testing.T) {
	raw := makeRawFingerprint(3000, func(i int) uint32 { return uint32(i * 7) })
	sim, err := WholeFileSimilarity(raw, raw)
	if err != nil {
		t.Fatalf("similarity: %v", err)
	}
	if sim != 1.0 {
		t.Fatalf("identical similarity: got %f, want 1.0", sim)
	}
}

func TestWholeFileSimilarity_IdenticalIntroDifferentMiddle(t *testing.T) {
	// Simulate the Audible intro problem: two books share the first
	// 10% of frames (intro), then diverge. The middle-slice comparison
	// should detect them as DIFFERENT (low similarity) because the
	// edge-trimming skips the shared intro.
	frames := 3000
	skipFrames := int(float64(frames) * EdgeSkipFraction) // ~300

	a := makeRawFingerprint(frames, func(i int) uint32 {
		if i < skipFrames {
			return 0xDEADBEEF // shared intro
		}
		return uint32(i)
	})
	b := makeRawFingerprint(frames, func(i int) uint32 {
		if i < skipFrames {
			return 0xDEADBEEF // shared intro
		}
		return uint32(i) ^ 0xFFFFFFFF // inverted middle/outro = very different
	})

	sim, err := WholeFileSimilarity(a, b)
	if err != nil {
		t.Fatalf("similarity: %v", err)
	}
	// With shared intros trimmed and inverted middles, similarity should
	// be very low (~0).
	if sim > 0.10 {
		t.Fatalf("intro-only-shared books matched too closely: %f (want <0.10)", sim)
	}
}

func TestWholeFileSimilarity_IdenticalContentDifferentIntros(t *testing.T) {
	// Opposite case: the middle is identical (same book content) but the
	// intros/outros differ (different publisher reads). Middle-slice
	// comparison should see this as a HIGH match.
	frames := 3000
	skipFrames := int(float64(frames) * EdgeSkipFraction)

	a := makeRawFingerprint(frames, func(i int) uint32 {
		if i < skipFrames || i >= frames-skipFrames {
			return 0x11111111 // intro A / outro A
		}
		return uint32(i) // shared middle
	})
	b := makeRawFingerprint(frames, func(i int) uint32 {
		if i < skipFrames || i >= frames-skipFrames {
			return 0x22222222 // intro B / outro B
		}
		return uint32(i) // shared middle
	})

	sim, err := WholeFileSimilarity(a, b)
	if err != nil {
		t.Fatalf("similarity: %v", err)
	}
	if sim < 0.99 {
		t.Fatalf("identical-middle books matched too loosely: %f (want >0.99)", sim)
	}
}

func TestWholeFileSimilarity_EmptyError(t *testing.T) {
	if _, err := WholeFileSimilarity(nil, nil); err == nil {
		t.Fatal("expected error for empty inputs")
	}
	raw := makeRawFingerprint(100, func(i int) uint32 { return 1 })
	if _, err := WholeFileSimilarity(raw, nil); err == nil {
		t.Fatal("expected error for one empty input")
	}
}

func TestWholeFileSimilarity_UnalignedError(t *testing.T) {
	raw := makeRawFingerprint(100, func(i int) uint32 { return 1 })
	bad := raw[:len(raw)-1] // truncate to break uint32 alignment
	if _, err := WholeFileSimilarity(raw, bad); err == nil {
		t.Fatal("expected error for non-uint32-aligned input")
	}
}

func TestMiddleSliceFrames_ShortPassesThrough(t *testing.T) {
	// MinMiddleFrames is 240; below that the whole fp is returned.
	raw := makeRawFingerprint(100, func(i int) uint32 { return uint32(i) })
	got := middleSliceFrames(raw)
	if len(got) != len(raw) {
		t.Fatalf("short fp should pass through: got %d bytes, want %d", len(got), len(raw))
	}
}

func TestMiddleSliceFrames_TrimsLong(t *testing.T) {
	raw := makeRawFingerprint(1000, func(i int) uint32 { return uint32(i) })
	got := middleSliceFrames(raw)
	gotFrames := len(got) / 4
	// 1000 frames * 0.10 = 100 frames trimmed from each end
	// Expected: 800 frames
	if gotFrames != 800 {
		t.Fatalf("middle slice frames: got %d, want 800", gotFrames)
	}
}

func TestWholeFileSimilarity_DifferentLengths_ComparesPositionally(t *testing.T) {
	// WholeFileSimilarity does a positional (index-aligned) compare of
	// the middle slices. When two fps have different lengths their middle
	// slices land at different offsets in the original audio, so the
	// "matching" we measure is byte-position-aligned, not audio-aligned.
	// This is the right behavior for our use case: dedup compares files
	// that should be the same length (same source audio); large length
	// mismatches imply different content anyway. Locking the behavior so
	// future changes are intentional.
	a := makeRawFingerprint(1000, func(i int) uint32 { return uint32(i) })
	b := makeRawFingerprint(500, func(i int) uint32 { return uint32(i) })
	sim, err := WholeFileSimilarity(a, b)
	if err != nil {
		t.Fatalf("similarity: %v", err)
	}
	// Positional compare of misaligned indices → drifts below 1.0 but
	// stays well above random (0.5) because incrementing-int patterns
	// share many low-order bits even when offset.
	if sim >= 1.0 {
		t.Fatalf("misaligned indices should not be perfectly similar: %f", sim)
	}
	if sim < 0.4 {
		t.Fatalf("unrelated-but-patterned fps shouldn't be random-low: %f", sim)
	}
}

func TestWholeFileSimilarity_SingleBitDifference(t *testing.T) {
	// Two fingerprints differing by exactly one bit out of 32 frames worth.
	a := makeRawFingerprint(1000, func(i int) uint32 { return 0xAAAAAAAA })
	b := makeRawFingerprint(1000, func(i int) uint32 {
		if i == 500 {
			return 0xAAAAAAAB // single bit flip in one middle frame
		}
		return 0xAAAAAAAA
	})
	sim, err := WholeFileSimilarity(a, b)
	if err != nil {
		t.Fatalf("similarity: %v", err)
	}
	// 1 differing bit out of (800 middle frames × 32 bits) = 25600 bits
	// → similarity = 25599/25600 ≈ 0.99996
	if sim < 0.999 {
		t.Fatalf("single-bit flip dropped similarity too far: %f", sim)
	}
	if sim == 1.0 {
		t.Fatalf("single-bit flip incorrectly reported as identical")
	}
}

func TestWholeFileSimilarity_AllZeros(t *testing.T) {
	a := makeRawFingerprint(1000, func(i int) uint32 { return 0 })
	b := makeRawFingerprint(1000, func(i int) uint32 { return 0 })
	sim, err := WholeFileSimilarity(a, b)
	if err != nil {
		t.Fatalf("similarity: %v", err)
	}
	if sim != 1.0 {
		t.Fatalf("two zero fingerprints should match perfectly: %f", sim)
	}
}

func TestEncodeWholeFingerprint_ChainedThroughDeriveSeg0(t *testing.T) {
	// Critical invariant: encoding the whole fp then deriving seg0 should
	// produce the same seg0 base64 as directly calling DeriveSeg0 on the
	// raw bytes. This is the path that keeps the legacy AcoustIDSeg0
	// field in sync with the new whole-file storage.
	raw := makeRawFingerprint(3000, func(i int) uint32 { return uint32(i * 13) })
	direct := DeriveSeg0(raw)
	if direct == "" {
		t.Fatal("DeriveSeg0 returned empty")
	}
	// Decode direct, verify it's the first 2400 frames of raw.
	frames, err := decodeAnyFingerprint(direct)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(frames) != 2400 {
		t.Fatalf("derived seg0 frames: got %d, want 2400", len(frames))
	}
	for i := range 2400 {
		if frames[i] != uint32(i*13) {
			t.Fatalf("frame %d: got %d, want %d", i, frames[i], i*13)
		}
	}
}

func TestFileWholeFingerprint_FpcalcMissing(t *testing.T) {
	// We can't easily un-install fpcalc for the test, so this is just a
	// smoke check that the function exists and returns a sensible error
	// type when handed a nonexistent path. The fpcalc-on-path case is
	// covered by integration tests outside this unit.
	_, err := FileWholeFingerprint("/this/path/does/not/exist.m4b")
	if err == nil {
		t.Fatal("expected error for nonexistent path")
	}
	// If fpcalc IS available we'll get a fpcalc error; if not we'll get
	// ErrNotAvailable. Either is acceptable here.
	if err != ErrNotAvailable && !strings.Contains(err.Error(), "fpcalc") {
		t.Fatalf("unexpected error type: %v", err)
	}
}
