// file: internal/sysinfo/memory_test.go
// version: 1.0.3
// guid: 7b8c9d0e-1f2a-3b4c-5d6e-7f8a9b0c1d2e
// last-edited: 2026-10-18

package sysinfo

import (
	"testing"
)

func TestGetTotalMemory(t *testing.T) {
	total := GetTotalMemory()

	// Memory should be either 0 (not implemented) or > 0 (implemented)
	// On most systems, if implemented, should return > 0
	t.Logf("Total memory: %d bytes (%.2f MB)", total, float64(total)/(1024*1024))
}

func TestGetTotalMemoryOverride(t *testing.T) {
	original := totalMemoryProvider
	t.Cleanup(func() {
		totalMemoryProvider = original
	})

	totalMemoryProvider = func() uint64 { return 123 }

	if got := GetTotalMemory(); got != 123 {
		t.Errorf("expected overridden total memory 123, got %d", got)
	}
}
