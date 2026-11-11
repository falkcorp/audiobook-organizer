// file: core/internal/libfs/reflink_darwin.go
// version: 1.0.0
// guid: ab5b61be-a714-4991-808c-89a3f60b2f9a
// last-edited: 2026-11-11

//go:build darwin

package libfs

import (
	"errors"
	"fmt"
	"io/fs"
	"runtime"

	"golang.org/x/sys/unix"
)

// Reflink creates a copy-on-write clone of oldName (in od) at newName (in nd)
// with the APFS clonefileat(2) syscall. clonefile creates the destination
// itself and refuses an existing one with EEXIST, which is exactly the
// no-clobber contract; it copies the source's mode, so perm is not applied
// (macOS differs from Linux here, and ABO's reflink_darwin.go documents the
// same difference). A volume that cannot clone is ErrReflinkUnsupported.
func Reflink(od *Dir, oldName string, nd *Dir, newName string, _ fs.FileMode) error {
	if err := errors.Join(require(od, nd), checkName(oldName), checkName(newName)); err != nil {
		return err
	}
	err := retry(func() error {
		return unix.Clonefileat(fd(od), oldName, fd(nd), newName, unix.CLONE_NOFOLLOW)
	})
	runtime.KeepAlive(od)
	runtime.KeepAlive(nd)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, unix.EEXIST):
		return linkErr("reflink", oldName, newName, err)
	}
	return linkErr("reflink", oldName, newName, fmt.Errorf("%w: %w", ErrReflinkUnsupported, err))
}
