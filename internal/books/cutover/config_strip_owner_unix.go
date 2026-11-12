// file: internal/books/cutover/config_strip_owner_unix.go
// version: 1.0.0
// guid: 5b8f0c1e-7a42-4d93-9c60-1e2a3f4b5c6d
// last-edited: 2026-11-11

//go:build unix

package cutover

import (
	"os"
	"syscall"
)

// fileOwner returns the numeric owner and group of a file, or ok=false when
// the platform does not report them (HEAD: internal/config fileOwner).
func fileOwner(info os.FileInfo) (uid, gid int, ok bool) {
	st, isStat := info.Sys().(*syscall.Stat_t)
	if !isStat {
		return 0, 0, false
	}
	return int(st.Uid), int(st.Gid), true
}
