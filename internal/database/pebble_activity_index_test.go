// file: internal/database/pebble_activity_index_test.go
// version: 1.1.0
// guid: 7f2a91c4-6b3d-4a52-9c18-2d0e5b7a4e31
// last-edited: 2026-08-29

// Package database — regression suite for activity secondary-index deletion.
//
// WHY a dedicated file: every assertion here is on STORED STATE — the number of
// act:op: / act:bk: keys actually left in the keyspace — never on a return
// value. Each deletion path returns a count of PRIMARY rows and returned that
// same count both before and after the fix, so a test that trusted the return
// value would pass against the bug it is meant to catch.
//
// Every seeder here sets BOTH OperationID and BookID, and every test asserts a
// NON-ZERO index count before acting. The existing seeders in
// pebble_activity_store_test.go set neither, so reusing one would make these
// tests pass 0 → 0 no matter what the code does.
package database

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// countKeysWithPrefix counts the keys stored under prefix. This is the
// instrument for every test in this file: it reads the keyspace directly rather
// than any method whose behaviour is under test.
func countKeysWithPrefix(t *testing.T, s *PebbleActivityStore, prefix string) int {
	t.Helper()
	iter, err := s.DB().NewIter(&pebble.IterOptions{
		LowerBound: []byte(prefix),
		UpperBound: []byte(prefix[:len(prefix)-1] + ";"),
	})
	require.NoError(t, err)
	defer func() { require.NoError(t, iter.Close()) }()

	n := 0
	for iter.First(); iter.Valid(); iter.Next() {
		n++
	}
	return n
}

// countIndexKeys returns the number of act:op: and act:bk: index keys stored.
func countIndexKeys(t *testing.T, s *PebbleActivityStore) (opKeys, bookKeys int) {
	t.Helper()
	return countKeysWithPrefix(t, s, "act:op:"), countKeysWithPrefix(t, s, "act:bk:")
}

// seedIndexedEntries records n entries in tier at ts, each with a DISTINCT,
// non-empty OperationID and BookID so both secondary index families are
// populated. Returns nothing: the tests read the keyspace, not this function.
func seedIndexedEntries(t *testing.T, s *PebbleActivityStore, tier string, ts time.Time, n int) {
	t.Helper()
	for i := range n {
		_, err := s.Record(ActivityEntry{
			Timestamp:   ts.Add(time.Duration(i) * time.Millisecond),
			Tier:        tier,
			Type:        "metadata_apply",
			Level:       "info",
			Source:      "seed",
			OperationID: fmt.Sprintf("op-%s-%d", tier, i),
			BookID:      fmt.Sprintf("book-%s-%d", tier, i),
			Summary:     fmt.Sprintf("seeded %s entry %d", tier, i),
		})
		require.NoError(t, err)
	}
}

// requireSeeded asserts the fixture actually produced index rows. Without this
// a broken seeder turns every "0 index keys remain" assertion into a vacuous
// pass.
func requireSeeded(t *testing.T, s *PebbleActivityStore, want int) {
	t.Helper()
	opKeys, bookKeys := countIndexKeys(t, s)
	require.Equal(t, want, opKeys, "fixture must seed act:op: index keys, otherwise this test cannot observe the bug")
	require.Equal(t, want, bookKeys, "fixture must seed act:bk: index keys, otherwise this test cannot observe the bug")
}

// ── the leak: every deletion path must delete the row's index entries ────────

func TestPebbleActivityStore_PruneDeletesSecondaryIndexes(t *testing.T) {
	s := newTestPebbleActivityStore(t)
	old := time.Now().UTC().Add(-72 * time.Hour)

	seedIndexedEntries(t, s, "debug", old, 5)
	requireSeeded(t, s, 5)

	deleted, err := s.Prune(time.Now().UTC().Add(-1*time.Hour), "debug")
	require.NoError(t, err)
	assert.Equal(t, 5, deleted)

	require.Equal(t, 0, countKeysWithPrefix(t, s, "act:debug:"), "primary rows must be gone")
	opKeys, bookKeys := countIndexKeys(t, s)
	assert.Equal(t, 0, opKeys, "Prune must delete each row's act:op: index entry")
	assert.Equal(t, 0, bookKeys, "Prune must delete each row's act:bk: index entry")
}

func TestPebbleActivityStore_SummarizeDeletesSecondaryIndexes(t *testing.T) {
	s := newTestPebbleActivityStore(t)
	old := time.Now().UTC().Add(-72 * time.Hour)

	seedIndexedEntries(t, s, "change", old, 4)
	requireSeeded(t, s, 4)

	summarized, err := s.Summarize(context.Background(), time.Now().UTC().Add(-1*time.Hour), "change")
	require.NoError(t, err)
	assert.Equal(t, 4, summarized)

	opKeys, bookKeys := countIndexKeys(t, s)
	assert.Equal(t, 0, opKeys, "Summarize must delete each summarized row's act:op: index entry")
	assert.Equal(t, 0, bookKeys, "Summarize must delete each summarized row's act:bk: index entry")
}

