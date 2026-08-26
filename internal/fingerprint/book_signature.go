// file: internal/fingerprint/book_signature.go
// version: 2.0.0
// guid: 7f8e9d0c-1b2a-3f4e-5d6c-7e8f9a0b1c2d
// last-edited: 2026-05-15

package fingerprint

import (
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"math/bits"
	"sort"
)

// ErrIncompleteFingerprint is returned when synthesizing a book signature from
// files that have not all been fingerprinted yet.
var ErrIncompleteFingerprint = errors.New("incomplete fingerprint: one or more files missing acoustid segments")

// BookSignatureFixedLength is the target down-sampled length in uint32 words.
// 4096 * 4 bytes = 16 KiB raw, ~22 KiB base64.
const BookSignatureFixedLength = 4096

// FileSegmentData holds the 7 acoustid segments for a single file.
type FileSegmentData struct {
	Seg0 string
	Seg1 string
	Seg2 string
	Seg3 string
	Seg4 string
	Seg5 string
	Seg6 string
}

// SynthesizeBookSignature synthesizes a unified per-book fingerprint from the
// per-file 7-segment chromaprint fingerprints. Files must be ordered by
// sort_order or filename. Returns (base64-encoded signature, pre-downsample
// segment count, error).
//
// Algorithm:
//  1. Concatenate all [seg0..seg6] from each file in order.
//  2. Decode base64 segments into one big []uint32.
//  3. Down-sample to BookSignatureFixedLength (4096) via max-pooling.
//  4. Re-encode to base64.
//
// Returns ErrIncompleteFingerprint if any file has an empty seg0.
func SynthesizeBookSignature(files []FileSegmentData) (string, int, error) {
	if len(files) == 0 {
		return "", 0, ErrIncompleteFingerprint
	}

	var allInts []uint32
	for _, f := range files {
		segs := []string{f.Seg0, f.Seg1, f.Seg2, f.Seg3, f.Seg4, f.Seg5, f.Seg6}
		for i, seg := range segs {
			if seg == "" {
				if i == 0 {
					return "", 0, ErrIncompleteFingerprint
				}
				continue
			}
			decoded, err := decodeAnyFingerprint(seg)
			if err != nil {
				return "", 0, fmt.Errorf("decode segment: %w", err)
			}
			allInts = append(allInts, decoded...)
		}
	}

	if len(allInts) == 0 {
		return "", 0, ErrIncompleteFingerprint
	}

	originalLen := len(allInts)
	downsampled := downsampleMaxPool(allInts, BookSignatureFixedLength)
	encoded := encodeUint32SliceToBase64(downsampled)
	return encoded, originalLen, nil
}

// downsampleMaxPool down-samples a uint32 slice to targetLen by max-pooling
// consecutive non-overlapping windows. For input shorter than targetLen, pads
// with zeros.
func downsampleMaxPool(data []uint32, targetLen int) []uint32 {
	if len(data) == 0 {
		out := make([]uint32, targetLen)
		return out
	}
	if len(data) <= targetLen {
		out := make([]uint32, targetLen)
		copy(out, data)
		return out
	}

	windowSize := (len(data) + targetLen - 1) / targetLen
	out := make([]uint32, targetLen)
	for i := range targetLen {
		start := i * windowSize
		end := min(start+windowSize, len(data))
		var maxVal uint32
		for j := start; j < end; j++ {
			if data[j] > maxVal {
				maxVal = data[j]
			}
		}
		out[i] = maxVal
	}
	return out
}

// encodeUint32SliceToBase64 encodes a []uint32 slice to base64 (little-endian).
func encodeUint32SliceToBase64(data []uint32) string {
	buf := make([]byte, len(data)*4)
	for i, val := range data {
		binary.LittleEndian.PutUint32(buf[i*4:], val)
	}
	return base64.StdEncoding.EncodeToString(buf)
}

// BookSignatureSimilarity compares two book signatures and returns a
// similarity score [0.0–1.0]. Both signatures must be base64-encoded
// BookSignatureFixedLength uint32 arrays.
//
// Algorithm:
//  1. Decode both to []uint32.
//  2. Compute Hamming distance per uint32 word (bits.OnesCount32(a[i] ^ b[i])).
//  3. Normalize: similarity = 1.0 - (totalBitDifferences / (4096*32)).
func BookSignatureSimilarity(a, b string) (float64, error) {
	intsA, err := decodeBase64Uint32Slice(a)
	if err != nil {
		return 0, fmt.Errorf("decode signature a: %w", err)
	}
	intsB, err := decodeBase64Uint32Slice(b)
	if err != nil {
		return 0, fmt.Errorf("decode signature b: %w", err)
	}
	if len(intsA) != BookSignatureFixedLength || len(intsB) != BookSignatureFixedLength {
		return 0, fmt.Errorf("invalid book signature length (expected %d, got %d and %d)", BookSignatureFixedLength, len(intsA), len(intsB))
	}

	var totalBitDiff int
	for i := range BookSignatureFixedLength {
		xor := intsA[i] ^ intsB[i]
		totalBitDiff += bits.OnesCount32(xor)
	}

	totalBits := BookSignatureFixedLength * 32
	errorRate := float64(totalBitDiff) / float64(totalBits)
	return 1.0 - errorRate, nil
}

