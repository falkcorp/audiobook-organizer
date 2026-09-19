// file: internal/fingerprint/workerclient/mount_linux.go
// version: 1.0.0
// guid: e0b3deb7-7631-48af-b055-e8c2d2e68dce
// last-edited: 2026-09-19

//go:build linux

package workerclient

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// Filesystem magic numbers (statfs(2) f_type) that name a network mount.
const (
	nfsSuperMagic  = 0x6969
	smbSuperMagic  = 0x517b
	smb2SuperMagic = 0xfe534d42
	cifsMagic      = 0xff534d42
)

// statMount reports the filesystem holding path: statfs(2)'s f_type (mapped
// to a name for the network filesystems) and the ST_RDONLY bit of f_flags.
// Linux's statfs does not report the mount point, so MountPoint is empty and
// the mount-point check is skipped; the fstype, read-only, write-probe and
// calibration checks still apply.
func statMount(path string) (MountInfo, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return MountInfo{}, fmt.Errorf("statfs %s: %w", path, err)
	}
	var fstype string
	switch uint64(st.Type) {
	case nfsSuperMagic:
		fstype = "nfs"
	case smbSuperMagic:
		fstype = "smbfs"
	case smb2SuperMagic:
		fstype = "smb2"
	case cifsMagic:
		fstype = "cifs"
	default:
		fstype = fmt.Sprintf("0x%x", uint64(st.Type))
	}
	return MountInfo{FSType: fstype, ReadOnly: st.Flags&unix.ST_RDONLY != 0}, nil
}
