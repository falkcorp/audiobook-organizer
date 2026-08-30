// file: internal/pathutil/appdirs_test.go
// version: 1.1.0
// guid: 7c1e4a90-58fd-42b7-9e06-1a3d8b5f0c24
// last-edited: 2026-08-29

package pathutil

import (
	"os"
	"path/filepath"
	"testing"
)

// The point of this whole file: every directory named here has NO leading dot.
//
// A test asserting `.backups` is skipped proves nothing -- the dot rule already
// skipped it. The regression being closed is that the protection was a naming
// coincidence: `backup_dir` is an operator-settable absolute path, and
// `openlibrary_dump_dir` defaults to `<root_dir>/openlibrary-dumps`, which
// never had a dot at all and was walked on every scan.
func TestShouldSkipDir_AppDirsSkippedWithoutAnyDot(t *testing.T) {
	root := "/srv/books"
	app := AppDirs{
		BackupDir:          "/srv/books/backups",
		OpenLibraryDumpDir: "/srv/books/openlibrary-dumps",
		PlaylistDir:        "/srv/books/playlists",
	}
	for _, tc := range []struct {
		path string
		want bool
		why  string
	}{
		{"/srv/books/backups", true, "backup_dir without a dot must be skipped"},
		{"/srv/books/backups/2026-08", true, "everything BENEATH an app dir must be skipped too"},
		{"/srv/books/backups/2026-08/db.tar.zst", true, "nested deeply under an app dir"},
		{"/srv/books/openlibrary-dumps", true, "the 39 GB dump dir that was being walked every scan"},
		{"/srv/books/openlibrary-dumps/pebble", true, "the embedded database nested inside it"},
		{"/srv/books/playlists", true, "playlist_dir without a dot"},
		{"/srv/books/abooks", false, "ordinary content must still be walked"},
		{"/srv/books", false, "the walk root is never skipped"},
	} {
		if got := ShouldSkipDir(root, tc.path, app); got != tc.want {
			t.Errorf("ShouldSkipDir(%q, %q) = %v, want %v -- %s", root, tc.path, got, tc.want, tc.why)
		}
	}
}

// A naive strings.HasPrefix would make "/srv/books/backups2" match
// "/srv/books/backups" and silently hide a real library folder. The match must
// be on path COMPONENTS.
func TestShouldSkipDir_SiblingPrefixIsNotSkipped(t *testing.T) {
	root := "/srv/books"
	app := AppDirs{BackupDir: "/srv/books/backups"}
	for _, p := range []string{
		"/srv/books/backups2",
		"/srv/books/backups2/inner",
		"/srv/books/backups-old",
		"/srv/books/backupsomething",
	} {
		if ShouldSkipDir(root, p, app) {
			t.Errorf("%q was skipped; only /srv/books/backups and its children are app-owned", p)
		}
	}
	// The real one is still skipped -- proving the test above is not passing
	// because the matcher is simply inert.
	if !ShouldSkipDir(root, "/srv/books/backups", app) {
		t.Error("the configured backup dir itself must be skipped")
	}
}

// THE most dangerous bug available here: filepath.Clean("") returns ".", which
// is a live relative prefix. If an unset setting were cleaned before being
// checked for emptiness, it could match everything and skip the whole library.
func TestShouldSkipDir_EmptySettingsNeverMatch(t *testing.T) {
	root := "/srv/books"
	empty := AppDirs{}
	for _, p := range []string{
		"/srv/books/abooks",
		"/srv/books/abooks/author/title",
		"/srv/books/backups",
		"/srv/books/openlibrary-dumps",
		".",
		"relative/path",
	} {
		if ShouldSkipDir(root, p, empty) {
			t.Errorf("%q was skipped with a ZERO AppDirs; an empty setting must never match a prefix", p)
		}
	}
	// A partially-configured AppDirs must not let its empty fields match either.
	partial := AppDirs{BackupDir: "/srv/books/backups"}
	if ShouldSkipDir(root, "/srv/books/abooks", partial) {
		t.Error("an empty OpenLibraryDumpDir/PlaylistDir matched ordinary content")
	}
	// Whitespace is not a configured path.
	blank := AppDirs{BackupDir: "   ", OpenLibraryDumpDir: "\t"}
	if ShouldSkipDir(root, "/srv/books/abooks", blank) {
		t.Error("a whitespace-only setting was treated as a real path")
	}
}

