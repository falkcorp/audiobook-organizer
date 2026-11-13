// file: core/health/sysinfo/host_darwin.go
// version: 1.0.0
// guid: f76e8d6b-84c7-4edd-8e98-12a27529faac
// last-edited: 2026-11-13

//go:build darwin

package sysinfo

import (
	"encoding/binary"
	"os"
	"syscall"
	"time"
	"unsafe"
)

// hostReader reads the host through sysctl. Docker Desktop runs containers in a
// Linux VM, so a darwin process is never in one.
type hostReader struct{}

func (hostReader) totalMemory() uint64 {
	mib := []int32{6 /* CTL_HW */, 24 /* HW_MEMSIZE */}
	var memsize uint64
	length := unsafe.Sizeof(memsize)
	_, _, err := syscall.Syscall6(syscall.SYS___SYSCTL,
		uintptr(unsafe.Pointer(&mib[0])), uintptr(len(mib)),
		uintptr(unsafe.Pointer(&memsize)), uintptr(unsafe.Pointer(&length)), 0, 0)
	if err != 0 {
		return 0
	}
	return memsize
}

// availableMemory is the free and speculative pages: what the kernel can hand
// out without evicting anything. It is an approximation of Linux's
// MemAvailable, which also counts reclaimable cache.
func (hostReader) availableMemory() uint64 {
	free, err1 := syscall.SysctlUint32("vm.page_free_count")
	spec, err2 := syscall.SysctlUint32("vm.page_speculative_count")
	if err1 != nil || err2 != nil {
		return 0
	}
	return (uint64(free) + uint64(spec)) * uint64(syscall.Getpagesize())
}

// rss is the process's peak resident set (getrusage's ru_maxrss, in bytes on
// darwin): macOS has no cheap way to read the current one without cgo.
func (hostReader) rss() uint64 {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil || ru.Maxrss < 0 {
		return 0
	}
	return uint64(ru.Maxrss)
}

// systemUptime is the time since kern.boottime, a struct timeval.
func (hostReader) systemUptime() time.Duration {
	raw, err := syscall.Sysctl("kern.boottime")
	if err != nil {
		return 0
	}
	// Sysctl trims a trailing NUL, so the value may be short by one byte.
	b := append([]byte(raw), 0, 0, 0, 0, 0, 0, 0, 0)
	if len(b) < 8 {
		return 0
	}
	boot := time.Unix(int64(binary.LittleEndian.Uint64(b[:8])), 0)
	if d := time.Since(boot); d > 0 {
		return d
	}
	return 0
}

// processStart is the process's start time: the leading timeval (p_starttime)
// of the kinfo_proc that sysctl kern.proc.pid returns.
func (hostReader) processStart() time.Time {
	mib := []int32{1 /* CTL_KERN */, 14 /* KERN_PROC */, 1 /* KERN_PROC_PID */, int32(os.Getpid())}
	raw := make([]byte, 1024) // sizeof(struct kinfo_proc) is 648
	length := uintptr(len(raw))
	_, _, err := syscall.Syscall6(syscall.SYS___SYSCTL,
		uintptr(unsafe.Pointer(&mib[0])), uintptr(len(mib)),
		uintptr(unsafe.Pointer(&raw[0])), uintptr(unsafe.Pointer(&length)), 0, 0)
	if err != 0 || length < 16 {
		return time.Time{}
	}
	sec := int64(binary.LittleEndian.Uint64(raw[:8]))
	usec := int64(int32(binary.LittleEndian.Uint32(raw[8:12])))
	if sec <= 0 {
		return time.Time{}
	}
	return time.Unix(sec, usec*1000)
}

func (hostReader) container() bool { return false }
