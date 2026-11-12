// file: core/library/watch_linux.go
// version: 1.0.0
// guid: 4a66c625-2c16-49ed-a059-9379abf3392e
// last-edited: 2026-11-11

package library

import "golang.org/x/sys/unix"

// networkFSMagic are the statfs f_type values of filesystems whose changes made
// by other clients inotify does not see: NFS, SMB and CIFS, FUSE, AFS, Ceph, 9p.
func networkFSMagic(t int64) bool {
	switch t {
	case 0x6969, // NFS
		0x517B,     // SMB
		0xFF534D42, // CIFS
		0xFE534D42, // SMB2
		0x65735546, // FUSE
		0x5346414F, // AFS
		0x00C36400, // CephFS
		0x01021997: // 9p
		return true
	}
	return false
}

// isNetworkFS reports whether abs lives on a filesystem where notifications are
// unreliable. A statfs that fails is "no": the root is then watched, and the
// overflow and add-failure paths still fall back to polling.
func isNetworkFS(abs string) bool {
	var st unix.Statfs_t
	if err := unix.Statfs(abs, &st); err != nil {
		return false
	}
	return networkFSMagic(int64(st.Type))
}
