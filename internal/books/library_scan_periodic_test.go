// file: internal/books/library_scan_periodic_test.go
// version: 0.1.0
// guid: 1d9e4b72-3c58-4a06-b8f1-7e2a5c90d43b
// last-edited: 2026-11-21

package books

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
)

// TestLibraryScanEnabledWritesPeriodicEnabled: HEAD's scheduled.library_scan
// enabled lives in modules.books.scan.periodic_enabled after the flip (form A),
// and the schedule key stays enabled. A HEAD-shaped save of the whole entry,
// as the settings page sends it, reaches the new key; so does a save of the
// leaf; and the projection reads both back into HEAD's fields.
func TestLibraryScanEnabledWritesPeriodicEnabled(t *testing.T) {
	bootFlippedFresh(t)
	f := config.Current()
	require.NotNil(t, f)
	set := func(head string, v any) {
		t.Helper()
		b, err := json.Marshal(v)
		require.NoError(t, err)
		_, err = f.Update(t.Context(), settingsAdmin, config.Update{Set: map[string]json.RawMessage{head: b}})
		require.NoError(t, err)
	}

	set("scheduled.library_scan", map[string]any{"enabled": false, "interval": 120})
	snap := config.Snapshot()
	require.False(t, snap.Scheduled.LibraryScan.Enabled, "the whole-entry save did not turn the timer off")
	require.Equal(t, 120, snap.Scheduled.LibraryScan.Interval)

	set("scheduled.library_scan.enabled", true)
	snap = config.Snapshot()
	require.True(t, snap.Scheduled.LibraryScan.Enabled, "the leaf save did not turn the timer on")
	require.Equal(t, 120, snap.Scheduled.LibraryScan.Interval, "the leaf save changed the interval")
}
