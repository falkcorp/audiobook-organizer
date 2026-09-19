// file: internal/ai/aijobs/durable_test.go
// version: 1.1.0
// guid: d96ffcde-7b33-4014-985a-a56c427cac9d
// last-edited: 2026-09-19

package aijobs

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// crashingStore fails the next MarkAIJobCompleted / MarkAIJobSubmitted call to
// stand in for a process killed right before that write lands.
type crashingStore struct {
	*fakeStore
	failNextComplete bool
	failNextSubmit   bool
}

func (c *crashingStore) MarkAIJobCompleted(id, status string, s, e int, re []database.AIJobRowError) error {
	if c.failNextComplete {
		c.failNextComplete = false
		return errors.New("killed before mark completed")
	}
	return c.fakeStore.MarkAIJobCompleted(id, status, s, e, re)
}

func (c *crashingStore) MarkAIJobSubmitted(id, b string) error {
	if c.failNextSubmit {
		c.failNextSubmit = false
		return errors.New("killed before mark submitted")
	}
	return c.fakeStore.MarkAIJobSubmitted(id, b)
}

// effectSink records applied results the way an idempotent callback must: keyed
// by CustomID, so a replay overwrites instead of adding. calls counts every
// callback invocation, so a test can tell "re-applied idempotently" apart from
// "not re-applied at all".
type effectSink struct {
	mu      sync.Mutex
	calls   int
	effects map[string]int // CustomID -> times the effect is present (must stay 1)
}

func newEffectSink() *effectSink { return &effectSink{effects: map[string]int{}} }

func (e *effectSink) callback(_ context.Context, _ []byte, results []RowResult) (int, int, []database.AIJobRowError, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls++
	for _, r := range results {
		e.effects[r.CustomID] = 1
	}
	return len(results), 0, nil, nil
}

// (d) A job already completed must not be applied again — this is the guard
// that makes the poller's re-delivery after a restart harmless.
func TestDispatch_CompletedJobIsNoOp(t *testing.T) {
	for _, status := range []string{"completed", "completed_with_errors", "failed"} {
		t.Run(status, func(t *testing.T) {
			store := newFakeStore()
			sink := newEffectSink()
			Register("durable_noop", sink.callback)
			require.NoError(t, store.CreateAIJob(database.AIJob{ID: "J1", Type: "durable_noop", Status: "submitted"}, []byte("[]")))
			require.NoError(t, store.MarkAIJobSubmitted("J1", "batch_done"))
			j := store.jobs["J1"]
			j.Status = status
			if status == "failed" {
				j.ApplyAttempts = MaxApplyAttempts // failed is terminal only once the budget is spent
			}
			store.jobs["J1"] = j

			err := Dispatch(context.Background(), store, "batch_done", []RowResult{{CustomID: "J1-0", Content: "{}"}})
			require.NoError(t, err, "a terminal job must return nil so the poller records it handled")
			assert.Equal(t, 0, sink.calls, "callback ran for a job already %s", status)
			assert.Equal(t, status, store.jobs["J1"].Status, "status must not be rewritten")
		})
	}
}

// (b) Killed after the callback applied its results but before the job row was
// marked completed: the next Dispatch replays the callback exactly once
// (idempotently — no duplicate effects), and every Dispatch after that is a no-op.
func TestDispatch_CrashBetweenApplyAndMark_ReplaysOnce(t *testing.T) {
	store := &crashingStore{fakeStore: newFakeStore(), failNextComplete: true}
	sink := newEffectSink()
	Register("durable_replay", sink.callback)
	require.NoError(t, store.CreateAIJob(database.AIJob{ID: "J2", Type: "durable_replay", Status: "pending"}, []byte("[]")))
	require.NoError(t, store.MarkAIJobSubmitted("J2", "batch_crash"))
	results := []RowResult{{CustomID: "J2-0", Content: "{}"}, {CustomID: "J2-1", Content: "{}"}}

	require.Error(t, Dispatch(context.Background(), store, "batch_crash", results), "the killed mark must surface")
	require.Equal(t, 1, sink.calls)
	require.Equal(t, "submitted", store.jobs["J2"].Status, "job must not look applied after the kill")

	// "Restart": the poller re-delivers the batch.
	require.NoError(t, Dispatch(context.Background(), store, "batch_crash", results))
	assert.Equal(t, 2, sink.calls, "exactly one replay after the interrupted apply")
	assert.Equal(t, "completed", store.jobs["J2"].Status)

	// Any further delivery (poller mark lost, second restart) applies nothing.
	require.NoError(t, Dispatch(context.Background(), store, "batch_crash", results))
	require.NoError(t, Dispatch(context.Background(), store, "batch_crash", results))
	assert.Equal(t, 2, sink.calls, "no re-apply once the job is completed")
	assert.Equal(t, map[string]int{"J2-0": 1, "J2-1": 1}, sink.effects, "no duplicate effects")
}

