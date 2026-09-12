// file: internal/reconcile/itunes_path_boundary_test.go
// version: 1.0.0
// guid: b8db9817-68dc-47c0-8997-12a3c3c587b1
// last-edited: 2026-09-12

package reconcile

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
)

// TranslateITunesPath must only apply a mapping on a separator boundary. None
// of these locations use the W: drive, so the hard-coded fallback never fires.
func TestTranslateITunesPath_SeparatorBoundary(t *testing.T) {
	one := func(from, to string) []config.ITunesPathMap { return []config.ITunesPathMap{{From: from, To: to}} }
	tests := []struct {
		name     string
		mappings []config.ITunesPathMap
		location string
		want     string
	}{
		{"sibling prefix unchanged", one("C:/Music/iTunes", "/lib"), "file://localhost/C:/Music/iTunes2/x.m4b", "file://localhost/C:/Music/iTunes2/x.m4b"},
		{"exact match", one("C:/Music/iTunes", "/lib"), "file://localhost/C:/Music/iTunes", "/lib"},
		{"child, From without trailing separator", one("C:/Music/iTunes", "/lib"), "file://localhost/C:/Music/iTunes/x.m4b", "/lib/x.m4b"},
		{"child, From with trailing separator", one(`C:\Music\iTunes\`, "/lib/"), "file://localhost/C:/Music/iTunes/x.m4b", "/lib/x.m4b"},
		{"sibling, From with trailing separator", one(`C:\Music\iTunes\`, "/lib/"), "file://localhost/C:/Music/iTunes2/x.m4b", "file://localhost/C:/Music/iTunes2/x.m4b"},
		{"windows backslash From, child", one(`C:\Music\iTunes`, "/lib"), `file://localhost/C:\Music\iTunes\x.m4b`, "/lib/x.m4b"},
		{"windows backslash From, sibling", one(`C:\Music\iTunes`, "/lib"), `file://localhost/C:\Music\iTunes2\x.m4b`, `file://localhost/C:\Music\iTunes2\x.m4b`},
		{"leading-slash From, child", one("/C:/Music/iTunes", "/lib"), "file://localhost/C:/Music/iTunes/x.m4b", "/lib/x.m4b"},
		{"url-encoded child is decoded", one("C:/Music/iTunes", "/lib"), "file://localhost/C:/Music/iTunes/My%20Book/x.m4b", "/lib/My Book/x.m4b"},
		{"url-encoded sibling unchanged", one("C:/Music/iTunes", "/lib"), "file://localhost/C:/Music/iTunes%20Extra/x.m4b", "file://localhost/C:/Music/iTunes%20Extra/x.m4b"},
		{"From of / still maps every location", one("/", "/root/"), "file://localhost/C:/x.m4b", "/root/C:/x.m4b"},
		{
			"sibling no longer shadows a later mapping",
			[]config.ITunesPathMap{{From: "C:/Music/iTunes", To: "/lib"}, {From: "C:/Music/iTunes2", To: "/other"}},
			"file://localhost/C:/Music/iTunes2/x.m4b", "/other/x.m4b",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := TranslateITunesPath(tt.location, tt.mappings); got != tt.want {
				t.Fatalf("TranslateITunesPath(%q) = %q, want %q", tt.location, got, tt.want)
			}
		})
	}
}
