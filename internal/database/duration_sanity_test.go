// file: internal/database/duration_sanity_test.go
// version: 1.1.0
// guid: c4e7a1b9-3d62-4f08-8a15-6b9e2c0f7d43
// last-edited: 2026-07-11

package database

import "testing"

func bytesForKbps(durationSec, kbps int) int64 {
	return int64(durationSec) * int64(kbps) * 1000 / 8
}

func TestDurationLooksLikeMillis(t *testing.T) {
	tests := []struct {
		name        string
		fileSize    int64
		durationSec int
		want        bool
	}{
		{"64kbps seconds", bytesForKbps(3600, 64), 3600, false},
		{"12kbps low-bitrate seconds", bytesForKbps(3600, 12), 3600, false},
		{"lossless seconds", bytesForKbps(3600, 1411), 3600, false},
		{"64kbps stored as ms", bytesForKbps(3600, 64), 3600 * 1000, true},
		{"12kbps stored as ms", bytesForKbps(3600, 12), 3600 * 1000, true},
		{"zero size", 0, 3600000, false},
		{"zero duration", bytesForKbps(3600, 64), 0, false},
		{"3kbps legit not over-corrected", bytesForKbps(3600, 3), 3600, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DurationLooksLikeMillis(tt.fileSize, tt.durationSec); got != tt.want {
				t.Errorf("DurationLooksLikeMillis(%d,%d)=%v want %v", tt.fileSize, tt.durationSec, got, tt.want)
			}
		})
	}
}

func TestNormalizeBookFileDuration(t *testing.T) {
	t.Run("repairs ms value", func(t *testing.T) {
		f := &BookFile{FileSize: bytesForKbps(3600, 64), Duration: 3600 * 1000}
		if !normalizeBookFileDuration(f) {
			t.Fatal("expected repair=true")
		}
		if f.Duration != 3600 {
			t.Errorf("Duration=%d want 3600", f.Duration)
		}
	})
	t.Run("leaves plausible seconds untouched", func(t *testing.T) {
		f := &BookFile{FileSize: bytesForKbps(3600, 64), Duration: 3600}
		if normalizeBookFileDuration(f) {
			t.Fatal("expected repair=false")
		}
		if f.Duration != 3600 {
			t.Errorf("Duration=%d want 3600 (unchanged)", f.Duration)
		}
	})
	t.Run("idempotent", func(t *testing.T) {
		f := &BookFile{FileSize: bytesForKbps(3600, 64), Duration: 3600 * 1000}
		normalizeBookFileDuration(f)
		if normalizeBookFileDuration(f) {
			t.Fatal("second pass must not re-divide a corrected value")
		}
		if f.Duration != 3600 {
			t.Errorf("Duration=%d want 3600", f.Duration)
		}
	})
	t.Run("no FileSize cannot judge", func(t *testing.T) {
		f := &BookFile{FileSize: 0, Duration: 3600 * 1000}
		if normalizeBookFileDuration(f) {
			t.Fatal("must not change a row it cannot bound")
		}
	})
	t.Run("nil safe", func(t *testing.T) {
		if normalizeBookFileDuration(nil) {
			t.Fatal("nil must return false")
		}
	})
}

func TestNormalizeDurationSecMillisDetected(t *testing.T) {
	size := bytesForKbps(3600, 64)
	if got := NormalizeDurationSec(size, 3600*1000); got != 3600 {
		t.Errorf("NormalizeDurationSec(%d, %d)=%d want 3600", size, 3600*1000, got)
	}
}

func TestNormalizeDurationSecPlausibleUnchanged(t *testing.T) {
	size := bytesForKbps(3600, 64)
	if got := NormalizeDurationSec(size, 3600); got != 3600 {
		t.Errorf("NormalizeDurationSec(%d, %d)=%d want 3600 (unchanged)", size, 3600, got)
	}
}

func TestNormalizeDurationSecUnknownUnchanged(t *testing.T) {
	// fileSize <= 0 → unknown, return unchanged even if the value looks ms-scale.
	if got := NormalizeDurationSec(0, 3600*1000); got != 3600*1000 {
		t.Errorf("NormalizeDurationSec(0, %d)=%d want %d (unchanged)", 3600*1000, got, 3600*1000)
	}
	// duration <= 0 → unknown, return unchanged.
	if got := NormalizeDurationSec(bytesForKbps(3600, 64), 0); got != 0 {
		t.Errorf("NormalizeDurationSec(size, 0)=%d want 0 (unchanged)", got)
	}
}
