// file: internal/database/pebble_activity_filter_index_test.go
// version: 1.0.0
// guid: cb551e14-7788-4c73-830d-3e0a46bfe67c
// last-edited: 2026-09-19

package database

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setFilterIndexReady flips the planner gate both ways, persistently and in the
// cache, so one fixture can be queried through both paths.
func setFilterIndexReady(t testing.TB, s *PebbleActivityStore, ready bool) {
	t.Helper()
	if ready {
		require.NoError(t, s.MarkFilterIndexBackfillDone())
		return
	}
	require.NoError(t, s.db.Delete([]byte(ActivityFilterIndexBackfillKey), pebble.Sync))
	s.filterIndexReady.Store(false)
}

// dropFilterIndexes deletes every filter-index key: the on-disk state of rows
// written by a build that predates the families.
func dropFilterIndexes(t testing.TB, s *PebbleActivityStore) {
	t.Helper()
	for _, fam := range pactFilterFamilies {
		require.NoError(t, s.db.DeleteRange([]byte(fam.prefix), []byte(fam.prefix[:len(fam.prefix)-1]+";"), pebble.Sync))
	}
}

// seedBulk writes n rows through RecordBatch (one fsync per 500 rows, not per
// row) with timestamps base+i*step, all carrying the given fields.
func seedBulk(t testing.TB, s *PebbleActivityStore, n int, base time.Time, step time.Duration, mk func(i int) ActivityEntry) {
	t.Helper()
	batch := make([]ActivityEntry, 0, 500)
	for i := 0; i < n; i++ {
		e := mk(i)
		e.Timestamp = base.Add(time.Duration(i) * step)
		batch = append(batch, e)
		if len(batch) == cap(batch) {
			_, err := s.RecordBatch(batch)
			require.NoError(t, err)
			batch = batch[:0]
		}
	}
	if len(batch) > 0 {
		_, err := s.RecordBatch(batch)
		require.NoError(t, err)
	}
}

// TestFilterIndex_FindsMatchesOlderThanScanBudget is the regression this change
// exists for: 5 rare rows sit behind 50,000 newer rows, i.e. outside the
// newest-20,000 window the unindexed scan examines.
func TestFilterIndex_FindsMatchesOlderThanScanBudget(t *testing.T) {
	if testing.Short() {
		t.Skip("50k-row fixture")
	}
	s := newTestPebbleActivityStore(t)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	seedBulk(t, s, 5, base, time.Second, func(int) ActivityEntry {
		return ActivityEntry{Tier: "change", Type: "rare_type", Level: "error", Source: "rare-src", Summary: "old rare row"}
	})
	seedBulk(t, s, 50_000, base.Add(time.Hour), time.Millisecond, func(int) ActivityEntry {
		return ActivityEntry{Tier: "change", Type: "noise", Level: "info", Source: "noise-src", Summary: "noise"}
	})
	ctx := context.Background()
	filters := map[string]ActivityFilter{
		"source": {Source: "rare-src", Limit: 50},
		"level":  {Level: "error", Limit: 50},
		"type":   {Type: "rare_type", Limit: 50},
	}

	// Gate closed: today's behaviour — the rows are missed, but the answer now
	// SAYS it is partial instead of passing for complete.
	setFilterIndexReady(t, s, false)
	for name, f := range filters {
		res, err := s.QueryWithPartial(ctx, f)
		require.NoError(t, err, name)
		assert.Empty(t, res.Entries, "%s: unindexed path cannot reach rows past the budget", name)
		assert.True(t, res.Partial, "%s: a budget-truncated answer must be flagged partial", name)
	}

	setFilterIndexReady(t, s, true)
	for name, f := range filters {
		res, err := s.QueryWithPartial(ctx, f)
		require.NoError(t, err, name)
		require.Len(t, res.Entries, 5, name)
		assert.Equal(t, 5, res.Total, name)
		assert.False(t, res.Partial, name)
		for i := 1; i < len(res.Entries); i++ {
			assert.True(t, res.Entries[i-1].Timestamp.After(res.Entries[i].Timestamp), "%s: newest first", name)
		}
		assert.NotNil(t, res.Entries[0].Summary)
	}

	// Combined, and paged: source+level over the same fixture.
	res, err := s.QueryWithPartial(ctx, ActivityFilter{Source: "rare-src", Level: "error", Limit: 2, Offset: 2})
	require.NoError(t, err)
	require.Len(t, res.Entries, 2)
	assert.Equal(t, 5, res.Total, "offset 2 + limit 2 + probe row = 5, all present")
	assert.Equal(t, base.Add(2*time.Second), res.Entries[0].Timestamp.UTC())

	// A combination with no rows at all is exact and not partial.
	res, err = s.QueryWithPartial(ctx, ActivityFilter{Source: "rare-src", Level: "info", Limit: 50})
	require.NoError(t, err)
	assert.Empty(t, res.Entries)
	assert.Equal(t, 0, res.Total)
	assert.False(t, res.Partial)
}

