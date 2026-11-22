// file: internal/server/settle_disk_unix_test.go
// version: 1.0.0
// guid: 7c3e51a8-92d4-4b06-8f1a-d5e0b6a47c29
// last-edited: 2026-11-22

//go:build !windows

package server

import (
	"syscall"
	"testing"
)

// settleDisk flushes pending filesystem writes so st_blocks is final.
//
// filePhysicalSize reports st_blocks*512. ZFS delays block allocation until the
// transaction group commits (about 5 s), so a file written a moment ago reports
// ~1 kB whatever its content or an fsync of it (measured on u0: 500 kB of random
// bytes read 2 blocks immediately and 1002 blocks after sync(2)). sync(2) forces
// the commit. On ext4, APFS and tmpfs it is a cheap no-op for this purpose.
func settleDisk(t *testing.T) {
	t.Helper()
	syscall.Sync()
}
