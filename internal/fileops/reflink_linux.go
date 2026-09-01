// file: internal/fileops/reflink_linux.go
// version: 1.1.0
// guid: decc2c9f-e678-427f-ab6c-3eff213b2ba8
// last-edited: 2026-09-01

//go:build linux

package fileops

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// reflinkPlatform clones src to dst with the FICLONE ioctl (btrfs, XFS with
// reflink=1, OCFS2, and ZFS 2.2+ with the block_cloning feature active).
//
// FICLONE needs an open destination descriptor, so dst is created first with
// O_EXCL — an existing destination is refused, never truncated. A failed clone
// removes the empty file it just created so a caller's fallback has a clear
// path to write to.
func reflinkPlatform(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open source %s: %w", src, err)
	}
	defer in.Close()

	info, err := in.Stat()
	if err != nil {
		return fmt.Errorf("stat source %s: %w", src, err)
	}
	perm := info.Mode().Perm()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, perm)
	if err != nil {
		if os.IsExist(err) {
			return err
		}
		return fmt.Errorf("create destination %s: %w", dst, err)
	}

	// Chmod because OpenFile's perm argument is masked by umask, and because
	// darwin's clonefile(2) copies the source's mode on its own: without this
	// the destination's permissions would silently reveal which of the three
	// code paths (linux clone, darwin clone, byte-copy fallback) produced it.
	if err := out.Chmod(perm); err != nil {
		out.Close()
		os.Remove(dst)
		return fmt.Errorf("chmod destination %s to %v: %w", dst, perm, err)
	}

	if cloneErr := unix.IoctlFileClone(int(out.Fd()), int(in.Fd())); cloneErr != nil {
		out.Close()
		os.Remove(dst)
		return fmt.Errorf("%w: %v", ErrReflinkUnsupported, cloneErr)
	}
	if cerr := out.Close(); cerr != nil {
		os.Remove(dst)
		return fmt.Errorf("close destination %s: %w", dst, cerr)
	}
	return nil
}
