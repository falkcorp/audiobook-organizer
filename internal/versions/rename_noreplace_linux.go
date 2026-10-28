// file: internal/versions/rename_noreplace_linux.go
// version: 1.0.0
// guid: 2b8e6c14-9d37-4a52-8f0e-6a1d4c7b3e95
// last-edited: 2026-10-28

//go:build linux

package versions

import (
	"os"

	"golang.org/x/sys/unix"
)

// renameNoReplace renames oldPath to newPath with renameat2(RENAME_NOREPLACE):
// the existence check and the rename are one atomic step. A kernel or
// filesystem that does not take the flag (ENOSYS, EINVAL, ENOTSUP) falls back
// to renameChecked, which has a time-of-check/time-of-use window (see there).
func renameNoReplace(oldPath, newPath string) error {
	for {
		err := unix.Renameat2(unix.AT_FDCWD, oldPath, unix.AT_FDCWD, newPath, unix.RENAME_NOREPLACE)
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
