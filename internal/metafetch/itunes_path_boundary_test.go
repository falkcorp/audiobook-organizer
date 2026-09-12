// file: internal/metafetch/itunes_path_boundary_test.go
// version: 1.0.0
// guid: 97a68338-ac91-49eb-8baf-da896bf5aa31
// last-edited: 2026-09-12

package metafetch

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
)

// ComputeITunesPath is the write-back reverse remapper (local To -> iTunes
// From); it must only match To on a separator boundary.
func TestComputeITunesPath_SeparatorBoundary(t *testing.T) {
	one := func(from, to string) []config.ITunesPathMap { return []config.ITunesPathMap{{From: from, To: to}} }
	tests := []struct {
		name     string
		mappings []config.ITunesPathMap
		input    string
		want     string
	}{
		{"sibling prefix yields no iTunes path", one("C:/Music/iTunes", "/lib"), "/lib2/a.m4b", ""},
		{"exact match", one("C:/Music/iTunes", "/lib"), "/lib", "file://localhost/C:/Music/iTunes"},
		{"child, To without trailing separator", one("C:/Music/iTunes", "/lib"), "/lib/a.m4b", "file://localhost/C:/Music/iTunes/a.m4b"},
		{"child, To with trailing separator", one("C:/Music/iTunes/", "/lib/"), "/lib/a.m4b", "file://localhost/C:/Music/iTunes/a.m4b"},
		{"child is URL-encoded", one("C:/Music/iTunes", "/lib"), "/lib/My Book.m4b", "file://localhost/C:/Music/iTunes/My%20Book.m4b"},
		{
			"sibling no longer shadows a later mapping",
			[]config.ITunesPathMap{{From: "C:/A", To: "/lib"}, {From: "C:/B", To: "/lib2"}},
			"/lib2/a.m4b", "file://localhost/C:/B/a.m4b",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			orig := config.AppConfig
			t.Cleanup(func() { config.AppConfig = orig })
			config.AppConfig.ITunes.PathMappings = tt.mappings
			if got := ComputeITunesPath(tt.input); got != tt.want {
				t.Fatalf("ComputeITunesPath(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
