// file: internal/deluge/protected_paths_unreachable_test.go
// version: 1.0.0
// guid: 5f1c8e24-9b3a-4d67-a0e2-7c84b1d93f56
// last-edited: 2026-09-14

package deluge

import "testing"

// Deluge unreachable from startup. Before 2026-09-14 refresh returned early
// on the ListTorrents error without ever merging the static extraPaths, so
// the path list stayed nil and IsProtected answered false for everything --
// the iTunes library included. Static paths must be protected regardless, and
// Loaded must say the Deluge list is unknown.
func TestProtectedPathCache_DelugeUnreachable_StaticPathsStillProtected(t *testing.T) {
	client, err := New("http://deluge.invalid:8112", "deluge")
	if err != nil {
		t.Fatal(err)
	}
	c := NewProtectedPathCache(client, []string{"/music/iTunes"})

	if !c.IsProtected("/music/iTunes/iTunes Media/Book/a.m4b") {
		t.Error("static path not protected while Deluge is unreachable")
	}
	if got := c.ProtectedBy("/music/iTunes/a.m4b"); got != ProtectedByStatic {
		t.Errorf("ProtectedBy = %q, want %q", got, ProtectedByStatic)
	}
	if c.Loaded() {
		t.Error("Loaded() = true, but the Deluge list never loaded")
	}
	if c.IsProtected("/library/Author/Book/a.m4b") {
		t.Error("an unrelated path is reported protected")
	}
}

// No Deluge client configured: there is no list to load, so the cache is
// loaded immediately and serves the static paths.
func TestProtectedPathCache_NoClient_LoadedWithStaticPaths(t *testing.T) {
	c := NewProtectedPathCache(nil, []string{"/keep"})
	if !c.Loaded() {
		t.Error("Loaded() = false with no Deluge client")
	}
	if !c.IsProtected("/keep/a.m4b") {
		t.Error("static path not protected")
	}
}

// A Deluge save_path hit is reported as such, not as static.
func TestProtectedPathCache_ProtectedBy_Deluge(t *testing.T) {
	lf := func() (map[string]TorrentStatus, error) {
		return map[string]TorrentStatus{"aa": {SavePath: "/dl/audiobooks"}}, nil
	}
	c := newTestCache(lf, []string{"/keep"})
	if got := c.ProtectedBy("/dl/audiobooks/Book/a.m4b"); got != ProtectedByDeluge {
		t.Errorf("ProtectedBy = %q, want %q", got, ProtectedByDeluge)
	}
	if !c.Loaded() {
		t.Error("Loaded() = false after a successful list")
	}
}