// Submit must tag the batch with its job id so an orphan can be found later.
func TestSubmit_TagsBatchWithJobID(t *testing.T) {
	store := newFakeStore()
	client := &fakeBatchClient{returnBatchID: "batch_tag"}
	jobID, err := Submit(context.Background(), Deps{Store: store, Client: client}, SubmitRequest{
		Type: "t", ItemCount: 1, PayloadJSON: []byte("[]"),
		Build: func(int) (BatchRequest, error) { return BatchRequest{Body: map[string]any{}}, nil },
	})
	require.NoError(t, err)
	assert.Equal(t, jobID, client.lastExtra[MetadataJobIDKey])
}

// (c) Killed between CreateBatch and MarkAIJobSubmitted: the row is pending with
// no batch id. ReconcileOrphans attaches the batch named in the metadata, after
// which Dispatch applies it exactly once.
func TestReconcileOrphans_AttachesPendingJobThenAppliesOnce(t *testing.T) {
	store := &crashingStore{fakeStore: newFakeStore(), failNextSubmit: true}
	client := &fakeBatchClient{returnBatchID: "batch_orphan"}
	sink := newEffectSink()
	Register("durable_orphan", sink.callback)

	jobID, err := Submit(context.Background(), Deps{Store: store, Client: client}, SubmitRequest{
		Type: "durable_orphan", ItemCount: 1, PayloadJSON: []byte("[]"),
		Build: func(int) (BatchRequest, error) { return BatchRequest{Body: map[string]any{}}, nil },
	})
	require.Error(t, err, "the killed MarkAIJobSubmitted must surface")
	require.Equal(t, "pending", store.jobs[jobID].Status)
	require.Empty(t, store.jobs[jobID].BatchID)
	require.Error(t, Dispatch(context.Background(), store, "batch_orphan", nil), "unreconciled orphan is unreachable by batch id")

	batches := []OrphanBatch{
		{ID: "batch_orphan", Metadata: client.lastExtra},
		{ID: "batch_foreign", Metadata: map[string]string{MetadataJobIDKey: "NOT-OURS"}},
		{ID: "batch_untagged", Metadata: map[string]string{}},
	}
	n, err := ReconcileOrphans(store, batches)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	assert.Equal(t, "batch_orphan", store.jobs[jobID].BatchID)
	assert.Equal(t, "submitted", store.jobs[jobID].Status)

	// A second reconcile (every poll tick runs one) changes nothing.
	n, err = ReconcileOrphans(store, batches)
	require.NoError(t, err)
	assert.Equal(t, 0, n)

	results := []RowResult{{CustomID: jobID + "-0", Content: "{}"}}
	require.NoError(t, Dispatch(context.Background(), store, "batch_orphan", results))
	require.NoError(t, Dispatch(context.Background(), store, "batch_orphan", results))
	assert.Equal(t, 1, sink.calls, "orphan applied exactly once")
}

// A failed row is left alone: its caller saw the failure and may have
// resubmitted, so attaching the old batch would apply the items twice.
func TestReconcileOrphans_LeavesFailedJobAlone(t *testing.T) {
	store := newFakeStore()
	require.NoError(t, store.CreateAIJob(database.AIJob{ID: "JF", Type: "t", Status: "failed"}, nil))
	n, err := ReconcileOrphans(store, []OrphanBatch{{ID: "b", Metadata: map[string]string{MetadataJobIDKey: "JF"}}})
	require.NoError(t, err)
	assert.Equal(t, 0, n)
	assert.Empty(t, store.jobs["JF"].BatchID)
}

// flakyCallback fails its first failN calls, then applies idempotently.
type flakyCallback struct {
	effectSink
	failN int
}

func (f *flakyCallback) cb(ctx context.Context, p []byte, r []RowResult) (int, int, []database.AIJobRowError, error) {
	f.mu.Lock()
	if f.failN > 0 {
		f.failN--
		f.calls++
		f.mu.Unlock()
		return 0, 0, nil, errors.New("database is busy")
	}
	f.mu.Unlock()
	return f.effectSink.callback(ctx, p, r)
}

func withClock(t *testing.T, start time.Time) *time.Time {
	t.Helper()
	cur := start
	prev := now
	now = func() time.Time { return cur }
	t.Cleanup(func() { now = prev })
	return &cur
}

func seedSubmitted(t *testing.T, store *fakeStore, id, typ, batch string) {
	t.Helper()
	require.NoError(t, store.CreateAIJob(database.AIJob{ID: id, Type: typ, Status: "pending"}, []byte("[]")))
	require.NoError(t, store.MarkAIJobSubmitted(id, batch))
}

