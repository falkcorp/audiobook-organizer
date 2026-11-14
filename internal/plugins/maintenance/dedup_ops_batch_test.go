// file: internal/plugins/maintenance/dedup_ops_batch_test.go
// version: 1.2.0
// guid: d3a390db-7a5a-41df-ba14-226aeb15655c
// last-edited: 2026-11-14

package maintenance

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/ai"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/mediacore/tasks"
	"github.com/stretchr/testify/require"
)

func newJobsStore(t *testing.T) database.AIJobsStore {
	t.Helper()
	ps, err := database.NewPebbleStoreInMemory(t.TempDir())
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

// ---- review M1: HEAD's shell checkpoints, the native shell restores it ----

// aiParsingDeps is fakeDeps with AI parsing switched on, so HEAD's shell body
// gets past its own HasAIParsing gate.
type aiParsingDeps struct{ fakeDeps }

func (aiParsingDeps) HasAIParsing() bool { return true }

// aiDedupHEADReporter is fakeReporter plus what runAIDedupBatchHEAD needs of a real
// registry reporter: a recording Checkpoint and a SetResult.
type aiDedupHEADReporter struct {
	fakeReporter
	checkpoints []aiDedupBatchParams
	result      any
}

func (r *aiDedupHEADReporter) Checkpoint(v any) error {
	r.checkpoints = append(r.checkpoints, v.(aiDedupBatchParams))
	return nil
}
func (r *aiDedupHEADReporter) SetResult(v any) error { r.result = v; return nil }

// payingSubmitter plays the OpenAI parser's batch submission: it writes the job
// row a real submit writes and counts paid batches.
type payingSubmitter struct{ calls int }

func (s *payingSubmitter) SubmitAuthorDedupJob(ctx context.Context, jobs database.AIJobsStore, sourceID string, inputs []ai.AuthorDiscoveryInput) (string, error) {
	s.calls++
	return (&countingSubmit{store: jobs}).submit(ctx, sourceID, inputs)
}

// TestAIDedupBatchHEADShell_CheckpointsTheJobThroughTheReporter pins HEAD's shell
// (runAIDedupBatchHEAD, what the plugin's op registry runs): the submitted job id
// goes to reporter.Checkpoint in HEAD's {"job_id"} shape, the result is set, and
// a run resumed with that checkpoint merged into its params submits nothing.
func TestAIDedupBatchHEADShell_CheckpointsTheJobThroughTheReporter(t *testing.T) {
	ps, err := database.NewPebbleStoreInMemory(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = ps.Close() })
	_, err = ps.CreateAuthor("Alfred Pennyworth")
	require.NoError(t, err)

	p := &Plugin{deps: aiParsingDeps{fakeDeps{store: ps}}}
	sub := &payingSubmitter{}
	rep := &aiDedupHEADReporter{}
	require.NoError(t, p.runAIDedupBatchHEAD(context.Background(), nil, rep, ps, ps, sub))

	require.Equal(t, 1, sub.calls)
	require.Len(t, rep.checkpoints, 1, "HEAD checkpoints the job id exactly once")
	job := rep.checkpoints[0].JobID
	require.NotEmpty(t, job)
	raw, err := json.Marshal(rep.checkpoints[0])
	require.NoError(t, err)
	require.JSONEq(t, `{"job_id":"`+job+`"}`, string(raw), "the checkpoint is HEAD's shape: the registry merges it back into params")
	require.Equal(t, aiDedupBatchResult{JobID: job, Submitted: true}, rep.result)

	// The resumed run: the registry merged the checkpoint into the params.
	resumed := &aiDedupHEADReporter{}
	require.NoError(t, p.runAIDedupBatchHEAD(context.Background(), raw, resumed, ps, ps, sub))
	require.Equal(t, 1, sub.calls, "a resumed run submits nothing")
	require.Empty(t, resumed.checkpoints)
	require.Equal(t, aiDedupBatchResult{JobID: job, Submitted: false}, resumed.result)
}

// TestAIDedupBatchKind_RestoresHEADShapedCheckpoint pins the native shell: it
// checkpoints HEAD's bytes through rc, and a run whose checkpoint holds HEAD's
// {"job_id"} bytes (written by HEAD's shell before the cutover) submits nothing.
func TestAIDedupBatchKind_RestoresHEADShapedCheckpoint(t *testing.T) {
	newRun := func(t *testing.T) (tasks.Definition, *t6g4Probe, *t6g4Host, *tasks.RC) {
		deps, pr := fakeT6G4Deps(t)
		pr.store.aiParsing = true
		pr.store.authors = []database.Author{{ID: 1, Name: "Alfred Pennyworth"}}
		def := t6g4KindByName(t, t6g4Kinds(deps, nil), aiDedupBatchKind)
		host, rc := newT6G4RC(aiDedupBatchKind, tasks.ModeLive, &t6g4RunWriter{})
		return def, pr, host, rc
	}
	submits := func(pr *t6g4Probe) int {
		n := 0
		for _, c := range pr.store.rec.all() {
			if c == "SubmitAuthorDedupJob" {
				n++
			}
		}
		return n
	}

	t.Run("fresh run submits once and checkpoints HEAD's shape", func(t *testing.T) {
		def, pr, host, rc := newRun(t)
		out, err := runT6G4Kind(t, def, rc, "")
		require.NoError(t, err)
		var res aiDedupBatchResult
		require.NoError(t, json.Unmarshal(out, &res))
		require.True(t, res.Submitted)
		require.Equal(t, 1, submits(pr))
		raw, ok := host.CheckpointJSON("")
		require.True(t, ok, "the native shell checkpoints the job id")
		require.JSONEq(t, `{"job_id":"`+res.JobID+`"}`, string(raw))
	})

	t.Run("a HEAD-shaped checkpoint stops the resubmission", func(t *testing.T) {
		def, pr, host, rc := newRun(t)
		require.NoError(t, host.SetCheckpoint("", json.RawMessage(`{"job_id":"job-bruce"}`)))
		out, err := runT6G4Kind(t, def, rc, "")
		require.NoError(t, err)
		var res aiDedupBatchResult
		require.NoError(t, json.Unmarshal(out, &res))
		require.Equal(t, aiDedupBatchResult{JobID: "job-bruce", Submitted: false}, res)
		require.Zero(t, submits(pr), "a resumed run must not pay for a second batch")
	})

	t.Run("an explicit job_id param wins over the checkpoint", func(t *testing.T) {
		def, pr, host, rc := newRun(t)
		require.NoError(t, host.SetCheckpoint("", json.RawMessage(`{"job_id":"job-from-checkpoint"}`)))
		out, err := runT6G4Kind(t, def, rc, `{"job_id":"job-from-params"}`)
		require.NoError(t, err)
		var res aiDedupBatchResult
		require.NoError(t, json.Unmarshal(out, &res))
		require.Equal(t, "job-from-params", res.JobID)
		require.Zero(t, submits(pr))
	})
}
