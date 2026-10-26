// file: internal/audiobooks/purge_file_identity_unix.go
// version: 1.0.0
// guid: 2a9c5e74-1d36-4f80-b7e3-6c0a8d4f2b19
// last-edited: 2026-10-26

//go:build !windows

package audiobooks

import (
	"os"
	"syscall"
)

// inodeOf reads the inode out of a stat result; ok is false when the platform
// does not report one. The device number is deliberately not read: see
// fileIdentity.
func inodeOf(info os.FileInfo) (ino uint64, ok bool) {
	st, isStat := info.Sys().(*syscall.Stat_t)
	if !isStat || st == nil {
		return 0, false
	}
	return uint64(st.Ino), true
}
