// file: internal/itunes/path_remap_boundary_test.go
// version: 1.0.0
// guid: 583ff3b9-f1ac-46f2-bd97-d16f63d50afc
// last-edited: 2026-09-12

package itunes

import "testing"

// RemapPath must only rewrite a path that is the mapping's From or lies under
// it on a separator boundary. A bare prefix match turned "/lib2/a.m4b" into
// "<To>2/a.m4b".
func TestRemapPath_SeparatorBoundary(t *testing.T) {
	one := func(from, to string) []PathMapping { return []PathMapping{{From: from, To: to}} }
	tests := []struct {
		name     string
		mappings []PathMapping
		input    string
		want     string
	}{
		{"posix sibling prefix unchanged", one("/lib", "/new"), "/lib2/a.m4b", "/lib2/a.m4b"},
		{"posix exact match", one("/lib", "/new"), "/lib", "/new"},
		{"posix child, From without trailing separator", one("/lib", "/new"), "/lib/a.m4b", "/new/a.m4b"},
		{"posix child, From with trailing separator", one("/lib/", "/new/"), "/lib/a.m4b", "/new/a.m4b"},
		{"posix sibling, From with trailing separator", one("/lib/", "/new/"), "/lib2/a.m4b", "/lib2/a.m4b"},
		{
			"sibling no longer shadows a later mapping",
			[]PathMapping{{From: "/lib", To: "/new"}, {From: "/lib2", To: "/other"}},
			"/lib2/a.m4b", "/other/a.m4b",
		},
		{"windows backslash child", one(`C:\Music\iTunes`, "/lib"), `C:\Music\iTunes\x.m4b`, "/lib/x.m4b"},
		{"windows backslash sibling unchanged", one(`C:\Music\iTunes`, "/lib"), `C:\Music\iTunes2\x.m4b`, `C:\Music\iTunes2\x.m4b`},
		{"windows From with trailing backslash", one(`C:\Music\iTunes\`, "/lib/"), `C:\Music\iTunes\x.m4b`, "/lib/x.m4b"},
		{"case-insensitive fallback on a boundary", one(`C:\Music\iTunes`, "/lib"), "c:/music/itunes/Sub/x.m4b", "/lib/Sub/x.m4b"},
		{"case-insensitive fallback not across a boundary", one(`C:\Music\iTunes`, "/lib"), "c:/music/itunes2/x.m4b", "c:/music/itunes2/x.m4b"},
		{"file:// From via plain fallback, on a boundary", one("file://localhost/C:/Music/iTunes", "/lib"), "C:/Music/iTunes/x.m4b", "/lib/x.m4b"},
		{"file:// From via plain fallback, sibling", one("file://localhost/C:/Music/iTunes", "/lib"), "C:/Music/iTunes2/x.m4b", "C:/Music/iTunes2/x.m4b"},
		{"url-encoded input child is decoded", one("C:/Music/iTunes", "/lib"), "file://localhost/C:/Music/iTunes/My%20Book/x.m4b", "/lib/My Book/x.m4b"},
		{"url-encoded input sibling is decoded, not remapped", one("C:/Music/iTunes", "/lib"), "file://localhost/C:/Music/iTunes%20Extra/x.m4b", "C:/Music/iTunes Extra/x.m4b"},
		{"url-encoded From child", one("file://localhost/C:/Music/iTunes%20Media", "/lib"), "file://localhost/C:/Music/iTunes%20Media/My%20Book.m4b", "/lib/My%20Book.m4b"},
		{"url-encoded From sibling", one("file://localhost/C:/Music/iTunes%20Media", "/lib"), "file://localhost/C:/Music/iTunes%20Media2/x.m4b", "C:/Music/iTunes Media2/x.m4b"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := &ImportOptions{PathMappings: tt.mappings}
			if got := opts.RemapPath(tt.input); got != tt.want {
				t.Fatalf("RemapPath(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestReverseRemapPath_SeparatorBoundary(t *testing.T) {
	one := func(from, to string) []PathMapping { return []PathMapping{{From: from, To: to}} }
	tests := []struct {
		name     string
		mappings []PathMapping
		input    string
		want     string
	}{
		{"sibling prefix unchanged", one("C:/Music/iTunes", "/lib"), "/lib2/a.m4b", "/lib2/a.m4b"},
		{"exact match", one("C:/Music/iTunes", "/lib"), "/lib", "C:/Music/iTunes"},
		{"child, To without trailing separator", one("C:/Music/iTunes", "/lib"), "/lib/a.m4b", "C:/Music/iTunes/a.m4b"},
		{"child, To with trailing separator", one("C:/Music/iTunes/", "/lib/"), "/lib/a.m4b", "C:/Music/iTunes/a.m4b"},
		{"backslash To and input, sibling unchanged", one("C:/Music/iTunes", `\lib`), `\lib2\a.m4b`, `\lib2\a.m4b`},
		{
			"sibling no longer shadows a later mapping",
			[]PathMapping{{From: "C:/A", To: "/lib"}, {From: "C:/B", To: "/lib2"}},
			"/lib2/a.m4b", "C:/B/a.m4b",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ReverseRemapPath(tt.input, tt.mappings); got != tt.want {
				t.Fatalf("ReverseRemapPath(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
