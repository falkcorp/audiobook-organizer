// file: core/health/sysinfo/host_windows.go
// version: 1.0.0
// guid: b894eeaf-7bbd-4aa4-8cf1-b7109229cab1
// last-edited: 2026-11-13

//go:build windows

package sysinfo

import (
	"syscall"
	"time"
	"unsafe"
)

// hostReader reads the host through kernel32.
type hostReader struct{}

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

func globalMemory() (total, avail uint64) {
	proc := syscall.NewLazyDLL("kernel32.dll").NewProc("GlobalMemoryStatusEx")
	var st memoryStatusEx
	st.dwLength = uint32(unsafe.Sizeof(st))
	if ret, _, _ := proc.Call(uintptr(unsafe.Pointer(&st))); ret == 0 {
		return 0, 0
	}
	return st.ullTotalPhys, st.ullAvailPhys
}

func (hostReader) totalMemory() uint64 { t, _ := globalMemory(); return t }

func (hostReader) availableMemory() uint64 { _, a := globalMemory(); return a }

// rss, systemUptime and processStart are not reported on Windows (HEAD did not
// either): the zero value says "the platform cannot say".
func (hostReader) rss() uint64                 { return 0 }
func (hostReader) systemUptime() time.Duration { return 0 }
func (hostReader) processStart() time.Time     { return time.Time{} }
func (hostReader) container() bool             { return false }
