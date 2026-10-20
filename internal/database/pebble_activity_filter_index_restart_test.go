// file: internal/database/pebble_activity_filter_index_restart_test.go
// version: 1.0.2
// guid: 7c1e4a92-5d38-4b6f-a2e0-9f83d1c5b740
// last-edited: 2026-10-19

package database

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rollbackFixture builds the state a rollback leaves: an indexing build that
// completed the backfill, then an older build that wrote n rows with no filter
// keys. The returned store is the first construction over that data (boot 1).
func rollbackFixture(t *testing.T, n int) (first *PebbleActivityStore, legacyFrom time.Time) {
	t.Helper()
	s := newTestPebbleActivityStore(t)
	now := time.Now().UTC()
	seedBulk(t, s, 50, now.Add(-3*time.Hour), time.Second, func(int) ActivityEntry {
		return ActivityEntry{Tier: "change", Type: "t", Source: "new-build", Summary: "n"}
	})
	require.NoError(t, s.MarkFilterIndexBackfillDone())
	legacyFrom = now.Add(-2 * time.Hour)
	for i := range n {
		writeLegacyRow(t, s, legacyFrom.Add(time.Duration(i)*time.Second), "old-build")
	}
	return NewPebbleActivityStore(s.db), legacyFrom
}

func requireDurableFrom(t *testing.T, s *PebbleActivityStore, want bool) int64 {
	t.Helper()
	n, ok, err := s.loadReconcileFrom()
	require.NoError(t, err)
	require.Equal(t, want, ok, "durable reconcile start mark present = %v, want %v", ok, want)
	return n
}

// TestFilterIndex_RestartBeforeReconcileKeepsTheGap: boot 1 writes its own
// rows (advancing the persisted seen-through mark past the rollback rows) and
// the server restarts before the reconcile op ran: it waits behind the warmup
// hold and a queue, and a check takes minutes. Boot 2 must still find the
// rollback rows. Without a durable start mark it reloaded a mark past them,
// found no gap, and left them unindexed behind a "complete" sentinel with
// filters coming back short and partial=false.
func TestFilterIndex_RestartBeforeReconcileKeepsTheGap(t *testing.T) {
	boot1, _ := rollbackFixture(t, 30)
	_ = requireDurableFrom(t, boot1, true) // written at open: rows exist above the mark
	_, err := boot1.Record(ActivityEntry{Tier: "change", Source: "new-build", Summary: "boot line"})
	require.NoError(t, err)

	boot2 := NewPebbleActivityStore(boot1.db) // restart before the reconcile ran
	res, err := boot2.ReconcileFilterIndexCoverage(context.Background(), nil)
	require.NoError(t, err)
	assert.True(t, res.GapFound, "the rollback rows must survive a restart that preceded the reconcile")
	assert.GreaterOrEqual(t, res.Reindexed, 30)
	assertFilterIndexConsistent(t, boot2)
	got, err := boot2.QueryWithPartial(context.Background(), ActivityFilter{Source: "old-build", Limit: 100})
	require.NoError(t, err)
	assert.Len(t, got.Entries, 30)
	assert.False(t, got.Partial)
	requireDurableFrom(t, boot2, false) // the sentinel re-set cleared it
}

// A reconcile that failed or was cancelled before it covered the rows leaves
// the durable mark, so the next boot still owes the check.
func TestFilterIndex_FailedReconcileLeavesTheDurableMark(t *testing.T) {
	boot1, _ := rollbackFixture(t, 30)
	want := requireDurableFrom(t, boot1, true)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := boot1.ReconcileFilterIndexCoverage(ctx, nil)
	require.Error(t, err)
	assert.Equal(t, want, requireDurableFrom(t, boot1, true), "a cancelled check leaves the mark where it was")

	boot2 := NewPebbleActivityStore(boot1.db)
	res, err := boot2.ReconcileFilterIndexCoverage(context.Background(), nil)
	require.NoError(t, err)
	assert.True(t, res.GapFound)
}

