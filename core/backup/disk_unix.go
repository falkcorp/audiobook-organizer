// file: core/backup/disk_unix.go
// version: 1.0.0
// guid: 6c1e84a2-3b97-4d5f-a028-9e7d41c5b630
// last-edited: 2026-11-12

//go:build unix

package backup

import (
	"errors"
	"syscall"
)

// osDevice is the ID of the device holding p, which the same-device rule
// compares between the database and the staging directory (a rename across
// devices is a copy, not the atomic step the checkpoint and the swap need).
func osDevice(p string) (uint64, error) {
	fi, err := osStat(p)
	if err != nil {
		return 0, err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, errors.New("backup: no device ID on this platform")
	}
	return uint64(st.Dev), nil
}
