// file: internal/organizer/saferename.go
// version: 1.0.0
// guid: 2df18e44-98f0-407e-ab5f-daf158f22554
// last-edited: 2026-07-17

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
	if _, err := os.Lstat(dst); err == nil {
		slog.Warn("safeRename refusing to overwrite existing destination",
			"src", src, "dst", dst)
		return &os.LinkError{Op: "rename", Old: src, New: dst, Err: fs.ErrExist}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat rename destination %s: %w", dst, err)
	}
	return os.Rename(src, dst)
}
