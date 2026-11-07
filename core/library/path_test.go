// file: core/library/path_test.go
// version: 1.0.1
// guid: a1b2c3d4-e5f6-7890-abcd-ef1234567890
// last-edited: 2026-11-07

package library

import (
	"path/filepath"
	"testing"
)

// TestSafeJoin verifies that SafeJoin blocks path traversal and allows valid sub-paths.
// These tests validate the behavior documented in the CodeQL MaD pack at
// .github/codeql/models/path-sanitizers.model.yml (barrierModel for SafeJoin).
func TestSafeJoin(t *testing.T) {
	root := "/var/lib/audiobooks"

	tests := []struct {
		name    string
		root    string
		parts   []string
		wantErr bool
		wantOut string
	}{
		{
			name:    "simple sub-path",
			root:    root,
			parts:   []string{"Author", "Book.m4b"},
			wantErr: false,
			wantOut: filepath.Join(root, "Author", "Book.m4b"),
		},
		{
			name:    "nested sub-path",
			root:    root,
			parts:   []string{"A", "B", "C.m4b"},
			wantErr: false,
			wantOut: filepath.Join(root, "A", "B", "C.m4b"),
		},
		{
			name:    "path equals root",
			root:    root,
			parts:   []string{},
			wantErr: false,
			wantOut: root,
		},
		{
			name:    "traversal escapes root",
			root:    root,
			parts:   []string{"../../etc/passwd"},
			wantErr: true,
		},
		{
			name:    "traversal via parent then sub",
			root:    root,
			parts:   []string{"Author", "../../../etc/shadow"},
			wantErr: true,
		},
		{
			name:    "absolute path in part does not escape",
			root:    root,
			parts:   []string{"sub", "ok.m4b"},
			wantErr: false,
			wantOut: filepath.Join(root, "sub", "ok.m4b"),
		},
		{
			name:    "double dot at start",
			root:    root,
			parts:   []string{"..", "other"},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := SafeJoin(tc.root, tc.parts...)
			if tc.wantErr {
				if err == nil {
					t.Errorf("SafeJoin(%q, %v) = %q, nil error; want error", tc.root, tc.parts, got)
				}
				return
			}
			if err != nil {
				t.Errorf("SafeJoin(%q, %v) returned unexpected error: %v", tc.root, tc.parts, err)
				return
			}
			if got != tc.wantOut {
				t.Errorf("SafeJoin(%q, %v) = %q; want %q", tc.root, tc.parts, got, tc.wantOut)
			}
		})
	}
}

// TestWithinRoot verifies that WithinRoot correctly identifies paths that are
// equal to or contained within the root.
// These tests validate the behavior documented in the CodeQL MaD pack at
// .github/codeql/models/path-sanitizers.model.yml (barrierGuardModel for WithinRoot).
func TestWithinRoot(t *testing.T) {
	root := "/var/lib/audiobooks"

	tests := []struct {
		name string
		path string
		root string
		want bool
	}{
		{
			name: "path equals root",
			path: root,
			root: root,
			want: true,
		},
		{
			name: "path is sub-directory",
			path: filepath.Join(root, "Author"),
			root: root,
			want: true,
		},
		{
			name: "path is deeply nested",
			path: filepath.Join(root, "A", "B", "C.m4b"),
			root: root,
			want: true,
		},
		{
			name: "path escapes root via traversal",
			path: "/etc/passwd",
			root: root,
			want: false,
		},
		{
			name: "sibling directory is not within root",
			path: "/var/lib/other",
			root: root,
			want: false,
		},
		{
			name: "path with trailing slash normalized",
			path: root + "/",
			root: root,
			want: true,
		},
		{
			name: "root with trailing slash normalized",
			path: filepath.Join(root, "file.m4b"),
			root: root + "/",
			want: true,
		},
		{
			name: "prefix match does not extend to sibling with same prefix",
			path: root + "sibling",
			root: root,
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := WithinRoot(tc.path, tc.root)
			if got != tc.want {
				t.Errorf("WithinRoot(%q, %q) = %v; want %v", tc.path, tc.root, got, tc.want)
			}
		})
	}
}
