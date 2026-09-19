// file: internal/scheduler/batch_poller_ctx_test.go
// version: 1.0.0
// guid: dc0ef604-53fc-4ba4-a9c2-af12432638ef
// last-edited: 2026-09-19

package scheduler

import (
	"context"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/lifecycle"
	"github.com/stretchr/testify/require"
)

// TestBatchPollerRunsOnLifecycleContext: work the batch poller starts
// (collecting a finished AI scan batch, then enrichment and cross-validation)
// used to run on context.Background(), so nothing could stop it — not a
// server shutdown, not a scan cancel. The poller's context must end with the
// server, carrying lifecycle.ErrShutdown as the cause.
func TestBatchPollerRunsOnLifecycleContext(t *testing.T) {
	var got context.Context
	deps := testDeps()
	deps.HasBatchPoller = func() bool { return true }
	deps.PollBatches = func(ctx context.Context) (int, error) {
		got = ctx
		return 0, nil
	}
	ts := NewTaskScheduler(deps)
	shutdown := make(chan struct{})
	ts.shutdown = shutdown
	task := ts.tasks["batch_poller"]
	require.NotNil(t, task)

	_, err := task.TriggerFn("test")
	require.NoError(t, err)
	require.NotNil(t, got)
	require.NoError(t, got.Err())

	close(shutdown)
	select {
	case <-got.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("the batch poller's context did not end with the server")
	}
	require.True(t, lifecycle.IsShutdown(got), "cause = %v", context.Cause(got))
}
