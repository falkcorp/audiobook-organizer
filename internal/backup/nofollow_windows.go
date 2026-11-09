// file: internal/backup/nofollow_windows.go
// version: 1.0.0
// guid: 0178e543-9254-445f-85a3-da7e8d69d0cc
// last-edited: 2026-11-08

//go:build windows

package backup

import (
	"fmt"
	"os"
)

// openNoFollow creates or truncates path for writing and refuses a symlink
// at its final component (ErrRestoreThroughSymlink). Windows has no
// O_NOFOLLOW, so this checks with Lstat first, which leaves a window between
// the check and the open that the unix version does not have.
func openNoFollow(path string) (*os.File, error) {
	if fi, err := os.Lstat(path); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%w: %s", ErrRestoreThroughSymlink, path)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o666)
	if err != nil {
		return nil, fmt.Errorf("failed to create file %s: %w", path, err)
	}
	return f, nil
}
