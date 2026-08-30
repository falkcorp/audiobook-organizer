// file: internal/activity/writer_batch_test.go
// version: 1.0.0
// guid: 2c7e5f10-9a63-4d84-8b21-4e0d7c395a6f
// last-edited: 2026-08-30

// Package activity — regression suite for the batched write path in
// Writer.writeBatch.
//
// WHY these tests are here and not only in internal/database: the store gained
// RecordBatch, but the defect was in the CALLER. writeBatch received a slice
// and then called store.Record once per entry, and Record commits with
// pebble.Sync — so the batching layer amortized rows and not fsyncs, and a
// flush of 100 entries cost 100 durable commits. A store-only test cannot see
// that, because the store was never the thing choosing to write one at a time.
//
// Every test backs its fake with a REAL PebbleActivityStore and asserts on the
// resulting keyspace, so "the entries were written" is read off the disk rather
// than off a return value or a mock's recorded calls. The call counters exist
// only to pin WHICH path ran; the keyspace pins that it ran correctly.
package activity

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// countingBatchStore is a real PebbleActivityStore that also counts how it was
// asked to write. Embedding the concrete store (rather than reimplementing the
// interface) means every assertion below runs against production storage code:
// a fake that "stored" entries in a map could not catch a key-format mistake.
type countingBatchStore struct {
	*database.PebbleActivityStore
	recordCalls atomic.Int64
	batchCalls  atomic.Int64
}

func (c *countingBatchStore) Record(e database.ActivityEntry) (int64, error) {
	c.recordCalls.Add(1)
	return c.PebbleActivityStore.Record(e)
}

func (c *countingBatchStore) RecordBatch(entries []database.ActivityEntry) (int, error) {
	c.batchCalls.Add(1)
	return c.PebbleActivityStore.RecordBatch(entries)
}

// noBatchStore hides RecordBatch behind the plain ActivityStorer interface, so
// a Writer given one must take the per-entry fallback. Embedding the INTERFACE
// (not the struct) is what drops the method: only ActivityStorer's own methods
// are promoted.
type noBatchStore struct {
	database.ActivityStorer
}

