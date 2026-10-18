// file: internal/util/util_coverage_test.go
// version: 1.0.2
// guid: 4c1d9e2a-7b5f-4e83-9a6d-2f8c0b1e5d47
// last-edited: 2026-10-18

package util

import (
	"testing"
)

// --- Coverage for pointer/extract functions not tested ---

func TestCoverage_ExtractStringField_TypeMismatch(t *testing.T) {
	m := map[string]any{"count": 42}
	_, ok := ExtractStringField(m, "count")
	if ok {
		t.Error("expected false for non-string value")
	}
}

func TestCoverage_ExtractIntField_NativeInt(t *testing.T) {
	m := map[string]any{"count": 42}
	i, ok := ExtractIntField(m, "count")
	if !ok {
		t.Error("expected true for native int")
	}
	if i != 42 {
		t.Errorf("expected 42, got %d", i)
	}
}

func TestCoverage_ExtractIntField_Missing(t *testing.T) {
	m := map[string]any{}
	_, ok := ExtractIntField(m, "missing")
	if ok {
		t.Error("expected false for missing key")
	}
}

func TestCoverage_ExtractIntField_WrongType(t *testing.T) {
	m := map[string]any{"count": "not-a-number"}
	_, ok := ExtractIntField(m, "count")
	if ok {
		t.Error("expected false for string value")
	}
}

func TestCoverage_ExtractBoolField_Missing(t *testing.T) {
	m := map[string]any{}
	_, ok := ExtractBoolField(m, "missing")
	if ok {
		t.Error("expected false for missing key")
	}
}

func TestCoverage_ExtractBoolField_WrongType(t *testing.T) {
	m := map[string]any{"active": "yes"}
	_, ok := ExtractBoolField(m, "active")
	if ok {
		t.Error("expected false for string value")
	}
}

// --- Coverage for perms.go ---

func TestCoverage_PermConstants(t *testing.T) {
	if DirMode != 0o775 {
		t.Errorf("DirMode = %o, want 775", DirMode)
	}
	if FileMode != 0o664 {
		t.Errorf("FileMode = %o, want 664", FileMode)
	}
	if SecretFileMode != 0o600 {
		t.Errorf("SecretFileMode = %o, want 600", SecretFileMode)
	}
}
