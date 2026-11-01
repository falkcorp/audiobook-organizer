// file: internal/audiobooks/trash_retention_cutoff_test.go
// version: 1.0.0
// guid: b6c0f78e-322e-4496-9828-229e17d721c4
// last-edited: 2026-11-01

package audiobooks

import (
	"testing"
	"time"
)

// A retention of N days is N*24h on both DST transitions, not the calendar
// day AddDate gives (25h the day clocks fall back, 23h the day they spring
// forward). The CI agents run in America/New_York, where this broke
// TestRunPurge_PreviewBoundRespectsTheRetentionWindow on 2026-11-01.
func TestTrashRetentionCutoff_IsFixedHoursAcrossDST(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("no tzdata: %v", err)
	}
	for _, now := range []time.Time{
		time.Date(2026, time.November, 1, 12, 0, 0, 0, ny), // fall back
		time.Date(2026, time.March, 8, 12, 0, 0, 0, ny),    // spring forward
	} {
		for _, days := range []int{1, 30} {
			got := now.Sub(trashRetentionCutoff(now, days))
			if want := time.Duration(days) * 24 * time.Hour; got != want {
				t.Errorf("%s, %d day(s): cutoff is %v before now, want %v", now.Format(time.DateOnly), days, got, want)
			}
		}
	}
}
