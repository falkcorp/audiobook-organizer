// file: internal/fileops/reflink_linux.go
// version: 1.2.0
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
//
// dst is created at LibraryFileMode under the umask, matching
// CopyFileIngestExclusive (the fallback for this function) — reflink is an
// INGEST primitive and must not adopt an external source's permission bits.
// A brief revision of this file chmodded dst to the source's mode "so the three
// code paths agree"; the paths did then agree, on the wrong answer. Note that
// darwin's clonefile(2) creates dst itself and copies the source's mode, so
// macOS genuinely differs here. That is a real inconsistency and it is written
// down rather than papered over: Linux is the production target, and forcing
// the darwin clone to match would mean reading the process umask, which is not
// safely readable from a multi-goroutine program.
func reflinkPlatform(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open source %s: %w", src, err)
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, LibraryFileMode)
	if err != nil {
		if os.IsExist(err) {
			return err
		}
		return fmt.Errorf("create destination %s: %w", dst, err)
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