// A relative app dir cannot be compared against the absolute paths a walk
// yields. Resolving it against the process CWD could fabricate a match on a
// subtree nobody configured, so it is dropped: fail OPEN.
func TestShouldSkipDir_RelativeAppDirIsIgnored(t *testing.T) {
	root := "/srv/books"
	app := AppDirs{BackupDir: "backups"}
	if ShouldSkipDir(root, "/srv/books/backups", app) {
		t.Error("a relative backup dir matched an absolute path; it must be dropped, not guessed at")
	}
}

// A directory configured outside the library root is simply never reached.
// That is not an error and must not skip anything inside the root.
func TestShouldSkipDir_AppDirOutsideRootIsHarmless(t *testing.T) {
	root := "/srv/books"
	app := AppDirs{BackupDir: "/var/backups/audiobooks", OpenLibraryDumpDir: "/data/dumps"}
	for _, p := range []string{"/srv/books/abooks", "/srv/books/backups", "/srv/books/dumps"} {
		if ShouldSkipDir(root, p, app) {
			t.Errorf("%q was skipped because of an app dir living outside the root", p)
		}
	}
	// It still matches if a walk ever does reach it.
	if !ShouldSkipDir("/var", "/var/backups/audiobooks", app) {
		t.Error("an app dir must still be skipped when a walk actually reaches it")
	}
}

// A walk rooted AT an app dir must walk its whole tree, not just survive the
// first callback.
//
// Exempting only `path == root` buys nothing on its own: WalkDir's first
// callback succeeds and then every DESCENDANT is skipped, so a library laid
// out as author/title/ scans to zero books -- the same silent outcome as
// abandoning the walk, reached one callback later. The original version of
// this test asserted only the root itself and therefore passed while that hole
// was wide open, certifying a guarantee the code did not provide.
func TestShouldSkipDir_AppDirAsWalkRootWalksWholeTree(t *testing.T) {
	app := AppDirs{BackupDir: "/srv/books"}
	if ShouldSkipDir("/srv/books", "/srv/books", app) {
		t.Fatal("the walk root was skipped; this abandons the entire walk on the first callback")
	}
	if ShouldSkipDir("/srv/books/", "/srv/books", app) {
		t.Fatal("a trailing separator on the root made the walk skip its own root")
	}
	// THE ASSERTION THAT WAS MISSING: descendants of a walk root that is
	// itself an app dir must still be walked.
	for _, p := range []string{
		"/srv/books/Author",
		"/srv/books/Author/Title",
		"/srv/books/Author/Title/disc1",
	} {
		if ShouldSkipDir("/srv/books", p, app) {
			t.Errorf("%q was skipped; a walk rooted at an app dir must see its whole tree, not just its root", p)
		}
	}
}

