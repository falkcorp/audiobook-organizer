// file: internal/backup/nofollow_unix.go
// version: 1.0.0
// guid: 0667b2c9-b922-4ff0-8242-c3077c42fb05
// last-edited: 2026-11-08

//go:build !windows

package backup

import (
	"fmt"
	"os"
	"syscall"
)

// openNoFollow creates or truncates path for writing and refuses a symlink
// at its final component (ErrRestoreThroughSymlink). O_NOFOLLOW makes the
// kernel refuse it (ELOOP on darwin and linux), so there is no window
// between a check and the open; the Lstat after a failed open only names
// the refusal.
func openNoFollow(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC|syscall.O_NOFOLLOW, 0o666)
	if err == nil {
		return f, nil
	}
	if fi, lerr := os.Lstat(path); lerr == nil && fi.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%w: %s", ErrRestoreThroughSymlink, path)
	}
	return nil, fmt.Errorf("failed to create file %s: %w", path, err)
}
