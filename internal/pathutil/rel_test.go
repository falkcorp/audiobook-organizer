// file: internal/pathutil/rel_test.go
// version: 1.0.0
// guid: 0a3450d4-3fdd-4bc0-be8f-e3b9ca68d8ce
// last-edited: 2026-10-11

package pathutil

import (
	"path/filepath"
	"testing"
)

func TestRelEscapesBase(t *testing.T) {
	sep := string(filepath.Separator)
	tests := []struct {
		rel  string
		want bool
	}{
		{"..", true},
		{".." + sep + "x", true},
		{".." + sep + ".." + sep + "x", true},
		{"..Extras", false},
		{"..Extras" + sep + "a.mp3", false},
		{"...", false},
		{".", false},
		{"a" + sep + "..b", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := RelEscapesBase(tt.rel); got != tt.want {
			t.Errorf("RelEscapesBase(%q) = %v, want %v", tt.rel, got, tt.want)
		}
	}
}

func TestIsUnderRoot(t *testing.T) {
	root := filepath.Join(string(filepath.Separator), "library")
	tests := []struct {
		name string
		root string
		path string
		want bool
	}{
		{"child", root, filepath.Join(root, "Author", "a.mp3"), true},
		{"dot-dot-named dir is inside", root, filepath.Join(root, "..Extras", "a.mp3"), true},
		{"root itself", root, root, false},
		{"parent", root, filepath.Dir(root), false},
		{"sibling sharing a prefix", root, root + "2" + string(filepath.Separator) + "a.mp3", false},
		{"climb out", root, filepath.Join(root, "..", "elsewhere", "a.mp3"), false},
		{"empty root", "", filepath.Join(root, "a.mp3"), false},
		{"empty path", root, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsUnderRoot(tt.root, tt.path); got != tt.want {
				t.Errorf("IsUnderRoot(%q, %q) = %v, want %v", tt.root, tt.path, got, tt.want)
			}
		})
	}
}
