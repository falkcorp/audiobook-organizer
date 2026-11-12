// file: core/backup/disk_other.go
// version: 1.0.0
// guid: 0f3d9b57-8a64-4e12-b7c1-5d2a6e08f9a4
// last-edited: 2026-11-12

//go:build !unix

package backup

import "errors"

// osDevice cannot name the device holding p here, so a backup that needs the
// same-device proof is refused rather than assumed to be on one device.
func osDevice(string) (uint64, error) {
	return 0, errors.New("backup: the same-device check is not supported on this platform")
}
