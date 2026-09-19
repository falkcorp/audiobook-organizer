// file: internal/fingerprint/fingerprint_decompress.go
// version: 1.0.0
// guid: 1c837334-fb5e-41f1-ac8b-c5fbdcdaff5c
// last-edited: 2026-09-19

package fingerprint

import (
	"errors"
	"fmt"
)

// Chromaprint's compressed fingerprint format, as written by `fpcalc -json`
// (without -raw), the ffmpeg chromaprint muxer (-fp_format base64) and the
// AcoustID web service. This is a Go port of Chromaprint's
// src/fingerprint_decompressor.cpp; the layout is:
//
//	byte 0      algorithm id (0..4; fpcalc's default is 1)
//	bytes 1..3  frame count, big-endian 24-bit
//	then        the "normal" stream: 3-bit values, packed LSB-first
//	then        the "exceptional" stream: 5-bit values, packed LSB-first,
//	            starting at the next whole byte after the normal stream
//
// Each frame is first XORed with the previous frame (frame 0 is kept as is),
// then described by the 1-based positions of its set bits, lowest first, as
// gaps from the previous set bit. Every gap is one normal value; a gap of 7
// or more is written as 7 in the normal stream plus (gap - 7) in the
// exceptional stream. A normal value of 0 ends the frame.
const (
	compressedHeaderLen     = 4
	compressedNormalBits    = 3
	compressedExceptBits    = 5
	compressedMaxNormal     = 1<<compressedNormalBits - 1 // 7
	compressedMaxAlgorithm  = 4                           // CHROMAPRINT_ALGORITHM_TEST5
	compressedFrameBitWidth = 32
)

// errNotCompressed marks a payload whose header does not describe a
// compressed print at all (as opposed to a compressed print that is corrupt).
var errNotCompressed = errors.New("not a compressed chromaprint payload")

// decompressChromaprint decodes a compressed Chromaprint fingerprint (already
// base64-decoded) into its frames. It is strict: the header's frame count
// must be reached exactly, both bit streams must be present in full, and no
// bytes may follow them. Anything else is an error, never a partial result.
func decompressChromaprint(b []byte) ([]uint32, error) {
	if len(b) < compressedHeaderLen {
		return nil, fmt.Errorf("%w: %d bytes is shorter than the header", errNotCompressed, len(b))
	}
	if b[0] > compressedMaxAlgorithm {
		return nil, fmt.Errorf("%w: unknown algorithm %d", errNotCompressed, b[0])
	}
	numFrames := int(b[1])<<16 | int(b[2])<<8 | int(b[3])
	body := b[compressedHeaderLen:]
	if numFrames == 0 {
		if len(body) != 0 {
			return nil, fmt.Errorf("%w: zero frames but %d body bytes", errNotCompressed, len(body))
		}
		return []uint32{}, nil
	}

	// Normal stream: read 3-bit values until numFrames terminators (0) have
	// been seen, counting the escapes (7) that need an exceptional value.
	normal := make([]uint8, 0, numFrames*4)
	var found, numExcept int
	maxNormal := len(body) * 8 / compressedNormalBits
	for i := 0; i < maxNormal && found < numFrames; i++ {
		v := readPackedBits(body, i*compressedNormalBits, compressedNormalBits)
		normal = append(normal, v)
		switch v {
		case 0:
			found++
		case compressedMaxNormal:
			numExcept++
		}
	}
	if found != numFrames {
		return nil, fmt.Errorf("compressed chromaprint: header says %d frames, stream ends after %d", numFrames, found)
	}

	// Exceptional stream starts at the next byte boundary.
	exceptStart := packedLen(len(normal), compressedNormalBits)
	exceptLen := packedLen(numExcept, compressedExceptBits)
	if want := exceptStart + exceptLen; len(body) != want {
		return nil, fmt.Errorf("compressed chromaprint: %d body bytes, the %d frames need exactly %d", len(body), numFrames, want)
	}
	except := body[exceptStart:]

	frames := make([]uint32, numFrames)
	var value uint32
	var frame, lastBit, exceptIdx int
	for _, v := range normal {
		if v == 0 {
			if frame > 0 {
				value ^= frames[frame-1]
			}
			frames[frame] = value
			frame++
			value, lastBit = 0, 0
			continue
		}
		gap := int(v)
		if v == compressedMaxNormal {
			gap += int(readPackedBits(except, exceptIdx*compressedExceptBits, compressedExceptBits))
			exceptIdx++
		}
		bit := lastBit + gap
		if bit > compressedFrameBitWidth {
			return nil, fmt.Errorf("compressed chromaprint: frame %d sets bit %d, past bit %d", frame, bit, compressedFrameBitWidth)
		}
		value |= 1 << (bit - 1)
		lastBit = bit
	}
	return frames, nil
}

// readPackedBits reads a width-bit value starting at bit offset pos of an
// LSB-first packed stream.
func readPackedBits(b []byte, pos, width int) uint8 {
	var v uint8
	for k := range width {
		p := pos + k
		if b[p/8]>>(p%8)&1 != 0 {
			v |= 1 << k
		}
	}
	return v
}

// packedLen is the number of bytes n width-bit values occupy.
func packedLen(n, width int) int {
	return (n*width + 7) / 8
}
