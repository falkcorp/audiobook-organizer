// file: core/storage/fsid_statfs_linux.go
// version: 1.0.0
// guid: 51c203b3-e0f4-4990-8b64-d1cb54b050cb
// last-edited: 2026-11-08

//go:build linux

package storage

import (
	"fmt"
	"syscall"
)

// dirFSID is statfs(2)'s f_fsid for path. ext4, XFS and btrfs derive it from
// the filesystem's UUID and ZFS from the dataset's GUID, so it survives a
// reboot that renumbers st_dev (osDirIdentity).
func dirFSID(path string, st *syscall.Stat_t) (uint64, error) {
	var s syscall.Statfs_t
	if err := syscall.Statfs(path, &s); err != nil {
		return 0, fmt.Errorf("storage: statfs %s: %w", path, err)
	}
	return fsidOr(s.Fsid.X__val, st), nil
}
