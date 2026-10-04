// file: internal/database/activity_summarize_grouping_test.go
// version: 1.1.1
// guid: d4544dd5-4577-4b7b-8e9c-59e9fdfba58b
// last-edited: 2026-10-03

package database

import (
	"context"
	"errors"
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
			entries := make([]ActivityEntry, 0, 2*ops)
			for i := range ops {
				for _, src := range []string{"library", "metadata"} {
					entries = append(entries, ActivityEntry{
						Tier:        "change",
						Type:        "library.scan",
						Level:       "info",
						Source:      src,
						OperationID: fmt.Sprintf("OP%05d", i),
						Summary:     "progress",
						Timestamp:   day.Add(time.Duration(i) * time.Second),
					})
				}
			}
			seedSummarizeEntries(t, s, entries)

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

// seedSummarizeEntries writes entries through RecordBatch on a backend that
// has one (Pebble, SQLite) and through Record otherwise (Nuts). RecordBatch
// runs the same per-row encoding and content key as Record in one commit;
// these tests are about Summarize, and on SQLite a commit per row made seeding
// 1,000 rows take 25 s on a loaded Mac (2026-10-03), against a few
// milliseconds for the Summarize it set up.
func seedSummarizeEntries(t *testing.T, s summarizeBackend, entries []ActivityEntry) {
	t.Helper()
	if b, ok := s.(batchRecorder); ok {
		got, err := b.RecordBatch(entries)
		require.NoError(t, err)
		require.Equal(t, len(entries), got, "every seeded row must be inserted")
		return
	}
	for _, e := range entries {
		_, err := s.Record(e)
		require.NoError(t, err)
	}
}

// seedSummarizeGroup records n change-tier rows of one (day, type, source)
// group, each under its own operation id OP00000.. so the ids sort by index.
func seedSummarizeGroup(t *testing.T, s summarizeBackend, day time.Time, n int) {
	t.Helper()
	entries := make([]ActivityEntry, 0, n)
	for i := range n {
		entries = append(entries, ActivityEntry{
			Tier: "change", Type: "library.scan", Level: "info", Source: "library",
			OperationID: fmt.Sprintf("OP%05d", i), Summary: "progress",
			Timestamp: day.Add(time.Duration(i) * time.Second),
		})
	}
	seedSummarizeEntries(t, s, entries)
}

// TestSummarize_OperationIDFindsSummary: a sampled operation id must lead to
// the summary that absorbed its rows, through the same ?operation_id= filter
// the UI and the operations drill-down use. Without this the ids in
// op_ids_sample were unreachable by any filter.
func TestSummarize_OperationIDFindsSummary(t *testing.T) {
	for name, mk := range summarizeBackends(t) {
		if name == "nuts" {
			continue // legacy backend: its op filter reads a per-op bucket only; see NutsActivityStore.Summarize
		}
		t.Run(name, func(t *testing.T) {
			s := mk()
			ctx := context.Background()
			day := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
			seedSummarizeGroup(t, s, day, 30)
			_, err := s.Summarize(ctx, day.Add(48*time.Hour), "change")
			require.NoError(t, err)

			rows, total, err := s.Query(ctx, ActivityFilter{OperationID: "OP00003", Limit: 10})
			require.NoError(t, err)
			require.Equal(t, 1, total, "a sampled operation id finds its summary")
			assert.Equal(t, "summarize", rows[0].Source)

			_, total, err = s.Query(ctx, ActivityFilter{OperationID: "OP00025", Limit: 10})
			require.NoError(t, err)
			assert.Zero(t, total, "an id beyond the 20-id sample is not claimed")

			_, total, err = s.Query(ctx, ActivityFilter{Tags: []string{"op:OP00003"}, Limit: 10})
			require.NoError(t, err)
			assert.Equal(t, 1, total, "the op: tag chip filter finds it too")
		})
	}
}

// TestSummarize_ChunkedDeleteNeverLosesRows: a group's originals are deleted
// in bounded chunks, the summary committed atomically with the FIRST chunk. A
// pass stopped after that first commit leaves the rest of the group in place
// (already represented by the summary) — it never leaves rows deleted without
// a summary, and never one unbounded delete.
func TestSummarize_ChunkedDeleteNeverLosesRows(t *testing.T) {
	for name, mk := range summarizeBackends(t) {
		if name == "nuts" {
			continue
		}
		t.Run(name, func(t *testing.T) {
			s := mk()
			day := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
			seedSummarizeGroup(t, s, day, 25)

			oldP, oldS := pactSummarizeDeleteBatch, sqlActSummarizeChunk
			pactSummarizeDeleteBatch, sqlActSummarizeChunk = 10, 10
			ctx, cancel := context.WithCancel(context.Background())
			chunks := 0
			summarizeTestHooks.afterChunk = func() { chunks++; cancel() }
			t.Cleanup(func() {
				pactSummarizeDeleteBatch, sqlActSummarizeChunk = oldP, oldS
				summarizeTestHooks.afterChunk = nil
				cancel()
			})

			deleted, err := s.Summarize(ctx, day.Add(48*time.Hour), "change")
			require.True(t, errors.Is(err, context.Canceled), "stopped after the first chunk: %v", err)
			assert.Equal(t, 10, deleted)
			assert.Equal(t, 1, chunks)

			rows, total, err := s.Query(context.Background(), ActivityFilter{Tier: "change", Limit: 50})
			require.NoError(t, err)
			require.Equal(t, 16, total, "one summary + 15 originals not yet deleted")
			var summaries int
			for _, e := range rows {
				if e.Source == "summarize" {
					summaries++
					n, _, _ := summaryDateSpan(t, e.Summary)
					assert.Equal(t, 25, n, "the summary describes the whole group")
				}
			}
			assert.Equal(t, 1, summaries)
		})
	}
}