// An app dir that is an ANCESTOR of the walk root must be ignored too. The
// scanner walks each enabled import path as its own root, so an import path
// added at or under an application directory would otherwise contribute
// nothing, silently.
func TestShouldSkipDir_AppDirAboveWalkRootIsIgnored(t *testing.T) {
	// backup_dir is an ancestor of the library root.
	app := AppDirs{BackupDir: "/srv"}
	for _, p := range []string{"/srv/books/Author", "/srv/books/Author/Title"} {
		if ShouldSkipDir("/srv/books", p, app) {
			t.Errorf("%q was skipped because an app dir sits ABOVE the walk root", p)
		}
	}
	// An import path rooted inside the dump dir walks in full.
	dumps := AppDirs{OpenLibraryDumpDir: "/srv/books/openlibrary-dumps"}
	if ShouldSkipDir("/srv/books/openlibrary-dumps", "/srv/books/openlibrary-dumps/sub", dumps) {
		t.Error("an import path rooted at the dump dir scanned nothing; the operator asked for that tree explicitly")
	}
	// But from the LIBRARY root, that same dir is still excluded -- proving
	// the ancestor rule is scoped to the walk, not a blanket disable.
	if !ShouldSkipDir("/srv/books", "/srv/books/openlibrary-dumps/sub", dumps) {
		t.Error("the dump dir must still be excluded from a walk rooted at the library root")
	}
	// A sibling app dir is unaffected by the root being under a different one.
	both := AppDirs{BackupDir: "/srv", OpenLibraryDumpDir: "/srv/books/openlibrary-dumps"}
	if !ShouldSkipDir("/srv/books", "/srv/books/openlibrary-dumps", both) {
		t.Error("an ancestor app dir must not disable the OTHER app dirs")
	}
}

// The app-dir rule beats the .alternates carve-out. The carve-out exists so
// deliberately dot-named CONTENT stays visible; it must not resurrect an
// app-owned tree that happens to sit at such a name.
func TestShouldSkipDir_AppDirBeatsAlternatesCarveOut(t *testing.T) {
	root := "/srv/books"
	app := AppDirs{BackupDir: "/srv/books/.alternates"}
	if !ShouldSkipDir(root, "/srv/books/.alternates", app) {
		t.Error("an app-owned directory was walked because it was named .alternates")
	}
	// Unconfigured, it is content again.
	if ShouldSkipDir(root, "/srv/books/.alternates", AppDirs{}) {
		t.Error(".alternates must stay visible when it is not an app dir")
	}
}

// End to end on a real tree with non-dot names, which is what the walkers do.
func TestShouldSkipDir_RealWalkSkipsAppDirsByPath(t *testing.T) {
	root := t.TempDir()
	mk := func(dir, file string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, dir, file), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	mk("abooks", "real.m4b")
	mk("backups", "db.tar.zst")
	mk("openlibrary-dumps", "editions.txt.gz")
	mk("openlibrary-dumps/pebble", "000123.sst")
	mk("playlists", "all.m3u")
	mk("backups2", "keepme.m4b") // sibling prefix: real content

	app := AppDirs{
		BackupDir:          filepath.Join(root, "backups"),
		OpenLibraryDumpDir: filepath.Join(root, "openlibrary-dumps"),
		PlaylistDir:        filepath.Join(root, "playlists"),
	}

	walk := func(app AppDirs) []string {
		t.Helper()
		var seen []string
		err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if ShouldSkipDir(root, path, app) {
					return filepath.SkipDir
				}
				return nil
			}
			rel, _ := filepath.Rel(root, path)
			seen = append(seen, rel)
			return nil
		})
		if err != nil {
			t.Fatalf("walk: %v", err)
		}
		return seen
	}

	got := walk(app)
	want := map[string]bool{
		filepath.Join("abooks", "real.m4b"):     true,
		filepath.Join("backups2", "keepme.m4b"): true,
	}
	if len(got) != len(want) {
		t.Fatalf("walk saw %v; want exactly %v -- app dirs must be invisible and backups2 must not be", got, want)
	}
	for _, g := range got {
		if !want[g] {
			t.Errorf("walk descended into an application directory: %s", g)
		}
	}

	// With a ZERO AppDirs the SAME tree must be walked in full: proof the
	// exclusion is driven by configuration and an empty setting changes
	// nothing. 6 files created above, none of them dot-named.
	if all := walk(AppDirs{}); len(all) != 6 {
		t.Fatalf("with empty AppDirs the walk saw %d files (%v); want all 6 -- an empty setting must not exclude anything", len(all), all)
	}
}
