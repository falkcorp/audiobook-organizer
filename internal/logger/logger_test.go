// file: internal/logger/logger_test.go
// version: 1.2.1
// guid: 9b1deb4d-3b7d-4bad-9bdd-2b0d7b3dcb6d
// last-edited: 2026-10-18

package logger

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/logger/mocks"
	"github.com/stretchr/testify/mock"
)

func TestLevelString(t *testing.T) {
	if LevelDebug.String() != "debug" {
		t.Errorf("LevelDebug.String() = %q, want 'debug'", LevelDebug.String())
	}
}

func TestStandardLogger_With(t *testing.T) {
	log := New("parent")
	child := log.With("child")
	sl, ok := child.(*StandardLogger)
	if !ok {
		t.Fatal("expected *StandardLogger from With()")
	}
	if sl.subsystem != "parent.child" {
		t.Errorf("subsystem = %q, want 'parent.child'", sl.subsystem)
	}
}

func TestStandardLogger_NoOps(t *testing.T) {
	log := New("test")
	log.UpdateProgress(1, 10, "msg")
	log.RecordChange(Change{ChangeType: "test"})
	if log.IsCanceled() {
		t.Error("StandardLogger.IsCanceled() should return false")
	}
	if log.ChangeCounters() != nil {
		t.Error("StandardLogger.ChangeCounters() should return nil")
	}
}

func TestStandardLogger_ActivityWriter(t *testing.T) {
	var captured []string
	writer := mocks.NewMockActivityLogWriter(t)
	writer.EXPECT().
		AddSystemActivityLog(mock.Anything, mock.Anything, mock.Anything).
		RunAndReturn(func(source, level, message string) error {
			captured = append(captured, level+":"+message)
			return nil
		}).Maybe()

	log := NewWithActivityLog("test", writer)
	log.Debug("should not be captured")
	log.Info("should be captured")
	log.Warn("also captured")

	if len(captured) != 2 {
		t.Errorf("expected 2 captured, got %d", len(captured))
	}
}