// TestFilterIndex_SearchIsBudgetedAndFlagged: a substring predicate cannot be
// indexed; with an indexed predicate beside it the walk is budgeted and says so.
func TestFilterIndex_SearchIsBudgetedAndFlagged(t *testing.T) {
	s := newTestPebbleActivityStore(t)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	seedBulk(t, s, 1, base, time.Second, func(int) ActivityEntry {
		return ActivityEntry{Tier: "change", Type: "t", Source: "src", Summary: "needle"}
	})
	seedBulk(t, s, 300, base.Add(time.Hour), time.Millisecond, func(int) ActivityEntry {
		return ActivityEntry{Tier: "change", Type: "t", Source: "src", Summary: "hay"}
	})
	setFilterIndexReady(t, s, true)
	old := activityQueryScanBudget
	activityQueryScanBudget = 100
	t.Cleanup(func() { activityQueryScanBudget = old })

	res, err := s.QueryWithPartial(context.Background(), ActivityFilter{Source: "src", Search: "needle", Limit: 10})
	require.NoError(t, err)
	assert.Empty(t, res.Entries)
	assert.True(t, res.Partial, "the budget cut the walk before the needle")

	activityQueryScanBudget = 1000
	res, err = s.QueryWithPartial(context.Background(), ActivityFilter{Source: "src", Search: "needle", Limit: 10})
	require.NoError(t, err)
	require.Len(t, res.Entries, 1)
	assert.False(t, res.Partial)
}

// TestFilterIndex_DifferentialAgainstUnboundedScan runs every filter shape the
// planner accepts through BOTH paths over one fixture — the indexed planner and
// the unindexed scan with an unbounded budget, which is the reference — and
// requires identical pages (same rows, same order) and identical totals.
func TestFilterIndex_DifferentialAgainstUnboundedScan(t *testing.T) {
	s := newTestPebbleActivityStore(t)
	rng := rand.New(rand.NewPCG(7, 9))
	sources := []string{"scanner", "itunes", "a", "a:change", "100%", "summarize"}
	levels := []string{"info", "warn", "error", "debug"}
	types := []string{"", "scan", "merge", "daily_digest"}
	base := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	var batch []ActivityEntry
	for i := 0; i < 3000; i++ {
		// Coarse timestamps so ties across tiers are common.
		ts := base.Add(time.Duration(rng.IntN(600)) * time.Second)
		batch = append(batch, ActivityEntry{
			Timestamp: ts,
			Tier:      actTiers[rng.IntN(len(actTiers))],
			Type:      types[rng.IntN(len(types))],
			Level:     levels[rng.IntN(len(levels))],
			Source:    sources[rng.IntN(len(sources))],
			Summary:   fmt.Sprintf("row %d %s", i, []string{"alpha", "beta"}[rng.IntN(2)]),
			Tags:      []string{[]string{"x", "y"}[rng.IntN(2)]},
		})
	}
	_, err := s.RecordBatch(batch)
	require.NoError(t, err)

	old := activityQueryScanBudget
	activityQueryScanBudget = 1 << 30
	t.Cleanup(func() { activityQueryScanBudget = old })

	since := base.Add(100 * time.Second)
	until := base.Add(400 * time.Second)
	shapes := []ActivityFilter{
		{Source: "scanner"}, {Source: "a"}, {Source: "a:change"}, {Source: "100%"}, {Source: "missing"},
		{Level: "error"}, {Type: "merge"}, {Type: "daily_digest"},
		{Source: "itunes", Level: "warn"}, {Source: "a", Type: "scan", Level: "info"},
		{Source: "scanner", Tier: "digest"}, {Level: "warn", Tier: "change"},
		{Source: "itunes", ExcludeTiers: []string{"digest", "debug"}},
		{Level: "error", Search: "alpha"}, {Type: "scan", Tags: []string{"x"}},
		{Level: "info", ExcludeSources: []string{"scanner"}}, {Source: "a", ExcludeTags: []string{"y"}},
		{Source: "scanner", Since: &since}, {Level: "debug", Until: &until}, {Type: "merge", Since: &since, Until: &until},
	}
	pages := []struct{ limit, offset int }{{50, 0}, {7, 0}, {7, 7}, {7, 100}, {1000, 0}, {3, 2998}}
	ctx := context.Background()
	for _, shape := range shapes {
		for _, pg := range pages {
			f := shape
			f.Limit, f.Offset = pg.limit, pg.offset
			name := fmt.Sprintf("%+v", f)

			setFilterIndexReady(t, s, false)
			want, err := s.QueryWithPartial(ctx, f)
			require.NoError(t, err, name)
			setFilterIndexReady(t, s, true)
			got, err := s.QueryWithPartial(ctx, f)
			require.NoError(t, err, name)

			require.False(t, want.Partial, name)
			assert.False(t, got.Partial, name)
			assert.Equal(t, want.Total, got.Total, name)
			assert.Equal(t, entryIDs(want.Entries), entryIDs(got.Entries), name)
		}
	}
}

