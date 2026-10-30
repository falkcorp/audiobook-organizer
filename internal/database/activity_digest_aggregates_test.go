// file: internal/database/activity_digest_aggregates_test.go
// version: 1.2.0
// guid: 2a7f5c93-6d1e-4b48-9e30-a8c4d71b5f26
// last-edited: 2026-10-30

package database

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// digestOf reads the one digest of the store and decodes its details.
func digestOf(t *testing.T, s *PebbleActivityStore) DigestDetails {
	t.Helper()
	all, _, err := s.Query(context.Background(), ActivityFilter{Limit: 50, Tier: DigestTier})
	require.NoError(t, err)
	require.Len(t, all, 1)
	b, err := json.Marshal(all[0].Details)
	require.NoError(t, err)
	var dd DigestDetails
	require.NoError(t, json.Unmarshal(b, &dd))
	return dd
}

func aggEntry(day time.Time, i int, typ, op string, tags ...string) ActivityEntry {
	return ActivityEntry{
		Tier: "change", Type: typ, Level: "info", Source: "pipeline", OperationID: op, Tags: tags,
		Summary: "aggregate fixture", Timestamp: day.Add(time.Duration(i) * time.Second),
	}
}

// TestCompactByDay_WritesPerOperationAggregates: the digest carries each
// operation's share of Counts, OriginalCount and TagCounts, the rows that name no
// operation are accounted for too, a row that looks like the Repairs lane is also
// counted in its operation's RepairsLike part, and the shares sum to the totals.
func TestCompactByDay_WritesPerOperationAggregates(t *testing.T) {
	s := newTestPebbleActivityStore(t)
	day := time.Date(2025, 6, 10, 0, 0, 0, 0, time.UTC)
	for i, e := range []ActivityEntry{
		aggEntry(day, 0, "metadata_applied", "op-gotham-scan", "action:apply", "source:scan"),
		aggEntry(day, 1, "metadata_applied", "op-gotham-scan", "action:apply"),
		aggEntry(day, 2, "tag_written", "op-gotham-scan", "action:write"),
		aggEntry(day, 3, "repairs.apply", "op-arkham-apply", "def:repairs.apply", "action:apply"),
		aggEntry(day, 4, "system_log", "op-arkham-apply", "source:slog"), // a log line without a def tag
		aggEntry(day, 5, "config_changed", "", "action:config"),
		aggEntry(day, 6, "repairs.plan", "", "def:repairs.plan"), // looks Repairs, names no operation
	} {
		_, err := s.Record(e)
		require.NoError(t, err, i)
	}
	_, err := s.CompactByDay(context.Background(), day.AddDate(0, 0, 1))
	require.NoError(t, err)

	dd := digestOf(t, s)
	require.Equal(t, DigestAggregatesVersion, dd.AggregatesVersion)
	require.Equal(t, 7, dd.OriginalCount)
	require.Len(t, dd.OpAggregates, 2)

	scan := dd.OpAggregates["op-gotham-scan"]
	require.NotNil(t, scan)
	assert.Equal(t, 3, scan.Count)
	assert.Equal(t, map[string]int{"metadata_applied": 2, "tag_written": 1}, scan.Counts)
	assert.Equal(t, map[string]map[string]int{
		"action": {"apply": 2, "write": 1}, "source": {"scan": 1},
	}, scan.TagCounts)
	assert.Nil(t, scan.RepairsLike, "nothing in this operation looks like the Repairs lane")

	arkham := dd.OpAggregates["op-arkham-apply"]
	require.NotNil(t, arkham)
	assert.Equal(t, 2, arkham.Count)
	require.NotNil(t, arkham.RepairsLike, "the def-tagged row is the part a record-less operation is judged by")
	assert.Equal(t, 1, arkham.RepairsLike.Count)
	assert.Equal(t, map[string]int{"repairs.apply": 1}, arkham.RepairsLike.Counts)

	require.NotNil(t, dd.NoOpAggregate)
	assert.Equal(t, 2, dd.NoOpAggregate.Count)
	require.NotNil(t, dd.NoOpAggregate.RepairsLike)
	assert.Equal(t, 1, dd.NoOpAggregate.RepairsLike.Count)

	// The shares add up to the totals the digest states.
	sum := &DigestOpAggregate{}
	for _, a := range dd.OpAggregates {
		sum.merge(a)
	}
	sum.merge(dd.NoOpAggregate)
	assert.Equal(t, dd.OriginalCount, sum.Count)
	assert.Equal(t, dd.Counts, sum.Counts)
	assert.Equal(t, dd.TagCounts, sum.TagCounts, "tag_counts is written at compaction and is the sum of the shares")
}

