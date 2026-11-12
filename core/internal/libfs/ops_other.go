// file: core/internal/libfs/ops_other.go
// version: 1.3.0
// guid: a83c203b-ea72-45f4-acb5-0a216009d1da
// last-edited: 2026-11-11

//go:build !unix

package libfs

import (
	"errors"
	"io/fs"
	"os"
)

// This file keeps the package compiling on platforms without directory-relative
// syscalls (Windows). Everything that mutates through a directory, or names two
// of them, refuses with errors.ErrUnsupported: a path-based stand-in cannot be
// confined to the directory the caller proved, and the no-replace rename it
// would need (MoveFileEx without REPLACE_EXISTING, which 05-library 5L.3.2
// names) is the handle-based follow-up. Core targets unix first (13 section 13.2).

// HasHandles is false: the operations below that name a directory descriptor
// refuse, and with them every transfer.
const HasHandles = false

// Link is refused: a path-based link cannot be confined to the directory the
// caller proved, so nothing links here.
func Link(*Dir, string, *Dir, string) error { return errNoHandles }

// RenameNoReplace is refused: its stand-in, Lstat then os.Rename, replaces a
// file another writer created in between, and MoveFileEx without
// REPLACE_EXISTING (the follow-up 05-library 5L.3.2 names) is not wired yet.
func RenameNoReplace(*Dir, string, *Dir, string) error { return errNoHandles }

// RenameNoReplaceFallback is refused, like RenameNoReplace.
func RenameNoReplaceFallback(*Dir, string, *Dir, string) error { return errNoHandles }

// RenameOver is refused.
func RenameOver(*Dir, string, *Dir, string) error { return errNoHandles }

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
