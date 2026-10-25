// file: internal/audiobooks/purge_file_identity_windows.go
// version: 1.0.0
// guid: 5e1b8d36-7c42-4a95-9f08-3d6a2c7e1b50
// last-edited: 2026-10-25

//go:build windows

package audiobooks

import "os"

// deviceAndInode: Windows file info carries no device or inode here, so the
// identity is the size and modification time.
func deviceAndInode(os.FileInfo) (dev, ino uint64, ok bool) {
	return 0, 0, false
}
