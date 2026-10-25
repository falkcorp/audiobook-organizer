// file: internal/audiobooks/purge_file_identity_unix.go
// version: 1.0.0
// guid: 2a9c5e74-1d36-4f80-b7e3-6c0a8d4f2b19
// last-edited: 2026-10-25

//go:build !windows

package audiobooks

import (
	"os"
	"syscall"
)

// deviceAndInode reads the device and inode out of a stat result; ok is false
// when the platform does not report them.
func deviceAndInode(info os.FileInfo) (dev, ino uint64, ok bool) {
	st, isStat := info.Sys().(*syscall.Stat_t)
	if !isStat || st == nil {
		return 0, 0, false
	}
	// The conversions are needed on darwin, where Dev is an int32.
	return uint64(st.Dev), uint64(st.Ino), true
}
