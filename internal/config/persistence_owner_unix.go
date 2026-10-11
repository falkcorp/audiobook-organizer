// file: internal/config/persistence_owner_unix.go
// version: 1.0.0
// guid: 0e16a8e9-dba1-4e66-9084-fe4986092b06
// last-edited: 2026-10-11

//go:build unix

package config

import (
	"os"
	"syscall"
)

// fileOwner returns the numeric owner and group of a file, or ok=false when the
// platform does not report them.
func fileOwner(info os.FileInfo) (uid, gid int, ok bool) {
	st, isStat := info.Sys().(*syscall.Stat_t)
	if !isStat {
		return 0, 0, false
	}
	return int(st.Uid), int(st.Gid), true
}
