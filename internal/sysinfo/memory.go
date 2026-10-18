// file: internal/sysinfo/memory.go
// version: 1.0.2
// guid: 7a8b9c0d-1e2f-3a4b-5c6d-7e8f9a0b1c2d
// last-edited: 2026-10-18

package sysinfo

// totalMemoryProvider allows tests to override platform memory queries.
var totalMemoryProvider = getTotalMemoryPlatform

// GetTotalMemory returns the total system memory in bytes.
// Returns 0 if unable to determine (will be implemented per-platform).
func GetTotalMemory() uint64 {
	return totalMemoryProvider()
}