// decodeBase64Uint32Slice decodes a base64 string into a []uint32 slice
// (little-endian).
func decodeBase64Uint32Slice(encoded string) ([]uint32, error) {
	b, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("base64 decode: %w", err)
	}
	if len(b)%4 != 0 {
		return nil, fmt.Errorf("decoded length %d is not a multiple of 4", len(b))
	}
	ints := make([]uint32, len(b)/4)
	for i := range ints {
		ints[i] = binary.LittleEndian.Uint32(b[i*4:])
	}
	return ints, nil
}

// OrderFileSegmentsByDefault sorts FileSegmentData in place by the provided
// sort keys. This is a helper for callers that need to order files by
// sort_order or original_filename before passing them to SynthesizeBookSignature.
type FileWithSegments struct {
	SortOrder int
	Filename  string
	Segments  FileSegmentData
}

// SortFilesByOrder sorts files by sort_order, falling back to filename.
func SortFilesByOrder(files []FileWithSegments) {
	sort.Slice(files, func(i, j int) bool {
		if files[i].SortOrder != files[j].SortOrder {
			return files[i].SortOrder < files[j].SortOrder
		}
		return files[i].Filename < files[j].Filename
	})
}

// ─── Partial signatures ───────────────────────────────────────────────────────

// chromaprintRatePerSec is the approximate number of uint32 fingerprint values
// produced per second of audio. Used only for segment length estimation on
// missing files; exact value is not critical.
const chromaprintRatePerSec = 8

// FileSegmentInput is a single file's contribution to SynthesizePartialBookSignature.
// Set Missing=true for files that failed fingerprinting; EstimatedLen is then used
// to zero-pad the positional slot.
type FileSegmentInput struct {
	Segments     FileSegmentData
	Missing      bool
	EstimatedLen int // estimated uint32 count when Missing=true; 0 → use default
}

// EstimateSegmentCount returns an estimate of how many uint32 fingerprint words
// a file would produce, using a cascade of available signals:
//  1. durationSec > 0 → duration-based (most accurate)
//  2. fileSizeBytes > 0 && bitrateKbps > 0 → infer duration from bitrate
//  3. peerRatio > 0 → uint32-per-byte ratio observed in sibling files
//
// Returns 0 when none of the inputs can produce an estimate.
func EstimateSegmentCount(durationSec, fileSizeBytes, bitrateKbps int, peerRatio float64) int {
	if durationSec > 0 {
		segDur := min(durationSec, SegmentSeconds)
		return segDur * NumSegments * chromaprintRatePerSec
	}
	if fileSizeBytes > 0 && bitrateKbps > 0 {
		estDur := (fileSizeBytes * 8) / (bitrateKbps * 1000)
		if estDur > 0 {
			segDur := min(estDur, SegmentSeconds)
			return segDur * NumSegments * chromaprintRatePerSec
		}
	}
	if fileSizeBytes > 0 && peerRatio > 0 {
		return int(float64(fileSizeBytes) * peerRatio)
	}
	return 0
}

// defaultEstimatedLen is the fallback zero-pad length when EstimatedLen == 0
// and we have no other information (300s × 7 segments × 8 uint32s/sec).
const defaultEstimatedLen = SegmentSeconds * NumSegments * chromaprintRatePerSec

// SynthesizePartialBookSignature builds a book_sig_v1 from a mix of real and
// missing files. Missing files are represented by EstimatedLen zero-padded words,
// preserving positional alignment for masked similarity comparisons.
//
// Returns ErrIncompleteFingerprint only when every file is missing or no real
// fingerprint data could be decoded.
//
// Return values:
//   - sig: base64-encoded 4096-word book signature (same format as SynthesizeBookSignature)
//   - mask: base64-encoded 512-byte (4096-bit) coverage mask; bit i=1 means output
//     word i was derived from real fingerprint data
//   - coveragePct: integer percentage of pre-downsample words that are real [0–100]
//   - preLen: total pre-downsample uint32 count (real + zero-padded)
func SynthesizePartialBookSignature(files []FileSegmentInput) (sig, mask string, coveragePct, preLen int, err error) {
	if len(files) == 0 {
		return "", "", 0, 0, ErrIncompleteFingerprint
	}

	var allInts []uint32
	var realFlags []bool
	realCount := 0

	for _, f := range files {
		var words []uint32
		isReal := false

		if !f.Missing && f.Segments.Seg0 != "" {
			segs := [NumSegments]string{
				f.Segments.Seg0, f.Segments.Seg1, f.Segments.Seg2,
				f.Segments.Seg3, f.Segments.Seg4, f.Segments.Seg5, f.Segments.Seg6,
			}
			for i, seg := range segs {
				if seg == "" && i > 0 {
					continue
				}
				decoded, decErr := decodeAnyFingerprint(seg)
				if decErr != nil {
					continue
				}
				words = append(words, decoded...)
			}
			isReal = len(words) > 0
		}

		if !isReal {
			n := f.EstimatedLen
			if n <= 0 {
				n = defaultEstimatedLen
			}
			words = make([]uint32, n)
		}

		allInts = append(allInts, words...)
		for range words {
			realFlags = append(realFlags, isReal)
		}
		if isReal {
			realCount += len(words)
		}
	}

	if realCount == 0 {
		return "", "", 0, 0, ErrIncompleteFingerprint
	}

	preLen = len(allInts)
	coveragePct = int(float64(realCount) / float64(preLen) * 100)

	downsampled := downsampleMaxPool(allInts, BookSignatureFixedLength)
	sig = encodeUint32SliceToBase64(downsampled)
	mask = EncodeMask(realFlags, preLen, BookSignatureFixedLength)
	return sig, mask, coveragePct, preLen, nil
}

