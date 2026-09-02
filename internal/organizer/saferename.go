// file: internal/organizer/saferename.go
// version: 1.2.0
// guid: 2df18e44-98f0-407e-ab5f-daf158f22554
// last-edited: 2026-09-02

package organizer

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"syscall"
)

// safeRename renames src to dst, refusing to overwrite an existing dst.
//
// POSIX rename(2) silently REPLACES an existing destination file, so a bare
// os.Rename on a path collision destroys another book's bytes. Every wired
// move/rename in this package must go through this helper (or perform its own
// explicit destination check, like MoveBookFile). Callers that intend to
// overwrite must delete dst explicitly first.
//
// The collision error is an *os.LinkError wrapping fs.ErrExist, so both
// os.IsExist(err) and errors.Is(err, fs.ErrExist) recognize it — matching the
// os.Link EEXIST behavior the organize worker pool already recovers from.
//
// There is an unavoidable TOCTOU window between the Lstat and the rename; the
// guard converts a silent overwrite into an explicit error in all but a
// sub-millisecond race, matching MoveBookFile's existing posture.
func safeRename(src, dst string) error {
	srcInfo, err := os.Lstat(src)
	if err != nil {
		return fmt.Errorf("stat rename source %s: %w", src, err)
	}

	if _, err := os.Lstat(dst); err == nil {
		slog.Warn("safeRename refusing to overwrite existing destination",
			"src", src, "dst", dst)
		return &os.LinkError{Op: "rename", Old: src, New: dst, Err: fs.ErrExist}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat rename destination %s: %w", dst, err)
	}

	if err := os.Rename(src, dst); err != nil {
		return err
	}

	return verifyRenamed(src, dst, srcInfo)
}

// verifyRenamed re-reads the filesystem after a rename that reported success.
//
// A rename returning nil is not evidence the file arrived: it says the syscall
// was accepted, which is a claim about the call, not about the tree. The
// separator bug is the case in point -- every one of those moves "succeeded",
// and the audio ended up in a directory nobody asked for, where it was
// invisible to the scanner and to every later lookup. 38,895 files across
// 1,145 books were misplaced by operations that all reported success.
//
// So the move is not complete until the destination has been read back:
//
//   - dst exists, and is the SAME KIND of thing the source was. Note that
//     safeRename moves directories as well as files -- ReOrganizeInPlace
//     renames whole book folders -- so this compares against the source
//     rather than demanding a regular file. An earlier version of this check
//     hard-coded "must be a regular file" and broke every directory move,
//     which the existing suite caught immediately.
//   - a regular file has the source's size. Catches a truncated or partially
//     written destination, which a bare rename cannot produce but a copy
//     fallback can. Directory sizes are filesystem bookkeeping, so they are
//     not compared.
//   - src is gone. If both paths exist the move silently became a copy, and
//     the next scan sees the book twice.
//
// This is cheap -- two Lstat calls against metadata the kernel just touched --
// and it converts a class of silent misplacement into a loud failure.
func verifyRenamed(src, dst string, srcInfo os.FileInfo) error {
	info, err := os.Lstat(dst)
	if err != nil {
		return fmt.Errorf("rename reported success but destination is unreadable %s: %w", dst, err)
	}
	if info.IsDir() != srcInfo.IsDir() {
		kind := func(fi os.FileInfo) string {
			if fi.IsDir() {
				return "a directory"
			}
			return "a file"
		}
		return fmt.Errorf("rename reported success but source was %s and destination %s is %s",
			kind(srcInfo), dst, kind(info))
	}
	if srcInfo.Mode().IsRegular() {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("rename reported success but destination %s is not a regular file (mode %s)", dst, info.Mode())
		}
		if info.Size() != srcInfo.Size() {
			return fmt.Errorf("rename reported success but destination %s is %d bytes, expected %d",
				dst, info.Size(), srcInfo.Size())
		}
	}
	if _, err := os.Lstat(src); err == nil {
		return fmt.Errorf("rename reported success but source %s still exists; the move became a copy", src)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("rename reported success but source %s is in an unknown state: %w", src, err)
	}
	return nil
}

