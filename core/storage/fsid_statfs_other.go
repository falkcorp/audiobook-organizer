// file: core/storage/fsid_statfs_other.go
// version: 1.0.0
// guid: 32eeb2d3-c16a-4657-b4b4-71a32795f6bf
// last-edited: 2026-11-08

//go:build unix && !linux && !darwin && !freebsd && !dragonfly && !openbsd

package storage

import "syscall"

// dirFSID is st_dev on the unix platforms whose f_fsid this package does not
// read (netbsd, solaris, illumos, aix, hurd): the identity there keeps the
// reboot hazard osDirIdentity avoids elsewhere.
func dirFSID(_ string, st *syscall.Stat_t) (uint64, error) {
	return uint64(st.Dev), nil //nolint:unconvert // Dev's type varies by platform
}
