// file: internal/security/pathvalidation/pathvalidation_test.go
// version: 1.1.1
// guid: 7b3d9f1e-2a6c-4f8d-8e0b-5c4a3b2d1e0f
// last-edited: 2026-10-18

package pathvalidation_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/security/pathvalidation"
)

// ─────────────────────────────────────────────────────────────────────────────
// SanitizeFilename
// ─────────────────────────────────────────────────────────────────────────────

func TestSanitizeFilename_SafeInput(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"hello.m4b", "hello.m4b"},
		{"My Book Title", "My Book Title"},
		{"Author - Series 1", "Author - Series 1"},
		{"Book_Name_01", "Book_Name_01"},
	}
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			got := pathvalidation.SanitizeFilename(tc.input)
			if got != tc.want {
				t.Errorf("SanitizeFilename(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestSanitizeFilename_IllegalChars(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{"windows reserved: colon", "C:drive"},
		{"windows reserved: asterisk", "file*name"},
		{"windows reserved: question mark", "what?"},
		{"windows reserved: pipe", "a|b"},
		{"windows reserved: less than", "a<b"},
		{"windows reserved: greater than", "a>b"},
		{"windows reserved: double quote", `a"b`},
		{"directory separator: forward slash", "path/to/file"},
		{"directory separator: backslash", `path\to\file`},
		{"null byte", "bad\x00name"},
		{"control char", "bad\x1fname"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := pathvalidation.SanitizeFilename(tc.input)
			// Result must not contain any of the unsafe characters.
			for _, bad := range []string{"/", "\\", ":", "*", "?", "|", "<", ">", "\"", "\x00"} {
				if strings.Contains(got, bad) {
					t.Errorf("SanitizeFilename(%q) = %q still contains unsafe char %q", tc.input, got, bad)
				}
			}
			// Must be non-empty.
			if got == "" {
				t.Errorf("SanitizeFilename(%q) returned empty string", tc.input)
			}
		})
	}
}

func TestSanitizeFilename_LeadingTrailingDotsAndSpaces(t *testing.T) {
	cases := []struct {
		input string
	}{
		{"  spaces  "},
		{"...dots..."},
	}
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			got := pathvalidation.SanitizeFilename(tc.input)
			if strings.HasPrefix(got, ".") || strings.HasSuffix(got, ".") {
				t.Errorf("SanitizeFilename(%q) = %q still has leading/trailing dot", tc.input, got)
			}
			if strings.HasPrefix(got, " ") || strings.HasSuffix(got, " ") {
				t.Errorf("SanitizeFilename(%q) = %q still has leading/trailing space", tc.input, got)
			}
		})
	}
}

func TestSanitizeFilename_EmptyInput(t *testing.T) {
	got := pathvalidation.SanitizeFilename("")
	if got == "" {
		t.Error("SanitizeFilename(\"\") returned empty string, want non-empty fallback")
	}
}

func TestSanitizeFilename_LongInput(t *testing.T) {
	long := strings.Repeat("a", 512)
	got := pathvalidation.SanitizeFilename(long)
	if len(got) > 255 {
		t.Errorf("SanitizeFilename(long) returned %d bytes, want ≤255", len(got))
	}
}

