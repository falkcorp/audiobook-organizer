// file: core/internal/libfs/noreplace_linux.go
// version: 1.0.0
// guid: 0c3a5a2b-0f4e-4d1c-8a53-6f1d7e9b2c40
// last-edited: 2026-11-11

//go:build linux

package libfs

import (
	"errors"
	"runtime"

	"golang.org/x/sys/unix"
)

// RenameNoReplace renames oldName in od to newName in nd with renameat2
// RENAME_NOREPLACE: the existence check and the rename are one atomic step. An
// existing newName of any type (a dangling symlink included) is fs.ErrExist and
// neither name changes. A kernel or filesystem that does not take the flag
// falls back to RenameNoReplaceFallback; a rename across filesystems is
// ErrCrossDevice.
func RenameNoReplace(od *Dir, oldName string, nd *Dir, newName string) error {
	if err := errors.Join(require(od, nd), checkName(oldName), checkName(newName)); err != nil {
		return err
	}
	err := retry(func() error {
		return unix.Renameat2(fd(od), oldName, fd(nd), newName, unix.RENAME_NOREPLACE)
	})
	runtime.KeepAlive(od)
	runtime.KeepAlive(nd)
	switch {
	case err == nil:
		return nil
	case noReplaceUnsupported(err):
		return RenameNoReplaceFallback(od, oldName, nd, newName)
	}
	return mapRenameErr(oldName, newName, err)
}