func entryIDs(es []ActivityEntry) []int64 {
	out := make([]int64, len(es))
	for i, e := range es {
		out[i] = e.ID
	}
	return out
}

// assertFilterIndexConsistent requires the filter-index keyspace to be EXACTLY
// the set of keys the stored rows derive — no orphan, no missing entry — with
// every value equal to the row's ref.
func assertFilterIndexConsistent(t *testing.T, s *PebbleActivityStore) {
	t.Helper()
	want := map[string]string{}
	for _, tier := range actTiers {
		it, err := s.db.NewIter(&pebble.IterOptions{LowerBound: pactPrimaryPrefix(tier), UpperBound: pactPrimaryUpperBound(tier)})
		require.NoError(t, err)
		for it.First(); it.Valid(); it.Next() {
			var e ActivityEntry
			require.NoError(t, json.Unmarshal(it.Value(), &e))
			keys, ok := pactFilterIndexKeysFor(it.Key(), e)
			require.True(t, ok)
			for _, k := range keys {
				want[string(k)] = string(it.Key()[len("act:"):])
			}
		}
		require.NoError(t, it.Close())
	}
	got := map[string]string{}
	for _, fam := range pactFilterFamilies {
		it, err := s.db.NewIter(&pebble.IterOptions{LowerBound: []byte(fam.prefix), UpperBound: []byte(fam.prefix[:len(fam.prefix)-1] + ";")})
		require.NoError(t, err)
		for it.First(); it.Valid(); it.Next() {
			got[string(it.Key())] = string(it.Value())
		}
		require.NoError(t, it.Close())
	}
	var missing, orphan []string
	for k := range want {
		if _, ok := got[k]; !ok {
			missing = append(missing, k)
		}
	}
	for k := range got {
		if _, ok := want[k]; !ok {
			orphan = append(orphan, k)
		}
	}
	sort.Strings(missing)
	sort.Strings(orphan)
	require.Empty(t, missing, "rows without their filter index keys")
	require.Empty(t, orphan, "filter index keys without their row")
	assert.Equal(t, want, got, "index values must be the row ref")
}

