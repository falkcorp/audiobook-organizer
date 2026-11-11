// file: core/internal/libfs/ops_unix.go
// version: 1.0.0
// guid: 7ec76d18-20c0-48f2-9a57-c01b4e75498f
// last-edited: 2026-11-11

//go:build unix

package libfs

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"runtime"

	"golang.org/x/sys/unix"
)

// fd is the descriptor of d for one syscall. Callers must keep d reachable
// until the call returns (runtime.KeepAlive).
func fd(d *Dir) int { return int(d.f.Fd()) }

// retry runs f until it stops returning EINTR.
func retry(f func() error) error {
	for {
		if err := f(); err != unix.EINTR {
			return err
		}
	}
}

// Link makes newName in nd a hard link to oldName in od (linkat, flags 0: a
// symlink is linked itself, never followed). An existing newName is
// fs.ErrExist and nothing changes. A link across filesystems is ErrCrossDevice.
func Link(od *Dir, oldName string, nd *Dir, newName string) error {
	if err := errors.Join(require(od, nd), checkName(oldName), checkName(newName)); err != nil {
		return err
	}
	err := retry(func() error { return unix.Linkat(fd(od), oldName, fd(nd), newName, 0) })
	runtime.KeepAlive(od)
	runtime.KeepAlive(nd)
	return mapLinkErr("link", oldName, newName, err)
}

// mapLinkErr wraps an errno as an *os.LinkError, naming EXDEV ErrCrossDevice.
func mapLinkErr(op, oldName, newName string, err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, unix.EXDEV):
		return linkErr(op, oldName, newName, fmt.Errorf("%w: %w", ErrCrossDevice, err))
	}
	return linkErr(op, oldName, newName, err)
}

// SyncDir fsyncs a directory so that a newly created or renamed NAME is
// durable, not just its bytes (fsync of a file does not commit the parent
// entry on ext4 data=ordered or XFS). EINVAL and ENOTSUP are success: some
// filesystems and network mounts do not support it and it was never their
// durability mechanism (05-library 5L.5). Every other error is returned.
func SyncDir(d *Dir) error {
	if err := require(d); err != nil {
		return err
	}
	if err := d.f.Sync(); err != nil {
		if errors.Is(err, unix.EINVAL) || errors.Is(err, unix.ENOTSUP) || errors.Is(err, unix.EOPNOTSUPP) {
			return nil
		}
		return fmt.Errorf("libfs: sync directory %s: %w", d.f.Name(), err)
	}
	return nil
}

// RenameNoReplaceFallback is the rename used where the kernel cannot do the
// check and the rename as one step (a filesystem or kernel without the
// no-replace flag, or a platform without it):
//
//   - a regular file is linked to its new name (linkat refuses an existing
//     name atomically) and then unlinked from the old one, so the no-clobber
//     check is still atomic;
//   - anything else (a directory, a symlink, or a filesystem that refuses hard
//     links) takes the checked rename: Lstat the destination, then rename.
//     That has a time-of-check/time-of-use window, accepted only here, as
//     internal/versions did.
//
// It is exported so a test can drive it on a filesystem that has the flag.
func RenameNoReplaceFallback(od *Dir, oldName string, nd *Dir, newName string) error {
	if err := errors.Join(require(od, nd), checkName(oldName), checkName(newName)); err != nil {
		return err
	}
	defer runtime.KeepAlive(od)
	defer runtime.KeepAlive(nd)
	var st unix.Stat_t
	if err := retry(func() error { return unix.Fstatat(fd(od), oldName, &st, unix.AT_SYMLINK_NOFOLLOW) }); err != nil {
		return linkErr("rename", oldName, newName, err)
	}
	if st.Mode&unix.S_IFMT == unix.S_IFREG {
		err := retry(func() error { return unix.Linkat(fd(od), oldName, fd(nd), newName, 0) })
		switch {
		case err == nil:
			if uerr := retry(func() error { return unix.Unlinkat(fd(od), oldName, 0) }); uerr != nil {
				// The new name exists and the old one could not be removed:
				// both names reach the same bytes, which loses nothing. Undo
				// the new name so the rename is all or nothing.
				_ = retry(func() error { return unix.Unlinkat(fd(nd), newName, 0) })
				return linkErr("rename", oldName, newName, uerr)
			}
			return nil
		case errors.Is(err, unix.EEXIST):
			return linkErr("rename", oldName, newName, err)
		case errors.Is(err, unix.EXDEV):
			return mapLinkErr("rename", oldName, newName, err)
		case errors.Is(err, unix.EPERM), errors.Is(err, unix.ENOTSUP), errors.Is(err, unix.EOPNOTSUPP),
			errors.Is(err, unix.EMLINK), errors.Is(err, unix.ENOSYS):
			// This filesystem has no hard links: the checked rename below.
		default:
			return linkErr("rename", oldName, newName, err)
		}
	}
	var dst unix.Stat_t
	switch err := retry(func() error { return unix.Fstatat(fd(nd), newName, &dst, unix.AT_SYMLINK_NOFOLLOW) }); {
	case err == nil:
		return linkErr("rename", oldName, newName, fs.ErrExist)
	case !errors.Is(err, unix.ENOENT):
		return linkErr("rename", oldName, newName, err)
	}
	err := retry(func() error { return unix.Renameat(fd(od), oldName, fd(nd), newName) })
	return mapRenameErr(oldName, newName, err)
}

// mapRenameErr wraps the errno of a rename; EXDEV is ErrCrossDevice.
func mapRenameErr(oldName, newName string, err error) error {
	return mapLinkErr("rename", oldName, newName, err)
}

// noReplaceUnsupported reports the errnos that mean "this kernel or filesystem
// does not take the no-replace flag".
func noReplaceUnsupported(err error) bool {
	return errors.Is(err, unix.EINVAL) || errors.Is(err, unix.ENOSYS) ||
		errors.Is(err, unix.ENOTSUP) || errors.Is(err, unix.EOPNOTSUPP)
}

// DeviceOf is the device ID of the filesystem a file lives on. ok is false
// when the platform's FileInfo carries none.
func DeviceOf(fi fs.FileInfo) (dev uint64, ok bool) {
	st, ok := fi.Sys().(*unixStat)
	if !ok || st == nil {
		return 0, false
	}
	return uint64(st.Dev), true
}

// InodeOf is the inode of a file, with its device: the pair identifies a
// directory across bind mounts (05-library 5L.3.8).
func InodeOf(fi fs.FileInfo) (dev, ino uint64, ok bool) {
	st, ok := fi.Sys().(*unixStat)
	if !ok || st == nil {
		return 0, 0, false
	}
	return uint64(st.Dev), uint64(st.Ino), true
}

// SameFile reports whether a and b are one file (device and inode).
func SameFile(a, b fs.FileInfo) bool { return os.SameFile(a, b) }
