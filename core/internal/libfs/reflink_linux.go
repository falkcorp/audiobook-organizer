// file: core/internal/libfs/reflink_linux.go
// version: 1.0.1
// guid: 96757184-7c57-4075-899d-c9ef8d4bf05e
// last-edited: 2026-11-11

//go:build linux

package libfs

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"runtime"

	"golang.org/x/sys/unix"
)

// Reflink creates a copy-on-write clone of oldName (in od) at newName (in nd)
// with the FICLONE ioctl (btrfs, XFS with reflink=1, OCFS2, ZFS 2.2+ with
// block cloning). perm is the creation mode of the clone, under the umask: a
// reflink is an ingest primitive and takes no source mode.
//
// FICLONE needs an open destination, so newName is created with O_EXCL first:
// an existing name is fs.ErrExist and is never truncated. A failed clone
// removes the empty file it just created. A filesystem or device that cannot
// clone is ErrReflinkUnsupported, never a statement about the destination.
func Reflink(od *Dir, oldName string, nd *Dir, newName string, perm fs.FileMode) (err error) {
	if err := errors.Join(require(od, nd), checkName(oldName), checkName(newName)); err != nil {
		return err
	}
	var sfd int
	err = retry(func() (e error) {
		sfd, e = unix.Openat(fd(od), oldName, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		return e
	})
	if err != nil {
		runtime.KeepAlive(od)
		return linkErr("reflink", oldName, newName, err)
	}
	// The source descriptor is only read, but one that will not close is not
	// ignored: Reflink reports a complete clone or none, so a clone that went
	// through is taken back when this close then fails.
	defer func() {
		cerr := unix.Close(sfd)
		if cerr == nil {
			return
		}
		cerr = &os.PathError{Op: "close", Path: oldName, Err: cerr}
		if err != nil {
			err = joinErr(err, cerr)
			return
		}
		err = undoNew(nd, newName, linkErr("reflink", oldName, newName, cerr))
	}()
	var dfd int
	err = retry(func() (e error) {
		dfd, e = unix.Openat(fd(nd), newName, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, uint32(perm.Perm()))
		return e
	})
	runtime.KeepAlive(od)
	runtime.KeepAlive(nd)
	if err != nil {
		return linkErr("reflink", oldName, newName, err)
	}
	if cloneErr := unix.IoctlFileClone(dfd, sfd); cloneErr != nil {
		failure := linkErr("reflink", oldName, newName, fmt.Errorf("%w: %w", ErrReflinkUnsupported, cloneErr))
		failure = joinErr(failure, rollbackFailed("close", newName, unix.Close(dfd)))
		return undoNew(nd, newName, failure)
	}
	if cerr := unix.Close(dfd); cerr != nil {
		return undoNew(nd, newName, linkErr("reflink", oldName, newName, &os.PathError{Op: "close", Path: newName, Err: cerr}))
	}
	return nil
}

// undoNew removes the file Reflink created, for a failure that is being
// returned. A removal that fails too leaves a stray file the caller must hear
// about, so it is part of the error.
func undoNew(nd *Dir, name string, err error) error {
	rerr := retry(func() error { return unix.Unlinkat(fd(nd), name, 0) })
	runtime.KeepAlive(nd)
	return joinErr(err, rollbackFailed("remove", name, rerr))
}