// finalizeExclusive publishes a fully written regular file tmp at dst WITHOUT
// ever replacing a dst that already exists. tmp is consumed on success.
//
// safeRename's Lstat-then-Rename has a TOCTOU window: two organize workers
// finishing the same dst at the same time both see "absent" and both rename,
// and rename(2) silently replaces — the loser's audio is gone with a success
// return. copyFile used to run exactly that race, and once its temp names
// became per-writer that window was the last way two workers could still
// destroy each other's output.
//
// os.Link is the primitive that closes it: link(2) is atomic and FAILS with
// EEXIST if dst appeared in the meantime, on every POSIX filesystem this
// project organizes onto (ext4, xfs, zfs, btrfs, apfs, and NFS/SMB when the
// server supports hard links). The link is then the published file and tmp is
// unlinked; both names are the same inode so there is no second copy.
//
// The collision error is an *os.LinkError wrapping fs.ErrExist so that
// os.IsExist and errors.Is(err, fs.ErrExist) both recognise it, the same shape
// safeRename and os.Link produce — callers' race recovery does not need to
// know which primitive refused.
//
// Filesystems that refuse hard links altogether (EPERM/ENOTSUP/EXDEV — exFAT,
// FAT32, some SMB mounts) fall back to safeRename and its documented
// sub-millisecond window. That is a narrowing, not a regression: those mounts
// had the window before this function existed.
func finalizeExclusive(tmp, dst string) error {
	tmpInfo, err := os.Lstat(tmp)
	if err != nil {
		return fmt.Errorf("stat finalize source %s: %w", tmp, err)
	}
	if !tmpInfo.Mode().IsRegular() {
		return fmt.Errorf("finalize source %s is not a regular file (mode %s)", tmp, tmpInfo.Mode())
	}

	if err := os.Link(tmp, dst); err != nil {
		if os.IsExist(err) {
			slog.Warn("finalizeExclusive refusing to overwrite existing destination",
				"tmp", tmp, "dst", dst)
			return &os.LinkError{Op: "link", Old: tmp, New: dst, Err: fs.ErrExist}
		}
		if !linkUnsupported(err) {
			return fmt.Errorf("link %s to %s: %w", tmp, dst, err)
		}
		slog.Debug("finalizeExclusive: filesystem refuses hard links, falling back to rename",
			"tmp", tmp, "dst", dst, "error", err)
		return safeRename(tmp, dst)
	}

	// dst is published; tmp is a second name for the same inode. Losing the
	// unlink is a leak cleanupTempFiles will sweep, not a correctness problem.
	if err := os.Remove(tmp); err != nil && !os.IsNotExist(err) {
		slog.Warn("finalizeExclusive: published destination but could not remove temp name",
			"tmp", tmp, "dst", dst, "error", err)
	}

	info, err := os.Lstat(dst)
	if err != nil {
		return fmt.Errorf("link reported success but destination is unreadable %s: %w", dst, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("link reported success but destination %s is not a regular file (mode %s)", dst, info.Mode())
	}
	if info.Size() != tmpInfo.Size() {
		return fmt.Errorf("link reported success but destination %s is %d bytes, expected %d",
			dst, info.Size(), tmpInfo.Size())
	}
	return nil
}

// linkUnsupported reports whether a link(2) failure means "this filesystem
// does not do hard links" rather than "this particular link was refused".
// Only the former justifies falling back to a rename; anything else (EACCES on
// the directory, ENOSPC for the dirent, EIO) must surface as the error it is.
func linkUnsupported(err error) bool {
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return false
	}
	switch errno {
	case syscall.EPERM, syscall.ENOTSUP, syscall.EXDEV, syscall.EMLINK:
		return true
	}
	return false
}
