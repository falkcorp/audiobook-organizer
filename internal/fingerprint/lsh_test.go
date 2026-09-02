// file: internal/fingerprint/lsh_test.go
// version: 1.0.2
// guid: 2b3c4d5e-6f70-8192-a3b4-c5d6e7f80921
// last-edited: 2026-09-02

package fingerprint

import (
	"encoding/binary"
	"math/rand"
	"testing"
)

// makeRaw returns a packed LE uint32 byte stream of n frames where each
// frame value is the supplied function of the frame index. Frame i's
// value is fn(i). 8fps semantics aren't enforced — we just want bytes
// with stable structure.
func makeRaw(n int, fn func(i int) uint32) []byte {
	raw := make([]byte, n*4)
	for i := range n {
		binary.LittleEndian.PutUint32(raw[i*4:], fn(i))
	}
	return raw
}

func TestSubprints_DeterministicAndCorrectCount(t *testing.T) {
	// 2 hr file at 8fps = 57600 frames; plenty for a full 64-band sample.
	raw := makeRaw(57600, func(i int) uint32 { return uint32(i*1103515245 + 12345) })
	sp1, ids1, err := Subprints(raw)
	if err != nil {
		t.Fatalf("Subprints: %v", err)
	}
	if len(sp1) != LSHBandCount {
		t.Fatalf("expected %d bands, got %d", LSHBandCount, len(sp1))
	}
	if len(ids1) != LSHBandCount {
		t.Fatalf("ids length: got %d want %d", len(ids1), LSHBandCount)
	}
	// Repeat — must be byte-identical (determinism).
	sp2, _, _ := Subprints(raw)
	for i := range sp1 {
		if sp1[i] != sp2[i] {
			t.Fatalf("subprint %d not deterministic: %x vs %x", i, sp1[i], sp2[i])
		}
	}
	// Band IDs strictly ascending 0..LSHBandCount-1.
	for i, id := range ids1 {
		if int(id) != i {
			t.Fatalf("band ID %d: got %d want %d", i, id, i)
		}
	}
}

func TestSubprints_IdenticalInputAllCollide(t *testing.T) {
	raw := makeRaw(57600, func(i int) uint32 { return uint32(i ^ 0xdeadbeef) })
	a, _, _ := Subprints(raw)
	b, _, _ := Subprints(raw)
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("identical input must produce identical subprint at band %d", i)
		}
	}
}

func TestSubprints_FivePercentBitFlipMeetsRecallThreshold(t *testing.T) {
	// Generate a synthetic fp, copy, flip 5% of bits uniformly at
	// random, then check that the candidate-set lookup would still
	// surface the pair — i.e. the collision count meets the engine's
	// LSHMinBandHits threshold.
	//
	// Math: each 64-bit subprint survives a 5% bit-flip with
	// p = (0.95)^64 ≈ 3.7%, so the expected collision count across 64
	// bands is ~2.4. P(collisions ≥ 2) is ~75% under this synthetic
	// (uniform) flip model. Real chromaprint drift concentrates in the
	// low bits, so production recall is much better than this test
	// implies — but the test still proves the floor.
	rng := rand.New(rand.NewSource(0xc0ffee))
	raw := makeRaw(57600, func(int) uint32 { return rng.Uint32() })

	flipped := make([]byte, len(raw))
	copy(flipped, raw)
	totalBits := len(flipped) * 8
	toFlip := totalBits * 5 / 100
	flipRng := rand.New(rand.NewSource(0xfeedface))
	for range toFlip {
		bit := flipRng.Intn(totalBits)
		flipped[bit/8] ^= 1 << uint(bit%8)
	}

	a, _, _ := Subprints(raw)
	b, _, _ := Subprints(flipped)

	matches := 0
	for i := range a {
		if a[i] == b[i] {
			matches++
		}
	}
	if matches < LSHMinBandHits {
		t.Fatalf("expected ≥LSHMinBandHits=%d collisions under 5%% bit-flip, got %d (LSH would miss this near-duplicate)",
			LSHMinBandHits, matches)
	}
}

func TestSubprints_UnrelatedInputsRarelyCollide(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	a, _, _ := Subprints(makeRaw(57600, func(int) uint32 { return rng.Uint32() }))
	rng2 := rand.New(rand.NewSource(2))
	b, _, _ := Subprints(makeRaw(57600, func(int) uint32 { return rng2.Uint32() }))

	collisions := 0
	for i := range a {
		if a[i] == b[i] {
			collisions++
		}
	}
	// Two independent 64-bit windows colliding is astronomically rare;
	// any non-zero number here would indicate a bug.
	if collisions != 0 {
		t.Fatalf("unrelated random fingerprints collided %d times — LSH would be useless", collisions)
	}
}

func TestSubprints_EmptyInput(t *testing.T) {
	sp, ids, err := Subprints(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sp != nil || ids != nil {
		t.Fatalf("expected nil result for empty input")
	}
}

func TestSubprints_TinyInputReturnsNoSubprints(t *testing.T) {
	// 3 frames = 12 bytes — below the 4-frame floor.
	raw := makeRaw(3, func(i int) uint32 { return uint32(i) })
	sp, _, err := Subprints(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sp) != 0 {
		t.Fatalf("expected zero subprints for sub-4-frame fp, got %d", len(sp))
	}
}

func TestSubprints_RejectsMisalignedBytes(t *testing.T) {
	// 7 bytes — not divisible by 4.
	_, _, err := Subprints([]byte{1, 2, 3, 4, 5, 6, 7})
	if err == nil {
		t.Fatalf("expected error for misaligned input")
	}
}

func TestSubprints_ShortFpFallsBackToFewerBands(t *testing.T) {
	// 200 frames — middle slice after edge-skip would be 160 frames,
	// below MinMiddleFrames (240). Edge-skip is disabled and we emit
	// min(LSHBandCount, totalFrames/2) = min(128, 100) = 100 bands.
	const wantBands = 100
	raw := makeRaw(200, func(i int) uint32 { return uint32(i) })
	sp, _, err := Subprints(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sp) != wantBands {
		t.Fatalf("expected %d bands for 200-frame fp, got %d", wantBands, len(sp))
	}
	// All sample positions must stay inside the fp.
	// (Implicit: no panic during Subprints call already proves bounds OK.)
}

func TestSubprints_VeryShortFpClampsBands(t *testing.T) {
	// 10 frames — middle slice falls through to whole fp; max bands =
	// frames/2 = 5.
	raw := makeRaw(10, func(i int) uint32 { return uint32(i) })
	sp, ids, err := Subprints(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sp) != 5 {
		t.Fatalf("expected 5 bands for 10-frame fp, got %d", len(sp))
	}
	if len(ids) != 5 {
		t.Fatalf("ids length mismatch: got %d want 5", len(ids))
	}
}
