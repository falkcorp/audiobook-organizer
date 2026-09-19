// file: internal/database/sql_activity_sources_test.go
// version: 1.0.0
// guid: 4328c194-881d-4326-8606-16234be725a4
// last-edited: 2026-09-19

package database

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSQLActivity_DistinctSourcesPlanIsCovering pins the query plan of the
// filters the Activity page's Sources picker actually sends (since/until, and
// tier for API callers). On prod a since=24h request took 11-103 s: the plan
// walked idx_act_ts and then fetched every matching row from the table — rows
// that carry a details BLOB — just to read one short source string. A covering
// index answers from the index alone.
func TestSQLActivity_DistinctSourcesPlanIsCovering(t *testing.T) {
	s := newTestSQLStore(t)
	since := time.Now().Add(-24 * time.Hour)
	until := time.Now()
	cases := map[string]ActivityFilter{
		"none":             {},
		"since":            {Since: &since},
		"since+until":      {Since: &since, Until: &until},
		"tier":             {Tier: "change"},
		"tier+since":       {Tier: "change", Since: &since},
		"tier+since+until": {Tier: "change", Since: &since, Until: &until},
	}
	for name, f := range cases {
		t.Run(name, func(t *testing.T) {
			q, args := s.distinctSourcesQuery(f)
			rows, err := s.reader.QueryContext(context.Background(), "EXPLAIN QUERY PLAN "+q, args...)
			require.NoError(t, err)
			defer rows.Close()
			var plan []string
			for rows.Next() {
				var id, parent, notused int
				var detail string
				require.NoError(t, rows.Scan(&id, &parent, &notused, &detail))
				plan = append(plan, detail)
			}
			require.NoError(t, rows.Err())
			joined := strings.Join(plan, " | ")
			assert.Contains(t, joined, "COVERING INDEX", "plan must not touch the table: %s", joined)
		})
	}
}

// TestSQLActivity_DistinctSourcesCounts checks the answer, not just the plan.
func TestSQLActivity_DistinctSourcesCounts(t *testing.T) {
	s := newTestSQLStore(t)
	now := time.Now()
	for i := range 30 {
		src := []string{"library", "metadata", "server"}[i%3]
		tier := "change"
		if i%5 == 0 {
			tier = "debug"
		}
		_, err := s.Record(ActivityEntry{Tier: tier, Type: "t", Level: "info", Source: src,
			Summary: "x", Timestamp: now.Add(-time.Duration(i) * time.Hour)})
		require.NoError(t, err)
	}
	since := now.Add(-10*time.Hour - time.Minute)
	got, err := s.GetDistinctSources(context.Background(), ActivityFilter{Tier: "change", Since: &since})
	require.NoError(t, err)
	// hours 0..10 → i=0..10; change tier drops i=0,5,10 → i=1,2,3,4,6,7,8,9
	// sources: i%3 → 1:metadata 2:server 3:library 4:metadata 6:library 7:metadata 8:server 9:library
	assert.Equal(t, []SourceCount{{"library", 3}, {"metadata", 3}, {"server", 2}}, got)
}

// TestSQLActivity_DistinctSourcesTiming is the before/after measurement on a
// large fixture. Opt-in: ACTIVITY_SOURCES_BENCH_ROWS=1000000.
func TestSQLActivity_DistinctSourcesTiming(t *testing.T) {
	n, _ := strconv.Atoi(os.Getenv("ACTIVITY_SOURCES_BENCH_ROWS"))
	if n <= 0 {
		t.Skip("set ACTIVITY_SOURCES_BENCH_ROWS to run")
	}
	s := newTestSQLStore(t)
	ctx := context.Background()
	now := time.Now()
	span := 30 * 24 * time.Hour
	details := []byte(strings.Repeat("d", 2048)) // rows carry a details blob, as prod's do
	tx, err := s.writer.BeginTx(ctx, nil)
	require.NoError(t, err)
	stmt, err := tx.PrepareContext(ctx, "INSERT INTO activity (ts, tier, type, level, source, operation_id, summary, details) VALUES (?,?,?,?,?,?,?,?)")
	require.NoError(t, err)
	srcs := []string{"server", "library", "background", "maintenance", "metadata", "compaction"}
	tiers := []string{"change", "change", "change", "debug", "audit"}
	for i := range n {
		ts := now.Add(-time.Duration(int64(span) / int64(n) * int64(i))).UnixNano()
		_, err := stmt.ExecContext(ctx, ts, tiers[i%len(tiers)], "t", "info", srcs[i%len(srcs)], fmt.Sprintf("OP%d", i/50), "s", details)
		require.NoError(t, err)
	}
	require.NoError(t, stmt.Close())
	require.NoError(t, tx.Commit())
	_, err = s.writer.ExecContext(ctx, "ANALYZE")
	require.NoError(t, err)

	since := now.Add(-24 * time.Hour)
	for name, f := range map[string]ActivityFilter{
		"since=24h":       {Since: &since},
		"tier=change":     {Tier: "change"},
		"tier=change+24h": {Tier: "change", Since: &since},
		"none":            {},
	} {
		start := time.Now()
		out, err := s.GetDistinctSources(ctx, f)
		require.NoError(t, err)
		t.Logf("rows=%d %-16s %v (%d sources)", n, name, time.Since(start), len(out))
	}
}
