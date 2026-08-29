// file: internal/diskstats/diskstats_unix.go
// version: 1.0.0
// guid: 9f2b7c14-6d38-4a05-b1e9-3c74a80d5216
// last-edited: 2026-08-29

//go:build !windows

package diskstats

import "syscall"

// Stats returns total and available bytes for the filesystem holding path.
//
// Bavail (not Bfree) is deliberate: it is the space available to an
// unprivileged process, excluding the root reserve. A backup runs as the
// service user, so Bfree would overstate what it can actually write.
func Stats(path string) (total, free uint64, err error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, 0, err
	}
	blockSize := uint64(stat.Bsize)
	return stat.Blocks * blockSize, stat.Bavail * blockSize, nil
}
