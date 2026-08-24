// file: internal/scheduler/wait_for_operation_test.go
// version: 1.0.0
// guid: 8c2e4f61-9a37-4b0d-85e2-1f6a3d7c9b40
// last-edited: 2026-08-23

package scheduler

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	dbmocks "github.com/falkcorp/audiobook-organizer/internal/database/mocks"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// waitTestScheduler builds a TaskScheduler whose WaitForOperation polls fast
// enough to test, backed by a store whose GetOperationV2 is scripted per call.
func waitTestScheduler(t *testing.T, script func(call int) (*database.OperationV2Row, error)) (*TaskScheduler, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	store := dbmocks.NewMockStore(t)
	store.EXPECT().GetOperationV2(mock.Anything).
		RunAndReturn(func(string) (*database.OperationV2Row, error) {
			return script(int(calls.Add(1)))
		}).Maybe()
	deps := testDeps()
	deps.Store = func() SchedulerStore { return store }
	return &TaskScheduler{deps: deps, waitPollInterval: 5 * time.Millisecond}, &calls
}

func row(status string) *database.OperationV2Row {
	return &database.OperationV2Row{ID: "op-1", Status: status}
}

// TestWaitForOperationKeepsPollingWhenRowIsMissing is the regression test for
// the nightly maintenance.window panic, and it is deliberately NOT a
// "does not panic" test.
//
// The bug: WaitForOperation read the LEGACY table via GetOperationByID, which
// returns (nil, nil) on not-found, and guarded only err — so it dereferenced a
// nil op on the first tick. Every scheduled task returns a v2 id that is never
// written to the legacy keyspace, so this fired on task 1 of every window.
//
// The obvious patch — `if err != nil || row == nil { return }` — stops the
// panic and introduces a WORSE, silent bug: the wait becomes an instant return,
// the window stops serializing its tasks, and it fans all 12 out concurrently
// while reporting success. A test that only asserted "no panic" would pass that
// version. So this asserts the loop KEEPS POLLING through the missing row and
// only returns once a terminal row actually appears.
func TestWaitForOperationKeepsPollingWhenRowIsMissing(t *testing.T) {
	const missingTicks = 4
	ts, calls := waitTestScheduler(t, func(call int) (*database.OperationV2Row, error) {
		if call <= missingTicks {
			return nil, nil // not-found, exactly as PebbleStore reports it
		}
		return row("completed"), nil
	})

	got := ts.WaitForOperation(context.Background(), "op-1")

	require.NotNil(t, got, "must wait for the real terminal row, not return on not-found")
	require.Equal(t, "completed", got.Status)
	require.Greater(t, int(calls.Load()), missingTicks,
		"returned before the row existed: the wait is a no-op and tasks would run concurrently")
}

// TestWaitForOperationKeepsPollingOnStoreError pins the same property for a
// transient DB error. Returning here would also collapse serialization, and the
// pre-fix code did exactly that (`if err != nil { return }`).
func TestWaitForOperationKeepsPollingOnStoreError(t *testing.T) {
	ts, calls := waitTestScheduler(t, func(call int) (*database.OperationV2Row, error) {
		if call <= 3 {
			return nil, context.DeadlineExceeded
		}
		return row("completed"), nil
	})

	got := ts.WaitForOperation(context.Background(), "op-1")

	require.NotNil(t, got)
	require.Greater(t, int(calls.Load()), 3, "a transient store error must not end the wait")
}

// TestWaitForOperationTerminalStatuses pins which statuses end the wait.
//
// The two interrupted_* states are the ones the legacy terminal set omitted.
// That omission is not cosmetic: library.scan ends interrupted_quiesced on
// nearly every production run and metadata.batch-save ends interrupted_dropped,
// so a wait that did not recognise them would block until ctx expired and the
// window would never reach its remaining tasks.
func TestWaitForOperationTerminalStatuses(t *testing.T) {
	terminal := []string{"completed", "failed", "canceled", "interrupted_dropped", "interrupted_quiesced"}
	for _, status := range terminal {
		t.Run("terminal/"+status, func(t *testing.T) {
			ts, calls := waitTestScheduler(t, func(int) (*database.OperationV2Row, error) {
				return row(status), nil
			})
			got := ts.WaitForOperation(context.Background(), "op-1")
			require.NotNil(t, got, "%s must end the wait", status)
			require.Equal(t, status, got.Status)
			require.Equal(t, 1, int(calls.Load()), "%s should be terminal on the first read", status)
		})
	}

	for _, status := range []string{"queued", "running"} {
		t.Run("nonterminal/"+status, func(t *testing.T) {
			ts, calls := waitTestScheduler(t, func(call int) (*database.OperationV2Row, error) {
				if call <= 3 {
					return row(status), nil
				}
				return row("completed"), nil
			})
			got := ts.WaitForOperation(context.Background(), "op-1")
			require.Equal(t, "completed", got.Status)
			require.Greater(t, int(calls.Load()), 3, "%s must NOT end the wait", status)
		})
	}
}

// TestWaitForOperationHeartbeatsWhileRunning pins the onPoll contract: the
// supervising op proves liveness to the watchdog on every tick it spends
// waiting. Without it maintenance.window looked wedged and was struck — 28 of
// the 44 stuck-op cancellations in the 30 days to 2026-08-16.
func TestWaitForOperationHeartbeatsWhileRunning(t *testing.T) {
	ts, _ := waitTestScheduler(t, func(call int) (*database.OperationV2Row, error) {
		if call <= 3 {
			return &database.OperationV2Row{ID: "op-1", Status: "running", ProgressCurrent: call, ProgressTotal: 10}, nil
		}
		return row("completed"), nil
	})

	var beats int
	got := ts.WaitForOperation(context.Background(), "op-1", func(*database.OperationV2Row) { beats++ })

	require.Equal(t, "completed", got.Status)
	require.Equal(t, 3, beats, "one heartbeat per running tick, none for the terminal read")
}

// TestWaitForOperationReturnsNilOnContextCancel pins that an aborted wait is
// distinguishable from a finished one. The caller reports these differently:
// nil means the WINDOW is shutting down and says nothing about the child.
func TestWaitForOperationReturnsNilOnContextCancel(t *testing.T) {
	ts, _ := waitTestScheduler(t, func(int) (*database.OperationV2Row, error) {
		return row("running"), nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	require.Nil(t, ts.WaitForOperation(ctx, "op-1"))
}

// TestWaitForOperationNilStoreReturnsNil covers the pre-DB-init path.
func TestWaitForOperationNilStoreReturnsNil(t *testing.T) {
	ts := &TaskScheduler{deps: testDeps(), waitPollInterval: time.Millisecond}
	require.Nil(t, ts.WaitForOperation(context.Background(), "op-1"))
}
