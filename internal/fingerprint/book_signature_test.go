// file: internal/fingerprint/book_signature_test.go
// version: 2.0.1
// guid: 8f9e0a1b-2c3d-4e5f-6a7b-8c9d0e1f2a3b
// last-edited: 2026-09-02

package fingerprint

import (
	"math/rand"
	"strings"
	"testing"
)

// makeTestSegment creates a valid fingerprint segment from uint32 values.
func makeTestSegment(values ...uint32) string {
	return encodeUint32SliceToBase64(values)
}

func TestSynthesizeBookSignature_Deterministic(t *testing.T) {
	// Use simple uint32 arrays encoded to base64
	files := []FileSegmentData{
		{
			Seg0: makeTestSegment(1, 2, 3, 4),
			Seg1: makeTestSegment(5, 6, 7, 8),
			Seg2: makeTestSegment(9, 10, 11, 12),
			Seg3: makeTestSegment(13, 14, 15, 16),
			Seg4: makeTestSegment(17, 18, 19, 20),
			Seg5: makeTestSegment(21, 22, 23, 24),
			Seg6: makeTestSegment(25, 26, 27, 28),
		},
	}

	sig1, segCount1, err := SynthesizeBookSignature(files)
	if err != nil {
		t.Fatalf("SynthesizeBookSignature failed: %v", err)
	}
	if sig1 == "" {
		t.Fatal("signature is empty")
	}
	if segCount1 <= 0 {
		t.Fatalf("segment count should be > 0, got %d", segCount1)
	}

	sig2, segCount2, err := SynthesizeBookSignature(files)
	if err != nil {
		t.Fatalf("second SynthesizeBookSignature failed: %v", err)
	}
	if sig1 != sig2 {
		t.Errorf("signature not deterministic: run1=%s, run2=%s", sig1, sig2)
	}
	if segCount1 != segCount2 {
		t.Errorf("segment count not deterministic: %d vs %d", segCount1, segCount2)
	}
}

func TestBookSignatureSimilarity_Identical(t *testing.T) {
	files := []FileSegmentData{
		{
			Seg0: makeTestSegment(1, 2, 3, 4),
			Seg1: makeTestSegment(5, 6, 7, 8),
			Seg2: makeTestSegment(9, 10, 11, 12),
			Seg3: makeTestSegment(13, 14, 15, 16),
			Seg4: makeTestSegment(17, 18, 19, 20),
			Seg5: makeTestSegment(21, 22, 23, 24),
			Seg6: makeTestSegment(25, 26, 27, 28),
		},
	}

	sig, _, err := SynthesizeBookSignature(files)
	if err != nil {
		t.Fatalf("SynthesizeBookSignature failed: %v", err)
	}

	sim, err := BookSignatureSimilarity(sig, sig)
	if err != nil {
		t.Fatalf("BookSignatureSimilarity failed: %v", err)
	}
	if sim != 1.0 {
		t.Errorf("identical signatures should have similarity 1.0, got %f", sim)
	}
}

func TestSynthesizeBookSignature_MultiFileSplit(t *testing.T) {
	// Simulate a 30-file split: each file has the same 7 segments
	// (in reality they'd differ, but this tests the concatenation logic).
	multiFile := make([]FileSegmentData, 30)
	for i := range multiFile {
		multiFile[i] = FileSegmentData{
			Seg0: makeTestSegment(1, 2, 3, 4),
			Seg1: makeTestSegment(5, 6, 7, 8),
			Seg2: makeTestSegment(9, 10, 11, 12),
			Seg3: makeTestSegment(13, 14, 15, 16),
			Seg4: makeTestSegment(17, 18, 19, 20),
			Seg5: makeTestSegment(21, 22, 23, 24),
			Seg6: makeTestSegment(25, 26, 27, 28),
		}
	}

	// Simulate a 1-file version with the "same" segments
	singleFile := []FileSegmentData{
		{
			Seg0: makeTestSegment(1, 2, 3, 4),
			Seg1: makeTestSegment(5, 6, 7, 8),
			Seg2: makeTestSegment(9, 10, 11, 12),
			Seg3: makeTestSegment(13, 14, 15, 16),
			Seg4: makeTestSegment(17, 18, 19, 20),
			Seg5: makeTestSegment(21, 22, 23, 24),
			Seg6: makeTestSegment(25, 26, 27, 28),
		},
	}

	sigMulti, _, err := SynthesizeBookSignature(multiFile)
	if err != nil {
		t.Fatalf("SynthesizeBookSignature (multi-file) failed: %v", err)
	}
	sigSingle, _, err := SynthesizeBookSignature(singleFile)
	if err != nil {
		t.Fatalf("SynthesizeBookSignature (single-file) failed: %v", err)
	}

	sim, err := BookSignatureSimilarity(sigMulti, sigSingle)
	if err != nil {
		t.Fatalf("BookSignatureSimilarity failed: %v", err)
	}

	// The threshold in the spec is 0.85. Because we're using identical segments
	// repeated, the similarity should be very high. If the segments were actually
	// different across the split, we'd tune the threshold. For now, expect >= 0.85.
	if sim < 0.85 {
		t.Errorf("multi-file vs single-file similarity too low: %f (expected >= 0.85)", sim)
	}
	t.Logf("Multi-file vs single-file similarity: %f", sim)
}

