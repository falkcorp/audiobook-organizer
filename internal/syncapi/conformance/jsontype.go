// file: internal/syncapi/conformance/jsontype.go
// version: 1.0.0
// guid: 3d768e0c-5b1f-4adf-9205-a0d7c5b1e959
// last-edited: 2026-07-29

// Package conformance diffs this server's Audiobookshelf-compatible API
// responses against golden fixtures captured from a real Audiobookshelf
// server. It checks field presence and type, not just values, because ABS
// clients hard-require specific fields and fail opaquely when they are absent
// or the wrong shape.
package conformance

// JSONType classifies a value produced by encoding/json into a stable type
// name. Conformance compares types as well as values, so this is the shared
// vocabulary used by both the differ and the normalizer.
func JSONType(v any) string {
	switch v.(type) {
	case map[string]any:
		return "object"
	case []any:
		return "array"
	case string:
		return "string"
	case float64:
		return "number"
	case bool:
		return "bool"
	case nil:
		return "null"
	default:
		return "unknown"
	}
}
