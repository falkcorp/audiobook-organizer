// file: internal/merge/itunes_guard_symlink_test.go
// version: 1.1.0
// guid: 9f1d4b62-2e7a-4c85-b3f0-6a8c1e5d7b24
// last-edited: 2026-09-13

package merge

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// A path outside books/itunes/ that reaches it through a symlink is the same
// files, and is refused -- including a target that does not exist yet.
func TestCheckITunesPath_SymlinkIntoITunesTreeIsRefused(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "books", "itunes", "Author")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "lib"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "lib", "linked")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	for _, p := range []string{
		link,
		filepath.Join(link, "Book", "not-yet-written.m4b"),
	} {
		err := GuardITunesProtectedLoaded([]*database.Book{{ID: "b", FilePath: p}}, nil)
		if !errors.Is(err, ErrITunesProtected) {
			t.Errorf("path %s: err = %v, want ErrITunesProtected", p, err)
		}
	}

	plain := filepath.Join(dir, "lib", "plain", "book.m4b")
	if err := GuardITunesProtectedLoaded([]*database.Book{{ID: "b", FilePath: plain}}, nil); err != nil {
		t.Fatalf("plain path refused: %v", err)
	}
}

func mustSymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
}

// The configured root is itself a symlink, and a DANGLING symlink (its target
// not written yet) points into it through that link. EvalSymlinks reports
// only not-exist for a dangling link, so it resolved to itself and passed.
func TestCheckITunesPath_DanglingLinkIntoSymlinkedRootIsRefused(t *testing.T) {
	dir := t.TempDir()
	realRoot := filepath.Join(dir, "real-media")
	for _, d := range []string{realRoot, filepath.Join(dir, "lib")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	rootLink := filepath.Join(dir, "media-link")
	mustSymlink(t, realRoot, rootLink)
	withITunesConfig(t, func(c *config.ITunesConfig) {
		c.MediaRoot = rootLink
		c.LibraryReadPath = ""
		c.SyncEnabled = false
	})

	abs := filepath.Join(dir, "lib", "dangling.m4b")
	mustSymlink(t, filepath.Join(rootLink, "Missing", "book.m4b"), abs)
	rel := filepath.Join(dir, "lib", "relative.m4b")
	mustSymlink(t, filepath.Join("..", "media-link", "Missing", "other.m4b"), rel)

	for _, p := range []string{abs, rel} {
		err := GuardITunesProtectedLoaded([]*database.Book{{ID: "b", FilePath: p}}, nil)
		if !errors.Is(err, ErrITunesProtected) {
			t.Errorf("dangling link %s: err = %v, want ErrITunesProtected", p, err)
		}
	}
	plain := filepath.Join(dir, "lib", "plain.m4b")
	if err := GuardITunesProtectedLoaded([]*database.Book{{ID: "b", FilePath: plain}}, nil); err != nil {
		t.Fatalf("plain path refused: %v", err)
	}
}

// A dangling link into books/itunes/ is refused with no root configured.
func TestCheckITunesPath_DanglingLinkIntoFrozenTreeIsRefused(t *testing.T) {
	dir := t.TempDir()
	for _, d := range []string{filepath.Join(dir, "books", "itunes"), filepath.Join(dir, "lib")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(dir, "lib", "d.m4b")
	mustSymlink(t, filepath.Join(dir, "books", "itunes", "Author", "missing.m4b"), link)
	if err := GuardITunesProtectedLoaded([]*database.Book{{ID: "b", FilePath: link}}, nil); !errors.Is(err, ErrITunesProtected) {
		t.Fatalf("err = %v, want ErrITunesProtected", err)
	}
}
