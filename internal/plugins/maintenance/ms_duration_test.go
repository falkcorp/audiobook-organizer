// file: internal/plugins/maintenance/ms_duration_test.go
// version: 1.0.0
// guid: 5e2c8a91-3b47-4d06-8f1a-9c73e4b28d50
// last-edited: 2026-09-21

package maintenance

import (
	"context"
	"log/slog"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// mockReporter is a minimal mock implementing registry.Reporter for testing.
type mockReporter struct {
	logs []string
}

func (m *mockReporter) UpdateProgress(current, total int, message string) error {
	return nil
}

func (m *mockReporter) Log(level slog.Level, message string, attrs ...slog.Attr) error {
	m.logs = append(m.logs, message)
	return nil
}

func (m *mockReporter) Logger() *slog.Logger {
	return slog.Default()
}

func (m *mockReporter) Checkpoint(state any) error {
	return nil
}

func (m *mockReporter) IsCanceled() bool {
	return false
}

func (m *mockReporter) RunPhase(ctx context.Context, name string, fn func(context.Context, registry.Reporter) error) error {
	return fn(ctx, m)
}

func (m *mockReporter) Trigger(ctx context.Context, eventName string, payload any) error {
	return nil
}

func (m *mockReporter) SetCurrentItem(label string) {
	// no-op for test
}

// bytesForBitrate returns the file size in bytes for a clip of durationSec
// seconds encoded at kbps kilobits per second.
func bytesForBitrate(durationSec, kbps int) int64 {
	return int64(durationSec) * int64(kbps) * 1000 / 8
}

// TestDurationLooksLikeMillis exercises the implied-bitrate predicate that
// decides whether a stored BookFile.Duration is actually milliseconds (CONS-16).
// The key safety property (advisor review): a genuine low-bitrate audiobook must
// never be flagged, while every ms-inflated value must be.
func TestDurationLooksLikeMillis(t *testing.T) {
	tests := []struct {
		name        string
		fileSize    int64
		durationSec int
		want        bool
	}{
		// Correct seconds values across the realistic bitrate range — never flagged.
		{"64kbps correct seconds", bytesForBitrate(3600, 64), 3600, false},
		{"32kbps correct seconds", bytesForBitrate(3600, 32), 3600, false},
		{"low 12kbps correct seconds", bytesForBitrate(3600, 12), 3600, false},
		{"lossless 1411kbps correct", bytesForBitrate(3600, 1411), 3600, false},

		// Same files, but duration stored as milliseconds (×1000) — must flag.
		{"64kbps stored as ms", bytesForBitrate(3600, 64), 3600 * 1000, true},
		{"32kbps stored as ms", bytesForBitrate(3600, 32), 3600 * 1000, true},
		{"12kbps stored as ms", bytesForBitrate(3600, 12), 3600 * 1000, true},

		// Cannot decide / nothing to fix.
		{"zero file size", 0, 3600000, false},
		{"zero duration", bytesForBitrate(3600, 64), 0, false},

		// Pathological: a genuine but absurdly low 3kbps clip. Dividing by 1000
		// would imply ~3000kbps, above any real codec, so the upper sanity bound
		// must REJECT the false positive rather than corrupt the row.
		{"3kbps legit not over-corrected", bytesForBitrate(3600, 3), 3600, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := durationLooksLikeMillis(tt.fileSize, tt.durationSec)
			if got != tt.want {
				t.Errorf("durationLooksLikeMillis(size=%d, dur=%d) = %v, want %v",
					tt.fileSize, tt.durationSec, got, tt.want)
			}
		})
	}
}

// TestDurationBackfill_ParallelProducesCorrectResults verifies that the parallel
// version using registry.RunItems produces the same output as the serial version,
// and that no data race occurs on the shared bookOrder and fixesByBook maps.
