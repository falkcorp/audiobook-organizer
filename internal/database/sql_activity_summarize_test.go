// file: internal/database/sql_activity_summarize_test.go
// version: 1.0.0
// guid: e0e16a93-b555-4a92-bfc9-537f0f51543e
// last-edited: 2026-09-19

package database

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSQLSummarize_StatsDescribeExactlyTheDeletedRows: the summary's count must
// be computed in the same transaction that deletes, over the same rows. A row
// that lands in the group between the group scan and the delete used to be
// deleted without being counted.
func TestSQLSummarize_StatsDescribeExactlyTheDeletedRows(t *testing.T) {
	s := newTestSQLStore(t)
	day := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	seedSummarizeGroup(t, s, day, 5)
	summarizeTestHooks.beforeGroup = func() {
		seedSummarizeGroup(t, s, day.Add(time.Hour), 1)
	}
	t.Cleanup(func() { summarizeTestHooks.beforeGroup = nil })

	deleted, err := s.Summarize(context.Background(), day.Add(48*time.Hour), "change")
	require.NoError(t, err)
	rows, _, err := s.Query(context.Background(), ActivityFilter{Tier: "change", Source: "summarize", Limit: 10})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	n, _, _ := summaryDateSpan(t, rows[0].Summary)
	assert.Equal(t, deleted, n, "summary count == rows deleted")
}

// TestSQLSummarize_GateWaitHonoursCtx: Summarize waits for the maintenance gate
// like every other deleting pass — cancellable, not a raw Lock that pins the
// nightly op behind a long backfill batch.
func TestSQLSummarize_GateWaitHonoursCtx(t *testing.T) {
	s := newTestSQLStore(t)
	s.backfillGate.RLock() // a backfill batch holds the gate
	released := false
	release := func() {
		if !released {
			released = true
			s.backfillGate.RUnlock()
		}
	}
	t.Cleanup(release)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := s.Summarize(ctx, time.Now(), "change")
		done <- err
	}()
	select {
	case err := <-done:
		assert.True(t, errors.Is(err, context.DeadlineExceeded), "got %v", err)
	case <-time.After(3 * time.Second):
		release()
		<-done
		t.Fatal("Summarize ignored ctx while waiting for the gate")
	}
}
