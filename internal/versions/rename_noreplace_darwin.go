// file: internal/versions/rename_noreplace_darwin.go
// version: 1.0.0
// guid: 9e4a1d73-5b20-4c68-a3f9-1c7e8b2d6054
// last-edited: 2026-10-28

//go:build darwin

package versions

import (
	"os"

	"golang.org/x/sys/unix"
)

// renameNoReplace renames oldPath to newPath with renamex_np(RENAME_EXCL): the
// existence check and the rename are one atomic step. A filesystem that does
// not take the flag (ENOTSUP, EINVAL: some network mounts) falls back to
// renameChecked, which has a time-of-check/time-of-use window (see there).
func renameNoReplace(oldPath, newPath string) error {
	for {
		err := unix.RenamexNp(oldPath, newPath, unix.RENAME_EXCL)
		switch {
		case err == nil:
			return nil
		case err == unix.EINTR:
			continue
		case err == unix.EINVAL || err == unix.ENOSYS || err == unix.ENOTSUP || err == unix.EOPNOTSUPP:
			return renameChecked(oldPath, newPath)
		}
		return &os.LinkError{Op: "rename", Old: oldPath, New: newPath, Err: err}
	}
}