func TestSynthesizeBookSignature_UnrelatedBooks(t *testing.T) {
	// Generate large test data with truly random values using different seeds
	segRandom := func(seed int64) string {
		rng := rand.New(rand.NewSource(seed))
		vals := make([]uint32, 1000)
		for i := range vals {
			vals[i] = rng.Uint32()
		}
		return encodeUint32SliceToBase64(vals)
	}

	bookA := []FileSegmentData{
		{
			Seg0: segRandom(1),
			Seg1: segRandom(2),
			Seg2: segRandom(3),
			Seg3: segRandom(4),
			Seg4: segRandom(5),
			Seg5: segRandom(6),
			Seg6: segRandom(7),
		},
	}

	bookB := []FileSegmentData{
		{
			Seg0: segRandom(1000),
			Seg1: segRandom(2000),
			Seg2: segRandom(3000),
			Seg3: segRandom(4000),
			Seg4: segRandom(5000),
			Seg5: segRandom(6000),
			Seg6: segRandom(7000),
		},
	}

	sigA, _, err := SynthesizeBookSignature(bookA)
	if err != nil {
		t.Fatalf("SynthesizeBookSignature (bookA) failed: %v", err)
	}
	sigB, _, err := SynthesizeBookSignature(bookB)
	if err != nil {
		t.Fatalf("SynthesizeBookSignature (bookB) failed: %v", err)
	}

	sim, err := BookSignatureSimilarity(sigA, sigB)
	if err != nil {
		t.Fatalf("BookSignatureSimilarity failed: %v", err)
	}

	// For synthetic random test data, we expect ~50% similarity due to random
	// bit patterns. Real chromaprint data has structure that allows lower
	// thresholds (~0.4). For this test, we verify that unrelated books have
	// *significantly lower* similarity than the related-books test (which is ~0.99).
	// In production, empirical tuning based on real audio fingerprints may allow
	// a lower threshold (the spec suggests 0.4).
	if sim > 0.7 {
		t.Errorf("unrelated books similarity too high: %f (expected <= 0.7 for synthetic test data)", sim)
	}
	t.Logf("Unrelated books similarity: %f", sim)
}

func TestSynthesizeBookSignature_IncompleteFingerprint(t *testing.T) {
	files := []FileSegmentData{
		{
			Seg0: "",
			Seg1: makeTestSegment(5, 6, 7, 8),
			Seg2: makeTestSegment(9, 10, 11, 12),
			Seg3: makeTestSegment(13, 14, 15, 16),
			Seg4: makeTestSegment(17, 18, 19, 20),
			Seg5: makeTestSegment(21, 22, 23, 24),
			Seg6: makeTestSegment(25, 26, 27, 28),
		},
	}

	sig, _, err := SynthesizeBookSignature(files)
	if err != ErrIncompleteFingerprint {
		t.Errorf("expected ErrIncompleteFingerprint when seg0 is empty, got: %v (sig=%s)", err, sig)
	}
}

func TestDownsampleMaxPool_Short(t *testing.T) {
	data := []uint32{1, 2, 3, 4, 5}
	out := downsampleMaxPool(data, 10)
	if len(out) != 10 {
		t.Errorf("expected length 10, got %d", len(out))
	}
	for i := range 5 {
		if out[i] != data[i] {
			t.Errorf("mismatch at %d: expected %d, got %d", i, data[i], out[i])
		}
	}
	for i := 5; i < 10; i++ {
		if out[i] != 0 {
			t.Errorf("expected 0 at %d, got %d", i, out[i])
		}
	}
}

