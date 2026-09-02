// file: internal/util/util_coverage_test.go
// version: 1.0.1
// guid: 4c1d9e2a-7b5f-4e83-9a6d-2f8c0b1e5d47
// last-edited: 2026-09-02

package util

import (
	"os"
	"path/filepath"
	"testing"
)

// --- Coverage for pointer/extract functions not tested ---

func TestCoverage_DerefBool(t *testing.T) {
	t.Run("nil returns false", func(t *testing.T) {
		if DerefBool(nil) {
			t.Error("DerefBool(nil) should return false")
		}
	})

	t.Run("true value", func(t *testing.T) {
		b := true
		if !DerefBool(&b) {
			t.Error("DerefBool(&true) should return true")
		}
	})

	t.Run("false value", func(t *testing.T) {
		b := false
		if DerefBool(&b) {
			t.Error("DerefBool(&false) should return false")
		}
	})
}

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

func TestCoverage_CreateFile(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "testfile.txt")

	f, err := CreateFile(path)
	if err != nil {
		t.Fatalf("CreateFile failed: %v", err)
	}
	defer f.Close()

	// Write something
	if _, err := f.WriteString("hello"); err != nil {
		t.Fatalf("write failed: %v", err)
	}

	// Close and verify contents
	f.Close()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	if string(data) != "hello" {
		t.Errorf("expected 'hello', got %q", string(data))
	}

	// Verify permissions (on non-Windows)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat failed: %v", err)
	}
	// On macOS/Linux the umask may affect the final bits, so just check it's reasonable
	mode := info.Mode().Perm()
	if mode == 0 {
		t.Error("file has no permissions")
	}
}

func TestCoverage_CreateFile_Overwrite(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "overwrite.txt")

	// Create first file
	f1, err := CreateFile(path)
	if err != nil {
		t.Fatal(err)
	}
	f1.WriteString("first")
	f1.Close()

	// Overwrite
	f2, err := CreateFile(path)
	if err != nil {
		t.Fatal(err)
	}
	f2.WriteString("second")
	f2.Close()

	data, _ := os.ReadFile(path)
	if string(data) != "second" {
		t.Errorf("expected 'second', got %q", string(data))
	}
}
