// file: core/internal/logjournal/prune_internal_test.go
// version: 1.0.0
// guid: 0d8c4b5e-91a7-4e36-8c2f-7a15e3b9d640
// last-edited: 2026-11-18

package logjournal

import (
	"errors"
	"math"
	"testing"
	"time"
)

// retentionCutoff is the one place retention_days becomes a time: it fails
// closed outside 1..MaxRetentionDays and never yields a cutoff at or after now.
func TestRetentionCutoff(t *testing.T) {
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	for _, days := range []int{0, -1, MaxRetentionDays + 1, 106_752, math.MaxInt, math.MinInt} {
		if _, err := retentionCutoff(now, days); !errors.Is(err, ErrRetentionOutOfRange) {
			t.Errorf("days %d: err = %v, want ErrRetentionOutOfRange", days, err)
		}
	}
	for _, days := range []int{1, 90, MaxRetentionDays} {
		c, err := retentionCutoff(now, days)
		if err != nil {
			t.Fatalf("days %d: %v", days, err)
		}
		if !c.Before(now) || !c.Equal(now.AddDate(0, 0, -days)) {
			t.Errorf("days %d: cutoff %v, want %v before %v", days, c, now.AddDate(0, 0, -days), now)
		}
	}
}
