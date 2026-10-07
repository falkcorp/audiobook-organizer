// file: internal/plugins/maintenance/fragment_filesig_other.go
// version: 1.0.0
// guid: 5b9a2e48-6c1f-4d73-9e05-c4a8f7d2b613
// last-edited: 2026-10-06

//go:build !linux && !darwin

package maintenance

import "os"

// fileIdentity is unavailable here: a content proof needs the file's device,
// inode and status-change time, so none is made (fail closed).
func fileIdentity(os.FileInfo) (dev, ino uint64, ctimeNS int64, ok bool) {
	return 0, 0, 0, false
}
