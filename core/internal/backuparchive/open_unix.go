// file: core/internal/backuparchive/open_unix.go
// version: 1.0.0
// guid: 76d8c472-d1fa-4fe4-9727-127c68243c8c
// last-edited: 2026-11-12

//go:build unix

package backuparchive

import "syscall"

// openNoFollow makes the open itself refuse a symlink (ELOOP) and never block
// on a FIFO swapped in after the Lstat.
const openNoFollow = syscall.O_NOFOLLOW | syscall.O_NONBLOCK
