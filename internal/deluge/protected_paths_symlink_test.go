// file: internal/deluge/protected_paths_symlink_test.go
// version: 1.0.0
// guid: 3b7e9d41-5c28-4f60-a1d3-8e4f2b6c9a05
// last-edited: 2026-10-30

package deluge

import (
	"os"
	"path/filepath"
	"testing"
)

// symlinkTree is a real directory tree under t.TempDir with a protected folder
// ("vault"), a free folder ("free") and the helpers to link between them.
type symlinkTree struct {
	t     *testing.T
	root  string
	vault string // the protected folder, a real directory
	free  string // an unprotected folder, a real directory
}

func newSymlinkTree(t *testing.T) *symlinkTree {
	t.Helper()
	root := t.TempDir()
	// TempDir may itself sit behind a link (macOS /var -> /private/var); the
	// tests want the spelled paths to be what they build, so keep them as is.
	st := &symlinkTree{t: t, root: root, vault: filepath.Join(root, "vault"), free: filepath.Join(root, "free")}
	for _, d := range []string{st.vault, st.free} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return st
}

func (st *symlinkTree) write(p string) string {
	st.t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		st.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		st.t.Fatal(err)
	}
	return p
}

func (st *symlinkTree) link(target, name string) string {
	st.t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		st.t.Fatal(err)
	}
	if err := os.Symlink(target, name); err != nil {
		st.t.Skipf("symlinks unavailable: %v", err)
	}
	return name
}

// staticCache protects st.vault through config.ProtectedPaths.
func (st *symlinkTree) staticCache() *ProtectedPathCache {
	return NewProtectedPathCache(nil, []string{st.vault})
}

// delugeCache protects st.vault as a torrent's save_path.
func (st *symlinkTree) delugeCache() *ProtectedPathCache {
	return newTestCache(func() (map[string]TorrentStatus, error) {
		return map[string]TorrentStatus{"aa": {SavePath: st.vault}}, nil
	}, nil)
}

// each runs fn against a cache whose only protected folder is st.vault, once as
// a static prefix and once as a Deluge save_path, with the source it must name.
func (st *symlinkTree) each(fn func(t *testing.T, c *ProtectedPathCache, source string)) {
	st.t.Run("static", func(t *testing.T) { fn(t, st.staticCache(), ProtectedByStatic) })
	st.t.Run("deluge", func(t *testing.T) { fn(t, st.delugeCache(), ProtectedByDeluge) })
}

func TestProtectedBy_SymlinkIntoProtectedTreeIsProtected(t *testing.T) {
	st := newSymlinkTree(t)
	real := st.write(filepath.Join(st.vault, "Year Zero", "book.m4b"))
	link := st.link(real, filepath.Join(st.free, "book-link.m4b"))
	st.each(func(t *testing.T, c *ProtectedPathCache, source string) {
		if got := c.ProtectedBy(link); got != source {
			t.Errorf("ProtectedBy(%q) = %q, want %q", link, got, source)
		}
		if got := c.ProtectedBy(real); got != source {
			t.Errorf("ProtectedBy(%q) = %q, want %q (the target itself)", real, got, source)
		}
	})
}

func TestProtectedBy_SymlinkedParentIsProtected(t *testing.T) {
	st := newSymlinkTree(t)
	st.write(filepath.Join(st.vault, "Court of Owls", "book.m4b"))
	linkDir := st.link(st.vault, filepath.Join(st.free, "shortcut"))
	st.each(func(t *testing.T, c *ProtectedPathCache, source string) {
		for _, p := range []string{
			filepath.Join(linkDir, "Court of Owls", "book.m4b"), // exists
			filepath.Join(linkDir, "Court of Owls", "new.m4b"),  // file does not exist yet
			filepath.Join(linkDir, "No Such Dir", "new.m4b"),    // nor does the folder
		} {
			if got := c.ProtectedBy(p); got != source {
				t.Errorf("ProtectedBy(%q) = %q, want %q", p, got, source)
			}
		}
	})
}

// The protected folder itself is configured through a link: the real folder
// is the same place.
func TestProtectedBy_ProtectedFolderConfiguredThroughALink(t *testing.T) {
	st := newSymlinkTree(t)
	real := st.write(filepath.Join(st.vault, "Hush", "book.m4b"))
	alias := st.link(st.vault, filepath.Join(st.free, "vault-alias"))
	for name, c := range map[string]*ProtectedPathCache{
		"static": NewProtectedPathCache(nil, []string{alias}),
		"deluge": newTestCache(func() (map[string]TorrentStatus, error) {
			return map[string]TorrentStatus{"aa": {SavePath: alias}}, nil
		}, nil),
	} {
		t.Run(name, func(t *testing.T) {
			if !c.IsProtected(real) {
				t.Errorf("IsProtected(%q) = false: the real path of a folder configured as %q", real, alias)
			}
			if !c.IsProtected(filepath.Join(alias, "Hush", "book.m4b")) {
				t.Errorf("IsProtected under the alias spelling = false")
			}
		})
	}
}

