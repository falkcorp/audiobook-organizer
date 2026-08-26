// file: internal/server/maintenance_concurrency_test.go
// version: 1.4.0
// guid: 14d07753-3a82-4678-8982-e488eef8a7e3
// last-edited: 2026-08-24

package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	dbmocks "github.com/falkcorp/audiobook-organizer/internal/database/mocks"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// TestMaintenanceOpSerializesAgainstItself pins the rule that every maintenance
// def carries a non-empty, job-specific ConcurrencyKey.
//
// Why this matters: all three of the dispatcher's serializing gates are guarded
// on a field that maintenance defs used to leave empty. EnqueueOp's dedupe block
// and Gate 3 both check `def.ConcurrencyKey != ""`; Gate 3b needs a non-empty
// `Writes`, which no maintenance def declares. With DefaultPolicy() hardcoding an
// empty key for all 37, none of the gates applied to this family at all — a job
// double-clicked in the UI started two concurrent runs over the same rows.
//
// This is the structural half of the fix. TestMaintenanceJobEnqueuedTwiceRunsSequentially
// is the half that proves the key actually produces sequential execution.
func TestMaintenanceOpSerializesAgainstItself(t *testing.T) {
	reg := maintReg(t)
	require.NoError(t, (&Server{}).RegisterMaintenanceJobOps(reg))

	jobs := maintenance.All()
	// Positive control: with no jobs registered every assertion below is skipped
	// and the test reports success while checking nothing.
	require.NotEmpty(t, jobs, "no maintenance jobs registered; this test would pass vacuously")

	seenKeys := make(map[string]string, len(jobs))
	for _, job := range jobs {
		t.Run(job.ID(), func(t *testing.T) {
			def, ok := reg.Def(maintenanceOpID(job.ID()))
			require.True(t, ok)

			require.NotEmpty(t, def.ConcurrencyKey,
				"an empty ConcurrencyKey skips EnqueueOp's dedupe block and dispatcher "+
					"Gate 3 entirely, so two enqueues of this job would run concurrently")

			// Distinct per job: a shared key would serialize unrelated jobs against
			// each other, turning 37 independently-runnable jobs into one queue.
			if prev, dup := seenKeys[def.ConcurrencyKey]; dup {
				t.Fatalf("ConcurrencyKey %q is shared with job %q; unrelated maintenance "+
					"jobs would serialize against each other", def.ConcurrencyKey, prev)
			}
			seenKeys[def.ConcurrencyKey] = job.ID()

			// A job that declares its own key keeps it; otherwise the key is derived
			// from the job's op ID. No job declares one today, so this currently
			// always takes the derived branch.
			want := job.Policy().ConcurrencyKey
			if want == "" {
				want = maintenanceOpID(job.ID())
			}
			require.Equal(t, want, def.ConcurrencyKey)
		})
	}
}

// TestMaintenanceJobDeclaredKeyWins covers the branch no real job exercises yet:
// a job that declares its own ConcurrencyKey keeps it rather than having the
// derived per-job key overwrite it.
//
// That branch is the only way two DIFFERENT maintenance jobs can ever share a
// serialization domain — two jobs that both rewrite file paths, say. Deriving
// unconditionally would be simpler but would make ExecutionPolicy.ConcurrencyKey
// permanently dead, and nothing would notice until someone declared one and it
// silently did nothing.
func TestMaintenanceJobDeclaredKeyWins(t *testing.T) {
	reg := maintReg(t)
	job := &fakeKeyedJob{id: "fake-keyed-job", key: "shared-file-path-domain"}

	require.NoError(t, (&Server{}).registerMaintenanceJobOp(reg, job))

	def, ok := reg.Def(maintenanceOpID(job.ID()))
	require.True(t, ok)
	require.Equal(t, "shared-file-path-domain", def.ConcurrencyKey,
		"a job's declared ConcurrencyKey must survive registration; overwriting it "+
			"with the derived per-job key makes the policy field dead")
}

