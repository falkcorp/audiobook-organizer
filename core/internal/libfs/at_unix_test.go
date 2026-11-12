// file: core/internal/libfs/at_unix_test.go
// version: 1.0.0
// guid: f659f83a-171b-4adc-996b-b61e175f5807
// last-edited: 2026-11-11

//go:build unix

package libfs

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// The *At operations address one name relative to the directory descriptor. A
// symlink swapped into that name is not followed, so what they create, open or
// remove is in the directory the descriptor is, whatever the names became.
func TestOpenFileAtCreatesExclusivelyAndNeverFollowsALink(t *testing.T) {
	root, dir := openRoot(t)
	d := openDir(t, root, ".")
	f, err := OpenFileAt(d, "a.m4b", os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o640)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("the bytes"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if got := read(t, filepath.Join(dir, "a.m4b")); got != "the bytes" {
		t.Errorf("file holds %q", got)
	}
	if _, err := OpenFileAt(d, "a.m4b", os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o640); !errors.Is(err, fs.ErrExist) {
		t.Errorf("a second exclusive create = %v, want ErrExist", err)
	}
	// A link in the name is refused, for a write as for a create.
	if err := os.Symlink("a.m4b", filepath.Join(dir, "link.m4b")); err != nil {
		t.Fatal(err)
	}
	for _, flag := range []int{os.O_WRONLY, os.O_RDWR | os.O_CREATE} {
		if f, err := OpenFileAt(d, "link.m4b", flag, 0o640); !errors.Is(err, syscall.ELOOP) {
			if err == nil {
				_ = f.Close()
			}
			t.Errorf("open of a symlink with flag %#x = %v, want ELOOP", flag, err)
		}
	}
	if got := read(t, filepath.Join(dir, "a.m4b")); got != "the bytes" {
		t.Errorf("the target changed through the link: %q", got)
	}
}

func TestMkdirAtRemoveAtSymlinkAtIdentityAt(t *testing.T) {
	root, dir := openRoot(t)
	d := openDir(t, root, ".")
	if err := MkdirAt(d, "Gotham", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := MkdirAt(d, "Gotham", 0o755); !errors.Is(err, fs.ErrExist) {
		t.Errorf("MkdirAt of an existing name = %v, want ErrExist", err)
	}
	id, isDir, err := IdentityAt(d, "Gotham")
	st, serr := os.Stat(filepath.Join(dir, "Gotham"))
	if err != nil || serr != nil || !isDir {
		t.Fatalf("IdentityAt = %v, %v, %v (%v)", id, isDir, err, serr)
	}
	if dev, ino, ok := InodeOf(st); !ok || (ID{Dev: dev, Ino: ino}) != id {
		t.Errorf("IdentityAt %v is not the directory's inode %v %v", id, dev, ino)
	}
	if err := SymlinkAt("Gotham", d, "Arkham"); err != nil {
		t.Fatal(err)
	}
	if err := SymlinkAt("Gotham", d, "Arkham"); !errors.Is(err, fs.ErrExist) {
		t.Errorf("SymlinkAt of an existing name = %v, want ErrExist", err)
	}
	// IdentityAt does not follow: the link has its own inode and is no directory.
	lid, lisDir, err := IdentityAt(d, "Arkham")
	if err != nil || lisDir || lid == id {
		t.Errorf("IdentityAt of a link = %v, %v, %v", lid, lisDir, err)
	}
	if _, _, err := IdentityAt(d, "nowhere"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("IdentityAt of a missing name = %v, want ErrNotExist", err)
	}
	write(t, filepath.Join(dir, "Gotham", "x"), "x")
	if err := RemoveAt(d, "Gotham"); err == nil {
		t.Error("RemoveAt of a non-empty directory succeeded")
	}
	if err := RemoveAt(d, "Arkham"); err != nil {
		t.Errorf("RemoveAt of a link = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "Gotham", "x")); err != nil {
		t.Errorf("RemoveAt of a link removed the target's content: %v", err)
	}
	if err := os.Remove(filepath.Join(dir, "Gotham", "x")); err != nil {
		t.Fatal(err)
	}
	if err := RemoveAt(d, "Gotham"); err != nil {
		t.Errorf("RemoveAt of an empty directory = %v", err)
	}
	write(t, filepath.Join(dir, "f"), "f")
	if err := RemoveAt(d, "f"); err != nil {
		t.Errorf("RemoveAt of a file = %v", err)
	}
	if err := RemoveAt(d, "f"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("RemoveAt of a missing name = %v, want ErrNotExist", err)
	}
	for _, bad := range []string{"", "a/b", ".."} {
		if _, err := OpenFileAt(d, bad, os.O_RDONLY, 0); err == nil {
			t.Errorf("OpenFileAt accepted %q", bad)
		}
		if err := MkdirAt(d, bad, 0o755); err == nil {
			t.Errorf("MkdirAt accepted %q", bad)
		}
	}
}