func TestPebbleActivityStore_CompactByDayDeletesSecondaryIndexes(t *testing.T) {
	s := newTestPebbleActivityStore(t)
	old := time.Now().UTC().Add(-72 * time.Hour)

	seedIndexedEntries(t, s, "change", old, 3)
	seedIndexedEntries(t, s, "audit", old, 2)
	requireSeeded(t, s, 5)

	res, err := s.CompactByDay(context.Background(), time.Now().UTC().Add(-1*time.Hour))
	require.NoError(t, err)
	assert.Equal(t, 5, res.EntriesDeleted)

	opKeys, bookKeys := countIndexKeys(t, s)
	assert.Equal(t, 0, opKeys, "CompactByDay must delete each compacted row's act:op: index entry")
	assert.Equal(t, 0, bookKeys, "CompactByDay must delete each compacted row's act:bk: index entry")
	assert.Positive(t, countKeysWithPrefix(t, s, "act:digest:"), "digest row should have been written")
}

// TestPebbleActivityStore_WipeAllActivityWipesIndexes covers the contract that
// is in the function's name. The fixture deliberately contains three shapes the
// per-row delete path cannot reach on its own: a live row, an already-orphaned
// index entry, and an index entry whose primary row holds undecodable JSON.
func TestPebbleActivityStore_WipeAllActivityWipesIndexes(t *testing.T) {
	s := newTestPebbleActivityStore(t)

	seedIndexedEntries(t, s, "change", time.Now().UTC(), 3)
	requireSeeded(t, s, 3)

	// An index entry whose primary row was deleted long ago (the population
	// this fix inherits on production).
	require.NoError(t, s.DB().Set(
		[]byte("act:op:op-orphan:00000000001700000000:01ABCDEF"),
		[]byte("change:00000000001700000000:01ABCDEF"), pebble.Sync))

	// A primary row whose JSON will not decode, plus its index entry.
	// scanTierKVs drops the row, so the per-row path never sees it.
	require.NoError(t, s.DB().Set(
		[]byte("act:change:00000000001700000001:01BADJSON"),
		[]byte("{not json"), pebble.Sync))
	require.NoError(t, s.DB().Set(
		[]byte("act:bk:book-undecodable:00000000001700000001:01BADJSON"),
		[]byte("change:00000000001700000001:01BADJSON"), pebble.Sync))

	opBefore, bookBefore := countIndexKeys(t, s)
	require.Equal(t, 4, opBefore)
	require.Equal(t, 4, bookBefore)

	total, err := s.WipeAllActivity(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(3), total, "the returned count is primary rows, not index keys")

	opKeys, bookKeys := countIndexKeys(t, s)
	assert.Equal(t, 0, opKeys, "WipeAllActivity must leave no act:op: entries")
	assert.Equal(t, 0, bookKeys, "WipeAllActivity must leave no act:bk: entries")
	assert.Equal(t, 0, countKeysWithPrefix(t, s, "act:change:"),
		"WipeAllActivity must leave no primary rows either, including the one whose JSON will not decode")
	assert.Equal(t, 0, countKeysWithPrefix(t, s, "act:"),
		"nothing under the act: prefix may survive a wipe")
}

// ── the repair: orphans that already exist must have a route to deletion ─────

// seedOrphanedIndexEntries records n entries and then deletes ONLY their
// primary rows, reproducing exactly what every deletion path did before this
// fix: index entries left pointing at primaries that no longer exist.
func seedOrphanedIndexEntries(t *testing.T, s *PebbleActivityStore, tier string, n int) {
	t.Helper()
	seedIndexedEntries(t, s, tier, time.Now().UTC().Add(-48*time.Hour), n)

	iter, err := s.DB().NewIter(&pebble.IterOptions{
		LowerBound: pactPrimaryPrefix(tier),
		UpperBound: pactPrimaryUpperBound(tier),
	})
	require.NoError(t, err)
	var keys [][]byte
	for iter.First(); iter.Valid(); iter.Next() {
		keys = append(keys, append([]byte(nil), iter.Key()...))
	}
	require.NoError(t, iter.Close())
	require.Len(t, keys, n)

	for _, k := range keys {
		require.NoError(t, s.DB().Delete(k, pebble.Sync))
	}
}

