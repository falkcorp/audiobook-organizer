// file: core/storage/fsid_unix.go
// version: 1.0.0
// guid: a3c187ed-9fc3-4d8c-94d2-944e59d4c7ba
// last-edited: 2026-11-07

//go:build unix

package storage

import (
	"fmt"
	"os"
	"syscall"

	"github.com/cockroachdb/pebble/v2/vfs"
)

// osDirIdentity is the DB directory's (device, inode) on the real
// filesystem, the store generation's identity (§5S.3.7); a constant on any
// other vfs.FS, where a directory cannot be swapped behind the process. "The
// real filesystem" is vfs.Root(fs) == vfs.Default, here and below, so a
// wrapper over it (a fault injector) gets the real checks.
func osDirIdentity(fs vfs.FS) func(string) (uint64, uint64, error) {
	return func(path string) (uint64, uint64, error) {
		if vfs.Root(fs) != vfs.Default {
			return 0, 0, nil
		}
		st, err := statT(path)
		if err != nil {
			return 0, 0, err
		}
		return uint64(st.Dev), st.Ino, nil //nolint:unconvert // Dev is int32 on darwin
	}
}

// osSameDevice reports whether two paths are on one filesystem.
func osSameDevice(fs vfs.FS) func(a, b string) (bool, error) {
	return func(a, b string) (bool, error) {
		if vfs.Root(fs) != vfs.Default {
			return true, nil
		}
		sa, err := statT(a)
		if err != nil {
			return false, err
		}
		sb, err := statT(b)
		if err != nil {
			return false, err
		}
		return sa.Dev == sb.Dev, nil
	}
}

// osChmod sets 0700 on a directory and 0600 on a file of the real
// filesystem; vfs.FS has no chmod, and an in-memory one has no modes.
func osChmod(fs vfs.FS) func(path string, dir bool) error {
	return func(path string, dir bool) error {
		if vfs.Root(fs) != vfs.Default {
			return nil
		}
		mode := os.FileMode(0o600)
		if dir {
			mode = 0o700
		}
		if err := os.Chmod(path, mode); err != nil {
			return fmt.Errorf("storage: chmod %s: %w", path, err)
		}
		return nil
	}
}

func statT(path string) (*syscall.Stat_t, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return nil, fmt.Errorf("storage: no device and inode for %s", path)
	}
	return st, nil
}