// EncodeMask converts a pre-downsample real-position bool slice to a
// BookSignatureFixedLength-bit coverage mask using the same window logic as
// downsampleMaxPool. Output bit i is 1 if any input in its window is real.
// Returns a base64-encoded byte slice of ceil(targetLen/8) bytes.
func EncodeMask(realPositions []bool, totalLen, targetLen int) string {
	maskBytes := make([]byte, (targetLen+7)/8)
	n := len(realPositions)
	if n == 0 || totalLen == 0 {
		return base64.StdEncoding.EncodeToString(maskBytes)
	}
	if n > totalLen {
		n = totalLen
	}

	if n <= targetLen {
		// 1:1 mapping — each output position i corresponds to input i
		for i := 0; i < n; i++ {
			if realPositions[i] {
				maskBytes[i/8] |= 1 << uint(i%8)
			}
		}
		return base64.StdEncoding.EncodeToString(maskBytes)
	}

	windowSize := (n + targetLen - 1) / targetLen
	for i := range targetLen {
		start := i * windowSize
		if start >= n {
			break
		}
		end := min(start+windowSize, n)
		for j := start; j < end; j++ {
			if realPositions[j] {
				maskBytes[i/8] |= 1 << uint(i%8)
				break
			}
		}
	}
	return base64.StdEncoding.EncodeToString(maskBytes)
}

// decodeMask decodes a base64 mask string into a bool slice of length targetLen.
// An empty string means all-real (all bits = 1).
func decodeMask(mask string, targetLen int) ([]bool, error) {
	out := make([]bool, targetLen)
	if mask == "" {
		for i := range out {
			out[i] = true
		}
		return out, nil
	}
	b, err := base64.StdEncoding.DecodeString(mask)
	if err != nil {
		return nil, fmt.Errorf("decode mask: %w", err)
	}
	for i := range targetLen {
		if i/8 < len(b) {
			out[i] = (b[i/8]>>uint(i%8))&1 == 1
		}
	}
	return out, nil
}

// BookSignatureSimilarityMasked compares two book signatures considering only
// positions where both masks indicate real data. An empty/nil mask means all
// positions are real (equivalent to BookSignatureSimilarity).
//
// Returns (similarity [0–1], overlapCount, error). Callers should treat results
// with overlapCount < 512 as unreliable.
func BookSignatureSimilarityMasked(a, b, maskA, maskB string) (float64, int, error) {
	intsA, err := decodeBase64Uint32Slice(a)
	if err != nil {
		return 0, 0, fmt.Errorf("decode signature a: %w", err)
	}
	intsB, err := decodeBase64Uint32Slice(b)
	if err != nil {
		return 0, 0, fmt.Errorf("decode signature b: %w", err)
	}
	if len(intsA) != BookSignatureFixedLength || len(intsB) != BookSignatureFixedLength {
		return 0, 0, fmt.Errorf("invalid signature length: expected %d, got %d and %d",
			BookSignatureFixedLength, len(intsA), len(intsB))
	}

	mA, err := decodeMask(maskA, BookSignatureFixedLength)
	if err != nil {
		return 0, 0, fmt.Errorf("decode mask a: %w", err)
	}
	mB, err := decodeMask(maskB, BookSignatureFixedLength)
	if err != nil {
		return 0, 0, fmt.Errorf("decode mask b: %w", err)
	}

	var totalBitDiff, overlapCount int
	for i := range BookSignatureFixedLength {
		if !mA[i] || !mB[i] {
			continue
		}
		overlapCount++
		totalBitDiff += bits.OnesCount32(intsA[i] ^ intsB[i])
	}

	if overlapCount == 0 {
		return 0, 0, nil
	}
	return 1.0 - float64(totalBitDiff)/float64(overlapCount*32), overlapCount, nil
}
