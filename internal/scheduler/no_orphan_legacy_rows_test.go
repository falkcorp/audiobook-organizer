// file: internal/scheduler/no_orphan_legacy_rows_test.go
// version: 1.1.0
// guid: 1e6d90b4-73af-4c25-8d10-b2c4f9a05e37
// last-edited: 2026-08-16

package scheduler

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	dbmocks "github.com/falkcorp/audiobook-organizer/internal/database/mocks"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// TestScheduledTasksDoNotWriteOrphanLegacyRows is the regression test for the
// 183 stuck "pending" operations.
//
// Five scheduled tasks used to do this:
//
//	opID := ulid.Make().String()
//	op, _ := store.CreateOperation(opID, "scan", nil)                 // legacy row
//	v2ID, _ := ts.deps.OpRegistry.EnqueueOp(ctx, "library.scan", ...) // DIFFERENT id
//	return op, nil
//
// The legacy row and the real operation got separate ids, nothing linked them,
// and nothing ever updated the legacy row — so every scheduled tick left one
// row at "pending" forever. Read against production on 2026-08-16, GET
// /api/v1/operations reported 183 of 200 rows pending, some six days old, while
// the v2 record for the same window showed 179 completed.
//
// The assertion is the ABSENCE of a call, which is why it is expressed through
// the mock rather than by inspecting rows: no CreateOperation expectation is
// registered, so mockery fails the test if the task calls it. An orphan row is
// invisible in every other way — that is exactly how it survived six days.
func TestScheduledTasksDoNotWriteOrphanLegacyRows(t *testing.T) {
	// Derived from taskV2DefIDs, not from a list written here. Every task the
	// scheduler can report on is covered, and a task added to that map is
	// covered the moment it is added.
	for task, defID := range taskV2DefIDs {
		t.Run(task, func(t *testing.T) {
			store := dbmocks.NewMockStore(t)
			// Deliberately NO CreateOperation expectation: that absence IS the
			// assertion. Everything the registry needs to enqueue is allowed.
			var inserted []database.OperationV2Row
			store.EXPECT().UpsertOpDefinitionV2(mock.Anything).Return(nil).Maybe()
			store.EXPECT().InsertOperationV2(mock.Anything).
				RunAndReturn(func(row database.OperationV2Row) error {
					inserted = append(inserted, row)
					return nil
				}).Maybe()
			store.EXPECT().GetOperationV2(mock.Anything).Return(nil, nil).Maybe()
			store.EXPECT().ListActiveOperationsV2().Return(nil, nil).Maybe()
			store.EXPECT().GetDepRev(mock.Anything).Return(0, nil).Maybe()

			reg := opsregistry.New(store, slog.New(slog.DiscardHandler), 1, nil)
			// A stub def standing in for the real one, which lives in
			// internal/server and cannot be imported here without a cycle. Only
			// its ID matters: EnqueueOp refuses an unregistered def.
			require.NoError(t, reg.RegisterOp(opsregistry.OperationDef{
				ID:     defID,
				Plugin: "test",
				// Required by RegisterOp — the registry refuses ResumeUnspecified.
				ResumePolicy: opsregistry.ResumeDrop,
				Liveness:     opsregistry.LivenessManual,
				Timeout:      0,
				Run: func(context.Context, json.RawMessage, opsregistry.Reporter) error {
					return nil
				},
			}))

			deps := testDeps()
			deps.Store = func() SchedulerStore { return store }
			deps.OpRegistry = reg

			ts := NewTaskScheduler(deps)
			task, ok := ts.GetTask(task)
			require.True(t, ok, "task must be registered")

			op, err := task.TriggerFn("test")
			require.NoError(t, err)
			require.NotNil(t, op, "the scheduler logs op.ID from this value")
			require.NotEmpty(t, op.ID,
				"the returned id is what the scheduler logs and /tasks/:name/run "+
					"returns; empty makes the operation unfindable")

			// THE MAP MUST MATCH WHAT THE TASK REALLY ENQUEUES. Asserting the
			// def id against the row the registry actually inserted is what
			// makes taskV2DefIDs safe to rely on: isTaskRunning and the
			// maintenance window's skip guard both read it, and a wrong or
			// missing value there does not fail — it quietly answers "not
			// running", which is how the map it replaced came to be missing ten
			// of the twenty-four tasks without anyone noticing.
			require.Len(t, inserted, 1, "task must enqueue exactly one v2 operation")
			require.Equal(t, defID, inserted[0].DefID,
				"taskV2DefIDs says %q enqueues %q", task.Name, defID)
			require.Equal(t, op.ID, inserted[0].ID,
				"the id handed back to the caller must be the id of the operation "+
					"that was actually enqueued, or nothing can look it up")
		})
	}
}

// TestEveryEnqueueingTaskHasADefIDEntry is the other direction: taskV2DefIDs
// having correct values is worth nothing if a task is simply absent from it.
//
// A missing entry makes isTaskRunning return false forever for that task, which
// reads exactly like "idle" — so the maintenance window will start it while it
// is already running, and the tasks page will render it as stopped. That is the
// defect the previous opTypeMap had, in ten places at once.
//
// The exempt set is small and each entry states why it enqueues nothing.
func TestEveryEnqueueingTaskHasADefIDEntry(t *testing.T) {
	exempt := map[string]string{
		"transcode":        "fails by design without a book_id; never enqueues",
		"label_refinement": "dispatches its own chain rather than one op",
		"batch_poller":     "polls an external batch API; not an operation",
	}

	ts := NewTaskScheduler(testDeps())
	for _, info := range ts.ListTasks() {
		if reason, ok := exempt[info.Name]; ok {
			require.NotContains(t, taskV2DefIDs, info.Name,
				"%s is exempt (%s) but has a taskV2DefIDs entry", info.Name, reason)
			continue
		}
		require.Contains(t, taskV2DefIDs, info.Name,
			"scheduled task %q has no taskV2DefIDs entry, so isTaskRunning will "+
				"report it idle forever — add one, or add it to the exempt set "+
				"here with the reason it enqueues nothing", info.Name)
	}
}
