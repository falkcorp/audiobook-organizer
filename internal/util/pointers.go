// file: internal/util/pointers.go
// version: 1.0.2
// guid: f1a2b3c4-d5e6-7f89-0a1b-2c3d4e5f6a7b
// last-edited: 2026-09-02

package util

// DerefStr returns the string value of a *string, or "" if nil.
func DerefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// DerefInt returns the int value of a *int, or 0 if nil.
func DerefInt(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

// DerefBool returns the bool value of a *bool, or false if nil.
func DerefBool(p *bool) bool {
	if p == nil {
		return false
	}
	return *p
}

// ExtractStringField extracts a string value from a map[string]any payload.
func ExtractStringField(payload map[string]any, key string) (string, bool) {
	val, ok := payload[key]
	if !ok {
		return "", false
	}
	str, ok := val.(string)
	return str, ok
}

// ExtractIntField extracts an int from a map[string]any payload (handles JSON float64 and native int).
func ExtractIntField(payload map[string]any, key string) (int, bool) {
	val, ok := payload[key]
	if !ok {
		return 0, false
	}
	switch v := val.(type) {
	case float64:
		return int(v), true
	case int:
		return v, true
	}
	return 0, false
}

// ExtractBoolField extracts a bool from a map[string]any payload.
func ExtractBoolField(payload map[string]any, key string) (bool, bool) {
	val, ok := payload[key]
	if !ok {
		return false, false
	}
	b, ok := val.(bool)
	return b, ok
}
