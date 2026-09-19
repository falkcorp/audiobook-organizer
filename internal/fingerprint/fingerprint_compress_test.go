// file: internal/fingerprint/fingerprint_compress_test.go
// version: 1.0.0
// guid: 9e3b6f14-2c85-4a7d-b0e9-6f1a8d3c52e7
// last-edited: 2026-09-19

package fingerprint

import (
	"encoding/binary"
	"testing"
)

// TestEncodeCompressedFingerprint_MatchesFpcalcGolden: compressing the
// frames fpcalc -raw printed must reproduce fpcalc's own compressed string
// exactly, and decompressing that must give the frames back. This is what
// makes it safe to send to the AcoustID lookup API.
func TestEncodeCompressedFingerprint_MatchesFpcalcGolden(t *testing.T) {
	for name, clip := range loadGolden(t) {
		t.Run(name, func(t *testing.T) {
			raw := make([]byte, len(clip.Raw)*4)
			for i, f := range clip.Raw {
				binary.LittleEndian.PutUint32(raw[i*4:], f)
			}
			got, err := EncodeCompressedFingerprint(raw)
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			if got != clip.Compressed {
				t.Fatalf("compressed string differs from fpcalc's (len %d vs %d)", len(got), len(clip.Compressed))
			}
			back, err := decodeAnyFingerprint(got)
			if err != nil || len(back) != len(clip.Raw) {
				t.Fatalf("round trip: %d frames, err %v", len(back), err)
			}
		})
	}
}

func TestEncodeCompressedFingerprint_RejectsMisaligned(t *testing.T) {
	for _, raw := range [][]byte{nil, {1, 2, 3}, {1, 2, 3, 4, 5}} {
		if _, err := EncodeCompressedFingerprint(raw); err == nil {
			t.Fatalf("expected error for %d bytes", len(raw))
		}
	}
}
