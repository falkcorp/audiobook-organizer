// file: internal/itunes/service/duration_test.go
// version: 1.0.0
// guid: 4d1e8b2c-6a09-47f3-9c5b-0e2f7a1d8c64
// last-edited: 2026-06-19

package itunesservice

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/itunes"
)

// TestTrackDurationSeconds verifies iTunes TotalTime (milliseconds) is converted
// to BookFile.Duration (seconds). Regression guard for CONS-16: per-file durations
// were stored raw in ms, which RecomputeBookAggregates then summed and used to
// clobber the correct seconds-valued Book.Duration.
func TestTrackDurationSeconds(t *testing.T) {
	tests := []struct {
		name      string
		totalTime int64 // milliseconds (iTunes units)
		want      int   // seconds (BookFile.Duration units)
	}{
		{"two minutes", 123000, 123},
		{"one hour", 3600000, 3600},
		{"zero", 0, 0},
		{"sub-second truncates", 999, 0},
		{"rounds toward zero", 1500, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := trackDurationSeconds(&itunes.Track{TotalTime: tt.totalTime})
			if got != tt.want {
				t.Errorf("trackDurationSeconds(%d ms) = %d, want %d s", tt.totalTime, got, tt.want)
			}
		})
	}
}
