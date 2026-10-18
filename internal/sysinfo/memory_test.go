// file: internal/sysinfo/memory_test.go
// version: 1.0.4
// guid: 7b8c9d0e-1f2a-3b4c-5d6e-7f8a9b0c1d2e
// last-edited: 2026-10-18

package sysinfo

import (
	"runtime"
	"testing"
)

func TestGetTotalMemory(t *testing.T) {
	total := GetTotalMemory()

	switch runtime.GOOS {
	case "linux", "darwin", "windows":
		// Each of these has a real platform probe (memory_<os>.go); a zero
		// here means the probe broke, not that it is unimplemented.
		if total == 0 {
			t.Fatalf("GetTotalMemory() = 0 on %s, want > 0", runtime.GOOS)
		}
	default:
		t.Skipf("no total-memory probe on %s", runtime.GOOS)
	}
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
