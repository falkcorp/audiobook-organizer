// file: internal/database/sql_activity_details_codec_test.go
// version: 1.0.0
// guid: 9b4d2f70-1a63-4c8e-b502-7f6a3c9d1e28
// last-edited: 2026-09-07

package database

import (
	"bytes"
	"encoding/json"
	"math/rand"
	"strings"
	"testing"
)

// TestActivityDetailsCodec_RoundTrip covers the four shapes the details codec
// must handle: a large repetitive blob (the iTunes-ITL-dump case that ballooned
// prod), a tiny payload (must stay raw, never grow), empty (→ nil, column NULL),
// and a legacy untagged raw-JSON value (a pre-compression DB must still read).
func TestActivityDetailsCodec_RoundTrip(t *testing.T) {
	// Large, highly repetitive — like the 9.6 MB ApplyITLOperations dumps.
	big := []byte(`{"err":"` + strings.Repeat("ITLSafetyCompare mismatch; ", 4000) + `"}`)
	small := []byte(`{"a":1,"b":"x"}`)

	t.Run("large compresses and round-trips", func(t *testing.T) {
		enc := encodeActivityDetails(big)
		if len(enc) == 0 || enc[0] != detailsFmtZstd {
			t.Fatalf("large payload not zstd-tagged: tag=%#v len=%d", enc[0], len(enc))
		}
		if len(enc) >= len(big) {
			t.Errorf("compressed (%d) not smaller than raw (%d)", len(enc), len(big))
		}
		got, err := decodeActivityDetails(enc)
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		if !bytes.Equal(got, big) {
			t.Errorf("round-trip mismatch: got %d bytes, want %d", len(got), len(big))
		}
	})

	t.Run("small stays raw and never grows", func(t *testing.T) {
		enc := encodeActivityDetails(small)
		if enc[0] != detailsFmtRaw {
			t.Fatalf("small payload not raw-tagged: tag=%#v", enc[0])
		}
		if len(enc) != len(small)+1 {
			t.Errorf("raw stored len = %d, want %d (payload + 1 tag byte)", len(enc), len(small)+1)
		}
		got, err := decodeActivityDetails(enc)
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		if !bytes.Equal(got, small) {
			t.Errorf("small round-trip mismatch")
		}
	})

	t.Run("empty in → nil out", func(t *testing.T) {
		if enc := encodeActivityDetails(nil); enc != nil {
			t.Errorf("encode(nil) = %#v, want nil", enc)
		}
		got, err := decodeActivityDetails(nil)
		if err != nil || got != nil {
			t.Errorf("decode(nil) = (%#v, %v), want (nil, nil)", got, err)
		}
	})

	t.Run("legacy untagged JSON reads back verbatim", func(t *testing.T) {
		legacy := []byte(`{"legacy":true,"n":42}`) // no format tag, starts with '{'
		got, err := decodeActivityDetails(legacy)
		if err != nil {
			t.Fatalf("decode legacy: %v", err)
		}
		if !bytes.Equal(got, legacy) {
			t.Errorf("legacy mismatch: got %q want %q", got, legacy)
		}
		// And it must still json-unmarshal.
		var m map[string]any
		if err := json.Unmarshal(got, &m); err != nil {
			t.Errorf("legacy not valid JSON after decode: %v", err)
		}
	})

	t.Run("incompressible payload above threshold falls back to raw", func(t *testing.T) {
		// High-entropy random bytes: zstd cannot shrink these below the original,
		// so encode must keep them raw rather than store something larger. Seeded
		// for reproducibility.
		payload := make([]byte, 4096)
		rng := rand.New(rand.NewSource(1))
		rng.Read(payload)
		enc := encodeActivityDetails(payload)
		if enc[0] != detailsFmtRaw {
			t.Errorf("incompressible payload should fall back to raw, got tag %#v", enc[0])
		}
		if len(enc) > len(payload)+1 {
			t.Errorf("raw fallback stored %d bytes, must not exceed payload+1 (%d)", len(enc), len(payload)+1)
		}
		got, err := decodeActivityDetails(enc)
		if err != nil || !bytes.Equal(got, payload) {
			t.Errorf("incompressible round-trip failed: err=%v equal=%v", err, bytes.Equal(got, payload))
		}
	})
}
