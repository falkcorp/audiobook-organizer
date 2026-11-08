// file: core/storage/fsid_statfs_bsd.go
// version: 1.0.0
// guid: b5fe76a8-2219-4fce-8b63-cc7f9dd06f9f
// last-edited: 2026-11-08

//go:build darwin || freebsd || dragonfly

package storage

import (
	"fmt"
	"syscall"
)

// dirFSID is statfs(2)'s f_fsid for path (osDirIdentity). On darwin its first
// word is the device number, so the identity is no steadier than st_dev
// there; darwin is a development platform, not a production one.
func dirFSID(path string, st *syscall.Stat_t) (uint64, error) {
	var s syscall.Statfs_t
	if err := syscall.Statfs(path, &s); err != nil {
		return 0, fmt.Errorf("storage: statfs %s: %w", path, err)
	}
	return fsidOr(s.Fsid.Val, st), nil
}
