// file: internal/diskstats/diskstats_windows.go
// version: 1.0.0
// guid: 4e81d05a-9b27-43c6-8f10-6d5b29ac7f31
// last-edited: 2026-08-29

//go:build windows

package diskstats

import (
	"fmt"
	"syscall"
	"unsafe"
)

// Stats returns total and available bytes for the filesystem holding path.
func Stats(path string) (total, free uint64, err error) {
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	proc := kernel32.NewProc("GetDiskFreeSpaceExW")
	pathPtr, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid path: %w", err)
	}
	var freeBytesAvailable, totalBytes, totalFreeBytes uint64
	r1, _, e1 := proc.Call(
		uintptr(unsafe.Pointer(pathPtr)),
		uintptr(unsafe.Pointer(&freeBytesAvailable)),
		uintptr(unsafe.Pointer(&totalBytes)),
		uintptr(unsafe.Pointer(&totalFreeBytes)),
	)
	if r1 == 0 {
		return 0, 0, fmt.Errorf("GetDiskFreeSpaceExW failed: %w", e1)
	}
	return totalBytes, freeBytesAvailable, nil
}
