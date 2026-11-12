// file: core/internal/libfs/fdwalk_unix_test.go
// version: 1.0.0
// guid: b2e19c47-3d85-4f06-a1c8-7e5d0a94f3b6
// last-edited: 2026-11-11

//go:build unix

package libfs

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func idOfPath(t *testing.T, p string) ID {
	t.Helper()
	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	dev, ino, ok := InodeOf(st)
	if !ok {
		t.Fatal("no inode")
	}
	return ID{dev, ino}
}

func openFile(t *testing.T, p string) *os.File {
	t.Helper()
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

// Ancestry lists the directory and each directory above it, nearest first, up
// to the stop, and says so about where the descriptor really is.
func TestAncestryWalksTheDescriptorUpToTheStop(t *testing.T) {
	_, dir := openRoot(t)
	deep := filepath.Join(dir, "Gotham", "Arkham", "Cells")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	stop := idOfPath(t, dir)
	got, err := Ancestry(openFile(t, deep), stop)
	if err != nil {
		t.Fatal(err)
	}
	want := []ID{idOfPath(t, deep), idOfPath(t, filepath.Join(dir, "Gotham", "Arkham")), idOfPath(t, filepath.Join(dir, "Gotham")), stop}
	if len(got) != len(want) {
		t.Fatalf("chain = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("chain[%d] = %v, want %v", i, got[i], want[i])
		}
	}
	// The stop itself: a one-element chain.
	if got, err := Ancestry(openFile(t, dir), stop); err != nil || len(got) != 1 || got[0] != stop {
		t.Errorf("Ancestry of the stop = %v, %v", got, err)
	}
}

// The walk follows where the descriptor is, not the name that opened it: a
// directory reached through a symlink reports the chain of the directory it
// really is.
func TestAncestryIgnoresTheNamesThatLedToTheDescriptor(t *testing.T) {
	_, dir := openRoot(t)
	real := filepath.Join(dir, "Real", "Inner")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "Real", "Inner"), filepath.Join(dir, "alias")); err != nil {
		t.Fatal(err)
	}
	got, err := Ancestry(openFile(t, filepath.Join(dir, "alias")), idOfPath(t, dir))
	if err != nil || len(got) != 3 || got[1] != idOfPath(t, filepath.Join(dir, "Real")) {
		t.Fatalf("chain through a symlink = %v, %v; want Inner, Real, root", got, err)
	}
}

// Every way the walk can fail is an error, never a partial chain.
func TestAncestryFailsClosed(t *testing.T) {
	_, dir := openRoot(t)
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "file.txt"), "x")
	// A stop that is not an ancestor: the walk reaches the top of the filesystem.
	other := t.TempDir()
	if got, err := Ancestry(openFile(t, sub), idOfPath(t, other)); !errors.Is(err, ErrNotUnder) || got != nil {
		t.Errorf("a stop that is not above = %v, %v; want ErrNotUnder and no chain", got, err)
	}
	// A directory moved out of the tree after it was opened.
	moved := filepath.Join(other, "moved")
	f := openFile(t, sub)
	if err := os.Rename(sub, moved); err != nil {
		t.Fatal(err)
	}
	if got, err := Ancestry(f, idOfPath(t, dir)); !errors.Is(err, ErrNotUnder) || got != nil {
		t.Errorf("a directory moved out of the tree = %v, %v; want ErrNotUnder", got, err)
	}
	// A file is not a directory.
	if got, err := Ancestry(openFile(t, filepath.Join(dir, "file.txt")), idOfPath(t, dir)); !errors.Is(err, syscall.ENOTDIR) || got != nil {
		t.Errorf("a file = %v, %v; want ENOTDIR", got, err)
	}
	// A closed descriptor.
	c := openFile(t, dir)
	_ = c.Close()
	if got, err := Ancestry(c, idOfPath(t, dir)); err == nil || got != nil {
		t.Errorf("a closed descriptor = %v, %v; want an error", got, err)
	}
}

// OpenAt opens one name in the directory the descriptor holds, and never
// follows a symlink in it.
func TestOpenAtRefusesASymlinkAndAnythingButOneName(t *testing.T) {
	r, dir := openRoot(t)
	write(t, filepath.Join(dir, "robin.txt"), "the boy wonder")
	if err := os.Mkdir(filepath.Join(dir, "cave"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("robin.txt", filepath.Join(dir, "link-to-file")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("cave", filepath.Join(dir, "link-to-dir")); err != nil {
		t.Fatal(err)
	}
	d := openDir(t, r, ".")
	f, err := OpenAt(d, "robin.txt", os.O_RDONLY)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	buf := make([]byte, 32)
	if n, _ := f.Read(buf); string(buf[:n]) != "the boy wonder" {
		t.Errorf("read %q", buf[:n])
	}
	if cf, err := OpenAt(d, "cave", os.O_RDONLY); err != nil {
		t.Errorf("OpenAt of a directory: %v", err)
	} else {
		_ = cf.Close()
	}
	for _, name := range []string{"link-to-file", "link-to-dir"} {
		if lf, err := OpenAt(d, name, os.O_RDONLY); !errors.Is(err, syscall.ELOOP) {
			if lf != nil {
				_ = lf.Close()
			}
			t.Errorf("OpenAt of the symlink %s = %v, want ELOOP", name, err)
		}
	}
	for _, bad := range []string{"", ".", "..", "a/b", "cave/../robin.txt"} {
		if bf, err := OpenAt(d, bad, os.O_RDONLY); !errors.Is(err, ErrBadName) {
			if bf != nil {
				_ = bf.Close()
			}
			t.Errorf("OpenAt(%q) = %v, want ErrBadName", bad, err)
		}
	}
	if _, err := OpenAt(d, "missing", os.O_RDONLY); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("OpenAt of a missing name = %v", err)
	}
	if _, err := OpenAt(nil, "robin.txt", os.O_RDONLY); err == nil {
		t.Error("OpenAt(nil) succeeded")
	}
}
