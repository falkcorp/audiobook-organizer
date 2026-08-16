// file: internal/organizer/saferename.go
// version: 1.1.0
// guid: 2df18e44-98f0-407e-ab5f-daf158f22554
// last-edited: 2026-08-16

package organizer

import (
	"fmt"
	"io/fs"
	"log/slog"
	"os"
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
