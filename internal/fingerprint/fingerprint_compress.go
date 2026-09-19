// file: internal/fingerprint/fingerprint_compress.go
// version: 1.0.0
// guid: 5d0c2e7a-8b41-4f3e-9a26-c1d7e4b90f58
// last-edited: 2026-09-19

package fingerprint

import (
	"encoding/base64"
	"encoding/binary"
	"fmt"
)

// compressedAlgorithm is the algorithm id written into compressed prints the
// app builds itself. fpcalc's default (and every stored print's) is 1.
const compressedAlgorithm = 1

// compressChromaprint is the inverse of decompressChromaprint: a Go port of
// Chromaprint's src/fingerprint_compressor.cpp. It produces byte-for-byte
// what fpcalc prints (before base64) for the same frames and algorithm.
func compressChromaprint(frames []uint32, algorithm byte) ([]byte, error) {
	if len(frames) >= 1<<24 {
		return nil, fmt.Errorf("compress chromaprint: %d frames do not fit the 24-bit header", len(frames))
	}
	normal := make([]uint8, 0, len(frames)*4)
	var except []uint8
	var prev uint32
	for _, f := range frames {
		x := f ^ prev
		prev = f
		lastBit := 0
		for bit := 1; x != 0; bit++ {
			if x&1 != 0 {
				gap := bit - lastBit
				if gap >= compressedMaxNormal {
					normal = append(normal, compressedMaxNormal)
					except = append(except, uint8(gap-compressedMaxNormal))
				} else {
					normal = append(normal, uint8(gap))
				}
				lastBit = bit
			}
			x >>= 1
		}
		normal = append(normal, 0)
	}

	n := len(frames)
	out := make([]byte, compressedHeaderLen,
		compressedHeaderLen+packedLen(len(normal), compressedNormalBits)+packedLen(len(except), compressedExceptBits))
	out[0] = algorithm
	out[1], out[2], out[3] = byte(n>>16), byte(n>>8), byte(n)
	out = appendPacked(out, normal, compressedNormalBits)
	out = appendPacked(out, except, compressedExceptBits)
	return out, nil
}

// appendPacked appends values as an LSB-first stream of width-bit fields,
// padded with zero bits to a whole byte.
func appendPacked(dst []byte, values []uint8, width int) []byte {
	buf := make([]byte, packedLen(len(values), width))
	for i, v := range values {
		for k := range width {
			if v>>k&1 != 0 {
				p := i*width + k
				buf[p/8] |= 1 << (p % 8)
			}
		}
	}
	return append(dst, buf...)
}

// EncodeCompressedFingerprint turns little-endian uint32 frames (the form
// BookFile.AcoustIDFingerprint and WholeFile.Raw hold) into Chromaprint's
// compressed, URL-safe unpadded base64 string: the form fpcalc prints and
// the AcoustID web service's lookup expects. EncodeWholeFingerprint is NOT
// that form (it is the app's uncompressed storage encoding).
func EncodeCompressedFingerprint(raw []byte) (string, error) {
	if len(raw) == 0 || len(raw)%4 != 0 {
		return "", fmt.Errorf("encode compressed fingerprint: %d bytes is not whole uint32 frames", len(raw))
	}
	frames := make([]uint32, len(raw)/4)
	for i := range frames {
		frames[i] = binary.LittleEndian.Uint32(raw[i*4:])
	}
	b, err := compressChromaprint(frames, compressedAlgorithm)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
