// file: internal/server/batch_poller_durable_test.go
// version: 1.1.0
// guid: fe113f87-b567-440e-8ec1-63022b2e72db
// last-edited: 2026-09-19

package server

import (
	"context"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/ai"
	"github.com/falkcorp/audiobook-organizer/internal/ai/aijobs"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// (a) A batch handled before a restart is not dispatched again by a new poller
// on the same store.
func TestBatchPoller_RestartDoesNotRedispatchHandledBatch(t *testing.T) {
	store := newPollerTestStore(t)
	client := &fakeBatchClient{batches: []ai.BatchInfo{
		{ID: "batch_a", Status: "completed", Type: "t", OutputFileID: "f"},
		{ID: "batch_running", Status: "in_progress", Type: "t"},
	}}
	var calls atomic.Int32
	handler := func(context.Context, string, string) error { calls.Add(1); return nil }

	first := newTestPoller(t, store, client)
	first.RegisterHandler("t", handler)
	n, err := first.Poll(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, n)

	// "Restart": a fresh poller, empty memory, same store.
	second := newTestPoller(t, store, client)
	second.RegisterHandler("t", handler)
	n, err = second.Poll(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 0, n)
	assert.Equal(t, int32(1), calls.Load(), "handled batch was re-dispatched after restart")
	assert.True(t, second.IsProcessed("batch_a"))
	assert.False(t, second.IsProcessed("batch_running"))
}

// A failed handler is not recorded: the batch is retried on the next poll.
func TestBatchPoller_FailedHandlerRetried(t *testing.T) {
	store := newPollerTestStore(t)
	client := &fakeBatchClient{batches: []ai.BatchInfo{{ID: "batch_f", Status: "completed", Type: "t"}}}
	fail := true
	calls := 0
	bp := newTestPoller(t, store, client)
	bp.RegisterHandler("t", func(context.Context, string, string) error {
		calls++
		if fail {
			fail = false
			return assert.AnError
		}
		return nil
	})
	_, _ = bp.Poll(context.Background())
	assert.False(t, bp.IsProcessed("batch_f"))
	_, _ = bp.Poll(context.Background())
	_, _ = bp.Poll(context.Background())
	assert.Equal(t, 2, calls)
	assert.True(t, bp.IsProcessed("batch_f"))
}

// Two concurrent Polls must not dispatch the same batch twice.
func TestBatchPoller_ConcurrentPollsDispatchOnce(t *testing.T) {
	store := newPollerTestStore(t)
	client := &fakeBatchClient{batches: []ai.BatchInfo{{ID: "batch_c", Status: "completed", Type: "t"}}}
	release := make(chan struct{})
	var calls atomic.Int32
	bp := newTestPoller(t, store, client)
	bp.RegisterHandler("t", func(context.Context, string, string) error {
		calls.Add(1)
		<-release
		return nil
	})
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() { _, _ = bp.Poll(context.Background()) })
	}
	for calls.Load() == 0 { // wait for one Poll to enter the handler
		runtime.Gosched()
	}
	close(release)
	wg.Wait()
	assert.Equal(t, int32(1), calls.Load())
}

// seedAIJob creates a submitted aijobs row on the real store with a registered
// callback that counts invocations and records effects keyed by custom id.
type countingCallback struct {
	mu      sync.Mutex
	calls   int
	effects map[string]int
}

func (c *countingCallback) cb(_ context.Context, _ []byte, results []aijobs.RowResult) (int, int, []database.AIJobRowError, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	for _, r := range results {
		c.effects[r.CustomID]++
	}
	return len(results), 0, nil, nil
}

func aijobsPoller(t *testing.T, store *crashJournalStore, client *fakeBatchClient) *BatchPoller {
	t.Helper()
	bp := newTestPoller(t, store, client)
	get := func() database.AIJobsStore { return store }
	bp.RegisterHandler("aijobs", aijobsBatchHandler(client, get))
	bp.RegisterReconciler("aijobs", aijobsReconciler(get))
	bp.RegisterTracker("aijobs", aijobsTracker(get))
	return bp
}

// (b) Killed after Dispatch marked the job completed but before the poller's
// handled mark: the restarted poller re-delivers the batch exactly once, and the
// completed job row stops the callback from applying it a second time.
func TestBatchPoller_CrashBeforeHandledMark_NoDoubleApply(t *testing.T) {
	store := newPollerTestStore(t)
	cc := &countingCallback{effects: map[string]int{}}
	aijobs.Register("poller_crash_test", cc.cb)
	require.NoError(t, store.CreateAIJob(database.AIJob{ID: "JOBB", Type: "poller_crash_test", Status: "pending"}, []byte("[]")))
	require.NoError(t, store.MarkAIJobSubmitted("JOBB", "batch_b"))
	client := &fakeBatchClient{
		batches: []ai.BatchInfo{{ID: "batch_b", Status: "completed", Type: "aijobs", OutputFileID: "out_b"}},
		outputs: map[string][]ai.BatchRawResult{"out_b": {{CustomID: "JOBB-0", Content: "{}"}}},
	}

	store.failNextJournal = true
	_, err := aijobsPoller(t, store, client).Poll(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, cc.calls)

	restarted := aijobsPoller(t, store, client)
	_, err = restarted.Poll(context.Background())
	require.NoError(t, err)
	_, err = aijobsPoller(t, store, client).Poll(context.Background())
	require.NoError(t, err)

	assert.Equal(t, 1, cc.calls, "completed job was applied again after restart")
	assert.Equal(t, map[string]int{"JOBB-0": 1}, cc.effects)
	job, err := store.GetAIJob("JOBB")
	require.NoError(t, err)
	assert.Equal(t, "completed", job.Status)
	assert.True(t, restarted.IsProcessed("batch_b"), "re-delivery must record the batch handled")
}

