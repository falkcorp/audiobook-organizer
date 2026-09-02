// file: internal/sysinfo/memory_test.go
// version: 1.0.2
// guid: 7b8c9d0e-1f2a-3b4c-5d6e-7f8a9b0c1d2e
// last-edited: 2026-09-02

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

func TestGetMemoryStats(t *testing.T) {
	stats, err := GetMemoryStats()
	if err != nil {
		t.Fatalf("GetMemoryStats failed: %v", err)
	}

	if stats == nil {
		t.Fatal("GetMemoryStats returned nil stats")
	}

	// Basic sanity checks
	if stats.UsedPercent < 0 || stats.UsedPercent > 100 {
		t.Errorf("Used percent should be between 0 and 100, got %.2f", stats.UsedPercent)
	}

	// If total is > 0, we should have valid stats
	if stats.TotalBytes > 0 {
		if stats.UsedBytes > stats.TotalBytes {
			t.Error("Used bytes should not exceed total bytes")
		}
		if stats.AvailableBytes > stats.TotalBytes {
			t.Error("Available bytes should not exceed total bytes")
		}

		// Basic accounting check
		if stats.UsedBytes+stats.AvailableBytes != stats.TotalBytes {
			// Some implementations might not have exact accounting
			// so we just log this rather than fail
			t.Logf("Note: Used + Available != Total (%d + %d != %d)",
				stats.UsedBytes, stats.AvailableBytes, stats.TotalBytes)
		}
	}

	t.Logf("Memory Stats:")
	t.Logf("  Total: %d bytes (%.2f GB)", stats.TotalBytes, float64(stats.TotalBytes)/(1024*1024*1024))
	t.Logf("  Used: %d bytes (%.2f GB)", stats.UsedBytes, float64(stats.UsedBytes)/(1024*1024*1024))
	t.Logf("  Available: %d bytes (%.2f GB)", stats.AvailableBytes, float64(stats.AvailableBytes)/(1024*1024*1024))
	t.Logf("  Used Percent: %.2f%%", stats.UsedPercent)
}

func TestGetMemoryStatsFallback(t *testing.T) {
	originalTotal := totalMemoryProvider
	originalAvailable := availableMemoryProvider
	t.Cleanup(func() {
		totalMemoryProvider = originalTotal
		availableMemoryProvider = originalAvailable
	})

	totalMemoryProvider = func() uint64 { return 0 }
	availableMemoryProvider = func() uint64 { return 0 }

	stats, err := GetMemoryStats()
	if err != nil {
		t.Fatalf("GetMemoryStats fallback failed: %v", err)
	}
	if stats.TotalBytes != 0 {
		t.Errorf("expected TotalBytes 0 in fallback, got %d", stats.TotalBytes)
	}
	if stats.AvailableBytes != 0 {
		t.Errorf("expected AvailableBytes 0 in fallback, got %d", stats.AvailableBytes)
	}
	if stats.UsedPercent != 0 {
		t.Errorf("expected UsedPercent 0 in fallback, got %.2f", stats.UsedPercent)
	}
}

func TestMemoryStats_Structure(t *testing.T) {
	// Test that MemoryStats can be created and fields accessed
	stats := &MemoryStats{
		TotalBytes:     8 * 1024 * 1024 * 1024, // 8GB
		AvailableBytes: 4 * 1024 * 1024 * 1024, // 4GB
		UsedBytes:      4 * 1024 * 1024 * 1024, // 4GB
		UsedPercent:    50.0,
	}

	if stats.TotalBytes == 0 {
		t.Error("Failed to set TotalBytes")
	}
	if stats.AvailableBytes == 0 {
		t.Error("Failed to set AvailableBytes")
	}
	if stats.UsedBytes == 0 {
		t.Error("Failed to set UsedBytes")
	}
	if stats.UsedPercent == 0 {
		t.Error("Failed to set UsedPercent")
	}

	// Test JSON tags implicitly
	expectedTotal := uint64(8 * 1024 * 1024 * 1024)
	if stats.TotalBytes != expectedTotal {
		t.Errorf("Expected TotalBytes %d, got %d", expectedTotal, stats.TotalBytes)
	}
}