// A transient apply failure is retried after backoff and applied exactly once.
func TestDispatch_TransientFailureRetriedThenAppliedOnce(t *testing.T) {
	store := newFakeStore()
	fc := &flakyCallback{effectSink: effectSink{effects: map[string]int{}}, failN: 1}
	Register("durable_transient", fc.cb)
	seedSubmitted(t, store, "JT", "durable_transient", "batch_t")
	clock := withClock(t, time.Now())
	results := []RowResult{{CustomID: "JT-0", Content: "{}"}}

	require.Error(t, Dispatch(context.Background(), store, "batch_t", results))
	j := store.jobs["JT"]
	require.Equal(t, "apply_failed", j.Status, "a transient failure must stay retriable")
	require.Equal(t, 1, j.ApplyAttempts)
	require.Contains(t, j.LastApplyError, "busy")
	// fakeStore stamps LastApplyAt with the wall clock; align it with ours.
	j.LastApplyAt = *clock
	store.jobs["JT"] = j

	// Next tick, inside the backoff window: not retried yet.
	*clock = clock.Add(time.Minute)
	err := Dispatch(context.Background(), store, "batch_t", results)
	require.ErrorIs(t, err, ErrApplyBackoff)
	require.Equal(t, 1, fc.calls)

	// After the backoff: retried and applied.
	*clock = clock.Add(applyBackoffBase)
	require.NoError(t, Dispatch(context.Background(), store, "batch_t", results))
	assert.Equal(t, "completed", store.jobs["JT"].Status)
	assert.Equal(t, map[string]int{"JT-0": 1}, fc.effects)

	// Completed: never applied again.
	require.NoError(t, Dispatch(context.Background(), store, "batch_t", results))
	assert.Equal(t, 2, fc.calls, "one failed attempt plus exactly one successful apply")
}

// A permanent failure becomes terminal "failed" after MaxApplyAttempts, and
// Dispatch then returns nil so the poller stops offering the batch.
func TestDispatch_PermanentFailureTerminalAfterMaxAttempts(t *testing.T) {
	store := newFakeStore()
	fc := &flakyCallback{effectSink: effectSink{effects: map[string]int{}}, failN: 1 << 30}
	Register("durable_permanent", fc.cb)
	seedSubmitted(t, store, "JP", "durable_permanent", "batch_perm")
	clock := withClock(t, time.Now())

	for i := 1; i <= MaxApplyAttempts; i++ {
		require.Error(t, Dispatch(context.Background(), store, "batch_perm", nil))
		j := store.jobs["JP"]
		require.Equal(t, i, j.ApplyAttempts)
		if i < MaxApplyAttempts {
			require.Equal(t, "apply_failed", j.Status)
		}
		j.LastApplyAt = *clock
		store.jobs["JP"] = j
		*clock = clock.Add(applyBackoffMax)
	}
	j := store.jobs["JP"]
	assert.Equal(t, "failed", j.Status)
	assert.Contains(t, j.ErrorMsg, "giving up")

	require.NoError(t, Dispatch(context.Background(), store, "batch_perm", nil))
	assert.Equal(t, MaxApplyAttempts, fc.calls, "no attempt after the budget is spent")
}

// A job an earlier build marked failed on its first apply error still gets the
// retry budget instead of being dropped.
func TestDispatch_LegacyFailedJobStillRetried(t *testing.T) {
	store := newFakeStore()
	sink := newEffectSink()
	Register("durable_legacy", sink.callback)
	seedSubmitted(t, store, "JL", "durable_legacy", "batch_legacy")
	require.NoError(t, store.MarkAIJobFailed("JL", "callback panic: old build"))

	require.NoError(t, Dispatch(context.Background(), store, "batch_legacy", []RowResult{{CustomID: "JL-0"}}))
	assert.Equal(t, 1, sink.calls)
	assert.Equal(t, "completed", store.jobs["JL"].Status)
}

// OpenAI failing or expiring the batch is terminal at once (nothing to retry),
// unlike our own apply failing.
func TestReconcileOrphans_OpenAIFailedBatchMarksJobFailed(t *testing.T) {
	for _, st := range []string{"failed", "expired", "cancelled"} {
		t.Run(st, func(t *testing.T) {
			store := newFakeStore()
			seedSubmitted(t, store, "JO", "t", "batch_o")
			_, err := ReconcileOrphans(store, []OrphanBatch{{ID: "batch_o", Status: st, Metadata: map[string]string{MetadataJobIDKey: "JO"}}})
			require.NoError(t, err)
			assert.Equal(t, "failed", store.jobs["JO"].Status)
			assert.Contains(t, store.jobs["JO"].ErrorMsg, st)
		})
	}
	// An in-progress batch leaves the job alone.
	store := newFakeStore()
	seedSubmitted(t, store, "JR", "t", "batch_r")
	_, err := ReconcileOrphans(store, []OrphanBatch{{ID: "batch_r", Status: "in_progress", Metadata: map[string]string{MetadataJobIDKey: "JR"}}})
	require.NoError(t, err)
	assert.Equal(t, "submitted", store.jobs["JR"].Status)
}
