// file: internal/fingerprint/fpcalc_decode_test.go
// version: 1.2.1
// guid: e2c4a6b8-9d0e-4f1a-8b2c-3d4e5f6a7b8c
// last-edited: 2026-09-02

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
	// Header (4 bytes) + 15 uint32s = 64 bytes.
	header := []byte{0xfb, 0x01, 0x00, 0x00}
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
	payload = append(payload, 0xfb, 0x01, 0x00, 0x00)
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
		// Append a stray byte so decoded length becomes 65 (4-byte header
		// + 60 bytes payload + 1 stray). Pre-fix this fell to base62 and
		// produced the misleading "invalid character '+' in fingerprint"
		// warning. Now we truncate to header + 60 bytes and parse 15 ints.
		"trailing_byte_misalign": base64.StdEncoding.EncodeToString(append(append([]byte{}, payload...), 0xff)),
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