// TestCompactByDay_AggregatesSurviveAMergeIntoTheDaysDigest: late rows added to a
// digest that has aggregates add to them.
func TestCompactByDay_AggregatesSurviveAMergeIntoTheDaysDigest(t *testing.T) {
	s := newTestPebbleActivityStore(t)
	day := time.Date(2025, 6, 10, 0, 0, 0, 0, time.UTC)
	cutoff := day.AddDate(0, 0, 1)
	for i := range 3 {
		_, err := s.Record(aggEntry(day, i, "metadata_applied", "op-gotham-scan", "action:apply"))
		require.NoError(t, err)
	}
	_, err := s.CompactByDay(context.Background(), cutoff)
	require.NoError(t, err)
	for i := range 2 {
		_, err := s.Record(aggEntry(day, 100+i, "tag_written", "op-gotham-scan", "action:write"))
		require.NoError(t, err)
	}
	_, err = s.Record(aggEntry(day, 200, "tag_written", "op-bludhaven-scan", "action:write"))
	require.NoError(t, err)
	_, err = s.CompactByDay(context.Background(), cutoff)
	require.NoError(t, err)

	dd := digestOf(t, s)
	require.Equal(t, DigestAggregatesVersion, dd.AggregatesVersion)
	assert.Equal(t, 6, dd.OriginalCount)
	assert.Equal(t, 5, dd.OpAggregates["op-gotham-scan"].Count)
	assert.Equal(t, map[string]int{"metadata_applied": 3, "tag_written": 2}, dd.OpAggregates["op-gotham-scan"].Counts)
	assert.Equal(t, 1, dd.OpAggregates["op-bludhaven-scan"].Count)
	assert.Equal(t, map[string]map[string]int{"action": {"apply": 3, "write": 3}}, dd.TagCounts)
}

// TestDigestDetails_MergeAggregates: a stored digest without per-operation
// aggregates cannot say which of its rows belong to which operation, so the
// merged digest does not either; two digests that have them merge exactly.
func TestDigestDetails_MergeAggregates(t *testing.T) {
	withAgg := func() DigestDetails {
		a := &DigestOpAggregate{}
		a.add(ActivityEntry{Type: "metadata_applied", Tags: []string{"action:apply"}})
		return DigestDetails{AggregatesVersion: DigestAggregatesVersion, OpAggregates: map[string]*DigestOpAggregate{"op-a": a}}
	}
	t.Run("both have them", func(t *testing.T) {
		dd, existing := withAgg(), withAgg()
		dd.mergeAggregates(existing)
		assert.Equal(t, DigestAggregatesVersion, dd.AggregatesVersion)
		assert.Equal(t, 2, dd.OpAggregates["op-a"].Count)
	})
	t.Run("stored digest is from before aggregates", func(t *testing.T) {
		dd := withAgg()
		dd.mergeAggregates(DigestDetails{OriginalCount: 40})
		assert.Zero(t, dd.AggregatesVersion)
		assert.Nil(t, dd.OpAggregates)
		assert.Nil(t, dd.NoOpAggregate)
	})
	t.Run("merged day names too many operations", func(t *testing.T) {
		dd, existing := withAgg(), withAgg()
		existing.OpAggregates = map[string]*DigestOpAggregate{}
		for i := range maxDigestOpAggregates {
			existing.OpAggregates[fmt.Sprintf("op-%d", i)] = &DigestOpAggregate{DigestAggregate: DigestAggregate{Count: 1}}
		}
		dd.mergeAggregates(existing)
		assert.Zero(t, dd.AggregatesVersion)
		assert.Nil(t, dd.OpAggregates)
	})
}