func TestDownsampleMaxPool_Long(t *testing.T) {
	data := make([]uint32, 10000)
	for i := range data {
		data[i] = uint32(i)
	}
	out := downsampleMaxPool(data, 100)
	if len(out) != 100 {
		t.Errorf("expected length 100, got %d", len(out))
	}
	// Each window should contain the max value from that window.
	// window 0: data[0..99] → max=99
	// window 1: data[100..199] → max=199, etc.
	if out[0] < 99 {
		t.Errorf("window 0 max should be >= 99, got %d", out[0])
	}
}

func TestSortFilesByOrder(t *testing.T) {
	files := []FileWithSegments{
		{SortOrder: 3, Filename: "c.mp3"},
		{SortOrder: 1, Filename: "a.mp3"},
		{SortOrder: 2, Filename: "b.mp3"},
		{SortOrder: 0, Filename: "z.mp3"},
	}

	SortFilesByOrder(files)

	expected := []string{"z.mp3", "a.mp3", "b.mp3", "c.mp3"}
	for i, f := range files {
		if f.Filename != expected[i] {
			t.Errorf("index %d: expected %s, got %s", i, expected[i], f.Filename)
		}
	}
}

func TestSortFilesByOrder_FallbackToFilename(t *testing.T) {
	files := []FileWithSegments{
		{SortOrder: 0, Filename: "c.mp3"},
		{SortOrder: 0, Filename: "a.mp3"},
		{SortOrder: 0, Filename: "b.mp3"},
	}

	SortFilesByOrder(files)

	expected := []string{"a.mp3", "b.mp3", "c.mp3"}
	for i, f := range files {
		if f.Filename != expected[i] {
			t.Errorf("index %d: expected %s, got %s", i, expected[i], f.Filename)
		}
	}
}

// TestEncodeDecodeRoundTrip ensures that encoding and decoding preserves data.
func TestEncodeDecodeRoundTrip(t *testing.T) {
	data := make([]uint32, 100)
	for i := range data {
		data[i] = uint32(i * 13)
	}

	encoded := encodeUint32SliceToBase64(data)
	decoded, err := decodeBase64Uint32Slice(encoded)
	if err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if len(decoded) != len(data) {
		t.Fatalf("length mismatch: %d != %d", len(decoded), len(data))
	}
	for i := range data {
		if decoded[i] != data[i] {
			t.Errorf("mismatch at %d: %d != %d", i, decoded[i], data[i])
		}
	}
}

// TestBookSignatureSimilarity_InvalidLength ensures we reject non-4096 signatures.
func TestBookSignatureSimilarity_InvalidLength(t *testing.T) {
	short := encodeUint32SliceToBase64(make([]uint32, 10))
	valid := encodeUint32SliceToBase64(make([]uint32, BookSignatureFixedLength))

	_, err := BookSignatureSimilarity(short, valid)
	if err == nil {
		t.Error("expected error for mismatched signature length")
	}
	if !strings.Contains(err.Error(), "invalid book signature length") {
		t.Errorf("unexpected error message: %v", err)
	}
}

// ─── Partial signature tests ──────────────────────────────────────────────────

func TestEstimateSegmentCount_DurationBased(t *testing.T) {
	// 600s audio, capped at SegmentSeconds (300) per segment → 300*7*8 = 16800
	got := EstimateSegmentCount(600, 0, 0, 0)
	want := SegmentSeconds * NumSegments * chromaprintRatePerSec
	if got != want {
		t.Errorf("duration=600: got %d, want %d", got, want)
	}

	// 60s audio: 60*7*8 = 3360
	got = EstimateSegmentCount(60, 0, 0, 0)
	want = 60 * NumSegments * chromaprintRatePerSec
	if got != want {
		t.Errorf("duration=60: got %d, want %d", got, want)
	}
}

func TestEstimateSegmentCount_SizeBitrate(t *testing.T) {
	// 50 MB at 128 kbps → duration = 50*1024*1024*8 / (128*1000) ≈ 3276s
	// capped at 300s per seg → 300*7*8 = 16800
	got := EstimateSegmentCount(0, 50*1024*1024, 128, 0)
	want := SegmentSeconds * NumSegments * chromaprintRatePerSec
	if got != want {
		t.Errorf("size+bitrate: got %d, want %d", got, want)
	}
}

