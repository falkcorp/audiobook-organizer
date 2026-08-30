// file: internal/database/pebble_activity_batch_test.go
// version: 1.0.0
// guid: 8b41d0c7-3e59-4a16-b2d7-6f9c1a840e35
// last-edited: 2026-08-30

// Package database — regression suite for the batched activity write path.
//
// WHY a dedicated file, and why every assertion reads the KEYSPACE: RecordBatch
// exists to replace N durable commits with one, and the thing that can go wrong
// while it still "works" is key drift. Pebble's Delete of a key that does not
// exist succeeds silently, so an index key written in a format no delete path
// derives would be invisible: RecordBatch would return a happy count, Query
// would answer from the primary rows, and only the disk would know. That is the
// defect pactDeleteEntry was added to repair. A test that trusted RecordBatch's
// return value could not see it, so nothing here does.
package database

import (
	"context"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// collectKeysWithPrefix returns every key stored under prefix, sorted. Unlike
// countKeysWithPrefix it keeps the bytes, because the key-identity test has to
// compare formats and not just totals.
func collectKeysWithPrefix(t *testing.T, s *PebbleActivityStore, prefix string) []string {
	t.Helper()
	iter, err := s.DB().NewIter(&pebble.IterOptions{
		LowerBound: []byte(prefix),
		UpperBound: []byte(prefix[:len(prefix)-1] + ";"),
	})
	require.NoError(t, err)
	defer func() { require.NoError(t, iter.Close()) }()

	var keys []string
	for iter.First(); iter.Valid(); iter.Next() {
		keys = append(keys, string(iter.Key()))
	}
	sort.Strings(keys)
	return keys
}

// batchTestEntry builds one entry with BOTH ids set, so both index families are
// exercised. An entry with neither would make every index assertion here pass
// 0 → 0 regardless of what the code does.
func batchTestEntry(i int, ts time.Time) ActivityEntry {
	return ActivityEntry{
		Timestamp:   ts.Add(time.Duration(i) * time.Millisecond),
		Tier:        "change",
		Type:        "metadata_apply",
		Level:       "info",
		Source:      "batch-test",
		OperationID: fmt.Sprintf("op-%d", i),
		BookID:      fmt.Sprintf("book-%d", i),
		Summary:     fmt.Sprintf("batched entry %d", i),
	}
}

func batchTestEntries(n int, ts time.Time) []ActivityEntry {
	out := make([]ActivityEntry, 0, n)
	for i := range n {
		out = append(out, batchTestEntry(i, ts))
	}
	return out
}

// TestRecordBatch_WritesPrimaryAndBothIndexFamilies is the baseline: a batched
// write must leave the keyspace in the same shape a run of Records would —
// one primary row and one key in each index family per entry.
func TestRecordBatch_WritesPrimaryAndBothIndexFamilies(t *testing.T) {
	s := newTestPebbleActivityStore(t)
	ts := time.Now().UTC().Add(-time.Hour)

	const n = 25
	written, err := s.RecordBatch(batchTestEntries(n, ts))
	require.NoError(t, err)
	require.Equal(t, n, written)

	assert.Equal(t, n, countKeysWithPrefix(t, s, "act:change:"), "primary rows")
	opKeys, bookKeys := countIndexKeys(t, s)
	assert.Equal(t, n, opKeys, "act:op: index keys")
	assert.Equal(t, n, bookKeys, "act:bk: index keys")
}

// TestRecordBatch_KeysAreByteIdenticalToRecord is the anti-drift guard.
//
// The same entries are written into two separate stores, one entry at a time
// through Record and all at once through RecordBatch, and the FULL key sets are
// compared. The ULID and the synthetic ID differ per write, so the comparison
// normalizes the ULID suffix away and compares everything else — prefix, tier,
// timestamp field, id component, and the shape of both index families.
//
// If this ever fails, deletion silently no-ops for whichever path drifted.
func TestRecordBatch_KeysAreByteIdenticalToRecord(t *testing.T) {
	ts := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	entries := batchTestEntries(10, ts)

	viaRecord := newTestPebbleActivityStore(t)
	for _, e := range entries {
		_, err := viaRecord.Record(e)
		require.NoError(t, err)
	}

	viaBatch := newTestPebbleActivityStore(t)
	written, err := viaBatch.RecordBatch(entries)
	require.NoError(t, err)
	require.Equal(t, len(entries), written)

	// stripULID replaces the trailing ULID component — the only part of a key
	// that is random per write — with a fixed token, so two runs of the same
	// entries are comparable byte-for-byte everywhere else.
	stripULID := func(keys []string) []string {
		out := make([]string, 0, len(keys))
		for _, k := range keys {
			i := len(k) - 1
			for i >= 0 && k[i] != ':' {
				i--
			}
			out = append(out, k[:i+1]+"<ulid>")
		}
		sort.Strings(out)
		return out
	}

	for _, prefix := range []string{"act:change:", "act:op:", "act:bk:"} {
		got := stripULID(collectKeysWithPrefix(t, viaBatch, prefix))
		want := stripULID(collectKeysWithPrefix(t, viaRecord, prefix))
		require.NotEmpty(t, want, "fixture wrote no %s keys — the comparison would be vacuous", prefix)
		assert.Equal(t, want, got, "%s keys written by RecordBatch differ from Record", prefix)
	}
}

// TestRecordBatch_RowsAreDeletableByPrune proves the round trip: what the batch
// path wrote, the delete path can actually remove — primary AND both index
// families, leaving zero orphans.
//
// This is the assertion that would have caught the original orphan leak. Prune
// returns a count of primary rows and returned the same count before and after
// that fix, so only the keyspace can answer.
func TestRecordBatch_RowsAreDeletableByPrune(t *testing.T) {
	s := newTestPebbleActivityStore(t)
	old := time.Now().UTC().Add(-48 * time.Hour)

	const n = 12
	written, err := s.RecordBatch(batchTestEntries(n, old))
	require.NoError(t, err)
	require.Equal(t, n, written)

	opKeys, bookKeys := countIndexKeys(t, s)
	require.Equal(t, n, opKeys, "fixture must create op index keys or the test is vacuous")
	require.Equal(t, n, bookKeys, "fixture must create book index keys or the test is vacuous")

	pruned, err := s.Prune(time.Now().UTC().Add(-time.Hour), "change")
	require.NoError(t, err)
	assert.Equal(t, n, pruned)

	assert.Zero(t, countKeysWithPrefix(t, s, "act:change:"), "primary rows left after prune")
	opKeys, bookKeys = countIndexKeys(t, s)
	assert.Zero(t, opKeys, "orphaned act:op: keys left after prune")
	assert.Zero(t, bookKeys, "orphaned act:bk: keys left after prune")
}

// TestRecordBatch_RowsAreDeletableByWipeAllActivity is the same round trip
// through the other deletion contract.
func TestRecordBatch_RowsAreDeletableByWipeAllActivity(t *testing.T) {
	s := newTestPebbleActivityStore(t)
	ts := time.Now().UTC().Add(-time.Hour)

	const n = 8
	written, err := s.RecordBatch(batchTestEntries(n, ts))
	require.NoError(t, err)
	require.Equal(t, n, written)
	require.Equal(t, n, countKeysWithPrefix(t, s, "act:change:"))

	// Assert the index families are NON-ZERO first. Without this the "zero
	// keys remain" assertions below pass 0 → 0 against a batch path that never
	// wrote an index key, or wrote it under some other prefix.
	opBefore, bookBefore := countIndexKeys(t, s)
	require.Equal(t, n, opBefore, "fixture must create act:op: keys or the wipe assertions are vacuous")
	require.Equal(t, n, bookBefore, "fixture must create act:bk: keys or the wipe assertions are vacuous")

	_, err = s.WipeAllActivity(context.Background())
	require.NoError(t, err)

	assert.Zero(t, countKeysWithPrefix(t, s, "act:change:"), "primary rows left after wipe")
	opKeys, bookKeys := countIndexKeys(t, s)
	assert.Zero(t, opKeys, "act:op: keys left after wipe")
	assert.Zero(t, bookKeys, "act:bk: keys left after wipe")
}

// TestRecordBatch_SplitsOverTheCap pins the memory bound: a flush larger than
// activityRecordBatchCap must become several commits, and every entry must
// still land. The cap is shrunk rather than the fixture grown, which is why it
// is a var.
func TestRecordBatch_SplitsOverTheCap(t *testing.T) {
	s := newTestPebbleActivityStore(t)

	orig := activityRecordBatchCap
	activityRecordBatchCap = 4
	t.Cleanup(func() { activityRecordBatchCap = orig })

	ts := time.Now().UTC().Add(-time.Hour)
	const n = 11 // 4 + 4 + 3 — deliberately not a multiple of the cap

	before := pactRecordBatchCommits.Load()
	written, err := s.RecordBatch(batchTestEntries(n, ts))
	require.NoError(t, err)
	require.Equal(t, n, written)

	// The commit count is the actual subject. 11 entries look identical on disk
	// whether they went down in one commit or three, so asserting only on rows
	// would pass against a cap that had stopped splitting anything.
	assert.Equal(t, int64(3), pactRecordBatchCommits.Load()-before,
		"11 entries at a cap of %d must be 3 commits, not one unbounded batch", activityRecordBatchCap)

	assert.Equal(t, n, countKeysWithPrefix(t, s, "act:change:"), "primary rows across the split")
	opKeys, bookKeys := countIndexKeys(t, s)
	assert.Equal(t, n, opKeys, "act:op: keys across the split")
	assert.Equal(t, n, bookKeys, "act:bk: keys across the split")
}

// TestRecordBatch_DropsUnmarshalableEntryAndCommitsTheRest pins the chosen
// error semantics.
//
// A row whose Details will not marshal is the ONE failure attributable to a
// single entry, and it must not take the rest of the flush with it: the good
// rows commit, the count returned names only those, and the error names the
// loss. Silence would be the unacceptable outcome, so the test asserts an
// error IS returned even though the batch mostly succeeded.
func TestRecordBatch_DropsUnmarshalableEntryAndCommitsTheRest(t *testing.T) {
	s := newTestPebbleActivityStore(t)
	ts := time.Now().UTC().Add(-time.Hour)

	entries := batchTestEntries(5, ts)
	// A channel cannot be marshalled to JSON; nothing else about this entry is
	// unusual, so only the marshal step can reject it.
	entries[2].Details = map[string]any{"unmarshalable": make(chan int)}

	written, err := s.RecordBatch(entries)
	require.Error(t, err, "a dropped entry must be reported, never swallowed")
	assert.Equal(t, 4, written, "the four good entries must still be durable")
	assert.Contains(t, err.Error(), "dropped 1 of 5")

	assert.Equal(t, 4, countKeysWithPrefix(t, s, "act:change:"), "primary rows")
	opKeys, bookKeys := countIndexKeys(t, s)
	assert.Equal(t, 4, opKeys, "act:op: keys")
	assert.Equal(t, 4, bookKeys, "act:bk: keys")
}

// TestRecordBatch_EmptyInputIsANoOp guards the boundary the split loop would
// otherwise turn into an empty commit.
func TestRecordBatch_EmptyInputIsANoOp(t *testing.T) {
	s := newTestPebbleActivityStore(t)

	written, err := s.RecordBatch(nil)
	require.NoError(t, err)
	assert.Zero(t, written)
	assert.Zero(t, countKeysWithPrefix(t, s, "act:change:"))
}

// benchmarkActivityEntries builds the shared fixture for the two benchmarks
// below so they differ only in how they write.
func benchmarkActivityEntries(n int) []ActivityEntry {
	ts := time.Now().UTC().Add(-time.Hour)
	return batchTestEntries(n, ts)
}

// BenchmarkActivityRecordPerEntry measures the pre-fix write path: one
// pebble.Sync commit per entry, which is what Writer.writeBatch did for every
// entry in every flush.
func BenchmarkActivityRecordPerEntry(b *testing.B) {
	const rows = 5000
	entries := benchmarkActivityEntries(rows)

	for b.Loop() {
		b.StopTimer()
		s := newBenchPebbleActivityStore(b)
		b.StartTimer()
		for _, e := range entries {
			if _, err := s.Record(e); err != nil {
				b.Fatalf("Record: %v", err)
			}
		}
	}
	b.ReportMetric(float64(rows)/b.Elapsed().Seconds()*float64(b.N), "rows/sec")
}

// BenchmarkActivityRecordBatch measures the same rows, same durability
// (pebble.Sync), written through the batched path.
func BenchmarkActivityRecordBatch(b *testing.B) {
	const rows = 5000
	entries := benchmarkActivityEntries(rows)

	for b.Loop() {
		b.StopTimer()
		s := newBenchPebbleActivityStore(b)
		b.StartTimer()
		if _, err := s.RecordBatch(entries); err != nil {
			b.Fatalf("RecordBatch: %v", err)
		}
	}
	b.ReportMetric(float64(rows)/b.Elapsed().Seconds()*float64(b.N), "rows/sec")
}

// newBenchPebbleActivityStore is newTestPebbleActivityStore for a *testing.B.
func newBenchPebbleActivityStore(b *testing.B) *PebbleActivityStore {
	b.Helper()
	db, err := pebble.Open(b.TempDir(), &pebble.Options{})
	if err != nil {
		b.Fatalf("pebble.Open: %v", err)
	}
	b.Cleanup(func() { _ = db.Close() })
	return NewPebbleActivityStore(db)
}
