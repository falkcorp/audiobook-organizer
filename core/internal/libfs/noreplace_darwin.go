// file: core/internal/libfs/noreplace_darwin.go
// version: 1.0.0
// guid: d8a994ab-adf2-4d55-9204-c6539aa138cf
// last-edited: 2026-11-11

//go:build darwin

package libfs

import (
	"errors"
	"runtime"

	"golang.org/x/sys/unix"
)

// RenameNoReplace renames oldName in od to newName in nd with renameatx_np
// RENAME_EXCL: the existence check and the rename are one atomic step. An
// existing newName is fs.ErrExist and neither name changes. A filesystem that
// does not take the flag (some network mounts) falls back to
// RenameNoReplaceFallback; a rename across filesystems is ErrCrossDevice.
func RenameNoReplace(od *Dir, oldName string, nd *Dir, newName string) error {
	if err := errors.Join(require(od, nd), checkName(oldName), checkName(newName)); err != nil {
		return err
	}
	err := retry(func() error {
		return unix.RenameatxNp(fd(od), oldName, fd(nd), newName, unix.RENAME_EXCL)
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
