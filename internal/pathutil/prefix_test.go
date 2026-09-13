// file: internal/pathutil/prefix_test.go
// version: 1.0.1
// guid: 8fb7e6b5-92b6-4818-a62f-c4cb2f90702e
// last-edited: 2026-09-12

package pathutil

import (
	"strings"
	"testing"
)

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

func TestIsWithin(t *testing.T) {
	tests := []struct {
		name string
		path string
		root string
		want bool
	}{
		{"child", "/lib/a.m4b", "/lib", true},
		{"sibling", "/lib2/a.m4b", "/lib", false},
		{"sibling with shared stem", "/library/a.m4b", "/lib", false},
		{"root equals path", "/lib", "/lib", true},
		{"root with trailing separator, child", "/lib/a.m4b", "/lib/", true},
		{"root with trailing separator, sibling", "/lib2/a.m4b", "/lib/", false},
		{"root with trailing separator, path is root without it", "/lib", "/lib/", false},
		{"root with trailing separator equals path", "/lib/", "/lib/", true},
		{"slash root, absolute path", "/lib/a.m4b", "/", true},
		{"slash root, itself", "/", "/", true},
		{"slash root, relative path", "lib/a.m4b", "/", false},
		{"empty root matches nothing", "/lib/a.m4b", "", false},
		{"empty root, empty path", "", "", false},
		{"empty path", "", "/lib", false},
		{"path shorter than root", "/li", "/lib", false},
		{"nested child", "/lib/Author/Title/01.mp3", "/lib/Author", true},
		{"nested sibling", "/lib/Author2/Title/01.mp3", "/lib/Author", false},
		{"case differs", "/LIB/a.m4b", "/lib", false},
		{"drive root forward slash, child", "C:/lib/a.m4b", "C:/lib", true},
		{"drive root forward slash, sibling", "C:/lib2/a.m4b", "C:/lib", false},
		{"drive root backslash, child", `C:\lib\a.m4b`, `C:\lib`, true},
		{"drive root backslash, sibling", `C:\lib2\a.m4b`, `C:\lib`, false},
		{"bare drive root with separator", `C:\lib\a.m4b`, `C:\`, true},
		{"bare drive letter without separator", "C:/lib/a.m4b", "C:", true},
		{"other drive", "D:/lib/a.m4b", "C:/lib", false},
		{"file URL root", "file://localhost/C:/lib/a%20b.m4b", "file://localhost/C:/lib", true},
		{"file URL sibling", "file://localhost/C:/lib2/a.m4b", "file://localhost/C:/lib", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsWithin(tt.path, tt.root); got != tt.want {
				t.Fatalf("IsWithin(%q, %q) = %v, want %v", tt.path, tt.root, got, tt.want)
			}
		})
	}
}

// TestIsWithin_AgreesWithHasPrefixInsideRoot pins the byte-identity promise:
// wherever the path really is inside the root, IsWithin gives the same answer
// a bare strings.HasPrefix gave, so only siblings change.
func TestIsWithin_AgreesWithHasPrefixInsideRoot(t *testing.T) {
	roots := []string{"/lib", "/lib/", "/", "C:/lib", `C:\lib`, "/x/lib"}
	suffixes := []string{"", "/a.m4b", "/Author/Title/01.mp3", `\a.m4b`}
	for _, root := range roots {
		for _, suf := range suffixes {
			p := root + suf
			if strings.HasSuffix(root, "/") || strings.HasSuffix(root, `\`) {
				p = root + strings.TrimLeft(suf, `/\`)
			}
			if !strings.HasPrefix(p, root) {
				t.Fatalf("fixture %q is not under %q", p, root)
			}
			if !IsWithin(p, root) {
				t.Errorf("IsWithin(%q, %q) = false; HasPrefix said true and the path is inside", p, root)
			}
		}
	}
}