// TestMaintenanceJobEnqueuedTwiceRunsSequentially is the behavioural half: two
// enqueues of the same maintenance job must not run at the same time.
//
// It registers the REAL def produced by registerMaintenanceJobOp and replaces
// only its Run body with an overlap detector. The substitution cannot affect the
// result: serialization is decided by ConcurrencyKey at Gate 3, before Run is
// ever called, and the real Run needs a fully wired Server this test has no
// reason to build.
//
// Mutation-checked: with the derived key removed from registerMaintenanceJobOp
// (ConcurrencyKey back to policy.ConcurrencyKey, i.e. ""), maxOverlap reaches 2
// and this test fails.
func TestMaintenanceJobEnqueuedTwiceRunsSequentially(t *testing.T) {
	ctx := t.Context()

	store := newOpsFake(t)
	reg := opsregistry.New(store, slog.New(slog.DiscardHandler), 4, nil)

	// Build the real def, then swap in an instrumented Run.
	src := maintReg(t)
	require.NoError(t, (&Server{}).RegisterMaintenanceJobOps(src))
	jobs := maintenance.All()
	require.NotEmpty(t, jobs, "no maintenance jobs registered; nothing to serialize")

	def, ok := src.Def(maintenanceOpID(jobs[0].ID()))
	require.True(t, ok)
	require.NotEmpty(t, def.ConcurrencyKey, "precondition: the def must carry a key")

	var running, maxOverlap int64
	started := make(chan struct{})
	var startedOnce sync.Once
	var finished atomic.Int64

	def.Run = func(context.Context, json.RawMessage, opsregistry.Reporter) error {
		cur := atomic.AddInt64(&running, 1)
		for {
			old := atomic.LoadInt64(&maxOverlap)
			if cur <= old || atomic.CompareAndSwapInt64(&maxOverlap, old, cur) {
				break
			}
		}
		startedOnce.Do(func() { close(started) })
		time.Sleep(40 * time.Millisecond)
		atomic.AddInt64(&running, -1)
		finished.Add(1)
		return nil
	}
	require.NoError(t, reg.RegisterOp(def))
	reg.Start(ctx)

	// Distinct params on purpose — and DryRun is what makes them distinct.
	//
	// This used to vary LegacyOpID, back when a fresh one per request meant a real
	// double-click never byte-matched and never deduped. Then LegacyOpID was
	// excluded from the comparison, so varying it MERGED and this test would have
	// passed vacuously with maxOverlap stuck at 1 for want of a second run. The
	// field is now gone from the params entirely, which settles it: nothing about
	// a request except the work it describes can distinguish two enqueues.
	//
	// A differing dry_run is genuinely different work, so it still queues a second
	// op. The claim under test is unchanged: two runs of the same job must not
	// overlap.
	id1, err := reg.EnqueueOp(ctx, def.ID, maintenanceJobOpParams{JobID: jobs[0].ID(), DryRun: true})
	require.NoError(t, err)
	<-started // only enqueue the second once the first is actually running
	id2, err := reg.EnqueueOp(ctx, def.ID, maintenanceJobOpParams{JobID: jobs[0].ID(), DryRun: false})
	require.NoError(t, err)
	require.NotEqual(t, id1, id2, "the second enqueue was deduped into the first; it "+
		"should have queued a separate run because dry_run differs")

	require.Eventually(t, func() bool { return finished.Load() == 2 }, 10*time.Second, 10*time.Millisecond,
		"both runs should complete")

	// The assertion. maxOverlap is the high-water mark of concurrent Run bodies.
	require.Equal(t, int64(1), atomic.LoadInt64(&maxOverlap),
		"two enqueues of the same maintenance job ran concurrently; ConcurrencyKey "+
			"is not serializing them")
}

// fakeKeyedJob is a maintenance job that declares its own ConcurrencyKey, which
// no real job does yet.
type fakeKeyedJob struct {
	id  string
	key string
}

func (j *fakeKeyedJob) ID() string          { return j.id }
func (j *fakeKeyedJob) Name() string        { return "Fake Keyed Job" }
func (j *fakeKeyedJob) Description() string { return "test double declaring a ConcurrencyKey" }

func (j *fakeKeyedJob) Policy() maintenance.ExecutionPolicy {
	p := maintenance.DefaultPolicy()
	p.ConcurrencyKey = j.key
	return p
}

func (j *fakeKeyedJob) Category() string   { return "test" }
func (j *fakeKeyedJob) DefaultParams() any { return struct{}{} }
func (j *fakeKeyedJob) CanResume() bool    { return false }

func (j *fakeKeyedJob) Run(context.Context, maintenance.JobStore, maintenance.ProgressReporter, bool) error {
	return nil
}

