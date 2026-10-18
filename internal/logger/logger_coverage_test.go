// file: internal/logger/logger_coverage_test.go
// version: 1.4.1
// guid: dbb463d6-f89b-4dee-8f50-b9ea2a790f2e
// last-edited: 2026-10-18

package logger

import (
	"context"
	"fmt"
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

// --- PruneOldLogs coverage ---

// retentionStoreConfig describes the desired behavior of a mock
// RetentionStore: the count each Prune method should return plus
// whether it should fail. Zero values ⇒ (0, nil) for that method.
type retentionStoreConfig struct {
	logsCount     int
	activityCount int
	logErr        error
	activityErr   error
}

func newRetentionStoreMock(t *testing.T, cfg retentionStoreConfig) *mocks.MockRetentionStore {
	t.Helper()
	m := mocks.NewMockRetentionStore(t)
	m.EXPECT().PruneOperationLogs(mock.Anything, mock.Anything, mock.Anything).Return(cfg.logsCount, cfg.logErr).Maybe()
	m.EXPECT().PruneSystemActivityLogs(mock.Anything, mock.Anything, mock.Anything).Return(cfg.activityCount, cfg.activityErr).Maybe()
	return m
}

func TestCoverage_PruneOldLogs(t *testing.T) {
	t.Run("disabled when 0 days", func(t *testing.T) {
		log := New("test")
		store := newRetentionStoreMock(t, retentionStoreConfig{})
		n, err := PruneOldLogs(context.Background(), store, 0, log, nil)
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
			activityCount: 3,
		})
		n, err := PruneOldLogs(context.Background(), store, 30, log, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if n != 13 {
			t.Errorf("expected 13 total pruned, got %d", n)
		}
	})

	t.Run("handles log prune error", func(t *testing.T) {
		log := New("test")
		store := newRetentionStoreMock(t, retentionStoreConfig{logErr: errMock})
		_, err := PruneOldLogs(context.Background(), store, 30, log, nil)
		if err == nil {
			t.Error("expected error")
		}
	})

	t.Run("handles activity prune error", func(t *testing.T) {
		log := New("test")
		store := newRetentionStoreMock(t, retentionStoreConfig{activityErr: errMock})
		_, err := PruneOldLogs(context.Background(), store, 30, log, nil)
		if err == nil {
			t.Error("expected error")
		}
	})
}

// --- test helpers ---

var errMock = fmt.Errorf("mock error")
