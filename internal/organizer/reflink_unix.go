// file: internal/organizer/reflink_unix.go
// version: 1.1.0
// guid: 6f7a8b9c-0d1e-2f3a-4b5c-6d7e8f9a0b1c
// last-edited: 2026-07-17

//go:build darwin || linux

package organizer

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

// reflinkFilePlatform creates a CoW reflink on macOS/Linux
func (o *Organizer) reflinkFilePlatform(sourcePath, targetPath string) error {
	// Open source file
	srcFile, err := os.Open(sourcePath)
	if err != nil {
		return fmt.Errorf("failed to open source file: %w", err)
	}
	defer srcFile.Close()

	// Create destination file. O_EXCL: os.Create would TRUNCATE an existing
	// destination — under the concurrent organize worker pool a stat→create
	// TOCTOU race could zero out a file another worker just finished (unlike
	// os.Link, which fails with EEXIST). With O_EXCL an existing destination
	// errors instead; the raw error is returned un-wrapped when it is an
	// exists-error so callers' os.IsExist recovery works, mirroring the
	// hardlink fallback.
	dstFile, err := os.OpenFile(targetPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o664)
	if err != nil {
		if os.IsExist(err) {
			return err
		}
		return fmt.Errorf("failed to create destination file: %w", err)
	}
	defer dstFile.Close()

	// Try platform-specific reflink
	srcFd := int(srcFile.Fd())
	dstFd := int(dstFile.Fd())

	// macOS: APFS clonefile
	// Linux: FICLONE ioctl
	var ret uintptr
	var errno syscall.Errno

	// Try Linux FICLONE first (most common)
	const FICLONE = 0x40049409
	_, _, errno = syscall.Syscall(syscall.SYS_IOCTL, uintptr(dstFd), FICLONE, uintptr(srcFd))
	if errno == 0 {
		return nil
	}

	// Try macOS clonefile
	const APFS_CLONE = 0xC0084A6D
	ret, _, errno = syscall.Syscall(syscall.SYS_IOCTL, uintptr(dstFd), APFS_CLONE, uintptr(unsafe.Pointer(&srcFd)))
	if errno == 0 {
		return nil
	}

	// Both failed — clean up the empty destination file so hardlink fallback can work
	dstFile.Close()
	os.Remove(targetPath)

	if ret != 0 || errno != 0 {
		return fmt.Errorf("reflink not supported on this filesystem (errno: %v)", errno)
	}

	return nil
}
