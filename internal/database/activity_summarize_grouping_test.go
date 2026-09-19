// file: internal/database/activity_summarize_grouping_test.go
// version: 1.0.0
// guid: d4544dd5-4577-4b7b-8e9c-59e9fdfba58b
// last-edited: 2026-09-19

package database

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// summarizeBackend is the slice of the store surface these tests drive, so one
// body runs against all three activity backends (Pebble, SQLite, Nuts).
type summarizeBackend interface {
	Record(ActivityEntry) (int64, error)
	Query(context.Context, ActivityFilter) ([]ActivityEntry, int, error)
	Summarize(context.Context, time.Time, string) (int, error)
}

func summarizeBackends(t *testing.T) map[string]func() summarizeBackend {
	return map[string]func() summarizeBackend{
		"pebble": func() summarizeBackend { return newTestPebbleActivityStore(t) },
		"sqlite": func() summarizeBackend { return newTestSQLStore(t) },
		"nuts":   func() summarizeBackend { return newTestNutsActivityStore(t) },
	}
}

// TestSummarize_DoesNotGroupByOperationID: an operation id is unique per run,
// so grouping by it made Summarize write one summary row per operation — a
// day with 500 operations became 500 rows instead of a handful. The group key
// is (day, type, source); operation ids survive only as a distinct count and a
// capped sample in the summary's details.
func TestSummarize_DoesNotGroupByOperationID(t *testing.T) {
	for name, mk := range summarizeBackends(t) {
		t.Run(name, func(t *testing.T) {
			s := mk()
			ctx := context.Background()
			day := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
			const ops = 500
			for i := range ops {
				for _, src := range []string{"library", "metadata"} {
					_, err := s.Record(ActivityEntry{
						Tier:        "change",
						Type:        "library.scan",
						Level:       "info",
						Source:      src,
						OperationID: fmt.Sprintf("OP%05d", i),
						Summary:     "progress",
						Timestamp:   day.Add(time.Duration(i) * time.Second),
					})
					require.NoError(t, err)
				}
			}

			deleted, err := s.Summarize(ctx, day.Add(48*time.Hour), "change")
			require.NoError(t, err)
			assert.Equal(t, 2*ops, deleted)

			rows, total, err := s.Query(ctx, ActivityFilter{Tier: "change", Limit: 50})
			require.NoError(t, err)
			require.Equal(t, 2, total, "one summary per (day, type, source), not one per operation")

			seen := map[string]bool{}
			for _, e := range rows {
				assert.Equal(t, "summarize", e.Source)
				assert.Empty(t, e.OperationID, "a summary spans many operations; it must not claim one")
				n, _, _ := summaryDateSpan(t, e.Summary)
				assert.Equal(t, ops, n)
				require.NotNil(t, e.Details)
				src, _ := e.Details["summarized_source"].(string)
				seen[src] = true
				assert.EqualValues(t, ops, e.Details["distinct_ops"])
				sample, ok := e.Details["op_ids_sample"].([]any)
				require.True(t, ok, "op_ids_sample must be a list, got %T", e.Details["op_ids_sample"])
				assert.Len(t, sample, summarizeOpSampleMax)
				assert.Equal(t, "OP00000", sample[0], "sample is the lowest ids, deterministic across backends")
				assert.Equal(t, true, e.Details["op_ids_truncated"])
			}
			assert.True(t, seen["library"] && seen["metadata"], "each source keeps its own summary: %v", seen)
		})
	}
}

// TestSummarize_OldFormatSummaryStillReadable: summary rows written before the
// regrouping carry a single operation_id and no details. They must still come
// back from Query unchanged, and a later Summarize must not fold them again.
func TestSummarize_OldFormatSummaryStillReadable(t *testing.T) {
	for name, mk := range summarizeBackends(t) {
		t.Run(name, func(t *testing.T) {
			s := mk()
			ctx := context.Background()
			pruned := time.Date(2025, 6, 2, 0, 0, 0, 0, time.UTC)
			_, err := s.Record(ActivityEntry{
				Tier:        "change",
				Type:        "library.scan",
				Level:       "info",
				Source:      "summarize",
				OperationID: "OLDOP",
				Summary:     "Summary: 3 library.scan entries (2025-06-01T00:00:00Z to 2025-06-01T00:02:00Z)",
				Timestamp:   pruned,
				PrunedAt:    &pruned,
			})
			require.NoError(t, err)

			if name != "nuts" { // Nuts never set PrunedAt on its summaries; see Summarize.
				deleted, err := s.Summarize(ctx, pruned.Add(48*time.Hour), "change")
				require.NoError(t, err)
				assert.Zero(t, deleted, "an already-summarized row is not summarized again")
			}
			rows, total, err := s.Query(ctx, ActivityFilter{Tier: "change", Limit: 10})
			require.NoError(t, err)
			require.Equal(t, 1, total)
			assert.Equal(t, "OLDOP", rows[0].OperationID)
			assert.Nil(t, rows[0].Details["distinct_ops"])
		})
	}
}
