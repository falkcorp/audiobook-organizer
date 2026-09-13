// file: internal/deluge/protected_paths_boundary_test.go
// version: 1.0.0
// guid: 9e3b1f72-6a0c-4d58-b2e4-7f1c8a5d3e90
// last-edited: 2026-09-12

package deluge

import "testing"

// A torrent saved under /dl/audiobooks protects that directory and what is in
// it, not a sibling directory whose name happens to start with it.
func TestIsProtected_SiblingOfSavePathIsNotProtected(t *testing.T) {
	lf := func() (map[string]TorrentStatus, error) {
		return map[string]TorrentStatus{"aa": {SavePath: "/dl/audiobooks"}}, nil
	}
	cache := newTestCache(lf, []string{"/keep/"})

	for path, want := range map[string]bool{
		"/dl/audiobooks":             true,
		"/dl/audiobooks/Book/a.m4b":  true,
		"/dl/audiobooks2/Book/a.m4b": false,
		"/dl/audiobooks-old/a.m4b":   false,
		"/keep/a.m4b":                true,
		"/keeper/a.m4b":              false,
		"":                           false,
	} {
		if got := cache.IsProtected(path); got != want {
			t.Errorf("IsProtected(%q) = %v, want %v", path, got, want)
		}
	}
}
