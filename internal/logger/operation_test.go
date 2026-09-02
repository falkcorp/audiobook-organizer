// file: internal/logger/operation_test.go
// version: 1.1.1
// guid: a1b2c3d4-e5f6-7890-abcd-ef1234567890
// last-edited: 2026-09-02

package logger

import (
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/logger/mocks"
	"github.com/stretchr/testify/mock"
)

// opStoreCalls collects what the OperationLogger pushed through its
// store dependency during a test. We use a struct of slices instead
// of inspecting mock.Calls directly because several tests care about
// the *order* and *content* of messages, not just that the method
// fired.
type opStoreCalls struct {
	mu       sync.Mutex
	logs     []string
	changes  []any
	progress []string
}

func newOpStoreMock(t *testing.T) (*mocks.MockOperationStore, *opStoreCalls) {
	t.Helper()
	calls := &opStoreCalls{}
	m := mocks.NewMockOperationStore(t)
	m.EXPECT().
		AddOperationLog(mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		RunAndReturn(func(opID, level, message string, details *string) error {
			calls.mu.Lock()
			defer calls.mu.Unlock()
			calls.logs = append(calls.logs, level+":"+message)
			return nil
		}).Maybe()
	m.EXPECT().
		CreateOperationChange(mock.Anything).
		RunAndReturn(func(change any) error {
			calls.mu.Lock()
			defer calls.mu.Unlock()
			calls.changes = append(calls.changes, change)
			return nil
		}).Maybe()
	m.EXPECT().
		UpdateOperationProgress(mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		RunAndReturn(func(id string, current, total int, message string) error {
			calls.mu.Lock()
			defer calls.mu.Unlock()
			calls.progress = append(calls.progress, message)
			return nil
		}).Maybe()
	return m, calls
}

type hubCalls struct {
	mu           sync.Mutex
	logsSent     int
	progressSent int
}

func newHubMock(t *testing.T) (*mocks.MockRealtimeHub, *hubCalls) {
	t.Helper()
	calls := &hubCalls{}
	m := mocks.NewMockRealtimeHub(t)
	m.EXPECT().
		SendOperationLog(mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Run(func(opID, level, message string, details *string) {
			calls.mu.Lock()
			defer calls.mu.Unlock()
			calls.logsSent++
		}).Maybe()
	m.EXPECT().
		SendOperationProgress(mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Run(func(opID string, current, total int, message string) {
			calls.mu.Lock()
			defer calls.mu.Unlock()
			calls.progressSent++
		}).Maybe()
	return m, calls
}

func TestOperationLogger_LevelFiltering(t *testing.T) {
	store, storeCalls := newOpStoreMock(t)
	hub, hubCalls := newHubMock(t)
	log := ForOperation("op1", store, hub)

	log.Debug("debug msg") // below default minDBLevel (INFO)
	log.Info("info msg")   // at minDBLevel
	log.Warn("warn msg")   // above minDBLevel

	if len(storeCalls.logs) != 2 {
		t.Errorf("expected 2 DB logs (info+warn), got %d: %v", len(storeCalls.logs), storeCalls.logs)
	}
	if hubCalls.logsSent != 2 {
		t.Errorf("expected 2 hub sends, got %d", hubCalls.logsSent)
	}
}

func TestOperationLogger_DebugWhenVerbose(t *testing.T) {
	store, storeCalls := newOpStoreMock(t)
	log := ForOperation("op1", store, nil)
	log.SetMinDBLevel(LevelDebug)

	log.Debug("debug msg")
	log.Trace("trace msg") // still below DEBUG

	if len(storeCalls.logs) != 1 {
		t.Errorf("expected 1 DB log (debug), got %d", len(storeCalls.logs))
	}
}

func TestOperationLogger_RecordChange(t *testing.T) {
	log := ForOperation("op1", nil, nil)
	log.RecordChange(Change{ChangeType: "book_create", Summary: "Created book A"})
	log.RecordChange(Change{ChangeType: "book_create", Summary: "Created book B"})
	log.RecordChange(Change{ChangeType: "book_update", Summary: "Updated book C"})

	counters := log.ChangeCounters()
	if counters["book_create"] != 2 {
		t.Errorf("book_create = %d, want 2", counters["book_create"])
	}
	if counters["book_update"] != 1 {
		t.Errorf("book_update = %d, want 1", counters["book_update"])
	}
}

func TestOperationLogger_IsCanceled(t *testing.T) {
	log := ForOperation("op1", nil, nil)
	if log.IsCanceled() {
		t.Error("should not be canceled initially")
	}
	log.SetCanceled()
	if !log.IsCanceled() {
		t.Error("should be canceled after SetCanceled()")
	}
}

func TestOperationLogger_With(t *testing.T) {
	log := ForOperation("op1", nil, nil)
	child := log.With("scanner")

	// Child should share canceled state
	log.SetCanceled()
	if !child.IsCanceled() {
		t.Error("child should see parent's canceled state")
	}
}

func TestOperationLogger_Progress(t *testing.T) {
	store, storeCalls := newOpStoreMock(t)
	hub, hubCalls := newHubMock(t)
	log := ForOperation("op1", store, hub)

	log.UpdateProgress(5, 100, "scanning")

	if len(storeCalls.progress) != 1 {
		t.Errorf("expected 1 progress update, got %d", len(storeCalls.progress))
	}
	if hubCalls.progressSent != 1 {
		t.Errorf("expected 1 hub progress, got %d", hubCalls.progressSent)
	}
}

func TestOperationLogger_BackwardCompatLog(t *testing.T) {
	store, storeCalls := newOpStoreMock(t)
	log := ForOperation("op1", store, nil)

	_ = log.Log("info", "backward compat message", nil)

	if len(storeCalls.logs) != 1 {
		t.Errorf("expected 1 DB log, got %d", len(storeCalls.logs))
	}
}
