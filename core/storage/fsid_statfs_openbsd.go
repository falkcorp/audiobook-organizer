// file: core/storage/fsid_statfs_openbsd.go
// version: 1.0.0
// guid: f13ef421-9cff-4cd2-b754-4da335eedc12
// last-edited: 2026-11-08

//go:build openbsd

package storage

import (
	"fmt"
	"syscall"
)

// dirFSID is statfs(2)'s f_fsid for path (osDirIdentity).
func dirFSID(path string, st *syscall.Stat_t) (uint64, error) {
	var s syscall.Statfs_t
	if err := syscall.Statfs(path, &s); err != nil {
		return 0, fmt.Errorf("storage: statfs %s: %w", path, err)
	}
	return fsidOr(s.F_fsid.Val, st), nil
}
