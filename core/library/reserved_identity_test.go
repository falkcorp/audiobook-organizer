// file: core/library/reserved_identity_test.go
// version: 1.0.0
// guid: 4e5aadd7-d117-487a-a72a-c5113d1fbce7
// last-edited: 2026-11-11

package library

import (
	"errors"
	"io/fs"
	"os"
	"testing"

	"github.com/falkcorp/mediacore/internal/libfs"
)

// aliasOf makes a directory named alias report the device and inode of the real
// directory at target: the second spelling of one directory that a
// case-insensitive volume or a bind mount produces and a case-sensitive CI
// filesystem cannot.
func aliasOf(t *testing.T, alias, target string) func(fs.FileInfo) (uint64, uint64, bool) {
	t.Helper()
	st, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	dev, ino, ok := libfs.InodeOf(st)
	if !ok {
		t.Skip("no inode numbers on this platform")
	}
	return func(i fs.FileInfo) (uint64, uint64, bool) {
		if i.Name() == alias {
			return dev, ino, true
		}
		return libfs.InodeOf(i)
	}
}

// A name that is not the area's spelling but is the same directory by device and
// inode is the area: the by-identity half of the reserved test, exercised with a
// faked second name.
func TestReservedByIdentityWithASecondName(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(dir+"/"+DefaultAppDir, 0o755); err != nil {
		t.Fatal(err)
	}
	r := newRig(t, newStore(t), rigOpts{seams: seams{inodeOf: aliasOf(t, "Mask", dir+"/"+DefaultAppDir)}},
		func() RootSpec { s := spec("books-library", books, RoleLibrary); s.Path = dir; return s }())
	for _, d := range []string{"Mask", "Plain"} {
		if err := os.Mkdir(r.abs("books-library", d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	root, err := r.m.Root("books-library")
	if err != nil {
		t.Fatal(err)
	}
	l := root.base
	if !l.reserved("Mask/x.json", false) || !l.reserved("Mask", false) {
		t.Error("a directory with the area's identity is not reserved")
	}
	if l.reserved("Plain/x.json", false) {
		t.Error("an ordinary directory is reserved")
	}
	fsys, err := root.FS(books)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fsys.CreateExclusive("Mask/planted.m4b", 0o644); !errors.Is(err, ErrReservedPath) {
		t.Errorf("create through the second name = %v, want ErrReservedPath", err)
	}
	if !r.exists("books-library", "Plain") {
		t.Fatal("setup")
	}
	if f, err := fsys.CreateExclusive("Plain/fine.m4b", 0o644); err != nil {
		t.Errorf("create in an ordinary directory = %v", err)
	} else if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

// A directory that holds the area (above it, inside the root) is carried along by
// a Move, so a second name for it is refused for a rename but not for a read:
// the carries half.
func TestReservedByIdentityOfADirectoryHoldingTheArea(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(dir+"/state/app", 0o755); err != nil {
		t.Fatal(err)
	}
	s := spec("books-library", books, RoleLibrary)
	s.Path, s.AppDir = dir, "state/app"
	r := newRig(t, newStore(t), rigOpts{seams: seams{inodeOf: aliasOf(t, "Holder", dir+"/state")}}, s)
	if err := os.Mkdir(r.abs("books-library", "Holder"), 0o755); err != nil {
		t.Fatal(err)
	}
	root, err := r.m.Root("books-library")
	if err != nil {
		t.Fatal(err)
	}
	l := root.base
	if !l.reserved("Holder", true) {
		t.Error("a second name for the directory that holds the area is not refused for a Move")
	}
	if l.reserved("Holder", false) {
		t.Error("a second name for the directory that holds the area is refused for a read")
	}
	fsys, err := root.FS(books)
	if err != nil {
		t.Fatal(err)
	}
	if err := fsys.RenameNoReplace("Holder", "Carried"); !errors.Is(err, ErrReservedPath) {
		t.Errorf("rename of the second name = %v, want ErrReservedPath", err)
	}
	if !r.exists("books-library", "Holder") || r.exists("books-library", "Carried") {
		t.Error("the directory moved")
	}
}
