// file: internal/sysinfo/memory_windows.go
// version: 1.0.1
// guid: 0d1e2f3a-4b5c-6d7e-8f9a-0b1c2d3e4f5a
// last-edited: 2026-10-18

//go:build windows

package sysinfo

import (
	"syscall"
	"unsafe"
)

type memoryStatusEx struct {
	dwLength                uint32
	dwMemoryLoad            uint32
	ullTotalPhys            uint64
	ullAvailPhys            uint64
	ullTotalPageFile        uint64
	ullAvailPageFile        uint64
	ullTotalVirtual         uint64
	ullAvailVirtual         uint64
	ullAvailExtendedVirtual uint64
}

// getTotalMemoryPlatform returns total system memory on Windows
func getTotalMemoryPlatform() uint64 {
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	globalMemoryStatusEx := kernel32.NewProc("GlobalMemoryStatusEx")

	var memStatus memoryStatusEx
	memStatus.dwLength = uint32(unsafe.Sizeof(memStatus))

	ret, _, _ := globalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&memStatus)))
	if ret == 0 {
		return 0
	}

	return memStatus.ullTotalPhys
}
