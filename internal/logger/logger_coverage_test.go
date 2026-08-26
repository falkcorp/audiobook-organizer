// file: internal/logger/logger_coverage_test.go
// version: 1.1.0

package logger

import (
	"fmt"
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/logger/mocks"
	"github.com/stretchr/testify/mock"
)

// --- Level coverage ---

func TestCoverage_LevelString_AllLevels(t *testing.T) {
	tests := []struct {
		level Level
		want  string
	}{
		{LevelTrace, "trace"},
		{LevelDebug, "debug"},
		{LevelInfo, "info"},
		{LevelWarn, "warn"},
		{LevelError, "error"},
		{Level(99), "unknown"},
	}
	for _, tt := range tests {
		got := tt.level.String()
		if got != tt.want {
			t.Errorf("Level(%d).String() = %q, want %q", tt.level, got, tt.want)
		}
	}
}

// --- StandardLogger coverage ---

func TestCoverage_StandardLogger_Trace(t *testing.T) {
	log := New("test")
	// Trace is below default minStdout (Debug), so this should be a no-op.
	log.Trace("trace message %d", 1)
}

func TestCoverage_StandardLogger_Error(t *testing.T) {
	log := New("test")
	log.Error("error message %s", "details")
}

func TestCoverage_StandardLogger_With_Nested(t *testing.T) {
	log := New("root")
	child := log.With("level1")
	grandchild := child.With("level2")

	sl, ok := grandchild.(*StandardLogger)
	if !ok {
		t.Fatal("expected *StandardLogger")
	}
	if sl.subsystem != "root.level1.level2" {
		t.Errorf("subsystem = %q, want 'root.level1.level2'", sl.subsystem)
	}
}

func TestCoverage_StandardLogger_With_EmptyParent(t *testing.T) {
	log := &StandardLogger{minStdout: LevelDebug}
	child := log.With("child")
	sl, ok := child.(*StandardLogger)
	if !ok {
		t.Fatal("expected *StandardLogger")
	}
	if sl.subsystem != "child" {
		t.Errorf("subsystem = %q, want 'child'", sl.subsystem)
	}
}

func TestCoverage_NewWithActivityLog(t *testing.T) {
	writer := mocks.NewMockActivityLogWriter(t)
	log := NewWithActivityLog("test", writer)
	if log.activityWriter == nil {
		t.Error("activity writer should be set")
	}
	if log.subsystem != "test" {
		t.Errorf("subsystem = %q, want 'test'", log.subsystem)
	}
}

// newActivityCapture returns a mock ActivityLogWriter whose
// AddSystemActivityLog appends "level:message" to a shared slice.
// Centralizes the capture boilerplate across coverage tests.
func newActivityCapture(t *testing.T) (*mocks.MockActivityLogWriter, *[]string) {
	t.Helper()
	var captured []string
	writer := mocks.NewMockActivityLogWriter(t)
	writer.EXPECT().
		AddSystemActivityLog(mock.Anything, mock.Anything, mock.Anything).
		RunAndReturn(func(source, level, message string) error {
			captured = append(captured, level+":"+message)
			return nil
		}).Maybe()
	return writer, &captured
}

func TestCoverage_StandardLogger_ActivityWriter_Trace(t *testing.T) {
	writer, captured := newActivityCapture(t)
	log := NewWithActivityLog("test", writer)
	log.Trace("should not capture") // below minStdout

	if len(*captured) != 0 {
		t.Errorf("expected 0 captured, got %d", len(*captured))
	}
}

func TestCoverage_StandardLogger_ActivityWriter_Error(t *testing.T) {
	writer, captured := newActivityCapture(t)
	log := NewWithActivityLog("test", writer)
	log.Error("error %s", "msg")

	if len(*captured) != 1 {
		t.Errorf("expected 1 captured, got %d", len(*captured))
	}
}

// --- OperationLogger coverage ---

func TestCoverage_OperationLogger_Trace(t *testing.T) {
	store, calls := newOpStoreMock(t)
	log := ForOperation("op1", store, nil)
	log.Trace("trace msg") // should not go to DB (below minDBLevel)
	if len(calls.logs) != 0 {
		t.Errorf("expected 0 DB logs, got %d", len(calls.logs))
	}
}

func TestCoverage_OperationLogger_Error(t *testing.T) {
	store, calls := newOpStoreMock(t)
	log := ForOperation("op1", store, nil)
	log.Error("error msg")
	if len(calls.logs) != 1 {
		t.Errorf("expected 1 DB log, got %d", len(calls.logs))
	}
}

func TestCoverage_OperationLogger_Changes(t *testing.T) {
	log := ForOperation("op1", nil, nil)
	log.RecordChange(Change{ChangeType: "book_create", Summary: "Created A"})
	log.RecordChange(Change{ChangeType: "book_update", Summary: "Updated B"})

	changes := log.Changes()
	if len(changes) != 2 {
		t.Fatalf("expected 2 changes, got %d", len(changes))
	}
	if changes[0].Summary != "Created A" {
		t.Errorf("expected 'Created A', got %q", changes[0].Summary)
	}
}

