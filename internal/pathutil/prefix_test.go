// file: internal/pathutil/prefix_test.go
// version: 1.0.0
// guid: 8fb7e6b5-92b6-4818-a62f-c4cb2f90702e
// last-edited: 2026-09-12

package pathutil

import "testing"

func TestCutPathPrefix(t *testing.T) {
	tests := []struct {
		name     string
		p        string
		prefix   string
		wantRest string
		wantOK   bool
	}{
		{"sibling prefix is not a match", "/lib2/a.m4b", "/lib", "", false},
		{"exact match", "/lib", "/lib", "", true},
		{"child without trailing separator", "/lib/a.m4b", "/lib", "/a.m4b", true},
		{"child with trailing separator", "/lib/a.m4b", "/lib/", "a.m4b", true},
		{"sibling against trailing separator", "/lib2/a.m4b", "/lib/", "", false},
		{"windows backslash child", `C:\Music\iTunes\x.m4b`, `C:\Music\iTunes`, `\x.m4b`, true},
		{"windows backslash sibling", `C:\Music\iTunes2\x.m4b`, `C:\Music\iTunes`, "", false},
		{"windows prefix with trailing backslash", `C:\Music\iTunes\x.m4b`, `C:\Music\iTunes\`, "x.m4b", true},
		{"mixed separators", `C:/Music\iTunes`, `C:/Music`, `\iTunes`, true},
		{"empty prefix never matches", "/lib/a.m4b", "", "", false},
		{"prefix longer than path", "/li", "/lib", "", false},
		{"case differs", "/LIB/a.m4b", "/lib", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rest, ok := CutPathPrefix(tt.p, tt.prefix)
			if rest != tt.wantRest || ok != tt.wantOK {
				t.Fatalf("CutPathPrefix(%q, %q) = (%q, %v), want (%q, %v)", tt.p, tt.prefix, rest, ok, tt.wantRest, tt.wantOK)
			}
		})
	}
}

func TestCutPathPrefixFold(t *testing.T) {
	tests := []struct {
		name     string
		p        string
		prefix   string
		wantRest string
		wantOK   bool
	}{
		{"case differs on a boundary", "c:/music/iTunes/Sub/x.m4b", "C:/Music/iTunes", "/Sub/x.m4b", true},
		{"case differs across a boundary", "c:/music/itunes2/x.m4b", "C:/Music/iTunes", "", false},
		{"exact match ignoring case", "C:/MUSIC", "c:/music", "", true},
		{"trailing separator prefix", "c:/music/x.m4b", "C:/Music/", "x.m4b", true},
		{"remainder keeps original case", "C:/MUSIC/Author/Book.m4b", "c:/music", "/Author/Book.m4b", true},
		{"empty prefix never matches", "C:/x", "", "", false},
		{"prefix longer than path", "C:/M", "C:/Music", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rest, ok := CutPathPrefixFold(tt.p, tt.prefix)
			if rest != tt.wantRest || ok != tt.wantOK {
				t.Fatalf("CutPathPrefixFold(%q, %q) = (%q, %v), want (%q, %v)", tt.p, tt.prefix, rest, ok, tt.wantRest, tt.wantOK)
			}
		})
	}
}
