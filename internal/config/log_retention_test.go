// file: internal/config/log_retention_test.go
// version: 1.0.1
// guid: 2e7b4c91-0a6d-4f38-9c15-b8d3e6a0f472
// last-edited: 2026-11-06

// log_retention_days was never read by InitConfig, so AppConfig carried 0 on
// every boot and purge_old_logs was disabled everywhere; the first settings
// save then froze that 0 into the stored blob. These pin the repaired
// semantics: default 90 reaches AppConfig, 0 means the default, -1 is the
// only "keep forever", and a stored 0 is dropped from the blob once.

package config

import (
	"testing"

	logging "github.com/falkcorp/mediacore/log"
)

func TestFactoryDefaultsLogRetention(t *testing.T) {
	if got := factoryDefaultConfigForTest().LogRetentionDays; got != logging.DefaultLogRetentionDays {
		t.Fatalf("factory default LogRetentionDays = %d, want %d (the purge_old_logs task is disabled at 0)",
			got, logging.DefaultLogRetentionDays)
	}
}

func TestValidateNormalizesZeroLogRetention(t *testing.T) {
	cfg := factoryDefaultConfigForTest()
	for _, tc := range []struct{ in, want int }{
		{in: 0, want: logging.DefaultLogRetentionDays},
		{in: logging.LogRetentionKeepForever, want: logging.LogRetentionKeepForever},
		{in: 14, want: 14},
	} {
		c := cfg
		c.LogRetentionDays = tc.in
		if got := c.Normalized().LogRetentionDays; got != tc.want {
			t.Errorf("Normalized left log_retention_days=%d as %d, want %d", tc.in, got, tc.want)
		}
		_ = c.Validate()
		if c.LogRetentionDays != tc.in {
			t.Errorf("Validate changed its receiver: log_retention_days %d -> %d", tc.in, c.LogRetentionDays)
		}
	}
}
