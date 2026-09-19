// file: internal/database/sql_activity_sources_test.go
// version: 1.1.0
// guid: 4328c194-881d-4326-8606-16234be725a4
// last-edited: 2026-09-19

package database

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
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
	cases := map[string]struct {
		f        ActivityFilter
		covering bool // the plan must answer from an index alone
		hinted   bool // INDEXED BY is only for a filter that is ts and nothing else
	}{
		"none":             {ActivityFilter{}, true, false},
		"since":            {ActivityFilter{Since: &since}, true, true},
		"since+until":      {ActivityFilter{Since: &since, Until: &until}, true, true},
		"tier":             {ActivityFilter{Tier: "change"}, true, false},
		"tier+since":       {ActivityFilter{Tier: "change", Since: &since}, true, false},
		"tier+since+until": {ActivityFilter{Tier: "change", Since: &since, Until: &until}, true, false},
		// level is in no index: no covering plan exists, and the ts hint must
		// not be forced on it either.
		"level":       {ActivityFilter{Level: "warn"}, false, false},
		"level+since": {ActivityFilter{Level: "warn", Since: &since}, false, false},
		"type+since":  {ActivityFilter{Type: "x", Since: &since}, false, false},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			q, args := s.distinctSourcesQuery(c.f)
			assert.Equal(t, c.hinted, strings.Contains(q, "INDEXED BY"), "query: %s", q)
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
			if c.covering {
				assert.Contains(t, joined, "COVERING INDEX", "plan must not touch the table: %s", joined)
			}
		})
	}
}

// sqlIndexExists reads sqlite_master directly — the test's own instrument, not
// the store's cached flag.
func sqlIndexExists(t *testing.T, s *SQLActivityStore, name string) bool {
	t.Helper()
	var n int
	require.NoError(t, s.writer.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name=?`, name).Scan(&n))
	return n > 0
}

// TestSQLActivity_OpenNeverBuildsCoveringIndexesOnAPopulatedTable: on a
// database that already holds rows, open must not build the covering indexes.
// Building them sorts the whole table (13.2M rows on prod), spills SQLite's
// sorter to TMPDIR — the root pool that filled on 2026-09-09 — and would run
// before the checkpointer starts. They are built by OptimizeStatistics, the
// scheduled maintenance pass, and the query falls back to the old plan until
// then.
func TestSQLActivity_OpenNeverBuildsCoveringIndexesOnAPopulatedTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "activity.sqlite")
	s, err := OpenSQLiteActivityStore(path)
	require.NoError(t, err)
	_, err = s.Record(ActivityEntry{Tier: "change", Type: "t", Level: "info", Source: "library", Summary: "x", Timestamp: time.Now()})
	require.NoError(t, err)
	// Put the file in its pre-upgrade shape: legacy indexes, no covering ones.
	for _, q := range []string{
		`DROP INDEX IF EXISTS idx_act_tier_ts_src`, `DROP INDEX IF EXISTS idx_act_ts_src`,
		`CREATE INDEX IF NOT EXISTS idx_act_tier_ts ON activity(tier, ts)`, `CREATE INDEX IF NOT EXISTS idx_act_ts ON activity(ts)`,
	} {
		_, err := s.writer.Exec(q)
		require.NoError(t, err)
	}
	require.NoError(t, s.Close())

	s, err = OpenSQLiteActivityStore(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	assert.False(t, sqlIndexExists(t, s, "idx_act_ts_src"), "open must not build a covering index on a populated table")
	assert.False(t, sqlIndexExists(t, s, "idx_act_tier_ts_src"))
	assert.True(t, sqlIndexExists(t, s, "idx_act_ts"), "legacy index stays until the replacement exists")
	since := time.Now().Add(-time.Hour)
	q, _ := s.distinctSourcesQuery(ActivityFilter{Since: &since})
	assert.NotContains(t, q, "INDEXED BY", "no hint may name an index that does not exist")
	got, err := s.GetDistinctSources(context.Background(), ActivityFilter{Since: &since})
	require.NoError(t, err)
	assert.Equal(t, []SourceCount{{"library", 1}}, got)

	res, err := s.OptimizeStatistics(context.Background())
	require.NoError(t, err)
	assert.True(t, res.CoveringIndexesBuilt)
	assert.True(t, sqlIndexExists(t, s, "idx_act_ts_src"))
	assert.True(t, sqlIndexExists(t, s, "idx_act_tier_ts_src"))
	assert.False(t, sqlIndexExists(t, s, "idx_act_ts"), "legacy dropped only after the replacement is built")
	assert.False(t, sqlIndexExists(t, s, "idx_act_tier_ts"))
	q, _ = s.distinctSourcesQuery(ActivityFilter{Since: &since})
	assert.Contains(t, q, "INDEXED BY idx_act_ts_src")

	res, err = s.OptimizeStatistics(context.Background())
	require.NoError(t, err)
	assert.False(t, res.CoveringIndexesBuilt, "second run has nothing to build")
}

// TestSQLActivity_FreshDatabaseGetsCoveringIndexesAtOpen: an empty table costs
// nothing to index, so a new database starts in the final shape.
func TestSQLActivity_FreshDatabaseGetsCoveringIndexesAtOpen(t *testing.T) {
	s := newTestSQLStore(t)
	assert.True(t, sqlIndexExists(t, s, "idx_act_ts_src"))
	assert.True(t, sqlIndexExists(t, s, "idx_act_tier_ts_src"))
	assert.False(t, sqlIndexExists(t, s, "idx_act_ts"))
	assert.False(t, sqlIndexExists(t, s, "idx_act_tier_ts"))
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

	// Before: the pre-upgrade index shape, and no hint (coveringTimeIdx false).
	for _, q := range append(s.dialect.timeIndexes().legacy,
		"DROP INDEX "+sqlActIdxTierTSSrc, "DROP INDEX "+sqlActIdxTSSrc) {
		_, err := s.writer.ExecContext(ctx, q)
		require.NoError(t, err)
	}
	s.coveringTimeIdx.Store(false)
	since := now.Add(-24 * time.Hour)
	filters := map[string]ActivityFilter{
		"since=24h":       {Since: &since},
		"tier=change":     {Tier: "change"},
		"tier=change+24h": {Tier: "change", Since: &since},
		"none":            {},
	}
	for name, f := range filters {
		start := time.Now()
		_, err := s.GetDistinctSources(ctx, f)
		require.NoError(t, err)
		t.Logf("BEFORE rows=%d %-16s %v", n, name, time.Since(start))
	}
	start := time.Now()
	res, err := s.OptimizeStatistics(ctx)
	require.NoError(t, err)
	require.True(t, res.CoveringIndexesBuilt)
	t.Logf("OptimizeStatistics (build covering indexes + analyze) rows=%d: %v", n, time.Since(start))

	for name, f := range filters {
		start := time.Now()
		out, err := s.GetDistinctSources(ctx, f)
		require.NoError(t, err)
		t.Logf("AFTER  rows=%d %-16s %v (%d sources)", n, name, time.Since(start), len(out))
	}
}