// TestCompactByDay_DayWithTooManyOperationsStoresNoAggregates: a row read on
// every page that includes the digest does not grow without bound.
func TestCompactByDay_DayWithTooManyOperationsStoresNoAggregates(t *testing.T) {
	s := newTestPebbleActivityStore(t)
	day := time.Date(2025, 6, 10, 0, 0, 0, 0, time.UTC)
	for i := range maxDigestOpAggregates + 1 {
		_, err := s.Record(aggEntry(day, i%80000, "metadata_applied", fmt.Sprintf("op-%04d", i)))
		require.NoError(t, err)
	}
	_, err := s.CompactByDay(context.Background(), day.AddDate(0, 0, 1))
	require.NoError(t, err)
	dd := digestOf(t, s)
	assert.Zero(t, dd.AggregatesVersion)
	assert.Nil(t, dd.OpAggregates)
	assert.Equal(t, maxDigestOpAggregates+1, dd.OriginalCount, "the totals are still exact")
}

// TestRecompactDigests_KeepsTheAggregatesInStepWithTheTotals: re-deriving a
// digest's legacy items rebuilds its totals together with its per-operation
// aggregates when the items are every row of the day, so the aggregates still
// sum to the totals; when the items are only a sample, the exact totals and the
// aggregates are left alone rather than replaced by counts of the sample.
func TestRecompactDigests_KeepsTheAggregatesInStepWithTheTotals(t *testing.T) {
	sumOf := func(dd DigestDetails) *DigestOpAggregate {
		sum := &DigestOpAggregate{}
		for _, a := range dd.OpAggregates {
			sum.merge(a)
		}
		sum.merge(dd.NoOpAggregate)
		return sum
	}
	t.Run("items are every row", func(t *testing.T) {
		s := newTestPebbleActivityStore(t)
		day := time.Date(2025, 6, 10, 0, 0, 0, 0, time.UTC)
		for i := range 4 {
			e := aggEntry(day, i, "system_log", "op-gotham-scan") // no tags: a legacy-shaped item
			e.Summary = "applied metadata to book"
			_, err := s.Record(e)
			require.NoError(t, err)
		}
		_, err := s.CompactByDay(context.Background(), day.AddDate(0, 0, 1))
		require.NoError(t, err)
		res, err := s.RecompactDigests(context.Background())
		require.NoError(t, err)
		require.Equal(t, 1, res.Touched)
		dd := digestOf(t, s)
		assert.NotContains(t, dd.Counts, "system_log", "the totals follow the re-derived items")
		sum := sumOf(dd)
		assert.Equal(t, dd.OriginalCount, sum.Count)
		assert.Equal(t, dd.Counts, sum.Counts)
		assert.Equal(t, dd.TagCounts, sum.TagCounts)
	})
	t.Run("items are a sample", func(t *testing.T) {
		s := newTestPebbleActivityStore(t)
		day := time.Date(2025, 6, 10, 0, 0, 0, 0, time.UTC)
		n := maxDigestItems + 20
		for i := range n {
			e := aggEntry(day, i, "system_log", "op-gotham-scan")
			e.Summary = "applied metadata to book"
			_, err := s.Record(e)
			require.NoError(t, err)
		}
		_, err := s.CompactByDay(context.Background(), day.AddDate(0, 0, 1))
		require.NoError(t, err)
		before := digestOf(t, s)
		_, err = s.RecompactDigests(context.Background())
		require.NoError(t, err)
		after := digestOf(t, s)
		assert.Equal(t, n, after.OriginalCount)
		assert.Equal(t, before.Counts, after.Counts, "exact totals are not replaced by counts of the sample")
		assert.Equal(t, n, sumOf(after).Count)
	})
}

func TestDigestAggregate_Subtract(t *testing.T) {
	a := &DigestAggregate{
		Count:     5,
		Counts:    map[string]int{"x": 3, "y": 2},
		TagCounts: map[string]map[string]int{"action": {"apply": 4, "write": 1}, "source": {"scan": 5}},
	}
	a.Subtract(&DigestAggregate{
		Count:     2,
		Counts:    map[string]int{"x": 3},
		TagCounts: map[string]map[string]int{"action": {"write": 1}, "source": {"scan": 9}},
	})
	assert.Equal(t, 3, a.Count)
	assert.Equal(t, map[string]int{"y": 2}, a.Counts, "a key that reaches zero drops out; one that would go negative stops at zero")
	assert.Equal(t, map[string]map[string]int{"action": {"apply": 4}}, a.TagCounts)
}