// A check that finds every filter key (a lagging mark, not a rollback) is
// finished: the durable mark goes, so it is not re-read forever.
func TestFilterIndex_CleanCheckClearsTheDurableMark(t *testing.T) {
	s := newTestPebbleActivityStore(t)
	now := time.Now().UTC()
	seedBulk(t, s, 20, now.Add(-time.Hour), time.Second, func(int) ActivityEntry {
		return ActivityEntry{Tier: "change", Type: "t", Source: "new-build", Summary: "n"}
	})
	require.NoError(t, s.MarkFilterIndexBackfillDone())
	// The persisted mark lagged behind the rows (two concurrent commits can land
	// in either order), so rows exist above it that are all indexed.
	require.NoError(t, s.db.Set([]byte(ActivityFilterIndexSeenThroughKey),
		[]byte(strconv.FormatInt(now.Add(-time.Hour).UnixNano(), 10)), nil))

	booted := NewPebbleActivityStore(s.db)
	requireDurableFrom(t, booted, true)
	res, err := booted.ReconcileFilterIndexCoverage(context.Background(), nil)
	require.NoError(t, err)
	assert.False(t, res.GapFound)
	requireDurableFrom(t, booted, false)
	assert.True(t, booted.FilterIndexBackfillDone())
}

// SF-1: MarkFilterIndexBackfillDone covers every row, so it drops the durable
// mark and lifts the in-memory one. A reconcile queued behind a resumed full
// backfill must not rescan the whole table from the old mark.
func TestFilterIndex_MarkDoneClearsTheDurableMarkAndRaisesTheStart(t *testing.T) {
	boot1, _ := rollbackFixture(t, 30)
	requireDurableFrom(t, boot1, true)
	// The full backfill, as the op runs it: withdraw, index everything, mark.
	require.NoError(t, boot1.ClearFilterIndexBackfillDone())
	_, err := boot1.BackfillFilterIndexWindow(context.Background(), 0, 0, true)
	require.NoError(t, err)
	require.NoError(t, boot1.MarkFilterIndexBackfillDone())
	requireDurableFrom(t, boot1, false)

	newest, ok, err := boot1.newestPrimaryNanos()
	require.NoError(t, err)
	require.True(t, ok)

	prev := activityCtxCheckInterval
	activityCtxCheckInterval = 1
	t.Cleanup(func() { activityCtxCheckInterval = prev })
	checked := 0
	res, err := boot1.ReconcileFilterIndexCoverage(context.Background(), func(phase string, rows int) { checked = rows })
	require.NoError(t, err)
	assert.False(t, res.GapFound)
	assert.GreaterOrEqual(t, res.SeenThrough, newest, "the start mark was lifted to the newest row")
	assert.Zero(t, checked, "nothing above the lifted mark, so the check reads no rows")

	boot2 := NewPebbleActivityStore(boot1.db) // and the next boot starts clean too
	requireDurableFrom(t, boot2, false)
	assert.GreaterOrEqual(t, boot2.reconcileFrom.Load(), newest)
}

// An existing durable mark is only ever lowered at open, never raised: a
// second restart before the reconcile ran keeps the original start.
func TestFilterIndex_DurableMarkKeepsTheLowerValueAcrossOpens(t *testing.T) {
	boot1, _ := rollbackFixture(t, 30)
	first := requireDurableFrom(t, boot1, true)
	_, err := boot1.Record(ActivityEntry{Tier: "change", Source: "new-build", Summary: "boot 1 line"})
	require.NoError(t, err)
	boot2 := NewPebbleActivityStore(boot1.db)
	_, err = boot2.Record(ActivityEntry{Tier: "change", Source: "new-build", Summary: "boot 2 line"})
	require.NoError(t, err)
	boot3 := NewPebbleActivityStore(boot1.db)
	assert.Equal(t, first, requireDurableFrom(t, boot3, true))
	assert.Equal(t, first, boot3.reconcileFrom.Load())
}

