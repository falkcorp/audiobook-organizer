// file: internal/fileops/reflink_darwin.go
// version: 1.0.0
// guid: 0951abe3-654d-428d-9472-75a803e9a879
// last-edited: 2026-09-01

//go:build darwin

package fileops

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// reflinkPlatform clones src to dst with the APFS clonefile(2) syscall.
//
// clonefile takes PATHS and creates the destination itself; it is not an ioctl
// on an open descriptor. Both implementations this file replaces got that
// wrong -- they issued ioctl(dstFd, 0xC0084A6D, &srcFd) against a
// pre-created destination, which is not a real APFS clone request. It always
// failed, so reflink silently degraded to a full byte copy on every macOS
// machine, and the clone path could not be exercised by a developer test.
//
// clonefile refuses an existing destination with EEXIST on its own, which is
// exactly the non-truncating contract Reflink documents.
func reflinkPlatform(src, dst string) error {
	if err := unix.Clonefile(src, dst, 0); err != nil {
		if os.IsExist(err) {
			return err
		}
		return fmt.Errorf("%w: %v", ErrReflinkUnsupported, err)
	}
	return nil
}
