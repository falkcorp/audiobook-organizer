// file: core/internal/libfs/reflink_other.go
// version: 1.0.0
// guid: da8dd18b-d0ba-4ad9-afd1-96a6211b89c5
// last-edited: 2026-11-11

//go:build !linux && !darwin

package libfs

import "io/fs"

// Reflink reports that this platform has no copy-on-write clone primitive, so a
// caller in Auto mode falls back to a link or a byte copy. Windows ReFS has
// block cloning behind DeviceIoControl on a configured volume, which nothing
// here targets (05-library 5L.12 item 4).
func Reflink(od *Dir, oldName string, nd *Dir, newName string, _ fs.FileMode) error {
	return ErrReflinkUnsupported
}
