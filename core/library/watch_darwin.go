// file: core/library/watch_darwin.go
// version: 1.0.0
// guid: dead4fb7-51d5-4b93-9f9e-6835731614b0
// last-edited: 2026-11-11

package library

import (
	"golang.org/x/sys/unix"
)

// isNetworkFS reports whether abs lives on a network or FUSE filesystem, where
// kqueue does not see other clients' changes.
func isNetworkFS(abs string) bool {
	var st unix.Statfs_t
	if err := unix.Statfs(abs, &st); err != nil {
		return false
	}
	switch unix.ByteSliceToString(st.Fstypename[:]) {
	case "nfs", "smbfs", "afpfs", "webdav", "osxfuse", "macfuse", "fuse":
		return true
	}
	return false
}
