// file: internal/sysinfo/memory_linux.go
// version: 1.0.1
// guid: 9c0d1e2f-3a4b-5c6d-7e8f-9a0b1c2d3e4f
// last-edited: 2026-10-18

//go:build linux

package sysinfo

import (
	"bufio"
	"os"
	"strconv"
	"strings"
)

// getTotalMemoryPlatform returns total system memory on Linux
func getTotalMemoryPlatform() uint64 {
	file, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "MemTotal:") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				kb, err := strconv.ParseUint(fields[1], 10, 64)
				if err == nil {
					return kb * 1024 // Convert KB to bytes
				}
			}
		}
	}

	return 0
}
