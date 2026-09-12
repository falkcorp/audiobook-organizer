// file: internal/itunes/service/remap_windows_boundary_test.go
// version: 1.0.0
// guid: ae380d06-a6ac-4652-a64a-4dd50c302ff0
// last-edited: 2026-09-12

package itunesservice

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/itunes"
)

// remapWindowsPath is the importer's copy of RemapPath's drive-letter
// fallback; it must honour the same separator boundary.
func TestRemapWindowsPath_SeparatorBoundary(t *testing.T) {
	one := func(from, to string) itunes.ImportOptions {
		return itunes.ImportOptions{PathMappings: []itunes.PathMapping{{From: from, To: to}}}
	}
	tests := []struct {
		name  string
		opts  itunes.ImportOptions
		input string
		want  string
	}{
		{"sibling prefix unchanged", one("C:/Music/iTunes", "/lib"), "C:/Music/iTunes2/x.m4b", "C:/Music/iTunes2/x.m4b"},
		{"exact match", one("C:/Music/iTunes", "/lib"), "C:/Music/iTunes", "/lib"},
		{"child, From without trailing separator", one("C:/Music/iTunes", "/lib"), "C:/Music/iTunes/x.m4b", "/lib/x.m4b"},
		{"child, From with trailing separator", one(`C:\Music\iTunes\`, "/lib/"), "C:/Music/iTunes/x.m4b", "/lib/x.m4b"},
		{"windows backslash child", one(`C:\Music\iTunes`, "/lib"), `C:\Music\iTunes\x.m4b`, "/lib/x.m4b"},
		{"windows backslash sibling unchanged", one(`C:\Music\iTunes`, "/lib"), `C:\Music\iTunes2\x.m4b`, `C:\Music\iTunes2\x.m4b`},
		{"file:// From, sibling unchanged", one("file://localhost/C:/Music/iTunes", "/lib"), "C:/Music/iTunes2/x.m4b", "C:/Music/iTunes2/x.m4b"},
		{"case-insensitive fallback on a boundary", one("C:/Music/iTunes", "/lib"), "c:/music/itunes/Sub/x.m4b", "/lib/Sub/x.m4b"},
		{"case-insensitive fallback not across a boundary", one("C:/Music/iTunes", "/lib"), "c:/music/itunes2/x.m4b", "c:/music/itunes2/x.m4b"},
		{
			"sibling no longer shadows a later mapping",
			itunes.ImportOptions{PathMappings: []itunes.PathMapping{{From: "C:/Music/iTunes", To: "/lib"}, {From: "C:/Music/iTunes2", To: "/other"}}},
			"C:/Music/iTunes2/x.m4b", "/other/x.m4b",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := remapWindowsPath(tt.input, tt.opts); got != tt.want {
				t.Fatalf("remapWindowsPath(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
