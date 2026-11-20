// file: internal/plugins/maintenance/native_runs_test.go
// version: 0.1.0
// guid: b46d8da6-af25-4337-bec5-24c75f00b9e1
// last-edited: 2026-11-20

package maintenance

import (
	"testing"
	"time"

	"github.com/falkcorp/mediacore/coretest"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/keys"
	"github.com/falkcorp/mediacore/tasks"
	"github.com/falkcorp/mediacore/tasks/taskstest"
	"github.com/stretchr/testify/require"
)

// The native run store the F5-18d state homes are proven against: core's own
// NativeStores over a real keyspace, so the closed-ledger rule (ReplaceRows
// refuses a terminal run), the paged listing and the stored result are core's,
// not a fake's.

// testTasksOwner is the storage owner of the test's task keyspace.
const testTasksOwner storage.Owner = "core/tasks"

// newNativeRuns is an empty NativeStores run store.
func newNativeRuns(t *testing.T) tasks.RunStore {
	t.Helper()
	st := coretest.NewStore(t, storage.OwnerSpec{Owner: testTasksOwner, Enabled: true, Spec: testTasksSpec()})
	return tasks.NativeStores(st.Keyspace(testTasksOwner)).Runs
}

// testTasksSpec is core's task keyspace spec as the kernel declares it: the
// families whose writes carry a guard precondition are Serialized (the m4 rig
// declares the same).
func testTasksSpec() storage.Spec {
	var ser []string
	for _, fam := range []string{"run", "debounce", "sched", "idem"} {
		ser = append(ser, string(keys.Prefix(keys.Str(fam))))
	}
	return storage.Spec{Keyspace: true, Serialized: ser}
}

// nativeRunAt is a run record of kind in state, created at created; a terminal
// one is finished an hour later.
func nativeRunAt(id tasks.RunID, kind tasks.Kind, mode tasks.Mode, state tasks.RunState, created time.Time) tasks.RunRecord {
	r := tasks.RunRecord{
		RunView: tasks.RunView{ID: id, Kind: kind, Mode: mode, State: state, Created: created, Started: created,
			Actor: tasks.Actor{UserID: "alfred"}},
		Priority: tasks.PriorityLow, Attempt: 1, FenceEpoch: 1,
	}
	if state.Terminal() {
		r.Finished = created.Add(time.Hour)
	}
	return r
}

// putNativeRun stores rec as it stands (the record the executor's last
// transition would have left).
func putNativeRun(t *testing.T, runs tasks.RunStore, rec tasks.RunRecord) {
	t.Helper()
	ctx := t.Context()
	b := runs.(taskstest.BatchOpener).NewBatch()
	defer func() { _ = b.Close() }()
	require.NoError(t, runs.Insert(ctx, b, rec))
	require.NoError(t, b.Commit(ctx, storage.Sync))
}

// finishNativeRun moves a running run to state with result, as the executor's
// attempt end does (one Transition under the run's attempt and fence).
func finishNativeRun(t *testing.T, runs tasks.RunStore, id tasks.RunID, state tasks.RunState, result []byte) {
	t.Helper()
	ctx := t.Context()
	cur, err := runs.Get(ctx, id)
	require.NoError(t, err)
	next := cur
	next.State, next.Result, next.Finished = state, result, cur.Started.Add(time.Hour)
	b := runs.(taskstest.BatchOpener).NewBatch()
	defer func() { _ = b.Close() }()
	require.NoError(t, runs.Transition(ctx, b, id, tasks.Expect{State: cur.State, Attempt: cur.Attempt, FenceEpoch: cur.FenceEpoch}, next))
	require.NoError(t, b.Commit(ctx, storage.Sync))
}

// runningCBRuns is a native run store holding the credits backfill's own run,
// running, under the id taskstest.FakeRun gives every run of the kind: the run
// the native kind keeps its class id lists under (ReplaceRows refuses a run that
// is missing or terminal).
func runningCBRuns(t *testing.T) tasks.RunStore {
	t.Helper()
	runs := newNativeRuns(t)
	putNativeRun(t, runs, nativeRunAt(cbFakeRunID, t6g3CreditsBackfillKind, tasks.ModeLive, tasks.StateRunning, time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)))
	return runs
}

// cbFakeRunID is the id taskstest.FakeRun gives a credits backfill run.
const cbFakeRunID tasks.RunID = "fake-" + tasks.RunID(t6g3CreditsBackfillKind)
