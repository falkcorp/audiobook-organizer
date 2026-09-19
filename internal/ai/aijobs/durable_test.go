// file: internal/ai/aijobs/durable_test.go
// version: 1.0.0
// guid: d96ffcde-7b33-4014-985a-a56c427cac9d
// last-edited: 2026-09-19

package aijobs

import (
	"context"
	"errors"
	"sync"
	"testing"

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
