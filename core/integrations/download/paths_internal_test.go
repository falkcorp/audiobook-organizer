// file: core/integrations/download/paths_internal_test.go
// version: 1.0.0
// guid: 3e9a5c71-0d84-4b26-a1f7-c85d2b6e4f90
// last-edited: 2026-11-11

package download

import "testing"

// The host-side guard of ToLocal, driven with both separators so the Windows
// host rules are exercised on any machine, and with segments parseRemote
// would already have refused (the guard must hold on its own).
func TestJoinUnder_RefusesWhatLeavesTheRoot(t *testing.T) {
	for _, tc := range []struct {
		name string
		root string
		rest []string
		sep  rune
		ok   bool
	}{
		{"plain names", "/srv/library", []string{"Hush", "a.m4b"}, '/', true},
		{"names with dots inside", "/srv/library", []string{"Hush. Year One", "a..b"}, '/', true},
		{"a backslash is a name on a POSIX host", "/srv/library", []string{`Hush\Year`}, '/', true},
		{"dot-dot", "/srv/library", []string{"..", "etc"}, '/', false},
		{"dot", "/srv/library", []string{"."}, '/', false},
		{"empty", "/srv/library", []string{""}, '/', false},
		{"NUL", "/srv/library", []string{"a\x00b"}, '/', false},
		{"a slash inside a segment", "/srv/library", []string{"a/../../x"}, '/', false},
		{"a backslash is a separator on a Windows host", `C:\srv\library`, []string{`a\..\..\x`}, '\\', false},
		{"a drive switch on a Windows host", `C:\srv\library`, []string{"D:"}, '\\', false},
		{"a colon in a name on a Windows host", `C:\srv\library`, []string{"Hush:Year"}, '\\', false},
		{"a slash inside a segment on a Windows host", `C:\srv\library`, []string{"a/b"}, '\\', false},
	} {
		got, ok := joinUnder(tc.root, tc.rest, tc.sep)
		if ok != tc.ok {
			t.Errorf("%s: joinUnder(%q, %q) = %q, %v; want ok=%v", tc.name, tc.root, tc.rest, got, ok, tc.ok)
		}
	}
}
