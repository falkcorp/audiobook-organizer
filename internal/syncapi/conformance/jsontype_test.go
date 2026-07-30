// file: internal/syncapi/conformance/jsontype_test.go
// version: 1.0.0
// guid: 0ef3b29e-4a78-4e1e-ba46-ff28f32e16a4
// last-edited: 2026-07-29

package conformance

import (
	"encoding/json"
	"testing"
)

func TestJSONType(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"object", `{"a":1}`, "object"},
		{"array", `[1,2]`, "array"},
		{"string", `"hi"`, "string"},
		{"number", `3.5`, "number"},
		{"integer is still number", `7`, "number"},
		{"bool", `true`, "bool"},
		{"null", `null`, "null"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var v any
			if err := json.Unmarshal([]byte(tc.raw), &v); err != nil {
				t.Fatalf("unmarshal %s: %v", tc.raw, err)
			}
			if got := JSONType(v); got != tc.want {
				t.Errorf("JSONType(%s) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}
