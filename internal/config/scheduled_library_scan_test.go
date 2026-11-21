// file: internal/config/scheduled_library_scan_test.go
// version: 1.0.1
// guid: 0f6a3c81-4b52-4e97-8d10-9c74be23a5df
// last-edited: 2026-11-09

package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestResetToDefaultsKeepsPeriodicScanOn pins the ResetToDefaultsForTest() literal,
// which builds a whole Config from scratch: omitting the Scheduled sub-struct
// there would leave a factory-reset install with automatic discovery off while
// viper's defaults claimed it was on.
func TestResetToDefaultsKeepsPeriodicScanOn(t *testing.T) {
	restore := Snapshot()
	t.Cleanup(func() { AppConfig = restore })

	AppConfig.Scheduled.LibraryScan = ScheduledTaskConfig{Enabled: false, Interval: 0, OnStartup: true}
	ResetToDefaultsForTest()

	assert.True(t, AppConfig.Scheduled.LibraryScan.Enabled, "factory reset must leave the periodic scan enabled")
	assert.Equal(t, 360, AppConfig.Scheduled.LibraryScan.Interval, "factory reset must restore the 6h interval")
	assert.False(t, AppConfig.Scheduled.LibraryScan.OnStartup)
}