func TestFilterIndex_ConsistentThroughWriteDeleteCompactPrune(t *testing.T) {
	s := newTestPebbleActivityStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	old := now.Add(-40 * 24 * time.Hour)

	_, err := s.Record(ActivityEntry{Timestamp: old, Tier: "change", Type: "scan", Source: "scanner", OperationID: "op1", Summary: "r"})
	require.NoError(t, err)
	seedBulk(t, s, 400, old, time.Minute, func(i int) ActivityEntry {
		return ActivityEntry{Tier: actTiers[i%(len(actTiers)-1)], Type: []string{"scan", "merge"}[i%2], Level: []string{"info", "warn"}[i%2],
			Source: []string{"scanner", "x:y", "100%"}[i%3], OperationID: fmt.Sprintf("op%d", i%5), BookID: fmt.Sprintf("bk%d", i%7), Summary: "s"}
	})
	seedBulk(t, s, 50, now.Add(-time.Hour), time.Second, func(i int) ActivityEntry {
		return ActivityEntry{Tier: "change", Type: "fresh", Source: "scanner", Summary: "fresh"}
	})
	assertFilterIndexConsistent(t, s)

	n, err := s.Summarize(ctx, now.Add(-35*24*time.Hour), "debug")
	require.NoError(t, err)
	require.Positive(t, n)
	assertFilterIndexConsistent(t, s)

	n, err = s.Prune(ctx, now.Add(-39*24*time.Hour), "info")
	require.NoError(t, err)
	require.Positive(t, n)
	assertFilterIndexConsistent(t, s)

	res, err := s.CompactByDay(ctx, now.Add(-30*24*time.Hour))
	require.NoError(t, err)
	require.Positive(t, res.EntriesDeleted)
	assertFilterIndexConsistent(t, s)

	// A second compaction into an existing day's digest REPLACES that digest:
	// the old digest's index keys must go with it.
	seedBulk(t, s, 20, old, time.Minute, func(int) ActivityEntry {
		return ActivityEntry{Tier: "change", Type: "late", Source: "late-src", Summary: "late"}
	})
	res, err = s.CompactByDay(ctx, now.Add(-30*24*time.Hour))
	require.NoError(t, err)
	require.Positive(t, res.EntriesDeleted)
	assertFilterIndexConsistent(t, s)

	setFilterIndexReady(t, s, true)
	got, err := s.QueryWithPartial(ctx, ActivityFilter{Source: "compaction", Type: "daily_digest", Limit: 100})
	require.NoError(t, err)
	assert.NotEmpty(t, got.Entries, "digests are reachable through the index")

	_, err = s.WipeAllActivity(ctx)
	require.NoError(t, err)
	assertFilterIndexConsistent(t, s)
	assert.True(t, s.FilterIndexBackfillDone(), "the sentinel lives outside act: and survives a wipe")
}

// TestFilterIndex_RepairRemovesFilterOrphans: an orphaned filter key (a row
// deleted by a path that could not derive its keys) is skipped by the planner
// and removed by RepairActivityIndexes.
func TestFilterIndex_RepairRemovesFilterOrphans(t *testing.T) {
	s := newTestPebbleActivityStore(t)
	ts := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	_, err := s.Record(ActivityEntry{Timestamp: ts, Tier: "change", Source: "gone", Summary: "x"})
	require.NoError(t, err)
	// Delete the primary row alone.
	it, err := s.db.NewIter(&pebble.IterOptions{LowerBound: pactPrimaryPrefix("change"), UpperBound: pactPrimaryUpperBound("change")})
	require.NoError(t, err)
	require.True(t, it.First())
	require.NoError(t, s.db.Delete(slices.Clone(it.Key()), pebble.Sync))
	require.NoError(t, it.Close())

	setFilterIndexReady(t, s, true)
	res, err := s.QueryWithPartial(context.Background(), ActivityFilter{Source: "gone", Limit: 10})
	require.NoError(t, err)
	assert.Empty(t, res.Entries)
	assert.Equal(t, 0, res.Total)

	rep, err := s.RepairActivityIndexes(context.Background())
	require.NoError(t, err)
	assert.GreaterOrEqual(t, rep.Deleted, int64(2), "src + lvl keys of the deleted row")
	assertFilterIndexConsistent(t, s)
}

