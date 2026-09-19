// file: internal/plugins/maintenance/dedup_ops_batch_test.go
// version: 1.1.0
// guid: d3a390db-7a5a-41df-ba14-226aeb15655c
// last-edited: 2026-09-19

package maintenance

import (
	"context"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/ai"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/require"
)

func newJobsStore(t *testing.T) database.AIJobsStore {
	t.Helper()
	ps, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = ps.Close() })
	return ps
}

// countingSubmit plays aijobs.Submit: it writes the job row a real submit
// writes (pending, then submitted with a batch id) and counts paid batches.
type countingSubmit struct {
	store database.AIJobsStore
	calls int
}

func (c *countingSubmit) submit(_ context.Context, sourceID string, _ []ai.AuthorDiscoveryInput) (string, error) {
	c.calls++
	id := "job-" + sourceID
	if err := c.store.CreateAIJob(database.AIJob{ID: id, Type: ai.AuthorDedupJobType, Status: "pending", ItemCount: 1, CreatedAt: time.Now()}, []byte("{}")); err != nil {
		return "", err
	}
	return id, c.store.MarkAIJobSubmitted(id, "batch-"+sourceID)
}

type recordingCheckpoint struct{ states []aiDedupBatchParams }

func (r *recordingCheckpoint) checkpoint(v any) error {
	r.states = append(r.states, v.(aiDedupBatchParams))
	return nil
}

var oneAuthor = func() ([]ai.AuthorDiscoveryInput, error) {
	return []ai.AuthorDiscoveryInput{{ID: 1, Name: "A"}}, nil
}

func TestAIDedupBatchSubmitsOnceAndCheckpointsTheJob(t *testing.T) {
	store := newJobsStore(t)
	sub := &countingSubmit{store: store}
	cp := &recordingCheckpoint{}

	jobID, submitted, err := submitAuthorDedupOnce(context.Background(), store, aiDedupBatchParams{}, oneAuthor, sub.submit, cp.checkpoint)
	require.NoError(t, err)
	require.True(t, submitted)
	require.Equal(t, 1, sub.calls)
	require.Equal(t, jobID, cp.states[len(cp.states)-1].JobID, "the job id is checkpointed so a resumed run does not submit again")

	// The next midnight run while that batch is still in flight: no new batch.
	again, submitted, err := submitAuthorDedupOnce(context.Background(), store, aiDedupBatchParams{}, oneAuthor, sub.submit, cp.checkpoint)
	require.NoError(t, err)
	require.False(t, submitted)
	require.Equal(t, jobID, again)
	require.Equal(t, 1, sub.calls)
}

func TestAIDedupBatchResumeWithCheckpointDoesNotSubmit(t *testing.T) {
	store := newJobsStore(t)
	sub := &countingSubmit{store: store}
	got, submitted, err := submitAuthorDedupOnce(context.Background(), store, aiDedupBatchParams{JobID: "job-x"}, oneAuthor, sub.submit, (&recordingCheckpoint{}).checkpoint)
	require.NoError(t, err)
	require.False(t, submitted)
	require.Equal(t, "job-x", got)
	require.Equal(t, 0, sub.calls)
}

// A pending job (CreateBatch errored, or the process died inside it) blocks a
// new submission however old it is: this op never writes it off. aijobs does,
// and only on a confirmed absence (see aijobs.WriteOffUnlinked); once it has,
// the next run submits.
func TestAIDedupBatchPendingJobGatesUntilAijobsResolvesIt(t *testing.T) {
	store := newJobsStore(t)
	created := time.Now().Add(-30 * 24 * time.Hour)
	require.NoError(t, store.CreateAIJob(database.AIJob{ID: "old", Type: ai.AuthorDedupJobType, Status: "pending", ItemCount: 1, CreatedAt: created}, []byte("{}")))
	sub := &countingSubmit{store: store}
	cp := (&recordingCheckpoint{}).checkpoint

	got, submitted, err := submitAuthorDedupOnce(context.Background(), store, aiDedupBatchParams{}, oneAuthor, sub.submit, cp)
	require.NoError(t, err)
	require.False(t, submitted, "a pending job may still own a billed batch")
	require.Equal(t, "old", got)
	old, err := store.GetAIJob("old")
	require.NoError(t, err)
	require.Equal(t, "pending", old.Status, "the op must not write it off itself")

	require.NoError(t, store.MarkAIJobFailed("old", "confirmed absent"))
	_, submitted, err = submitAuthorDedupOnce(context.Background(), store, aiDedupBatchParams{}, oneAuthor, sub.submit, cp)
	require.NoError(t, err)
	require.True(t, submitted)
	require.Equal(t, 1, sub.calls)
}
