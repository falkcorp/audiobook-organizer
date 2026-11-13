// file: core/health/sysinfo/host_other.go
// version: 1.0.0
// guid: 13d73510-2379-4a59-9c3f-1ee3ce77a71c
// last-edited: 2026-11-13

//go:build !linux && !darwin && !windows

package sysinfo

import "time"

// hostReader reports nothing on a platform with no reader: every field is the
// zero value that means "the platform cannot say".
type hostReader struct{}

func (hostReader) totalMemory() uint64         { return 0 }
func (hostReader) availableMemory() uint64     { return 0 }
func (hostReader) rss() uint64                 { return 0 }
func (hostReader) systemUptime() time.Duration { return 0 }
func (hostReader) processStart() time.Time     { return time.Time{} }
func (hostReader) container() bool             { return false }
