// file: internal/database/activity_sources_exclude_test.go
// version: 1.0.0
// guid: 3c6f1d92-8a47-4e05-b9d1-7f20a5e84c13
// last-edited: 2026-10-30

package database

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// GetDistinctSources counts what the filtered list would show: an entry
// ActivityFilter.ExcludeEntry hides is not counted, and a source whose only
// entries are hidden is absent. The result is memoized per filter, and a
// predicate cannot be part of the cache key, so a filtered call must neither be
// served from nor stored in the cache - otherwise an unfiltered caller's counts
// leak to a restricted one (or the reverse).
func TestGetDistinctSources_HonoursExcludeEntry(t *testing.T) {
	s := newTestPebbleActivityStore(t)
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	rows := []ActivityEntry{
		{Source: "scanner", Summary: "scan 1", OperationID: "op-shown"},
		{Source: "scanner", Summary: "scan 2", OperationID: "op-hidden"},
		{Source: "scanner", Summary: "scan 3", OperationID: "op-shown"},
		{Source: "repairs", Summary: "plan", OperationID: "op-hidden"},
		{Source: "repairs", Summary: "apply", OperationID: "op-hidden"},
	}
	for i, e := range rows {
		e.Timestamp = base.Add(time.Duration(i) * time.Minute)
		e.Tier, e.Level = "change", "info"
		_, err := s.Record(e)
		require.NoError(t, err)
	}
	ctx := context.Background()
	hide := func(e ActivityEntry) bool { return e.OperationID == "op-hidden" }
	counts := func(f ActivityFilter) map[string]int {
		got, err := s.GetDistinctSources(ctx, f)
		require.NoError(t, err)
		m := map[string]int{}
		for _, sc := range got {
			m[sc.Source] = sc.Count
		}
		return m
	}

	// Unfiltered first, so its result is in the cache when the filtered call runs.
	assert.Equal(t, map[string]int{"scanner": 3, "repairs": 2}, counts(ActivityFilter{}))

	// The hidden entry is not counted; "repairs" has only hidden entries and is absent.
	assert.Equal(t, map[string]int{"scanner": 2}, counts(ActivityFilter{ExcludeEntry: hide}))

	// And the filtered result did not poison the cache for the next caller.
	assert.Equal(t, map[string]int{"scanner": 3, "repairs": 2}, counts(ActivityFilter{}))

	// A predicate that hides nothing changes nothing.
	assert.Equal(t, map[string]int{"scanner": 3, "repairs": 2},
		counts(ActivityFilter{ExcludeEntry: func(ActivityEntry) bool { return false }}))
}