// newOpsFake returns a mockery MockStore backed by an in-memory op table, wired
// for the enqueue -> dispatch -> complete path only. Everything the reporter
// touches is a no-op .Maybe(); this test asserts on Run overlap, not on rows.
func newOpsFake(t *testing.T) *dbmocks.MockStore {
	t.Helper()
	m := dbmocks.NewMockStore(t)

	var mu sync.Mutex
	rows := map[string]*database.OperationV2Row{}

	snapshot := func(filter func(*database.OperationV2Row) bool) []database.OperationV2Row {
		mu.Lock()
		defer mu.Unlock()
		out := make([]database.OperationV2Row, 0, len(rows))
		for _, r := range rows {
			if filter(r) {
				out = append(out, *r)
			}
		}
		return out
	}

	m.EXPECT().UpsertOpDefinitionV2(mock.Anything).Return(nil).Maybe()

	m.EXPECT().InsertOperationV2(mock.Anything).RunAndReturn(
		func(row database.OperationV2Row) error {
			mu.Lock()
			defer mu.Unlock()
			cp := row
			rows[row.ID] = &cp
			return nil
		}).Maybe()

	m.EXPECT().GetOperationV2(mock.Anything).RunAndReturn(
		func(id string) (*database.OperationV2Row, error) {
			mu.Lock()
			defer mu.Unlock()
			r, ok := rows[id]
			if !ok {
				return nil, nil
			}
			cp := *r
			return &cp, nil
		}).Maybe()

	m.EXPECT().ListQueuedOperationsV2().RunAndReturn(
		func() ([]database.OperationV2Row, error) {
			return snapshot(func(r *database.OperationV2Row) bool { return r.Status == "queued" }), nil
		}).Maybe()

	m.EXPECT().ListActiveOperationsV2().RunAndReturn(
		func() ([]database.OperationV2Row, error) {
			return snapshot(func(r *database.OperationV2Row) bool {
				return r.Status == "queued" || r.Status == "running"
			}), nil
		}).Maybe()

	// The startup resume sweep reads this one, NOT ListActiveOperationsV2: the
	// active index drops a row as soon as it stops being queued/running, so a
	// quiesced op was invisible to the sweep. The predicate here must mirror
	// isResumableV2Status, or this fixture quietly stops representing the store.
	m.EXPECT().ListResumableOperationsV2().RunAndReturn(
		func() ([]database.OperationV2Row, error) {
			return snapshot(func(r *database.OperationV2Row) bool {
				return r.Status == "queued" || r.Status == "running" ||
					r.Status == "interrupted_quiesced"
			}), nil
		}).Maybe()

	// The claim. Compare-and-set is what stops two dispatcher workers from
	// starting the same row, so it must stay atomic under the same lock.
	m.EXPECT().SetOperationV2StatusIfQueued(mock.Anything, mock.Anything).RunAndReturn(
		func(id, newStatus string) (bool, error) {
			mu.Lock()
			defer mu.Unlock()
			r, ok := rows[id]
			if !ok || r.Status != "queued" {
				return false, nil
			}
			r.Status = newStatus
			return true, nil
		}).Maybe()

	m.EXPECT().UpdateOperationV2Status(mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).RunAndReturn(
		func(id, status string, _, _ *time.Time, _ *string) error {
			mu.Lock()
			defer mu.Unlock()
			if r, ok := rows[id]; ok {
				r.Status = status
			}
			return nil
		}).Maybe()

	// Observability and state writes the run path touches; irrelevant here.
	m.EXPECT().AppendOpLogsV2(mock.Anything).Return(nil).Maybe()
	m.EXPECT().UpdateOpProgressV2(mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil).Maybe()
	m.EXPECT().UpdateOpPhaseV2(mock.Anything, mock.Anything).Return(nil).Maybe()
	m.EXPECT().UpdateOpCheckpointV2(mock.Anything, mock.Anything).Return(nil).Maybe()
	m.EXPECT().InsertOpErrorV2(mock.Anything).Return(nil).Maybe()
	m.EXPECT().InsertOpStrikeV2(mock.Anything).Return(nil).Maybe()
	m.EXPECT().UpsertOpStateV2(mock.Anything).Return(nil).Maybe()
	m.EXPECT().GetOpStateV2(mock.Anything).Return(nil, nil).Maybe()
	m.EXPECT().DeleteOpStateV2(mock.Anything).Return(nil).Maybe()
	m.EXPECT().IncrementResumeCountV2(mock.Anything).Return(nil).Maybe()
	m.EXPECT().GetDepRev(mock.Anything).Return(uint64(0), nil).Maybe()
	m.EXPECT().UpdateOperationV2Params(mock.Anything, mock.Anything).Return(nil).Maybe()

	// The v1 legacy-status bridge. propagateLegacyOpStatus runs on every terminal
	// op and reads the row's params for a legacy_op_id before touching a v1 row.
	//
	// The previous note here said this expectation "stays until activity tagging
	// is re-keyed to the v2 op id, whatever happens to the dispatcher", correcting
	// an earlier one that had tied it to the dispatcher's deletion. That condition
	// is now met: maintenanceJobOpParams no longer carries the field, and the
	// activity log, the results and the summary log are all keyed off
	// ReporterOpID. The bridge returns before reaching either call, so both
	// expectations go — they are kept .Maybe() nowhere, because nothing calls them.
	m.EXPECT().AddToBatchBucket(mock.Anything, mock.Anything).Return(nil).Maybe()
	m.EXPECT().ListBatchBucket(mock.Anything).Return(nil, nil).Maybe()
	m.EXPECT().ClearBatchBucket(mock.Anything, mock.Anything).Return(nil).Maybe()

	return m
}