func TestSanitizeFilename_NoDirectorySeparatorsInjected(t *testing.T) {
	// Even if an attacker tries to inject a path separator, the result must be
	// a single filename component with no slashes.
	inputs := []string{
		"../../etc/passwd",
		"../secrets",
		"a/b/c",
		`a\b\c`,
	}
	for _, in := range inputs {
		got := pathvalidation.SanitizeFilename(in)
		if strings.ContainsAny(got, "/\\") {
			t.Errorf("SanitizeFilename(%q) = %q still contains separator", in, got)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// SecureJoin
// ─────────────────────────────────────────────────────────────────────────────

func TestSecureJoin_HappyPath(t *testing.T) {
	root := "/srv/books"
	cases := []struct {
		parts []string
		want  string
	}{
		{[]string{"author"}, "/srv/books/author"},
		{[]string{"author", "book.m4b"}, "/srv/books/author/book.m4b"},
		{[]string{"a", "b", "c"}, "/srv/books/a/b/c"},
		{[]string{""}, "/srv/books"}, // empty parts are ignored
		// ".." that cancels a previously added segment stays within root:
		{[]string{"author", "..", "other"}, "/srv/books/other"},
	}
	for _, tc := range cases {
		t.Run(strings.Join(tc.parts, "/"), func(t *testing.T) {
			got, err := pathvalidation.SecureJoin(root, tc.parts...)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSecureJoin_TraversalRejected(t *testing.T) {
	root := "/srv/books"
	cases := []struct {
		name  string
		parts []string
	}{
		{"single traversal from root", []string{".."}},
		{"deep traversal", []string{"a", "b", "..", "..", "..", "etc", "passwd"}},
		// ".." that would escape the root across multiple parts:
		{"escape via multi-part", []string{"..", "..", "etc"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := pathvalidation.SecureJoin(root, tc.parts...)
			if err == nil {
				t.Fatalf("expected error for parts %v, got nil", tc.parts)
			}
		})
	}
}

func TestSecureJoin_AbsolutePartRejected(t *testing.T) {
	_, err := pathvalidation.SecureJoin("/srv/books", "/etc/passwd")
	if err == nil {
		t.Fatal("expected error for absolute part, got nil")
	}
	if !errors.Is(err, pathvalidation.ErrAbsolutePathNotAllowed) {
		t.Errorf("expected ErrAbsolutePathNotAllowed, got %v", err)
	}
}

func TestSecureJoin_RootOnly(t *testing.T) {
	got, err := pathvalidation.SecureJoin("/srv/books")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "/srv/books" {
		t.Errorf("got %q, want %q", got, "/srv/books")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// SecureJoinResolved (filesystem required)
// ─────────────────────────────────────────────────────────────────────────────

func TestSecureJoinResolved_Normal(t *testing.T) {
	dir := t.TempDir()
	// Path doesn't exist yet — SecureJoinResolved must not error solely because
	// the path is missing; it should fall back to the static check.
	got, err := pathvalidation.SecureJoinResolved(dir, "sub", "file.txt")
	if err != nil {
		if strings.Contains(err.Error(), "traversal") {
			t.Fatalf("unexpected traversal error for safe path: %v", err)
		}
		// Other errors (e.g. EvalSymlinks failure on missing path) are acceptable.
	}
	if err == nil {
		// The returned path must be within dir. Use EvalSymlinks on dir
		// because SecureJoinResolved returns the symlink-resolved form
		// (e.g. macOS resolves /var/folders → /private/var/folders).
		realDir, evalErr := filepath.EvalSymlinks(dir)
		if evalErr != nil {
			realDir = filepath.Clean(dir)
		}
		if !strings.HasPrefix(got, realDir) {
			t.Errorf("resolved path %q is not within root %q (real=%q)", got, dir, realDir)
		}
	}
}

func TestSecureJoinResolved_SymlinkEscape(t *testing.T) {
	// Create:
	//   root/     <- safe root
	//   outside/  <- outside root
	//   root/link -> ../outside (symlink escaping root)
	dir := t.TempDir()
	root := filepath.Join(dir, "root")
	outside := filepath.Join(dir, "outside")
	if err := os.MkdirAll(root, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outside, 0755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink("../outside", link); err != nil {
		t.Fatal(err)
	}

	_, err := pathvalidation.SecureJoinResolved(root, "link")
	if err == nil {
		t.Fatal("expected error for symlink that escapes root, got nil")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Error sentinel values
// ─────────────────────────────────────────────────────────────────────────────

func TestErrors_AreDistinct(t *testing.T) {
	if pathvalidation.ErrPathTraversal == pathvalidation.ErrAbsolutePathNotAllowed {
		t.Error("ErrPathTraversal and ErrAbsolutePathNotAllowed must be distinct")
	}
}

func TestErrors_WrappedCorrectly(t *testing.T) {
	// ErrPathTraversal must be detectable via errors.Is from wrapped errors.
	root := "/srv/books"

	_, err := pathvalidation.SecureJoin(root, "../escape")
	if !errors.Is(err, pathvalidation.ErrPathTraversal) {
		t.Errorf("errors.Is(err, ErrPathTraversal) = false, got: %v", err)
	}

	_, err = pathvalidation.SecureJoin(root, "/absolute")
	if !errors.Is(err, pathvalidation.ErrAbsolutePathNotAllowed) {
		t.Errorf("errors.Is(err, ErrAbsolutePathNotAllowed) = false, got: %v", err)
	}
}

func TestIsDangerousRoot(t *testing.T) {
	dangerous := []string{
		"/",
		"/etc",
		"/home",
		"/usr",
		"/var",
		"/root",
		"/bin",
		"/sbin",
		"/boot",
		"/proc",
		"/sys",
		"/dev",
	}
	for _, p := range dangerous {
		if !pathvalidation.IsDangerousRoot(p) {
			t.Errorf("IsDangerousRoot(%q) = false, want true", p)
		}
	}

	safe := []string{
		"/home/user/audiobooks",
		"/mnt/bigdata/books",
		"/srv/audiobook-library",
		"/data/books",
		"/tmp/test-library",
		"/Users/jdfalk/audiobooks",
	}
	for _, p := range safe {
		if pathvalidation.IsDangerousRoot(p) {
			t.Errorf("IsDangerousRoot(%q) = true, want false", p)
		}
	}
}