func TestEstimateSegmentCount_PeerRatio(t *testing.T) {
	// 10 MB at ratio 0.001 → 10*1024*1024 * 0.001 ≈ 10485
	size := 10 * 1024 * 1024
	got := EstimateSegmentCount(0, size, 0, 0.001)
	want := int(float64(size) * 0.001)
	if got != want {
		t.Errorf("peer ratio: got %d, want %d", got, want)
	}
}

func TestEstimateSegmentCount_Unknown(t *testing.T) {
	if got := EstimateSegmentCount(0, 0, 0, 0); got != 0 {
		t.Errorf("all-zero inputs: expected 0, got %d", got)
	}
}

func makeFileInput(seed int64, count int) FileSegmentInput {
	rng := rand.New(rand.NewSource(seed))
	makeSegs := func() string {
		vals := make([]uint32, count)
		for i := range vals {
			vals[i] = rng.Uint32()
		}
		return encodeUint32SliceToBase64(vals)
	}
	return FileSegmentInput{
		Segments: FileSegmentData{
			Seg0: makeSegs(), Seg1: makeSegs(), Seg2: makeSegs(),
			Seg3: makeSegs(), Seg4: makeSegs(), Seg5: makeSegs(), Seg6: makeSegs(),
		},
	}
}

func TestSynthesizePartialBookSignature_AllReal(t *testing.T) {
	files := []FileSegmentInput{
		makeFileInput(1, 500),
		makeFileInput(2, 500),
		makeFileInput(3, 500),
	}
	sig, mask, coveragePct, preLen, err := SynthesizePartialBookSignature(files)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sig == "" || mask == "" {
		t.Fatal("sig and mask must be non-empty")
	}
	if coveragePct != 100 {
		t.Errorf("all-real coverage: got %d%%, want 100%%", coveragePct)
	}
	if preLen <= 0 {
		t.Errorf("preLen must be positive, got %d", preLen)
	}
}

func TestSynthesizePartialBookSignature_OneMissing(t *testing.T) {
	files := []FileSegmentInput{
		makeFileInput(10, 500),
		{Missing: true, EstimatedLen: 3500},
		makeFileInput(30, 500),
	}
	sig, mask, coveragePct, preLen, err := SynthesizePartialBookSignature(files)
	if err != nil {
		t.Fatalf("unexpected error with one missing file: %v", err)
	}
	if sig == "" || mask == "" {
		t.Fatal("sig and mask must be non-empty")
	}
	// Real files: 2 × 7 × 500 = 7000 words. Missing: 3500. Total = 10500.
	// Coverage = 7000/10500 ≈ 66%
	if coveragePct <= 0 || coveragePct >= 100 {
		t.Errorf("mixed coverage should be between 0 and 100, got %d", coveragePct)
	}
	t.Logf("one-missing coverage: %d%%, preLen: %d", coveragePct, preLen)
}

func TestSynthesizePartialBookSignature_AllMissing(t *testing.T) {
	files := []FileSegmentInput{
		{Missing: true, EstimatedLen: 1000},
		{Missing: true, EstimatedLen: 1000},
	}
	_, _, _, _, err := SynthesizePartialBookSignature(files)
	if err != ErrIncompleteFingerprint {
		t.Errorf("all-missing: expected ErrIncompleteFingerprint, got %v", err)
	}
}

func TestSynthesizePartialBookSignature_Empty(t *testing.T) {
	_, _, _, _, err := SynthesizePartialBookSignature(nil)
	if err != ErrIncompleteFingerprint {
		t.Errorf("nil input: expected ErrIncompleteFingerprint, got %v", err)
	}
}

func TestSynthesizePartialBookSignature_MatchesFullSignature(t *testing.T) {
	// Partial synthesis of all-real files should produce the same sig as
	// SynthesizeBookSignature on the same data.
	f := makeFileInput(42, 200)
	full, fullPre, err := SynthesizeBookSignature([]FileSegmentData{f.Segments})
	if err != nil {
		t.Fatalf("SynthesizeBookSignature: %v", err)
	}
	partial, _, _, partPre, err := SynthesizePartialBookSignature([]FileSegmentInput{f})
	if err != nil {
		t.Fatalf("SynthesizePartialBookSignature: %v", err)
	}
	if full != partial {
		t.Error("all-real partial sig must equal full sig")
	}
	if fullPre != partPre {
		t.Errorf("preLen mismatch: full=%d partial=%d", fullPre, partPre)
	}
}

