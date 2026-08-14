// file: internal/maintenance/jobs/fix_file_modes_unix.go
// version: 1.0.0
// guid: 3e7b1c94-6a25-4d08-9f31-2c8d5e0a4b76
// last-edited: 2026-08-14

//go:build unix

package jobs

import (
	"os"
	"syscall"
)

// ownedByUID reports whether info's underlying file is owned by uid.
func ownedByUID(info os.FileInfo, uid int) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == uid
}