// (c) Killed between CreateBatch and MarkAIJobSubmitted: the job is pending with
// no batch id. The first poll re-attaches it from the batch metadata and applies
// it; later polls apply nothing.
func TestBatchPoller_OrphanedJobAttachedAndAppliedOnce(t *testing.T) {
	store := newPollerTestStore(t)
	cc := &countingCallback{effects: map[string]int{}}
	aijobs.Register("poller_orphan_test", cc.cb)
	require.NoError(t, store.CreateAIJob(database.AIJob{ID: "JOBC", Type: "poller_orphan_test", Status: "pending"}, []byte("[]")))
	client := &fakeBatchClient{
		batches: []ai.BatchInfo{{
			ID: "batch_c", Status: "completed", Type: "aijobs", OutputFileID: "out_c",
			Metadata: map[string]string{"project": "audiobook-organizer", "type": "aijobs", aijobs.MetadataJobIDKey: "JOBC"},
		}},
		outputs: map[string][]ai.BatchRawResult{"out_c": {{CustomID: "JOBC-0", Content: "{}"}}},
	}

	bp := aijobsPoller(t, store, client)
	n, err := bp.Poll(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	job, err := store.GetAIJob("JOBC")
	require.NoError(t, err)
	assert.Equal(t, "batch_c", job.BatchID)
	assert.Equal(t, "completed", job.Status)

	_, _ = bp.Poll(context.Background())
	_, _ = aijobsPoller(t, store, client).Poll(context.Background())
	assert.Equal(t, 1, cc.calls)
}

// A batch type with no handler is skipped in memory only: a new poller (or a
// later build that registers a handler) must still see it.
func TestBatchPoller_UnhandledTypeNotJournaled(t *testing.T) {
	store := newPollerTestStore(t)
	client := &fakeBatchClient{batches: []ai.BatchInfo{{ID: "batch_u", Status: "completed", Type: "author_review"}}}
	_, err := newTestPoller(t, store, client).Poll(context.Background())
	require.NoError(t, err)

	var calls atomic.Int32
	later := newTestPoller(t, store, client)
	later.RegisterHandler("author_review", func(context.Context, string, string) error { calls.Add(1); return nil })
	_, err = later.Poll(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int32(1), calls.Load())
}

// A panicking handler must not leave its batch claimed forever.
func TestBatchPoller_PanickingHandlerReleasesClaim(t *testing.T) {
	store := newPollerTestStore(t)
	client := &fakeBatchClient{batches: []ai.BatchInfo{{ID: "batch_p", Status: "completed", Type: "t"}}}
	bp := newTestPoller(t, store, client)
	boom := true
	calls := 0
	bp.RegisterHandler("t", func(context.Context, string, string) error {
		calls++
		if boom {
			boom = false
			panic("boom")
		}
		return nil
	})
	require.Panics(t, func() { _, _ = bp.Poll(context.Background()) })
	_, err := bp.Poll(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 2, calls)
	assert.True(t, bp.IsProcessed("batch_p"))
}

func seedSubmittedJob(t *testing.T, store *crashJournalStore, id, typ, batchID string) *countingCallback {
	t.Helper()
	cc := &countingCallback{effects: map[string]int{}}
	aijobs.Register(typ, cc.cb)
	require.NoError(t, store.CreateAIJob(database.AIJob{ID: id, Type: typ, Status: "pending", CreatedAt: time.Now()}, []byte("[]")))
	require.NoError(t, store.MarkAIJobSubmitted(id, batchID))
	return cc
}

// FIX-2: a batch that has scrolled out of the recent listing is still fetched
// by id from the store's own list of owed batches, and applied.
func TestBatchPoller_TrackedBatchOutsideListingStillApplied(t *testing.T) {
	store := newPollerTestStore(t)
	cc := seedSubmittedJob(t, store, "JOBL", "poller_unlisted_test", "batch_old")
	client := &fakeBatchClient{
		unlisted: []ai.BatchInfo{{ID: "batch_old", Status: "completed", Type: "aijobs", OutputFileID: "out_old"}},
		outputs:  map[string][]ai.BatchRawResult{"out_old": {{CustomID: "JOBL-0", Content: "{}"}}},
	}
	n, err := aijobsPoller(t, store, client).Poll(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	assert.Equal(t, 1, cc.calls)
	job, _ := store.GetAIJob("JOBL")
	assert.Equal(t, "completed", job.Status)

	// Completed jobs are no longer tracked: no further fetches by id.
	gets := client.gets
	_, _ = aijobsPoller(t, store, client).Poll(context.Background())
	assert.Equal(t, gets, client.gets)
	assert.Equal(t, 1, cc.calls)
}

// FIX-4: a submitted job whose batch-id index is missing (a kill split the old
// two-write MarkAIJobSubmitted) is repaired by the tracker and applied.
func TestBatchPoller_MissingBatchIndexRebuilt(t *testing.T) {
	store := newPollerTestStore(t)
	cc := seedSubmittedJob(t, store, "JOBI", "poller_index_test", "batch_idx")
	require.NoError(t, store.DeleteRaw("aijob_batch:batch_idx"))
	_, err := store.GetAIJobByBatchID("batch_idx")
	require.ErrorIs(t, err, database.ErrAIJobNotFound)

	client := &fakeBatchClient{
		batches: []ai.BatchInfo{{ID: "batch_idx", Status: "completed", Type: "aijobs", OutputFileID: "out_idx"}},
		outputs: map[string][]ai.BatchRawResult{"out_idx": {{CustomID: "JOBI-0", Content: "{}"}}},
	}
	_, err = aijobsPoller(t, store, client).Poll(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, cc.calls)
	job, _ := store.GetAIJob("JOBI")
	assert.Equal(t, "completed", job.Status)
}

// FIX-5: an expired batch with a partial output file is applied, not failed.
func TestBatchPoller_ExpiredBatchWithOutputApplied(t *testing.T) {
	store := newPollerTestStore(t)
	cc := seedSubmittedJob(t, store, "JOBE", "poller_expired_test", "batch_exp")
	client := &fakeBatchClient{
		batches: []ai.BatchInfo{{ID: "batch_exp", Status: "expired", Type: "aijobs", OutputFileID: "out_exp",
			Metadata: map[string]string{aijobs.MetadataJobIDKey: "JOBE"}}},
		outputs: map[string][]ai.BatchRawResult{"out_exp": {{CustomID: "JOBE-0", Content: "{}"}}},
	}
	_, err := aijobsPoller(t, store, client).Poll(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, cc.calls, "partial results of an expired batch were dropped")
	job, _ := store.GetAIJob("JOBE")
	assert.Equal(t, "completed", job.Status)
}

// An expired batch with no output has nothing to apply: its job is failed.
func TestBatchPoller_ExpiredBatchWithoutOutputFailsJob(t *testing.T) {
	store := newPollerTestStore(t)
	cc := seedSubmittedJob(t, store, "JOBX", "poller_expired_empty_test", "batch_x")
	client := &fakeBatchClient{batches: []ai.BatchInfo{{ID: "batch_x", Status: "expired", Type: "aijobs",
		Metadata: map[string]string{aijobs.MetadataJobIDKey: "JOBX"}}}}
	_, err := aijobsPoller(t, store, client).Poll(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 0, cc.calls)
	job, _ := store.GetAIJob("JOBX")
	assert.Equal(t, "failed", job.Status)
}

// FIX-6: a failed embedding upsert fails the handler so the batch is retried,
// not recorded handled with books missing vectors.
func TestBatchPoller_EmbedAsyncUpsertFailureRetried(t *testing.T) {
	store := newPollerTestStore(t)
	client := &fakeBatchClient{batches: []ai.BatchInfo{{ID: "batch_emb", Status: "completed", Type: "embed_async", OutputFileID: "out_emb"}}}
	stored := map[string]int{}
	failOnce := true
	bp := newTestPoller(t, store, client)
	bp.RegisterHandler("embed_async", embedAsyncBatchHandler(
		func(context.Context, string) ([]ai.EmbedBatchResult, error) {
			return []ai.EmbedBatchResult{{BookID: "B1"}, {BookID: "B2"}}, nil
		},
		func(e database.Embedding) error {
			if e.EntityID == "B2" && failOnce {
				failOnce = false
				return assert.AnError
			}
			stored[e.EntityID]++
			return nil
		},
	))
	_, _ = bp.Poll(context.Background())
	assert.False(t, bp.IsProcessed("batch_emb"), "batch recorded handled with a failed upsert")
	_, _ = bp.Poll(context.Background())
	assert.True(t, bp.IsProcessed("batch_emb"))
	assert.Equal(t, 1, stored["B2"])
}

// A foreign install's aijobs batch (no local job) is skipped for the process
// without erroring every tick, and is not journaled.
func TestBatchPoller_ForeignAIJobsBatchSkippedInMemory(t *testing.T) {
	store := newPollerTestStore(t)
	client := &fakeBatchClient{
		batches: []ai.BatchInfo{{ID: "batch_foreign", Status: "completed", Type: "aijobs", OutputFileID: "out_f"}},
		outputs: map[string][]ai.BatchRawResult{"out_f": {{CustomID: "X-0"}}},
	}
	bp := aijobsPoller(t, store, client)
	_, err := bp.Poll(context.Background())
	require.NoError(t, err)
	assert.True(t, bp.IsProcessed("batch_foreign"))
	assert.False(t, aijobsPoller(t, store, client).IsProcessed("batch_foreign"), "foreign batch must not be journaled")
}