func TestEncodeMask_AllReal(t *testing.T) {
	flags := make([]bool, BookSignatureFixedLength*2)
	for i := range flags {
		flags[i] = true
	}
	mask := EncodeMask(flags, len(flags), BookSignatureFixedLength)
	bits, err := decodeMask(mask, BookSignatureFixedLength)
	if err != nil {
		t.Fatalf("decodeMask: %v", err)
	}
	for i, b := range bits {
		if !b {
			t.Errorf("bit %d should be 1 (real)", i)
		}
	}
}

func TestEncodeMask_AllFake(t *testing.T) {
	flags := make([]bool, BookSignatureFixedLength*2) // all false
	mask := EncodeMask(flags, len(flags), BookSignatureFixedLength)
	bits, err := decodeMask(mask, BookSignatureFixedLength)
	if err != nil {
		t.Fatalf("decodeMask: %v", err)
	}
	for i, b := range bits {
		if b {
			t.Errorf("bit %d should be 0 (fake)", i)
		}
	}
}

func TestEncodeMask_FirstHalfReal(t *testing.T) {
	// n = 2 × targetLen → windowSize = 2; first 4096 inputs real → first 2048 output bits real
	n := BookSignatureFixedLength * 2 // 8192
	flags := make([]bool, n)
	for i := 0; i < n/2; i++ {
		flags[i] = true
	}
	mask := EncodeMask(flags, n, BookSignatureFixedLength)
	bits, err := decodeMask(mask, BookSignatureFixedLength)
	if err != nil {
		t.Fatalf("decodeMask: %v", err)
	}
	halfOut := BookSignatureFixedLength / 2
	for i := range halfOut {
		if !bits[i] {
			t.Errorf("first-half bit %d should be 1", i)
		}
	}
	for i := halfOut; i < BookSignatureFixedLength; i++ {
		if bits[i] {
			t.Errorf("second-half bit %d should be 0", i)
		}
	}
}

func TestDecodeMask_EmptyMeansAllReal(t *testing.T) {
	bits, err := decodeMask("", BookSignatureFixedLength)
	if err != nil {
		t.Fatalf("decodeMask empty: %v", err)
	}
	for i, b := range bits {
		if !b {
			t.Errorf("bit %d should be 1 (empty mask = all real)", i)
		}
	}
}

func TestBookSignatureSimilarityMasked_NoMask(t *testing.T) {
	// Without masks, should produce the same result as BookSignatureSimilarity
	sig, _, _ := SynthesizeBookSignature([]FileSegmentData{makeFileInput(7, 300).Segments})
	simUnmasked, err := BookSignatureSimilarity(sig, sig)
	if err != nil {
		t.Fatalf("BookSignatureSimilarity: %v", err)
	}
	simMasked, overlap, err := BookSignatureSimilarityMasked(sig, sig, "", "")
	if err != nil {
		t.Fatalf("BookSignatureSimilarityMasked: %v", err)
	}
	if simUnmasked != simMasked {
		t.Errorf("masked (no mask) should equal unmasked: %f vs %f", simMasked, simUnmasked)
	}
	if overlap != BookSignatureFixedLength {
		t.Errorf("no-mask overlap should be %d, got %d", BookSignatureFixedLength, overlap)
	}
}

func TestBookSignatureSimilarityMasked_ZeroOverlap(t *testing.T) {
	// Mask A covers first half, mask B covers second half — no overlap
	nTotal := BookSignatureFixedLength * 4
	flagsA := make([]bool, nTotal)
	flagsB := make([]bool, nTotal)
	for i := 0; i < nTotal/2; i++ {
		flagsA[i] = true
	}
	for i := nTotal / 2; i < nTotal; i++ {
		flagsB[i] = true
	}
	maskA := EncodeMask(flagsA, nTotal, BookSignatureFixedLength)
	maskB := EncodeMask(flagsB, nTotal, BookSignatureFixedLength)

	sig := encodeUint32SliceToBase64(make([]uint32, BookSignatureFixedLength))
	sim, overlap, err := BookSignatureSimilarityMasked(sig, sig, maskA, maskB)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if overlap != 0 {
		t.Errorf("expected 0 overlap, got %d", overlap)
	}
	if sim != 0 {
		t.Errorf("expected 0 similarity with no overlap, got %f", sim)
	}
}
