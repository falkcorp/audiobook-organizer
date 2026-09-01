// file: internal/fileops/reflink_control_linux_test.go
// version: 1.0.0
// guid: b6f3348f-5438-4d60-b047-4c0064d2c919
// last-edited: 2026-09-01

//go:build linux

package fileops

import (
	"os"

	"golang.org/x/sys/unix"
)

// rawClone issues the platform clone primitive directly, bypassing Reflink.
// It is the KNOWN-GOOD TWIN for the clone test: without it, a regression that
// disables cloning is indistinguishable from a filesystem that cannot clone,
// and the test skips instead of failing.
func rawClone(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o664)
	if err != nil {
		return err
	}
	defer out.Close()
	if err := unix.IoctlFileClone(int(out.Fd()), int(in.Fd())); err != nil {
		os.Remove(dst)
		return err
	}
	return nil
}
