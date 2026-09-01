// file: internal/metastate/metastate_test.go
// version: 1.0.0
// guid: 5d9b2e73-4a18-4c56-9e02-7f3a1c8b6d45
// last-edited: 2026-09-01

package metastate

import (
	"encoding/json"
	"testing"
)

// TestKeyFormatIsPinned guards a PERSISTED namespace. Existing user-preference
// rows live under this exact prefix, so a "tidier" key would not fail to
// compile and would not fail any behavioural test -- it would simply stop
// finding every row already written. The literal is spelled out here rather
// than built from keyPrefix on purpose: a test that reuses the constant moves
// with it and guards nothing.
func TestKeyFormatIsPinned(t *testing.T) {
	if got := Key("book-123"); got != "metadata_state_book-123" {
		t.Errorf("Key(%q) = %q, want %q -- this prefix is a persisted namespace, not a format choice", "book-123", got, "metadata_state_book-123")
	}
	if got := Key(""); got != "metadata_state_" {
		t.Errorf("Key(\"\") = %q, want %q", got, "metadata_state_")
	}
}

func TestEncodeDecodeRoundTrip(t *testing.T) {
	cases := []any{
		"hello",
		float64(42), // JSON numbers decode as float64
		true,
		[]any{"a", "b"},
		map[string]any{"k": "v"},
		"", // an explicitly empty string, distinct from unset
	}
	for _, want := range cases {
		enc, err := Encode(want)
		if err != nil {
			t.Fatalf("Encode(%#v): %v", want, err)
		}
		if enc == nil {
			t.Fatalf("Encode(%#v) returned nil for a non-nil value", want)
		}
		got := Decode(enc)
		a, _ := json.Marshal(got)
		b, _ := json.Marshal(want)
		if string(a) != string(b) {
			t.Errorf("round trip of %#v produced %#v", want, got)
		}
	}
}

// TestUnsetIsAbsenceNotNullLiteral pins the contract Decode's nil check relies
// on: an unset value must encode to a NIL POINTER, not to the four bytes
// "null". If Encode emitted "null", Decode would take the json.Unmarshal path
// and return a nil `any` from a non-empty string -- reading back as unset only
// by coincidence, and storing four bytes per unset field forever.
func TestUnsetIsAbsenceNotNullLiteral(t *testing.T) {
	enc, err := Encode(nil)
	if err != nil {
		t.Fatalf("Encode(nil): %v", err)
	}
	if enc != nil {
		t.Fatalf("Encode(nil) = %q, want a nil pointer: unset is absence, not the JSON null literal", *enc)
	}
}

// TestDecodeKeepsNonJSONAsRawString covers rows written before values were
// JSON-encoded. Returning an error (or nil) here would silently discard real
// user data on read.
func TestDecodeKeepsNonJSONAsRawString(t *testing.T) {
	raw := "a bare legacy string"
	if got := Decode(&raw); got != raw {
		t.Errorf("Decode(%q) = %#v, want the raw string back -- legacy rows are not JSON", raw, got)
	}
}

func TestDecodeUnsetForms(t *testing.T) {
	if got := Decode(nil); got != nil {
		t.Errorf("Decode(nil) = %#v, want nil", got)
	}
	empty := ""
	if got := Decode(&empty); got != nil {
		t.Errorf("Decode(&\"\") = %#v, want nil", got)
	}
}
