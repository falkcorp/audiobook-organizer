// file: internal/security/safepath/safepath_test.go
// version: 1.0.1
// guid: 8a7b6c5d-4e3f-2a1b-9c0d-123456789abc
// last-edited: 2026-10-18
package safepath

import (
	"path/filepath"
	"testing"
)

func TestJoinNormal(t *testing.T) {
	root := "/tmp/safepath-root"
	p, err := Join(root, "sub", "file.txt")
	if err != nil {
		t.Fatalf("Join unexpected error: %v", err)
	}
	want := filepath.Clean(filepath.Join(root, "sub", "file.txt"))
	if p.String() != want {
		t.Fatalf("String() = %q; want %q", p.String(), want)
	}
}

func TestJoinTraversalReturnsError(t *testing.T) {
	root := "/tmp/safepath-root"
	_, err := Join(root, "..")
	if err == nil {
		t.Fatalf("expected error for traversal, got nil")
	}
}

func TestJoinAbsolutePartReturnsError(t *testing.T) {
	root := "/tmp/safepath-root"
	p, err := Join(root, "/etc/passwd")
	if err == nil {
		t.Fatalf("expected error for absolute part, got nil; result=%q", p.String())
	}
}

func TestValidateInside(t *testing.T) {
	root := "/tmp/safepath-root"
	path := filepath.Clean(filepath.Join(root, "sub", "file.txt"))
	p, err := Validate(root, path)
	if err != nil {
		t.Fatalf("Validate unexpected error: %v", err)
	}
	if p.String() != path {
		t.Fatalf("Validate returned %q; want %q", p.String(), path)
	}
}

func TestValidateOutsideReturnsError(t *testing.T) {
	root := "/tmp/safepath-root"
	_, err := Validate(root, "/etc/passwd")
	if err == nil {
		t.Fatalf("expected error for Validate outside root, got nil")
	}
}

func TestStringReturnsCleanedPath(t *testing.T) {
	root := "/tmp/safepath-root"
	p, err := Join(root, "sub", ".", "file.txt")
	if err != nil {
		t.Fatalf("Join unexpected error: %v", err)
	}
	want := filepath.Clean(filepath.Join(root, "sub", "file.txt"))
	if p.String() != want {
		t.Fatalf("String() = %q; want %q", p.String(), want)
	}
}
