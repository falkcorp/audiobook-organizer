// file: core/storage/fsid_other.go
// version: 1.0.0
// guid: c16c0aef-e3f0-4ca6-b406-785d447e8c42
// last-edited: 2026-11-07

//go:build !unix

package storage

import (
	"fmt"
	"os"

	"github.com/cockroachdb/pebble/v2/vfs"
)

// osDirIdentity has no (device, inode) to read off unix: the generation then
// changes only when the epoch record is absent.
func osDirIdentity(vfs.FS) func(string) (uint64, uint64, error) {
	return func(string) (uint64, uint64, error) { return 0, 0, nil }
}

// osSameDevice cannot tell off unix and admits the directory; the hard links
// of the checkpoint fail loudly if it is wrong.
func osSameDevice(vfs.FS) func(a, b string) (bool, error) {
	return func(string, string) (bool, error) { return true, nil }
}

func osChmod(fs vfs.FS) func(path string, dir bool) error {
	return func(path string, dir bool) error {
		if fs != vfs.Default {
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