func TestCoverage_OperationLogger_OperationID(t *testing.T) {
	log := ForOperation("test-op-id", nil, nil)
	if log.OperationID() != "test-op-id" {
		t.Errorf("OperationID() = %q, want 'test-op-id'", log.OperationID())
	}
}

func TestCoverage_OperationLogger_SetMinDBLevel(t *testing.T) {
	store, calls := newOpStoreMock(t)
	log := ForOperation("op1", store, nil)
	log.SetMinDBLevel(LevelError)

	log.Info("should not appear")
	log.Warn("should not appear")
	log.Error("should appear")

	if len(calls.logs) != 1 {
		t.Errorf("expected 1 log, got %d: %v", len(calls.logs), calls.logs)
	}
}

func TestCoverage_OperationLogger_With_Prefix(t *testing.T) {
	log := ForOperation("op1", nil, nil)
	child := log.With("scanner")
	child2 := child.With("deep")

	opLog, ok := child2.(*OperationLogger)
	if !ok {
		t.Fatal("expected *OperationLogger")
	}
	if opLog.subsystem != "scanner.deep" {
		t.Errorf("subsystem = %q, want 'scanner.deep'", opLog.subsystem)
	}
}

func TestCoverage_OperationLogger_WithHub(t *testing.T) {
	store, _ := newOpStoreMock(t)
	hub, hubCalls := newHubMock(t)
	log := ForOperation("op1", store, hub)

	log.Info("msg")

	hubCalls.mu.Lock()
	sent := hubCalls.logsSent
	hubCalls.mu.Unlock()
	if sent != 1 {
		t.Errorf("expected 1 hub log, got %d", sent)
	}
}

func TestCoverage_OperationLogger_Log_WithDetails(t *testing.T) {
	store, calls := newOpStoreMock(t)
	log := ForOperation("op1", store, nil)
	details := "some details"
	_ = log.Log("info", "msg with details", &details)
	if len(calls.logs) != 1 {
		t.Errorf("expected 1 DB log, got %d", len(calls.logs))
	}
}

func TestCoverage_OperationLogger_UpdateProgress_NilStore(t *testing.T) {
	log := ForOperation("op1", nil, nil)
	log.UpdateProgress(1, 10, "test") // should not panic
}

func TestCoverage_OperationLogger_ConcurrentRecordChange(t *testing.T) {
	log := ForOperation("op1", nil, nil)
	var wg sync.WaitGroup
	for i := range 100 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			log.RecordChange(Change{ChangeType: "test"})
		}(i)
	}
	wg.Wait()

	counters := log.ChangeCounters()
	if counters["test"] != 100 {
		t.Errorf("expected 100, got %d", counters["test"])
	}
}

// --- PruneOldLogs coverage ---

// retentionStoreConfig describes the desired behavior of a mock
// RetentionStore: the count each Prune method should return plus
// whether it should fail. Zero values ⇒ (0, nil) for that method.
type retentionStoreConfig struct {
	logsCount     int
	changesCount  int
	activityCount int
	logErr        error
	changeErr     error
	activityErr   error
}

func newRetentionStoreMock(t *testing.T, cfg retentionStoreConfig) *mocks.MockRetentionStore {
	t.Helper()
	m := mocks.NewMockRetentionStore(t)
	m.EXPECT().PruneOperationLogs(mock.Anything).Return(cfg.logsCount, cfg.logErr).Maybe()
	m.EXPECT().PruneOperationChanges(mock.Anything).Return(cfg.changesCount, cfg.changeErr).Maybe()
	m.EXPECT().PruneSystemActivityLogs(mock.Anything).Return(cfg.activityCount, cfg.activityErr).Maybe()
	return m
}

func TestCoverage_PruneOldLogs(t *testing.T) {
	t.Run("disabled when 0 days", func(t *testing.T) {
		log := New("test")
		store := newRetentionStoreMock(t, retentionStoreConfig{})
		n, err := PruneOldLogs(store, 0, log)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if n != 0 {
			t.Errorf("expected 0 pruned, got %d", n)
		}
	})

	t.Run("prunes all types", func(t *testing.T) {
		log := New("test")
		store := newRetentionStoreMock(t, retentionStoreConfig{
			logsCount:     10,
			changesCount:  5,
			activityCount: 3,
		})
		n, err := PruneOldLogs(store, 30, log)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if n != 18 {
			t.Errorf("expected 18 total pruned, got %d", n)
		}
	})

	t.Run("handles log prune error", func(t *testing.T) {
		log := New("test")
		store := newRetentionStoreMock(t, retentionStoreConfig{logErr: errMock})
		_, err := PruneOldLogs(store, 30, log)
		if err == nil {
			t.Error("expected error")
		}
	})

	t.Run("handles change prune error", func(t *testing.T) {
		log := New("test")
		store := newRetentionStoreMock(t, retentionStoreConfig{changeErr: errMock})
		_, err := PruneOldLogs(store, 30, log)
		if err == nil {
			t.Error("expected error")
		}
	})

	t.Run("handles activity prune error", func(t *testing.T) {
		log := New("test")
		store := newRetentionStoreMock(t, retentionStoreConfig{activityErr: errMock})
		_, err := PruneOldLogs(store, 30, log)
		if err == nil {
			t.Error("expected error")
		}
	})
}

// --- test helpers ---

var errMock = fmt.Errorf("mock error")
