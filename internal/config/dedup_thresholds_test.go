// file: internal/config/dedup_thresholds_test.go
// version: 1.0.0
// guid: 4e2b7c91-8d3a-4f60-9b21-6c5e0a1f7d84
// last-edited: 2026-07-03

package config

import "testing"

// TestThresholdsForModel_FallsBackToFlat verifies that a model with no entry in
// EmbeddingThresholdsByModel resolves to the flat BookHighThreshold/
// BookLowThreshold values (byte-for-byte unchanged default behaviour).
func TestThresholdsForModel_FallsBackToFlat(t *testing.T) {
	c := DedupConfig{
		BookHighThreshold: 0.95,
		BookLowThreshold:  0.85,
	}

	high, low := c.ThresholdsForModel("text-embedding-3-large")
	if high != 0.95 || low != 0.85 {
		t.Fatalf("absent model: got high=%v low=%v, want 0.95/0.85", high, low)
	}

	// Nil map is also a valid absent case.
	c.EmbeddingThresholdsByModel = map[string]EmbeddingModelThresholds{
		"bge-m3": {High: 0.90, Low: 0.78},
	}
	high, low = c.ThresholdsForModel("some-other-model")
	if high != 0.95 || low != 0.85 {
		t.Fatalf("unlisted model: got high=%v low=%v, want flat 0.95/0.85", high, low)
	}
}

// TestThresholdsForModel_UsesPerModelEntry verifies that a model present in the
// map resolves to its calibrated override, not the flat fallback.
func TestThresholdsForModel_UsesPerModelEntry(t *testing.T) {
	c := DedupConfig{
		BookHighThreshold: 0.95,
		BookLowThreshold:  0.85,
		EmbeddingThresholdsByModel: map[string]EmbeddingModelThresholds{
			"bge-m3": {High: 0.90, Low: 0.78},
		},
	}

	high, low := c.ThresholdsForModel("bge-m3")
	if high != 0.90 || low != 0.78 {
		t.Fatalf("present model: got high=%v low=%v, want 0.90/0.78", high, low)
	}
}