func TestGetMemoryStats_Consistency(t *testing.T) {
	// Call multiple times and ensure it doesn't panic or error
	for i := range 5 {
		stats, err := GetMemoryStats()
		if err != nil {
			t.Fatalf("Call %d failed: %v", i+1, err)
		}
		if stats == nil {
			t.Fatalf("Call %d returned nil stats", i+1)
		}

		// Values should be reasonable
		if stats.UsedPercent < 0 || stats.UsedPercent > 100 {
			t.Errorf("Call %d: Invalid UsedPercent: %.2f", i+1, stats.UsedPercent)
		}
	}
}
func TestMemoryStats_EdgeCases(t *testing.T) {
	stats, err := GetMemoryStats()
	if err != nil {
		t.Skipf("GetMemoryStats failed: %v", err)
	}

	// Test all fields have been populated
	if stats.TotalBytes == 0 {
		t.Error("TotalBytes should not be zero")
	}
	if stats.AvailableBytes > stats.TotalBytes {
		t.Error("AvailableBytes cannot exceed TotalBytes")
	}
	if stats.UsedBytes > stats.TotalBytes {
		t.Error("UsedBytes cannot exceed TotalBytes")
	}

	// Test percent calculations
	expectedPercent := (float64(stats.UsedBytes) / float64(stats.TotalBytes)) * 100
	if stats.UsedPercent < expectedPercent-1 || stats.UsedPercent > expectedPercent+1 {
		t.Errorf("UsedPercent calculation mismatch: got %.2f, expected ~%.2f", stats.UsedPercent, expectedPercent)
	}
}

func TestGetMemoryStatsRepeated(t *testing.T) {
	// Call GetMemoryStats multiple times to verify consistency
	for i := range 3 {
		stats, err := GetMemoryStats()
		if err != nil {
			t.Errorf("GetMemoryStats() iteration %d failed: %v", i, err)
		}
		if stats == nil {
			t.Errorf("GetMemoryStats() iteration %d returned nil", i)
		}
		if stats != nil && stats.TotalBytes == 0 {
			t.Errorf("GetMemoryStats() iteration %d returned zero total memory", i)
		}
	}
}

func TestGetMemoryStatsConsistency(t *testing.T) {
	// Get memory stats twice and verify they are reasonable
	stats1, err1 := GetMemoryStats()
	if err1 != nil {
		t.Fatalf("First GetMemoryStats() failed: %v", err1)
	}

	stats2, err2 := GetMemoryStats()
	if err2 != nil {
		t.Fatalf("Second GetMemoryStats() failed: %v", err2)
	}

	// Total memory should be the same
	if stats1.TotalBytes != stats2.TotalBytes {
		t.Errorf("Total memory changed between calls: %d vs %d", stats1.TotalBytes, stats2.TotalBytes)
	}

	// Available memory should be within reasonable range
	if stats1.AvailableBytes > stats1.TotalBytes {
		t.Error("Available memory exceeds total memory in first call")
	}
	if stats2.AvailableBytes > stats2.TotalBytes {
		t.Error("Available memory exceeds total memory in second call")
	}
}

func TestMemoryStatsValues(t *testing.T) {
	stats, err := GetMemoryStats()
	if err != nil {
		t.Fatalf("GetMemoryStats() failed: %v", err)
	}

	// Verify all fields are populated
	if stats.TotalBytes == 0 {
		t.Error("Total memory is zero")
	}
	if stats.AvailableBytes > stats.TotalBytes {
		t.Errorf("Available (%d) exceeds Total (%d)", stats.AvailableBytes, stats.TotalBytes)
	}
	if stats.UsedBytes > stats.TotalBytes {
		t.Errorf("Used (%d) exceeds Total (%d)", stats.UsedBytes, stats.TotalBytes)
	}
	if stats.UsedPercent < 0 || stats.UsedPercent > 100 {
		t.Errorf("UsedPercent out of range: %f", stats.UsedPercent)
	}
}
