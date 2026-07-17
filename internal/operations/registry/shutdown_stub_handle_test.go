// file: internal/operations/registry/shutdown_stub_handle_test.go
// version: 1.1.0
// guid: 7b3c9d1e-4a52-4c8f-9e0a-2f6b1d8c4a37
// last-edited: 2026-07-17

// White-box regression tests for the "stub handle" nil-cancel race.
//
// The dispatcher inserts a placeholder runHandle into r.running with a nil
// cancel func the instant an op is claimed (to block Gate-0 re-dispatch); the
// worker overwrites it with the full handle on pickup. Between those two
// events a handle is present in r.running with cancel == nil. Code that walks
// r.running and cancels (Shutdown, Cancel, the watchdog) must tolerate that —
// before the fix, hitting this window panicked with a nil-pointer dereference
// (observed as a flaky crash in setupTestServer cleanup under parallel test
// load). These tests reproduce the window deterministically by inserting a
// stub handle directly and asserting no panic.
package registry

import (
	"context"
	"log/slog"
	"testing"
	"time"

	databasemocks "github.com/falkcorp/audiobook-organizer/internal/database/mocks"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// insertStubHandle mimics the dispatcher's stub insertion: a handle in
// r.running with a nil cancel func.
func insertStubHandle(r *Registry, opID string) {
	r.mu.Lock()
	r.running[opID] = &runHandle{
		id:           opID,
		defID:        "test.stub",
		resumePolicy: ResumeRequeue,
		// cancel intentionally left nil — this is the dispatcher stub window.
	}
	r.mu.Unlock()
}

// TestShutdown_StubHandleNilCancel_NoPanic verifies Shutdown does not panic
// when a stub handle (nil cancel) is present in r.running.
func TestShutdown_StubHandleNilCancel_NoPanic(t *testing.T) {
	store := databasemocks.NewMockOpsV2Store(t)
	// The stub never drains, so Shutdown times out and marks it interrupted.
	store.EXPECT().
		UpdateOperationV2Status(mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return(nil).Maybe()

	r := New(store, slog.Default(), 1, nil)
	insertStubHandle(r, "stub-op")

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	require.NotPanics(t, func() {
		_ = r.Shutdown(ctx)
	})
}

// TestCancel_StubHandleNilCancel_NoPanic verifies Cancel does not panic when
// the targeted op is a stub handle (nil cancel), and that it flags the stub +
// attempts the DB cancel (C-1: canceling a channel-queued op used to be a
// silent no-op — the op ran anyway).
func TestCancel_StubHandleNilCancel_NoPanic(t *testing.T) {
	store := databasemocks.NewMockOpsV2Store(t)
	// The stub path now marks the still-'queued' row canceled in the DB.
	store.EXPECT().
		SetOperationV2StatusIfQueued("stub-op", "canceled").
		Return(true, nil).Once()

	r := New(store, slog.Default(), 1, nil)
	insertStubHandle(r, "stub-op")

	require.NotPanics(t, func() {
		err := r.Cancel("stub-op")
		require.NoError(t, err)
	})

	// The stub must be flagged so the worker drops the run before invoking it.
	r.mu.Lock()
	flagged := r.running["stub-op"].queuedCancel
	r.mu.Unlock()
	require.True(t, flagged, "Cancel must set queuedCancel on the stub handle")
}
