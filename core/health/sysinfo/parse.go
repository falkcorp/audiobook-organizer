// file: core/health/sysinfo/parse.go
// version: 1.0.2
// guid: 9e695b0b-4ab4-473c-aed6-8b9b6f049d0f
// last-edited: 2026-11-13

package sysinfo

import (
	"strconv"
	"strings"
	"time"
)

// The parsers below read Linux's /proc text. They are plain functions over the
// text so they run, and are tested, on every platform.

// parseMeminfo is MemTotal and MemAvailable of /proc/meminfo in bytes; a
// missing or malformed line is 0.
func parseMeminfo(text string) (total, available uint64) {
	for line := range strings.SplitSeq(text, "\n") {
		key, rest, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		f := strings.Fields(rest)
		if len(f) == 0 {
			continue
		}
		kb, err := strconv.ParseUint(f[0], 10, 64)
		if err != nil {
			continue
		}
		switch key {
		case "MemTotal":
			total = kb * 1024
		case "MemAvailable":
			available = kb * 1024
		}
	}
	return total, available
}

// parseStatmRSS is the resident set of /proc/self/statm (its second field, in
// pages) in bytes.
func parseStatmRSS(text string, pageSize int) uint64 {
	f := strings.Fields(text)
	if len(f) < 2 || pageSize <= 0 {
		return 0
	}
	pages, err := strconv.ParseUint(f[1], 10, 64)
	if err != nil {
		return 0
	}
	return pages * uint64(pageSize)
}

// clockTicksPerSecond is USER_HZ, which is 100 on every Linux architecture Go
// supports: it is the unit of /proc/<pid>/stat's starttime, and Go has no
// sysconf without cgo.
const clockTicksPerSecond = 100

// maxStartSeconds bounds boot time and a process's start offset, in seconds:
// 2^40 is about 35,000 years, far past any real value and far inside int64, so
// a corrupt /proc cannot overflow the arithmetic below.
const (
	maxStartSeconds = 1 << 40
	maxStartTicks   = maxStartSeconds * clockTicksPerSecond
)

// parseProcessStart is the process start time from /proc/self/stat and
// /proc/stat: boot time plus the stat's starttime ticks. The zero Time when
// either is malformed. The comm field of stat may contain spaces and
// parentheses, so fields are counted from the last ")".
func parseProcessStart(stat, procStat string) time.Time {
	end := strings.LastIndexByte(stat, ')')
	if end < 0 {
		return time.Time{}
	}
	f := strings.Fields(stat[end+1:])
	// f[0] is field 3 (state), so starttime (field 22) is f[19].
	if len(f) < 20 {
		return time.Time{}
	}
	ticks, err := strconv.ParseUint(f[19], 10, 64)
	if err != nil || ticks > maxStartTicks {
		return time.Time{}
	}
	var btime int64
	for line := range strings.SplitSeq(procStat, "\n") {
		if v, ok := strings.CutPrefix(line, "btime "); ok {
			btime, err = strconv.ParseInt(strings.TrimSpace(v), 10, 64)
			if err != nil {
				return time.Time{}
			}
		}
	}
	if btime <= 0 || btime > maxStartSeconds {
		return time.Time{}
	}
	// Whole seconds and the sub-second remainder are added apart: ticks times
	// time.Second would overflow a Duration for a large starttime. ticks is
	// at most maxStartTicks, so the conversion to int64 cannot wrap.
	t := int64(ticks)
	return time.Unix(btime+t/clockTicksPerSecond, t%clockTicksPerSecond*int64(time.Second/clockTicksPerSecond))
}

// parseUptime is /proc/uptime's first field.
func parseUptime(text string) time.Duration {
	f := strings.Fields(text)
	if len(f) == 0 {
		return 0
	}
	secs, err := strconv.ParseFloat(f[0], 64)
	if err != nil || secs < 0 {
		return 0
	}
	return time.Duration(secs * float64(time.Second))
}

// detectContainer reports whether the process runs in a container: the runtime
// marker files exist, the "container" variable is set (podman, systemd-nspawn,
// LXC), or PID 1's cgroup path names a container runtime.
func detectContainer(env string, exists func(path string) bool, cgroup string) bool {
	if env != "" || exists("/.dockerenv") || exists("/run/.containerenv") {
		return true
	}
	for line := range strings.SplitSeq(cgroup, "\n") {
		for _, marker := range [...]string{"/docker/", "/kubepods", "/containerd/", "/libpod-", "/lxc/", "/crio-"} {
			if strings.Contains(line, marker) {
				return true
			}
		}
	}
	return false
}
