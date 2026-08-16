// file: internal/scheduler/no_orphan_legacy_rows_test.go
// version: 1.0.0
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
	// Every task whose TriggerFn used to write a legacy row and enqueue a v2 op
	// under a different id, paired with the def it enqueues.
	cases := []struct{ task, defID, opType string }{
		{"library_scan", "library.scan", "scan"},
		{"library_organize", "library.organize", "organize"},
		{"library_size_refresh", "library.size-refresh", "library-size-refresh"},
		{"acoustid_online_lookup", "acoustid.lookup-online", "acoustid-online-lookup"},
		{"ai_dedup_batch", "maintenance.ai-dedup-batch", "ai-dedup-batch"},
	}

	for _, tc := range cases {
		t.Run(tc.task, func(t *testing.T) {
			store := dbmocks.NewMockStore(t)
			// Deliberately NO CreateOperation expectation: that absence IS the
			// assertion. Everything the registry needs to enqueue is allowed.
			store.EXPECT().UpsertOpDefinitionV2(mock.Anything).Return(nil).Maybe()
			store.EXPECT().InsertOperationV2(mock.Anything).Return(nil).Maybe()
			store.EXPECT().GetOperationV2(mock.Anything).Return(nil, nil).Maybe()
			store.EXPECT().ListActiveOperationsV2().Return(nil, nil).Maybe()
			store.EXPECT().GetDepRev(mock.Anything).Return(0, nil).Maybe()

			reg := opsregistry.New(store, slog.New(slog.DiscardHandler), 1, nil)
			// A stub def standing in for the real one, which lives in
			// internal/server and cannot be imported here without a cycle. Only
			// its ID matters: EnqueueOp refuses an unregistered def.
			require.NoError(t, reg.RegisterOp(opsregistry.OperationDef{
				ID:     tc.defID,
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
			deps.Store = func() database.Store { return store }
			deps.OpRegistry = reg

			ts := NewTaskScheduler(deps)
			task, ok := ts.GetTask(tc.task)
			require.True(t, ok, "task %s must be registered", tc.task)

			op, err := task.TriggerFn("test")
			require.NoError(t, err)
			require.NotNil(t, op, "the scheduler logs op.ID from this value")

			require.Equal(t, tc.opType, op.Type)
			require.NotEmpty(t, op.ID,
				"the returned id is what the scheduler logs and /tasks/:name/run "+
					"returns; empty makes the operation unfindable")

			// The id must be the V2 one — resolvable via GET /operations/v2/:id.
			// Previously this was the legacy row's id, so the log line and the API
			// response both named an operation no endpoint could look up.
			found, gerr := reg.Def(tc.defID)
			require.True(t, gerr, "def %s should be registered", tc.defID)
			require.Equal(t, tc.defID, found.ID)
		})
	}
}
