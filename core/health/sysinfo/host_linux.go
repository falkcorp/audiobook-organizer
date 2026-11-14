// file: core/health/sysinfo/host_linux.go
// version: 1.0.0
// guid: 97227563-872a-4c9c-998e-28af2e5248e4
// last-edited: 2026-11-13

//go:build linux

package sysinfo

import (
	"os"
	"syscall"
	"time"
)

// hostReader reads the host through /proc.
type hostReader struct{}

func readProc(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(b)
}

func (hostReader) totalMemory() uint64 {
	total, _ := parseMeminfo(readProc("/proc/meminfo"))
	return total
}

func (hostReader) availableMemory() uint64 {
	_, avail := parseMeminfo(readProc("/proc/meminfo"))
	return avail
}

func (hostReader) rss() uint64 {
	return parseStatmRSS(readProc("/proc/self/statm"), syscall.Getpagesize())
}

func (hostReader) systemUptime() time.Duration { return parseUptime(readProc("/proc/uptime")) }

func (hostReader) processStart() time.Time {
	return parseProcessStart(readProc("/proc/self/stat"), readProc("/proc/stat"))
}

func (hostReader) container() bool {
	exists := func(p string) bool { _, err := os.Stat(p); return err == nil }
	return detectContainer(os.Getenv("container"), exists, readProc("/proc/1/cgroup"))
}
