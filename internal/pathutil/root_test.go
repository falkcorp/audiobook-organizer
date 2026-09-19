// file: internal/pathutil/root_test.go
// version: 1.0.0
// guid: 62f8c58b-6cc2-486b-9ee7-06af5c6e6a58
// last-edited: 2026-09-19

package pathutil

import (
	"encoding/base64"
	"testing"
)

func TestSplitRoot(t *testing.T) {
	vars := PathVars("/srv/books/library")
	cases := []struct {
		name, p, root, rel string
		ok                 bool
	}{
		{"under libroot", "/srv/books/library/A/B/01.m4b", "libroot", "A/B/01.m4b", true},
		// Most specific root wins: libroot is nested under books.
		{"most specific wins", "/srv/books/library/x.mp3", "libroot", "x.mp3", true},
		{"books but not libroot", "/srv/books/itunes/Music/x.m4b", "books", "itunes/Music/x.m4b", true},
		// A sibling whose name extends the root must not match it.
		{"sibling extends libroot", "/srv/books/library-old/x.mp3", "books", "library-old/x.mp3", true},
		{"sibling extends books", "/srv/books2/x.mp3", "", "", false},
		{"no root", "/etc/passwd", "", "", false},
		{"the root itself", "/srv/books/library", "", "", false},
		{"traversal", "/srv/books/library/../../etc/passwd", "", "", false},
		{"dot segment", "/srv/books/library/./x.mp3", "", "", false},
		{"double slash", "/srv/books/library//x.mp3", "", "", false},
		{"trailing slash", "/srv/books/library/dir/", "", "", false},
		{"NUL", "/srv/books/library/a\x00b", "", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root, rel, ok := SplitRoot(c.p, vars)
			if ok != c.ok || root != c.root || rel != c.rel {
				t.Fatalf("SplitRoot(%q) = (%q, %q, %v), want (%q, %q, %v)", c.p, root, rel, ok, c.root, c.rel, c.ok)
			}
		})
	}
}

func TestValidateRel(t *testing.T) {
	good := []string{"a", "a/b.m4b", "Name: With Colon/01.mp3", "café/x", "café/x", "\xff\xfe/bad-utf8.mp3", "..hidden", "a/..b"}
	for _, r := range good {
		if err := ValidateRel(r); err != nil {
			t.Errorf("ValidateRel(%q) = %v, want nil", r, err)
		}
	}
	bad := []string{"", ".", "..", "../x", "/abs", "a/../b", "a/./b", "a//b", "a/", "a\x00b"}
	for _, r := range bad {
		if err := ValidateRel(r); err == nil {
			t.Errorf("ValidateRel(%q) = nil, want an error", r)
		}
	}
}

// TestSplitRoot_NonUTF8SurvivesBase64 pins why rel travels as base64: a ZFS
// name need not be UTF-8, and JSON would replace invalid bytes with U+FFFD.
func TestSplitRoot_NonUTF8SurvivesBase64(t *testing.T) {
	p := "/srv/books/library/Auth\xe9r/\xff01.mp3"
	root, rel, ok := SplitRoot(p, PathVars("/srv/books/library"))
	if !ok || root != "libroot" {
		t.Fatalf("SplitRoot = (%q, %q, %v)", root, rel, ok)
	}
	enc := base64.StdEncoding.EncodeToString([]byte(rel))
	dec, err := base64.StdEncoding.DecodeString(enc)
	if err != nil || string(dec) != rel {
		t.Fatalf("round trip = %q, %v; want %q", dec, err, rel)
	}
}
