// file: internal/sysinfo/memory_darwin.go
// version: 1.0.1
// guid: 8b9c0d1e-2f3a-4b5c-6d7e-8f9a0b1c2d3e
// last-edited: 2026-10-18

//go:build darwin

package sysinfo

import (
	"syscall"
	"unsafe"
)

// getTotalMemoryPlatform returns total system memory on macOS
func getTotalMemoryPlatform() uint64 {
	mib := []int32{6 /* CTL_HW */, 24 /* HW_MEMSIZE */}
	var memsize uint64
	length := unsafe.Sizeof(memsize)

	_, _, err := syscall.Syscall6(
		syscall.SYS___SYSCTL,
		uintptr(unsafe.Pointer(&mib[0])),
		uintptr(len(mib)),
		uintptr(unsafe.Pointer(&memsize)),
		uintptr(unsafe.Pointer(&length)),
		0, 0,
	)

	if err != 0 {
		return 0
	}

	return memsize
}