func TestPebbleActivityStore_RepairRemovesOrphanedIndexEntries(t *testing.T) {
	s := newTestPebbleActivityStore(t)

	// 6 orphans (primary deleted behind the index's back)...
	seedOrphanedIndexEntries(t, s, "debug", 6)
	// ...and 4 live rows whose indexes must survive untouched.
	seedIndexedEntries(t, s, "change", time.Now().UTC(), 4)

	opBefore, bookBefore := countIndexKeys(t, s)
	require.Equal(t, 10, opBefore, "fixture must contain both orphaned and live index rows")
	require.Equal(t, 10, bookBefore)

	res, err := s.RepairActivityIndexes(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(20), res.Scanned)
	assert.Equal(t, int64(12), res.Orphaned, "6 orphaned rows × 2 index families")
	assert.Equal(t, int64(12), res.Deleted)
	assert.Equal(t, int64(0), res.Malformed)

	opAfter, bookAfter := countIndexKeys(t, s)
	assert.Equal(t, 4, opAfter, "the 4 live rows must keep their act:op: entries")
	assert.Equal(t, 4, bookAfter, "the 4 live rows must keep their act:bk: entries")
	assert.Equal(t, 4, countKeysWithPrefix(t, s, "act:change:"), "repair must not touch primary rows")
}

func TestPebbleActivityStore_RepairDeletesMalformedRefs(t *testing.T) {
	s := newTestPebbleActivityStore(t)

	// A ref with no ':' cannot be turned back into a primary key, so no reader
	// can follow it either. It must be deleted and counted, not skipped.
	require.NoError(t, s.DB().Set(
		[]byte("act:op:op-broken:00000000001700000000:01ABCDEF"),
		[]byte("garbage-no-separator"), pebble.Sync))
	seedIndexedEntries(t, s, "change", time.Now().UTC(), 2)

	opBefore, _ := countIndexKeys(t, s)
	require.Equal(t, 3, opBefore)

	res, err := s.RepairActivityIndexes(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(1), res.Malformed)
	assert.Equal(t, int64(0), res.Orphaned)
	assert.Equal(t, int64(1), res.Deleted)

	opAfter, bookAfter := countIndexKeys(t, s)
	assert.Equal(t, 2, opAfter)
	assert.Equal(t, 2, bookAfter)
}

// TestPebbleActivityStore_RepairIsChunkedAcrossWorkers shrinks the chunk size so
// the errgroup actually dispatches many chunks concurrently. Chunks are disjoint
// by construction (one sequential iterator produces each key exactly once), so
// the outcome must be identical to the single-chunk case — this is the
// regression guard for a worker ever double-handling a key.
func TestPebbleActivityStore_RepairIsChunkedAcrossWorkers(t *testing.T) {
	orig := activityRepairChunkSize
	activityRepairChunkSize = 3
	t.Cleanup(func() { activityRepairChunkSize = orig })

	s := newTestPebbleActivityStore(t)
	seedOrphanedIndexEntries(t, s, "debug", 25)
	seedIndexedEntries(t, s, "change", time.Now().UTC(), 7)

	opBefore, bookBefore := countIndexKeys(t, s)
	require.Equal(t, 32, opBefore)
	require.Equal(t, 32, bookBefore)

	res, err := s.RepairActivityIndexes(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(64), res.Scanned)
	assert.Equal(t, int64(50), res.Orphaned)
	assert.Equal(t, int64(50), res.Deleted)

	opAfter, bookAfter := countIndexKeys(t, s)
	assert.Equal(t, 7, opAfter)
	assert.Equal(t, 7, bookAfter)
}

// TestPebbleActivityStore_RepairIsIdempotent proves a second nightly run is
// cheap and finds nothing, which is what makes wiring it into the daily
// maintenance job safe.
func TestPebbleActivityStore_RepairIsIdempotent(t *testing.T) {
	s := newTestPebbleActivityStore(t)
	seedOrphanedIndexEntries(t, s, "debug", 5)

	first, err := s.RepairActivityIndexes(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(10), first.Deleted)

	second, err := s.RepairActivityIndexes(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(0), second.Scanned)
	assert.Equal(t, int64(0), second.Deleted)
}

// TestPebbleActivityStore_PruneLeavesNothingForRepair is the end-to-end
// statement of the fix: after the leak is closed, a prune produces zero work
// for the repair pass. Before the fix this reported 10 orphans.
func TestPebbleActivityStore_PruneLeavesNothingForRepair(t *testing.T) {
	s := newTestPebbleActivityStore(t)
	seedIndexedEntries(t, s, "debug", time.Now().UTC().Add(-72*time.Hour), 5)
	requireSeeded(t, s, 5)

	_, err := s.Prune(time.Now().UTC().Add(-1*time.Hour), "debug")
	require.NoError(t, err)

	res, err := s.RepairActivityIndexes(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(0), res.Scanned, "a pruned row must leave no index entries behind at all")
	assert.Equal(t, int64(0), res.Orphaned)
}
