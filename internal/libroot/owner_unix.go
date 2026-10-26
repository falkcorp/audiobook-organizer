//go:build unix

// file: internal/libroot/owner_unix.go
// version: 1.1.0
// guid: 2b6ff7ad-1d85-4bb6-b0cb-641f33e1a418
// last-edited: 2026-10-26

package libroot

import (
	"os"
	"syscall"
)

// euid is the effective user ID this process is compared against; a variable so
// a test can stand in for a process running as someone else.
var euid = os.Geteuid

// OwnedByCurrentUser reports whether info (from Lstat or Fstat) describes a file
// owned by the effective user this process runs as. A file whose owner cannot be
// read is reported as not owned: callers use this to refuse a path that someone
// else could have planted.
func OwnedByCurrentUser(info os.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == euid()
}