// The "check" progress is a running total across tiers: rows read so far,
// never a count that restarts with each tier.
func TestFilterIndex_CheckProgressIsARunningTotalAcrossTiers(t *testing.T) {
	prev := activityCtxCheckInterval
	activityCtxCheckInterval = 4
	t.Cleanup(func() { activityCtxCheckInterval = prev })

	s := newTestPebbleActivityStore(t)
	now := time.Now().UTC()
	seedBulk(t, s, 3, now.Add(-3*time.Hour), time.Second, func(int) ActivityEntry {
		return ActivityEntry{Tier: "change", Type: "t", Source: "new-build", Summary: "n"}
	})
	require.NoError(t, s.MarkFilterIndexBackfillDone())
	booted := NewPebbleActivityStore(s.db)
	// 20 indexed rows in each of two tiers above the mark.
	for _, tier := range []string{"change", "info"} {
		seedBulk(t, s, 20, now.Add(-time.Hour), time.Second, func(int) ActivityEntry {
			return ActivityEntry{Tier: tier, Type: "t", Source: "new-build", Summary: "n"}
		})
	}
	var rows []int
	res, err := booted.ReconcileFilterIndexCoverage(context.Background(), func(phase string, n int) {
		if phase == "check" {
			rows = append(rows, n)
		}
	})
	require.NoError(t, err)
	require.False(t, res.GapFound)
	require.NotEmpty(t, rows)
	assert.IsNonDecreasing(t, rows)
	assert.Greater(t, rows[len(rows)-1], 20, "40 rows over two tiers: the total passes any single tier's 20, got %v", rows)
}

// Fail closed: an unreadable durable mark must not read as absent. Falling
// back to the live mark finds a clean table above it, and the clean check then
// deleted the key, losing the rollback gap for good.
func TestFilterIndex_CorruptDurableMarkChecksEverything(t *testing.T) {
	boot1, _ := rollbackFixture(t, 30)
	requireDurableFrom(t, boot1, true)
	_, err := boot1.Record(ActivityEntry{Tier: "change", Source: "new-build", Summary: "boot line"})
	require.NoError(t, err)
	require.NoError(t, boot1.db.Set([]byte(ActivityFilterIndexReconcileFromKey), []byte("garbage"), nil))

	boot2 := NewPebbleActivityStore(boot1.db)
	assert.Zero(t, boot2.reconcileFrom.Load(), "an unreadable mark starts the check from 0")
	assert.Zero(t, requireDurableFrom(t, boot2, true), "and is replaced by a readable 0, not deleted")
	res, err := boot2.ReconcileFilterIndexCoverage(context.Background(), nil)
	require.NoError(t, err)
	assert.True(t, res.GapFound, "the rollback rows are found despite the corrupt key")
	assert.GreaterOrEqual(t, res.Reindexed, 30)
	assertFilterIndexConsistent(t, boot2)
}

// The loader reports an unparsable value as an error, never as absent.
func TestFilterIndex_LoadReconcileFromDistinguishesAbsentFromCorrupt(t *testing.T) {
	s := newTestPebbleActivityStore(t)
	_, found, err := s.loadReconcileFrom()
	require.NoError(t, err)
	assert.False(t, found)
	require.NoError(t, s.db.Set([]byte(ActivityFilterIndexReconcileFromKey), []byte("12x"), nil))
	_, _, err = s.loadReconcileFrom()
	assert.Error(t, err)
	require.NoError(t, s.db.Set([]byte(ActivityFilterIndexReconcileFromKey), []byte("42"), nil))
	n, found, err := s.loadReconcileFrom()
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, int64(42), n)
}

// A read failure that is not "not found" (here: the engine gate refusing new
// work) fails closed too: the check starts from 0, never from the live mark.
func TestFilterIndex_UnreadableDurableMarkFromAReadErrorChecksEverything(t *testing.T) {
	s := newTestPebbleActivityStore(t)
	seedBulk(t, s, 5, time.Now().UTC().Add(-time.Hour), time.Second, func(int) ActivityEntry {
		return ActivityEntry{Tier: "change", Type: "t", Source: "new-build", Summary: "n"}
	})
	require.Positive(t, s.seenThrough.Load(), "precondition: a live mark to fall back to")
	s.db.gate.beginClose() // every engine read now fails

	s.initReconcileFrom()
	assert.Zero(t, s.reconcileFrom.Load(), "a failed read of the durable mark must not fall back to the live mark")
}
