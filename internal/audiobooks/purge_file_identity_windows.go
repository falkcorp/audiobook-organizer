// file: internal/audiobooks/purge_file_identity_windows.go
// version: 1.0.0
// guid: 5e1b8d36-7c42-4a95-9f08-3d6a2c7e1b50
// last-edited: 2026-10-26

//go:build windows

package audiobooks

import "os"

// inodeOf: Windows file info carries no inode here, so the identity is the size
// and modification time.
func inodeOf(os.FileInfo) (ino uint64, ok bool) {
	return 0, false
}
