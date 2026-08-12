// file: internal/config/scheduled_library_scan_test.go
// version: 1.0.0
// guid: 0f6a3c81-4b52-4e97-8d10-9c74be23a5df
// last-edited: 2026-08-11

package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestStoredSettingsDoNotDisableTheScheduledLibraryScan is the guard against
// the shape of bug that ResetToDefaults() already had: scheduled.library_scan
// is the ONE member of the scheduled.* family that ships enabled, so any code
// path that rebuilds the Scheduled sub-struct without carrying it silently
// turns automatic book discovery back off.
//
// Every deployment that predates this key has no stored setting for it. This
// pins that applySetting's per-leaf-key switch leaves the shipped default
// alone rather than decoding an absent key into a zero-valued
// ScheduledTaskConfig (Enabled=false, Interval=0 → no ticker).
func TestStoredSettingsDoNotDisableTheScheduledLibraryScan(t *testing.T) {
	restore := Snapshot()
	t.Cleanup(func() { AppConfig = restore })

	ResetToDefaults()
	require.True(t, AppConfig.Scheduled.LibraryScan.Enabled, "precondition: default should be enabled")
	require.Equal(t, 360, AppConfig.Scheduled.LibraryScan.Interval, "precondition: default should be 360 min")

	// Replay the kind of settings an existing install has stored — a mix of
	// scheduled.* siblings and unrelated keys, none of which is library_scan.
	stored := []struct{ key, value, typ string }{
		{"scheduled_dedup_refresh_enabled", "true", "bool"},
		{"scheduled_dedup_refresh_interval", "720", "int"},
		{"scheduled_db_optimize_enabled", "true", "bool"},
		{"scheduled_series_prune_enabled", "true", "bool"},
		{"scan_on_startup", "false", "bool"},
		{"auto_scan_enabled", "false", "bool"},
		{"maintenance_window_library_scan", "false", "bool"},
	}
	for _, s := range stored {
		require.NoError(t, applySetting(s.key, s.value, s.typ), "applySetting(%s)", s.key)
	}

	assert.True(t, AppConfig.Scheduled.LibraryScan.Enabled,
		"replaying a pre-existing settings blob must not disable the periodic library scan")
	assert.Equal(t, 360, AppConfig.Scheduled.LibraryScan.Interval,
		"replaying a pre-existing settings blob must not zero the periodic scan interval")
	// Sanity: the siblings that WERE stored did get applied, so the test is
	// exercising a live code path rather than a no-op.
	assert.True(t, AppConfig.Scheduled.DedupRefresh.Enabled)
	assert.Equal(t, 720, AppConfig.Scheduled.DedupRefresh.Interval)
}

// TestScheduledLibraryScanSettingsAreApplicable pins that the new keys are
// operable the same way their eight siblings are — an operator can turn the
// periodic scan off or retune the interval through the settings path instead
// of only through the config file or environment.
func TestScheduledLibraryScanSettingsAreApplicable(t *testing.T) {
	restore := Snapshot()
	t.Cleanup(func() { AppConfig = restore })

	ResetToDefaults()

	require.NoError(t, applySetting("scheduled_library_scan_interval", "30", "int"))
	assert.Equal(t, 30, AppConfig.Scheduled.LibraryScan.Interval)

	require.NoError(t, applySetting("scheduled_library_scan_on_startup", "true", "bool"))
	assert.True(t, AppConfig.Scheduled.LibraryScan.OnStartup)

	require.NoError(t, applySetting("scheduled_library_scan_enabled", "false", "bool"))
	assert.False(t, AppConfig.Scheduled.LibraryScan.Enabled)
}

// TestResetToDefaultsKeepsPeriodicScanOn pins the ResetToDefaults() literal,
// which builds a whole Config from scratch: omitting the Scheduled sub-struct
// there would leave a factory-reset install with automatic discovery off while
// viper's defaults claimed it was on.
func TestResetToDefaultsKeepsPeriodicScanOn(t *testing.T) {
	restore := Snapshot()
	t.Cleanup(func() { AppConfig = restore })

	AppConfig.Scheduled.LibraryScan = ScheduledTaskConfig{Enabled: false, Interval: 0, OnStartup: true}
	ResetToDefaults()

	assert.True(t, AppConfig.Scheduled.LibraryScan.Enabled, "factory reset must leave the periodic scan enabled")
	assert.Equal(t, 360, AppConfig.Scheduled.LibraryScan.Interval, "factory reset must restore the 6h interval")
	assert.False(t, AppConfig.Scheduled.LibraryScan.OnStartup)
}