func TestProtectedBy_DanglingSymlinkIsProtected(t *testing.T) {
	st := newSymlinkTree(t)
	// A dangling link inside the protected folder: protected as spelled.
	inside := st.link(filepath.Join(st.root, "gone", "nowhere.m4b"), filepath.Join(st.vault, "dangling.m4b"))
	// A dangling link outside that points at a not-yet-existing file in it: only
	// following the link shows where it lands.
	outside := st.link(filepath.Join(st.vault, "not-yet.m4b"), filepath.Join(st.free, "dangling-in.m4b"))
	// A dangling link outside that points elsewhere: nothing protects it.
	elsewhere := st.link(filepath.Join(st.root, "gone", "other.m4b"), filepath.Join(st.free, "dangling-out.m4b"))
	st.each(func(t *testing.T, c *ProtectedPathCache, source string) {
		if got := c.ProtectedBy(inside); got != source {
			t.Errorf("dangling link inside the folder: ProtectedBy(%q) = %q, want %q", inside, got, source)
		}
		if got := c.ProtectedBy(outside); got != source {
			t.Errorf("dangling link into the folder: ProtectedBy(%q) = %q, want %q", outside, got, source)
		}
		if got := c.ProtectedBy(elsewhere); got != "" {
			t.Errorf("dangling link elsewhere: ProtectedBy(%q) = %q, want not protected", elsewhere, got)
		}
	})
}

func TestProtectedBy_FrozenITunesSegmentIsProtected(t *testing.T) {
	st := newSymlinkTree(t)
	frozen := st.write(filepath.Join(st.root, "mnt", "bigdata", "books", "itunes", "iTunes Media", "Audiobooks", "a.m4b"))
	frozenDir := filepath.Join(st.root, "mnt", "bigdata", "books", "itunes")
	viaLink := st.link(frozenDir, filepath.Join(st.free, "tunes"))
	// None of these has any configured prefix: the rule needs no configuration.
	c := NewProtectedPathCache(nil, nil)
	for _, p := range []string{
		frozen,
		filepath.Join(frozenDir, "never-created.m4b"),
		filepath.Join(viaLink, "iTunes Media", "Audiobooks", "a.m4b"), // only the resolved path says so
		"/mnt/bigdata/Books/iTunes/x.m4b",                             // any case
		"/x/audiobooks/itunes/x.m4b",                                  // a "books" suffix on the parent
	} {
		if got := c.ProtectedBy(p); got != ProtectedByStatic {
			t.Errorf("ProtectedBy(%q) = %q, want %q", p, got, ProtectedByStatic)
		}
	}
	for _, p := range []string{
		filepath.Join(st.free, "iTunes", "x.m4b"),                 // a folder merely named iTunes
		filepath.Join(st.root, "books", "itunes-backup", "x.m4b"), // a longer segment
	} {
		if got := c.ProtectedBy(p); got != "" {
			t.Errorf("ProtectedBy(%q) = %q, want not protected", p, got)
		}
	}
}

func TestProtectedBy_UnrelatedPathIsNotProtected(t *testing.T) {
	st := newSymlinkTree(t)
	plain := st.write(filepath.Join(st.free, "plain.m4b"))
	// A link between two unprotected places, and a sibling that shares a prefix
	// with the protected folder's name.
	link := st.link(plain, filepath.Join(st.free, "plain-link.m4b"))
	sibling := st.write(filepath.Join(st.root, "vault-old", "x.m4b"))
	st.each(func(t *testing.T, c *ProtectedPathCache, _ string) {
		for _, p := range []string{plain, link, sibling, filepath.Join(st.free, "not-created.m4b")} {
			if got := c.ProtectedBy(p); got != "" {
				t.Errorf("ProtectedBy(%q) = %q, want not protected", p, got)
			}
		}
	})
}

// A candidate whose links loop cannot be shown to be outside any folder, so it
// is protected rather than waved through.
func TestProtectedBy_UnresolvablePathFailsClosed(t *testing.T) {
	st := newSymlinkTree(t)
	a := filepath.Join(st.free, "loop-a")
	b := filepath.Join(st.free, "loop-b")
	st.link(b, a)
	st.link(a, b)
	st.each(func(t *testing.T, c *ProtectedPathCache, _ string) {
		if got := c.ProtectedBy(filepath.Join(a, "x.m4b")); got != ProtectedByStatic {
			t.Errorf("ProtectedBy(through a link loop) = %q, want %q", got, ProtectedByStatic)
		}
	})
}

// A protected folder that loops cannot be resolved; it keeps the protection of
// its spelled form instead of closing every other path.
func TestProtectedBy_UnresolvableFolderKeepsSpelledProtection(t *testing.T) {
	st := newSymlinkTree(t)
	a := filepath.Join(st.root, "loop-a")
	b := filepath.Join(st.root, "loop-b")
	st.link(b, a)
	st.link(a, b)
	c := NewProtectedPathCache(nil, []string{a})
	if !c.IsProtected(filepath.Join(a, "x.m4b")) {
		t.Error("a path under the looping folder as spelled is not protected")
	}
	if c.IsProtected(filepath.Join(st.free, "y.m4b")) {
		t.Error("one unresolvable folder closed an unrelated path")
	}
}

// The resolved form of a static prefix is rebuilt on each refresh, so a link
// re-pointed in front of it is seen once the TTL ends (Invalidate here).
func TestProtectedBy_RepointedLinkSeenAfterRefresh(t *testing.T) {
	st := newSymlinkTree(t)
	other := filepath.Join(st.root, "other")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	alias := st.link(other, filepath.Join(st.root, "alias"))
	c := NewProtectedPathCache(nil, []string{alias})
	probe := filepath.Join(st.vault, "x.m4b")
	if c.IsProtected(probe) {
		t.Fatalf("%q protected before the link points at the vault", probe)
	}
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	st.link(st.vault, alias)
	c.Invalidate()
	if !c.IsProtected(probe) {
		t.Errorf("%q not protected after the link was re-pointed at the vault and the cache refreshed", probe)
	}
}
