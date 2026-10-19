// file: internal/database/pebble_activity_progress_test.go
// version: 1.0.0
// guid: 4d7c1e92-6b3a-4f58-a0c4-9e2b7d5f1a83
// last-edited: 2026-10-19

package database

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seedOldChangeRows records `n` change-tier rows on distinct UTC days in
// January 2026, all well before the cutoffs the tests below use.
func seedOldChangeRows(t *testing.T, s *PebbleActivityStore, n int) {
	t.Helper()
	base := time.Date(2026, 1, 10, 9, 0, 0, 0, time.UTC)
	for i := range n {
		_, err := s.Record(ActivityEntry{
			Timestamp: base.AddDate(0, 0, i), Tier: "change", Type: "metadata_changed",
			Level: "info", Source: "test", Summary: "applied metadata to book",
		})
		require.NoError(t, err, "record row %d", i)
	}
}

// TestPebbleActivityStore_CompactByDayReportsProgress pins that the real
// Pebble store emits progress events through a hook attached to the context.
// The maintenance op forwards these to reporter.UpdateProgress, the only thing
// that keeps the registry watchdog from cancelling a long compaction as
// never_reported; a store that compacted silently would pass every other test.
func TestPebbleActivityStore_CompactByDayReportsProgress(t *testing.T) {
	const days = 2
	s := newTestPebbleActivityStore(t)
	seedOldChangeRows(t, s, days)

	var events []CompactProgressEvent
	ctx := WithCompactProgress(context.Background(), func(ev CompactProgressEvent) {
		events = append(events, ev)
	})
	res, err := s.CompactByDay(ctx, time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	require.Equal(t, CompactResult{DaysCompacted: days, EntriesDeleted: days}, res)

	require.NotEmpty(t, events, "the Pebble store emitted no compaction progress; the op watchdog would see a silent op")
	for _, ev := range events {
		assert.Equal(t, "pebble", ev.Backend)
	}
	// The last event carries the store's final running totals.
	assert.Equal(t, res, events[len(events)-1].Result)
}

// TestPebbleActivityStore_SummarizeReportsLiveness pins the liveness wiring
// the nightly cleanup depends on: the real store emits a summarize-phase
// event as it works, through the WithMaintenanceProgress hook on ctx.
func TestPebbleActivityStore_SummarizeReportsLiveness(t *testing.T) {
	const rows = 3
	s := newTestPebbleActivityStore(t)
	seedOldChangeRows(t, s, rows)

	var events []MaintenanceProgressEvent
	ctx := WithMaintenanceProgress(context.Background(), func(ev MaintenanceProgressEvent) {
		events = append(events, ev)
	})
	n, err := s.Summarize(ctx, time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), "change")
	require.NoError(t, err)
	require.Equal(t, rows, n, "fixture: Summarize should have folded every seeded row")

	require.NotEmpty(t, events, "the Pebble store emitted no progress while summarizing")
	for _, ev := range events {
		assert.Equal(t, MaintenancePhaseSummarize, ev.Phase)
		assert.Equal(t, "pebble", ev.Backend)
	}
}
