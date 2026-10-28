// file: internal/versions/rename_noreplace.go
// version: 1.0.0
// guid: 7d2f4a91-3c68-4e05-b1a7-5e9c0d3f8a26
// last-edited: 2026-10-28

package versions

import (
	"errors"
	"io/fs"
	"os"
)

// renameNoReplace is os.Rename that never replaces an existing destination: if
// newPath is there, in any form (a dangling link included), it fails with an
// error satisfying errors.Is(err, fs.ErrExist) and neither path is touched.
//
// os.Rename silently replaces a destination file on Unix, so a check followed
// by a plain rename leaves a window in which a file that appears at newPath is
// destroyed. The swap's moves, and the undo of them, must not clobber anything
// they did not put there. Where the kernel can do the check and the rename as
// one step it does (renameat2 RENAME_NOREPLACE on Linux, renamex_np RENAME_EXCL
// on macOS: rename_noreplace_linux.go, rename_noreplace_darwin.go); everywhere
// else, and on a filesystem that does not support the flag, renameChecked is
// used.

// renameChecked is the fallback: Lstat the destination, then os.Rename.
//
// It has a time-of-check/time-of-use window: a file created at newPath between
// the Lstat and the rename is replaced. The window is a few microseconds and
// needs another writer to create that very name in the same folder at that
// moment; it is accepted only where the atomic form is not available.
func renameChecked(oldPath, newPath string) error {
	if _, err := os.Lstat(newPath); err == nil {
		return &os.LinkError{Op: "rename", Old: oldPath, New: newPath, Err: fs.ErrExist}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return os.Rename(oldPath, newPath)
}
