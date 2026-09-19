// file: internal/fingerprint/fpcalc_decode_test.go
// version: 1.3.0
// guid: e2c4a6b8-9d0e-4f1a-8b2c-3d4e5f6a7b8c
// last-edited: 2026-09-19

package fingerprint

import (
	"encoding/base64"
	"encoding/binary"
	"strings"
	"testing"
)

// TestDecodeAnyFingerprint_URLSafeBase64 ensures fingerprints encoded with the
// URL-safe base64 alphabet (using '-' and '_' instead of '+' and '/') decode
// successfully. Regression test for log spam:
//
//	[WARN] acoustid backfill: synthesize book signature for ...:
//	  synthesize signature: decode segment: invalid character '-' in fingerprint
func TestDecodeAnyFingerprint_URLSafeBase64(t *testing.T) {
	// Build a payload guaranteed to round-trip through URL-safe base64
	// and contain at least one '-' or '_' when encoded.
	payload := make([]byte, 0, 64)
	// Header (4 bytes) + 15 uint32s = 64 bytes. The header is the app's
	// uncompressed form (algorithm 1, frame count 0) that
	// EncodeWholeFingerprint writes.
	header := []byte{0x01, 0x00, 0x00, 0x00}
	payload = append(payload, header...)
	for i := range uint32(15) {
		var b [4]byte
		binary.LittleEndian.PutUint32(b[:], 0xFFFFFFF0|i)
		payload = append(payload, b[:]...)
	}

	stdEncoded := base64.StdEncoding.EncodeToString(payload)
	urlEncoded := base64.URLEncoding.EncodeToString(payload)
	rawURLEncoded := base64.RawURLEncoding.EncodeToString(payload)

	// Sanity: URL-safe encoding should differ from std for this payload.
	if !strings.ContainsAny(urlEncoded, "-_") {
		t.Skip("payload happens not to contain URL-safe chars; pick another payload")
	}

	for name, fp := range map[string]string{
		"std":    stdEncoded,
		"url":    urlEncoded,
		"rawurl": rawURLEncoded,
	} {
		t.Run(name, func(t *testing.T) {
			ints, err := decodeAnyFingerprint(fp)
			if err != nil {
				t.Fatalf("decodeAnyFingerprint(%s) err: %v", name, err)
			}
			if len(ints) != 15 {
				t.Fatalf("expected 15 ints, got %d", len(ints))
			}
		})
	}
}

// TestDecodeAnyFingerprint_BrokenPadding is the regression for the prod
// log-spam after the URL-safe fix shipped: chromaprint output sometimes
// arrives with the URL-safe alphabet *and* wrong-length `=` padding. Both
// `URLEncoding` (needs correct padding) and `RawURLEncoding` (needs no
// padding) reject those, and the loop in v3.1 fell through to the base62
// decoder which barfs on `-`/`_`. v3.2 normalizes padding before decoding.
func TestDecodeAnyFingerprint_BrokenPadding(t *testing.T) {
	payload := make([]byte, 0, 64)
	payload = append(payload, 0x01, 0x00, 0x00, 0x00)
	for i := range uint32(15) {
		var b [4]byte
		binary.LittleEndian.PutUint32(b[:], 0xFFFFFFF0|i)
		payload = append(payload, b[:]...)
	}
	urlEncoded := base64.URLEncoding.EncodeToString(payload)
	if !strings.ContainsAny(urlEncoded, "-_") {
		t.Skip("payload happens not to contain URL-safe chars")
	}

	cases := map[string]string{
		"strip_padding":          strings.TrimRight(urlEncoded, "="),
		"too_few_pad":            strings.TrimRight(urlEncoded, "=") + "=", // 1 `=` when input needs 0/2
		"too_many_pad":           urlEncoded + "==",                        // extra padding
		"whitespace_in_middle":   urlEncoded[:10] + "\n  \t" + urlEncoded[10:],
		"raw_url_with_extra_pad": base64.RawURLEncoding.EncodeToString(payload) + "===",
	}
	for name, fp := range cases {
		t.Run(name, func(t *testing.T) {
			ints, err := decodeAnyFingerprint(fp)
			if err != nil {
				t.Fatalf("decodeAnyFingerprint(%s) err: %v (input %q)", name, err, fp)
			}
			if len(ints) != 15 {
				t.Fatalf("expected 15 ints, got %d", len(ints))
			}
		})
	}
}

// TestDecodeAnyFingerprint_MisalignedPayloadIsAnError replaces the old
// "trailing_byte_misalign" case, which asserted that a payload with a stray
// trailing byte was truncated into 15 frames. That truncation branch was
// also what turned every compressed fingerprint into bitstream "frames", so
// it was removed on 2026-09-19: a payload that is not whole frames is now an
// error, never a guess.
func TestDecodeAnyFingerprint_MisalignedPayloadIsAnError(t *testing.T) {
	payload := []byte{0x01, 0x00, 0x00, 0x00}
	for i := range uint32(15) {
		var b [4]byte
		binary.LittleEndian.PutUint32(b[:], 0xFFFFFFF0|i)
		payload = append(payload, b[:]...)
	}
	payload = append(payload, 0xff)
	if ints, err := decodeAnyFingerprint(base64.StdEncoding.EncodeToString(payload)); err == nil {
		t.Fatalf("expected an error for a 61-byte body, got %d frames", len(ints))
	}
}