func TestFilterIndex_BackfillWindowRebuildsDroppedIndexes(t *testing.T) {
	s := newTestPebbleActivityStore(t)
	base := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	seedBulk(t, s, 700, base, time.Hour, func(i int) ActivityEntry {
		return ActivityEntry{Tier: actTiers[i%len(actTiers)], Type: "t", Source: fmt.Sprintf("s%d", i%4), Summary: "b"}
	})
	dropFilterIndexes(t, s)
	oldEvery := activityFilterBackfillCommitEvery
	activityFilterBackfillCommitEvery = 37
	t.Cleanup(func() { activityFilterBackfillCommitEvery = oldEvery })

	earliest, ok, err := s.FilterIndexEarliestNanos(context.Background())
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, base.UnixNano(), earliest)

	mid := base.Add(300 * time.Hour).UnixNano()
	n1, err := s.BackfillFilterIndexWindow(context.Background(), 0, mid, false)
	require.NoError(t, err)
	n2, err := s.BackfillFilterIndexWindow(context.Background(), mid, 0, true)
	require.NoError(t, err)
	assert.Equal(t, 700, n1+n2, "the two windows partition the rows")
	assertFilterIndexConsistent(t, s)

	// Idempotent: a re-run (a resumed window) writes the same keys.
	_, err = s.BackfillFilterIndexWindow(context.Background(), 0, mid, false)
	require.NoError(t, err)
	assertFilterIndexConsistent(t, s)

	// Cancelled mid-window: returns ctx.Err and leaves only valid keys behind.
	dropFilterIndexes(t, s)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = s.BackfillFilterIndexWindow(ctx, 0, 0, true)
	require.ErrorIs(t, err, context.Canceled)
}

func TestPactFilterFamiliesAreNotTiers(t *testing.T) {
	for _, fam := range pactFilterFamilies {
		token := strings.TrimSuffix(strings.TrimPrefix(fam.prefix, "act:"), ":")
		assert.NotContains(t, actTiers, token, "family %s would sit inside a tier's primary range", fam.name)
		assert.Contains(t, pactIndexFamilyPrefixes, fam.prefix, "repair must cover %s", fam.name)
	}
}

func TestPactEscapeIndexComponent(t *testing.T) {
	assert.Equal(t, "plain", pactEscapeIndexComponent("plain"))
	assert.Equal(t, "a%3Achange", pactEscapeIndexComponent("a:change"))
	assert.Equal(t, "100%25", pactEscapeIndexComponent("100%"))
	assert.NotEqual(t, pactEscapeIndexComponent("a%3A"), pactEscapeIndexComponent("a:"))
	assert.False(t, bytes.ContainsRune([]byte(pactEscapeIndexComponent("x:y:z")), ':'))
}

// ── benchmark ────────────────────────────────────────────────────────────────

// BenchmarkActivityListFilter measures GET /activity's store call for each
// indexed filter on a 1M-row store, through the unindexed scan ("scan", what
// production ran before this change) and the planner ("index"). The target
// rows are spread uniformly (1 in 200 per source, 1 in 1000 per level, 1 in 100
// per type), so the scan path usually returns a truncated page.
//
// Rows default to 1,000,000; ACTIVITY_BENCH_ROWS is not read — edit the const
// to measure a different size, and report the size you measured.
func BenchmarkActivityListFilter(b *testing.B) {
	const rows = 1_000_000
	s := newBenchPebbleActivityStore(b)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	seedBulk(b, s, rows, base, 10*time.Millisecond, func(i int) ActivityEntry {
		lvl := "info"
		if i%1000 == 0 {
			lvl = "error"
		}
		return ActivityEntry{
			Tier: []string{"change", "info", "debug", "audit"}[i%4], Type: fmt.Sprintf("type%d", i%100),
			Level: lvl, Source: fmt.Sprintf("src%d", i%200), Summary: "bench row",
		}
	})
	filters := map[string]ActivityFilter{
		"source": {Source: "src7", Limit: 50},
		"level":  {Level: "error", Limit: 50},
		"type":   {Type: "type42", Limit: 50},
		"source_level_page3": {Source: "src0", Level: "error", Limit: 50, Offset: 100},
	}
	names := make([]string, 0, len(filters))
	for n := range filters {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, mode := range []string{"scan", "index"} {
		setFilterIndexReady(b, s, mode == "index")
		for _, name := range names {
			f := filters[name]
			b.Run(mode+"/"+name, func(b *testing.B) {
				var res ActivityQueryResult
				for b.Loop() {
					var err error
					res, err = s.QueryWithPartial(context.Background(), f)
					if err != nil {
						b.Fatal(err)
					}
				}
				b.ReportMetric(float64(len(res.Entries)), "rows")
				b.ReportMetric(float64(boolInt(res.Partial)), "partial")
			})
		}
	}
}