// A Summarize row names its operations in op: tags, not in OperationID, and a
// reader hides the row when any of them is hidden. Filing it by OperationID
// alone would count it under "no operation", where a reader who may not read one
// of its operations would still be served its share.
func TestCompactByDay_SummarizeRowsAreFiledByTheMembershipTheReaderUses(t *testing.T) {
	summarize := func(day time.Time, i int, op string, opTags ...string) ActivityEntry {
		e := aggEntry(day, i, "metadata_applied", op, opTags...)
		e.Source = summarizeSource
		return e
	}
	day := time.Date(2025, 6, 10, 0, 0, 0, 0, time.UTC)

	t.Run("one operation in op: tags is that operation's row", func(t *testing.T) {
		s := newTestPebbleActivityStore(t)
		for i, e := range []ActivityEntry{
			summarize(day, 0, "", "op:op-arkham-apply"),
			aggEntry(day, 1, "metadata_applied", "op-gotham-scan"),
		} {
			_, err := s.Record(e)
			require.NoError(t, err, i)
		}
		_, err := s.CompactByDay(context.Background(), day.AddDate(0, 0, 1))
		require.NoError(t, err)
		dd := digestOf(t, s)
		require.Equal(t, DigestAggregatesVersion, dd.AggregatesVersion)
		require.Contains(t, dd.OpAggregates, "op-arkham-apply", "filed by its op: tag, not under no operation")
		assert.Equal(t, 1, dd.OpAggregates["op-arkham-apply"].Count)
		assert.Nil(t, dd.NoOpAggregate)
	})
	t.Run("several operations make the digest unattributable", func(t *testing.T) {
		s := newTestPebbleActivityStore(t)
		for i, e := range []ActivityEntry{
			summarize(day, 0, "", "op:op-arkham-apply", "op:op-gotham-scan"),
			aggEntry(day, 1, "metadata_applied", "op-gotham-scan"),
		} {
			_, err := s.Record(e)
			require.NoError(t, err, i)
		}
		_, err := s.CompactByDay(context.Background(), day.AddDate(0, 0, 1))
		require.NoError(t, err)
		dd := digestOf(t, s)
		assert.Zero(t, dd.AggregatesVersion, "no shares: the row's hidden operations cannot be told apart")
		assert.Nil(t, dd.OpAggregates)
		assert.Nil(t, dd.NoOpAggregate)
		assert.Equal(t, 2, dd.OriginalCount, "the day's totals are still exact")
	})
	t.Run("own operation plus another in a tag also counts as several", func(t *testing.T) {
		s := newTestPebbleActivityStore(t)
		_, err := s.Record(summarize(day, 0, "op-gotham-scan", "op:op-arkham-apply"))
		require.NoError(t, err)
		_, err = s.CompactByDay(context.Background(), day.AddDate(0, 0, 1))
		require.NoError(t, err)
		assert.Zero(t, digestOf(t, s).AggregatesVersion)
	})
	t.Run("a row that is not a Summarize row ignores op: tags, as the reader does", func(t *testing.T) {
		s := newTestPebbleActivityStore(t)
		_, err := s.Record(aggEntry(day, 0, "metadata_applied", "op-gotham-scan", "op:op-arkham-apply"))
		require.NoError(t, err)
		_, err = s.CompactByDay(context.Background(), day.AddDate(0, 0, 1))
		require.NoError(t, err)
		dd := digestOf(t, s)
		assert.Equal(t, DigestAggregatesVersion, dd.AggregatesVersion)
		assert.Equal(t, []string{"op-gotham-scan"}, mapKeys(dd.OpAggregates))
	})
}

func mapKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// Recompaction rebuilds the shares from the items, which keep no Source. Only
// Summarize rows carry op: tags, so an item with two of them is filed as the
// Summarize row it came from and the digest drops its shares rather than guess;
// an item with none is filed by its own operation as before.
func TestRecompactDigests_FilesItemsWithOperationTagsAsSummarizeRows(t *testing.T) {
	day := time.Date(2025, 6, 10, 0, 0, 0, 0, time.UTC)
	t.Run("two op: tags", func(t *testing.T) {
		s := newTestPebbleActivityStore(t)
		// A digest with shares, whose item (a legacy-shaped one) carries two op:
		// tags: a row the compaction filed by its OperationID alone, as a build
		// before the Summarize membership was applied did.
		e := aggEntry(day, 0, "system_log", "op-gotham-scan", "op:op-arkham-apply", "op:op-gotham-scan")
		e.Summary = "applied metadata to book"
		_, err := s.Record(e)
		require.NoError(t, err)
		_, err = s.CompactByDay(context.Background(), day.AddDate(0, 0, 1))
		require.NoError(t, err)
		require.Equal(t, DigestAggregatesVersion, digestOf(t, s).AggregatesVersion, "not a Summarize row, so filed by OperationID")

		res, err := s.RecompactDigests(context.Background())
		require.NoError(t, err)
		require.Equal(t, 1, res.Touched)
		dd := digestOf(t, s)
		assert.Zero(t, dd.AggregatesVersion, "the item cannot be told from a Summarize row, so no shares are stated")
		assert.Nil(t, dd.OpAggregates)
		assert.Nil(t, dd.NoOpAggregate)
		assert.Equal(t, 1, dd.OriginalCount)
	})
	t.Run("no op: tags", func(t *testing.T) {
		s := newTestPebbleActivityStore(t)
		e := aggEntry(day, 0, "system_log", "op-gotham-scan")
		e.Summary = "applied metadata to book"
		_, err := s.Record(e)
		require.NoError(t, err)
		_, err = s.CompactByDay(context.Background(), day.AddDate(0, 0, 1))
		require.NoError(t, err)
		_, err = s.RecompactDigests(context.Background())
		require.NoError(t, err)
		dd := digestOf(t, s)
		assert.Equal(t, DigestAggregatesVersion, dd.AggregatesVersion)
		assert.Contains(t, dd.OpAggregates, "op-gotham-scan")
	})
}

// A legacy item is re-derived from its message, which cannot say it belonged to a
// Repairs run. The item's own def: tag and repairs.* type survive, so an item of
// an operation whose record is gone still looks like a Repairs row and is still
// counted in its operation's RepairsLike share.
func TestRecompactDigests_KeepsTheRepairsIdentityOfAnItemWhoseOperationIsGone(t *testing.T) {
	s := newTestPebbleActivityStore(t)
	day := time.Date(2025, 6, 10, 0, 0, 0, 0, time.UTC)
	for i, e := range []ActivityEntry{
		// A slog-bridge line of a repairs.apply run: a generic type, the def: tag only.
		aggEntry(day, 0, "system", "op-pruned-apply", "def:repairs.apply"),
		// A reporter row of one: the repairs.* type, no tags.
		aggEntry(day, 1, "repairs.plan", "op-pruned-plan"),
		aggEntry(day, 2, "system", "op-gotham-scan", "source:scan"),
	} {
		e.Summary = "applied metadata to book"
		_, err := s.Record(e)
		require.NoError(t, err, i)
	}
	_, err := s.CompactByDay(context.Background(), day.AddDate(0, 0, 1))
	require.NoError(t, err)
	before := digestOf(t, s)
	require.Equal(t, 1, before.OpAggregates["op-pruned-apply"].RepairsLike.Count)

	res, err := s.RecompactDigests(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, res.Touched, "the items are legacy-shaped and are re-derived")

	dd := digestOf(t, s)
	looks := map[string]bool{}
	for _, it := range dd.Items {
		looks[it.OperationID] = EntryLooksRepairs(ActivityEntry{Type: it.Type, Tags: it.Tags})
	}
	assert.True(t, looks["op-pruned-apply"], "the def: tag was overwritten by the re-derived tags")
	assert.True(t, looks["op-pruned-plan"], "the repairs.plan type was overwritten by the re-derived type")
	assert.False(t, looks["op-gotham-scan"])

	require.Equal(t, DigestAggregatesVersion, dd.AggregatesVersion)
	for _, op := range []string{"op-pruned-apply", "op-pruned-plan"} {
		require.NotNil(t, dd.OpAggregates[op].RepairsLike, op)
		assert.Equal(t, 1, dd.OpAggregates[op].RepairsLike.Count, "%s: still counts as a Repairs row for a reader who cannot resolve the operation", op)
	}
	assert.Nil(t, dd.OpAggregates["op-gotham-scan"].RepairsLike)
}

func TestDigestItemSource(t *testing.T) {
	assert.Equal(t, summarizeSource, DigestItemSource([]string{"source:scan", "op:op-arkham-apply"}))
	assert.Empty(t, DigestItemSource([]string{"source:scan", "def:repairs.apply"}))
	assert.Empty(t, DigestItemSource(nil))
	assert.Equal(t, summarizeSource, digestItemAsEntry(DigestItem{Tags: []string{"op:x"}}).Source)
}