func newCountingBatchStore(t *testing.T) *countingBatchStore {
	t.Helper()
	db, err := pebble.Open(t.TempDir(), &pebble.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return &countingBatchStore{PebbleActivityStore: database.NewPebbleActivityStore(db)}
}

// countActivityKeys reads the keyspace directly — the instrument for every
// assertion here, chosen because it is downstream of everything under test.
func countActivityKeys(t *testing.T, s *database.PebbleActivityStore, prefix string) int {
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

// writerTestEntries builds entries with BOTH ids set so the secondary index
// families are populated; an entry with neither would make the index
// assertions pass 0 → 0 no matter what the code does.
func writerTestEntries(n int) []database.ActivityEntry {
	ts := time.Now().UTC().Add(-time.Hour)
	out := make([]database.ActivityEntry, 0, n)
	for i := range n {
		out = append(out, database.ActivityEntry{
			Timestamp:   ts.Add(time.Duration(i) * time.Millisecond),
			Tier:        "change",
			Type:        "metadata_apply",
			Level:       "info",
			Source:      "writer-batch-test",
			OperationID: fmt.Sprintf("op-%d", i),
			BookID:      fmt.Sprintf("book-%d", i),
			Summary:     fmt.Sprintf("entry %d", i),
		})
	}
	return out
}

// TestWriteBatch_UsesOneCommitForTheWholeFlush is the test that fails before
// the fix.
//
// Before: writeBatch called Record 100 times, so recordCalls == 100 and
// batchCalls == 0 — 100 fsyncs for one flush. After: one RecordBatch call, one
// durable commit, and the same rows on disk.
func TestWriteBatch_UsesOneCommitForTheWholeFlush(t *testing.T) {
	store := newCountingBatchStore(t)
	w := NewWriter(store, 200)

	const n = 100
	w.writeBatch(writerTestEntries(n))

	assert.Equal(t, int64(1), store.batchCalls.Load(), "the whole flush must be one batched write")
	assert.Zero(t, store.recordCalls.Load(), "no entry may take the per-entry commit path")

	// The rows must actually be there — a batched write that stored nothing
	// would satisfy the call counts above.
	assert.Equal(t, n, countActivityKeys(t, store.PebbleActivityStore, "act:change:"), "primary rows")
	assert.Equal(t, n, countActivityKeys(t, store.PebbleActivityStore, "act:op:"), "act:op: index keys")
	assert.Equal(t, n, countActivityKeys(t, store.PebbleActivityStore, "act:bk:"), "act:bk: index keys")
}

// TestWriteBatch_EnrichesTagsOnTheBatchedPath pins behaviour the batched path
// must not drop. Tag enrichment used to happen inside the per-entry loop; if it
// had been left there, every row written by the new path would reach the
// Activity Log UI untagged and the row count above would still be right.
func TestWriteBatch_EnrichesTagsOnTheBatchedPath(t *testing.T) {
	store := newCountingBatchStore(t)
	w := NewWriter(store, 50)

	entries := writerTestEntries(3)
	w.writeBatch(entries)
	require.Equal(t, int64(1), store.batchCalls.Load(), "fixture must exercise the batched path")

	stored, _, err := store.Query(context.Background(), database.ActivityFilter{Limit: 10})
	require.NoError(t, err)
	require.Len(t, stored, 3)
	for _, e := range stored {
		assert.NotEmpty(t, e.Tags, "batched rows must carry derived tags, same as per-entry rows")
	}
}

// TestWriteBatch_FallsBackToPerEntryWithoutTheCapability proves the fallback is
// a real path and not a dead branch: a store that does not implement
// batchRecorder still gets every row, durably, one commit at a time.
func TestWriteBatch_FallsBackToPerEntryWithoutTheCapability(t *testing.T) {
	store := newCountingBatchStore(t)
	w := NewWriter(noBatchStore{ActivityStorer: store}, 200)
	require.Nil(t, w.batchStore, "the fixture must not expose RecordBatch, or it tests the wrong path")

	const n = 20
	w.writeBatch(writerTestEntries(n))

	assert.Zero(t, store.batchCalls.Load(), "no batched write is available here")
	assert.Equal(t, int64(n), store.recordCalls.Load(), "every entry takes its own commit")
	assert.Equal(t, n, countActivityKeys(t, store.PebbleActivityStore, "act:change:"), "primary rows")
	assert.Equal(t, n, countActivityKeys(t, store.PebbleActivityStore, "act:op:"), "act:op: index keys")
}

// failingBatchStore commits nothing and reports the loss, standing in for a
// full or failing disk.
type failingBatchStore struct {
	database.ActivityStorer
}

func (failingBatchStore) RecordBatch([]database.ActivityEntry) (int, error) {
	return 0, fmt.Errorf("simulated commit failure")
}

// TestWriteBatch_ReportsALostBatch pins the error semantics at the caller.
//
// A commit failure loses every row in the flush, and that is accepted — losing
// them SILENTLY is not. The report goes to stdout rather than through slog on
// purpose: this Writer is what the log system tees into, so logging a failed
// flush would enqueue an entry whose own flush fails and logs again.
func TestWriteBatch_ReportsALostBatch(t *testing.T) {
	store := newCountingBatchStore(t)
	w := NewWriter(failingBatchStore{ActivityStorer: store}, 50)
	require.NotNil(t, w.batchStore, "the fixture must take the batched path")

	var out bytes.Buffer
	w.stdout = &out

	w.writeBatch(writerTestEntries(7))

	logged := out.String()
	assert.Contains(t, logged, "lost 7 of 7 entries", "the loss must name how many rows went missing")
	assert.Contains(t, logged, "simulated commit failure", "the cause must be reported")
}

// TestWriteBatch_ReportsAPartialLoss covers the other half of the semantics:
// when the store commits most of a flush and drops one entry, the report must
// name the entries actually lost — one — and not the whole flush.
func TestWriteBatch_ReportsAPartialLoss(t *testing.T) {
	store := newCountingBatchStore(t)
	w := NewWriter(store, 50)

	var out bytes.Buffer
	w.stdout = &out

	entries := writerTestEntries(5)
	entries[1].Details = map[string]any{"unmarshalable": make(chan int)}
	w.writeBatch(entries)

	assert.Contains(t, out.String(), "lost 1 of 5 entries")
	assert.Equal(t, 4, countActivityKeys(t, store.PebbleActivityStore, "act:change:"),
		"the four good rows must still be durable")
}

// TestFlush_WritesEveryQueuedEntryInOneCommit covers the other caller changed
// by this fix. Flush drained the channel and recorded entry by entry, the same
// one-fsync-per-entry shape writeBatch had; it now routes through writeBatch,
// and the contract callers rely on — everything queued is durable when Flush
// returns — must be unchanged.
func TestFlush_WritesEveryQueuedEntryInOneCommit(t *testing.T) {
	store := newCountingBatchStore(t)
	w := NewWriter(store, 100)

	const n = 30
	for _, e := range writerTestEntries(n) {
		w.ch <- e
	}

	w.Flush()

	assert.Equal(t, int64(1), store.batchCalls.Load(), "a queue under the chunk size is one commit")
	assert.Zero(t, store.recordCalls.Load())
	assert.Equal(t, n, countActivityKeys(t, store.PebbleActivityStore, "act:change:"), "primary rows")
	assert.Equal(t, n, countActivityKeys(t, store.PebbleActivityStore, "act:bk:"), "act:bk: index keys")
}

// TestFlush_BoundsItsAccumulator pins the memory bound Flush needs BECAUSE it
// now batches.
//
// The per-entry version could not grow — it committed each entry as it pulled
// it. A drain that buffers everything before writing grows without limit while
// producers keep filling the channel, and Flush runs precisely then (scanner
// and iTunes shutdown with work in flight) on a service that has OOMed before.
// Shrinking the chunk rather than growing the fixture is why flushChunkSize is
// a var.
func TestFlush_BoundsItsAccumulator(t *testing.T) {
	store := newCountingBatchStore(t)
	w := NewWriter(store, 100)

	orig := flushChunkSize
	flushChunkSize = 5
	t.Cleanup(func() { flushChunkSize = orig })

	const n = 23 // 4 full chunks of 5 plus a final partial 3
	for _, e := range writerTestEntries(n) {
		w.ch <- e
	}

	w.Flush()

	assert.Equal(t, int64(5), store.batchCalls.Load(),
		"a queue of %d at a chunk of %d must be written in bounded pieces, not one growing slice", n, flushChunkSize)
	assert.Equal(t, n, countActivityKeys(t, store.PebbleActivityStore, "act:change:"),
		"every entry must still land despite the split")
}

// TestStart_LogsWhichWritePathIsLive pins the assertion-hit signal. A type
// assertion that misses is only acceptable because the miss is observable; if
// this line disappears, "is batching actually on in production" stops being a
// question the startup log can answer.
func TestStart_LogsWhichWritePathIsLive(t *testing.T) {
	t.Run("capability present", func(t *testing.T) {
		store := newCountingBatchStore(t)
		w := NewWriter(store, 10)
		var out bytes.Buffer
		w.stdout = &out
		require.NoError(t, w.Start(context.Background()))
		t.Cleanup(func() { _ = w.Stop(context.Background()) })

		assert.True(t, strings.Contains(out.String(), "batched activity writes enabled"),
			"startup must say the batched path is live, got %q", out.String())
	})

	t.Run("capability absent", func(t *testing.T) {
		store := newCountingBatchStore(t)
		w := NewWriter(noBatchStore{ActivityStorer: store}, 10)
		var out bytes.Buffer
		w.stdout = &out
		require.NoError(t, w.Start(context.Background()))
		t.Cleanup(func() { _ = w.Stop(context.Background()) })

		assert.True(t, strings.Contains(out.String(), "no batch write path"),
			"a missed assertion must not be silent, got %q", out.String())
	})
}
