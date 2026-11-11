// file: core/internal/libfs/stat_unix.go
// version: 1.0.0
// guid: 19d9c01d-eaa9-4d32-82b2-f74906d4e383
// last-edited: 2026-11-11

//go:build unix

package libfs

import "syscall"

// unixStat is the platform's stat structure, the type FileInfo.Sys returns.
type unixStat = syscall.Stat_t
