// file: core/internal/libfs/ops_other.go
// version: 1.2.0
// guid: a83c203b-ea72-45f4-acb5-0a216009d1da
// last-edited: 2026-11-11

//go:build !unix

package libfs

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// This file keeps the package compiling and usable on platforms without
// directory-relative syscalls (Windows). Names resolve against the directory's
// path instead of its descriptor, so the confinement the unix build gets from
// the descriptor is the caller's here; core targets unix first (13 §13.2) and
// 05-library 5L.3.2 names MoveFileEx for the Windows no-replace rename, which
// is the follow-up this fallback stands in for.

// Link makes newName in nd a hard link to oldName in od. An existing newName is
// fs.ErrExist.
func Link(od *Dir, oldName string, nd *Dir, newName string) error {
	if err := errors.Join(require(od, nd), checkName(oldName), checkName(newName)); err != nil {
		return err
	}
	return os.Link(filepath.Join(od.f.Name(), oldName), filepath.Join(nd.f.Name(), newName))
}

// RenameNoReplace is the checked rename: Lstat the destination, then rename. It
// has a time-of-check/time-of-use window.
func RenameNoReplace(od *Dir, oldName string, nd *Dir, newName string) error {
	if err := errors.Join(require(od, nd), checkName(oldName), checkName(newName)); err != nil {
		return err
	}
	return RenameNoReplaceFallback(od, oldName, nd, newName)
}

// RenameNoReplaceFallback is the checked rename.
func RenameNoReplaceFallback(od *Dir, oldName string, nd *Dir, newName string) error {
	oldPath, newPath := filepath.Join(od.f.Name(), oldName), filepath.Join(nd.f.Name(), newName)
	if _, err := os.Lstat(newPath); err == nil {
		return &os.LinkError{Op: "rename", Old: oldPath, New: newPath, Err: fs.ErrExist}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return os.Rename(oldPath, newPath)
}

// SyncDir is a no-op: Windows has no directory fsync.
func SyncDir(d *Dir) error { return require(d) }

// Ancestry is not available without directory descriptors: the caller must
// treat the answer as unknown.
func Ancestry(*os.File, ID) ([]ID, error) {
	return nil, errors.Join(errors.ErrUnsupported, errors.New("libfs: no directory-descriptor walk on this platform"))
}

// OpenAt is not available without directory descriptors.
func OpenAt(*Dir, string, int) (*os.File, error) {
	return nil, errors.Join(errors.ErrUnsupported, errors.New("libfs: no directory-relative open on this platform"))
}

// errNoHandles is what the descriptor-relative operations answer where there are
// no descriptors: they refuse, so a caller never acts on a path it cannot pin.
var errNoHandles = errors.Join(errors.ErrUnsupported, errors.New("libfs: no directory-relative operations on this platform"))

// OpenFileAt is not available without directory descriptors.
func OpenFileAt(*Dir, string, int, fs.FileMode) (*os.File, error) { return nil, errNoHandles }

// MkdirAt is not available without directory descriptors.
func MkdirAt(*Dir, string, fs.FileMode) error { return errNoHandles }

// RemoveAt is not available without directory descriptors.
func RemoveAt(*Dir, string) error { return errNoHandles }

// SymlinkAt is not available without directory descriptors.
func SymlinkAt(string, *Dir, string) error { return errNoHandles }

// IdentityAt is not available without directory descriptors.
func IdentityAt(*Dir, string) (ID, bool, error) { return ID{}, false, errNoHandles }

// InodeOf reports no inode.
func InodeOf(fs.FileInfo) (dev, ino uint64, ok bool) { return 0, 0, false }

// SameFile reports whether a and b are one file.
func SameFile(a, b fs.FileInfo) bool { return os.SameFile(a, b) }
